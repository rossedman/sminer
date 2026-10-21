// Package store wraps the SQLite database every pipeline stage reads from and
// writes to. Raw transcripts are never copied in: turns hold redacted,
// truncated extracts and point back to their file and byte offset.
package store

import (
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // pure-Go driver, no cgo
)

const schema = `
CREATE TABLE IF NOT EXISTS meta(
  key TEXT PRIMARY KEY,
  value TEXT
);
CREATE TABLE IF NOT EXISTS files(
  id INTEGER PRIMARY KEY,
  path TEXT UNIQUE NOT NULL,
  size INTEGER NOT NULL DEFAULT 0,
  mtime INTEGER NOT NULL DEFAULT 0,
  last_offset INTEGER NOT NULL DEFAULT 0,
  schema_version TEXT NOT NULL DEFAULT '',
  lines INTEGER NOT NULL DEFAULT 0,
  malformed INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS sessions(
  session_id TEXT PRIMARY KEY,
  repo TEXT NOT NULL DEFAULT '',
  branch TEXT NOT NULL DEFAULT '',
  cwd TEXT NOT NULL DEFAULT '',
  started_at INTEGER NOT NULL DEFAULT 0,
  ended_at INTEGER NOT NULL DEFAULT 0,
  turns INTEGER NOT NULL DEFAULT 0,
  dirty INTEGER NOT NULL DEFAULT 1
);
CREATE TABLE IF NOT EXISTS turns(
  id TEXT PRIMARY KEY,
  session_id TEXT NOT NULL,
  event_uuid TEXT NOT NULL DEFAULT '',
  parent_uuid TEXT NOT NULL DEFAULT '',
  seq INTEGER NOT NULL DEFAULT 0,
  ts INTEGER NOT NULL DEFAULT 0,
  role TEXT NOT NULL DEFAULT '',
  kind TEXT NOT NULL,
  text TEXT NOT NULL DEFAULT '',
  tool_name TEXT NOT NULL DEFAULT '',
  tool_key TEXT NOT NULL DEFAULT '',
  tool_use_id TEXT NOT NULL DEFAULT '',
  is_error INTEGER NOT NULL DEFAULT 0,
  sidechain INTEGER NOT NULL DEFAULT 0,
  file_id INTEGER NOT NULL DEFAULT 0,
  file_offset INTEGER NOT NULL DEFAULT 0,
  embedding BLOB
);
CREATE INDEX IF NOT EXISTS turns_session ON turns(session_id, ts, file_id, seq);
CREATE TABLE IF NOT EXISTS signals(
  id TEXT PRIMARY KEY,
  session_id TEXT NOT NULL,
  repo TEXT NOT NULL DEFAULT '',
  type TEXT NOT NULL,
  weight INTEGER NOT NULL,
  ts INTEGER NOT NULL,
  excerpt TEXT NOT NULL DEFAULT '',
  context TEXT NOT NULL DEFAULT '',
  key TEXT NOT NULL DEFAULT '',
  file_id INTEGER NOT NULL DEFAULT 0,
  file_offset INTEGER NOT NULL DEFAULT 0,
  classified INTEGER NOT NULL DEFAULT 0,
  confirmed INTEGER,
  rule TEXT NOT NULL DEFAULT '',
  remedy_hint TEXT NOT NULL DEFAULT '',
  embedding BLOB
);
CREATE INDEX IF NOT EXISTS signals_session ON signals(session_id);
CREATE INDEX IF NOT EXISTS signals_ts ON signals(ts);
CREATE TABLE IF NOT EXISTS findings(
  id TEXT PRIMARY KEY,
  title TEXT NOT NULL DEFAULT '',
  family TEXT NOT NULL DEFAULT '',
  key TEXT NOT NULL DEFAULT '',
  remedy_type TEXT NOT NULL DEFAULT '',
  proposal TEXT NOT NULL DEFAULT '',
  target TEXT NOT NULL DEFAULT '',
  covered_by TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'open',
  reason TEXT NOT NULL DEFAULT '',
  centroid BLOB,
  first_seen INTEGER NOT NULL DEFAULT 0,
  last_seen INTEGER NOT NULL DEFAULT 0,
  updated_at INTEGER NOT NULL DEFAULT 0,
  synthesized INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS finding_signals(
  finding_id TEXT NOT NULL,
  signal_id TEXT NOT NULL,
  PRIMARY KEY(finding_id, signal_id)
);
CREATE INDEX IF NOT EXISTS finding_signals_signal ON finding_signals(signal_id);
CREATE TABLE IF NOT EXISTS adoptions(
  finding_id TEXT PRIMARY KEY,
  adopted_at INTEGER NOT NULL,
  patch_path TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS event_types(
  type TEXT PRIMARY KEY,
  count INTEGER NOT NULL DEFAULT 0,
  known INTEGER NOT NULL DEFAULT 0
);
`

// Store is an open sminer database.
type Store struct {
	DB *sql.DB
}

// Open creates (0600) or opens the database at path and applies the schema.
func Open(path string) (*Store, error) {
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, err
		}
		f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
		if err != nil {
			return nil, err
		}
		f.Close()
		// Tighten permissions on a file created by an older run.
		_ = os.Chmod(path, 0o600)
	}
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(off)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	return &Store{DB: db}, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.DB.Close() }

// Meta returns a value from the meta table, or "" if unset.
func (s *Store) Meta(key string) string {
	var v string
	_ = s.DB.QueryRow(`SELECT value FROM meta WHERE key=?`, key).Scan(&v)
	return v
}

// SetMeta stores a value in the meta table.
func (s *Store) SetMeta(key, value string) error {
	_, err := s.DB.Exec(`INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

// Ms converts a time to the integer milliseconds stored in the database.
func Ms(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

// FromMs is the inverse of Ms.
func FromMs(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}

// EncodeVec packs a float32 vector as little-endian bytes.
func EncodeVec(v []float32) []byte {
	if v == nil {
		return nil
	}
	b := make([]byte, 4*len(v))
	for i, f := range v {
		binary.LittleEndian.PutUint32(b[4*i:], math.Float32bits(f))
	}
	return b
}

// DecodeVec unpacks EncodeVec output.
func DecodeVec(b []byte) []float32 {
	if len(b) == 0 || len(b)%4 != 0 {
		return nil
	}
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
	}
	return v
}

// Signal types.
const (
	Correction          = "correction"
	Interrupt           = "interrupt"
	Rewind              = "rewind"
	RepeatedInstruction = "repeated_instruction"
	HookBlock           = "hook_block"
	ToolFailure         = "tool_failure"
	ManualFixup         = "manual_fixup"
)

// Weights: High, Medium, Low.
const (
	High   = 3
	Medium = 2
	Low    = 1
)

// Signal is one piece of friction evidence.
type Signal struct {
	ID         string
	SessionID  string
	Repo       string
	Type       string
	Weight     int
	TS         time.Time
	Excerpt    string
	Context    string
	Key        string
	FileID     int64
	FileOffset int64
	Classified bool
	Confirmed  *bool
	Rule       string
	RemedyHint string
	Embedding  []float32
}

const signalCols = `id, session_id, repo, type, weight, ts, excerpt, context, key, file_id, file_offset, classified, confirmed, rule, remedy_hint, embedding`

// ScanSignal reads a row selected with signalCols.
func scanSignal(sc interface{ Scan(...any) error }) (Signal, error) {
	var s Signal
	var ts int64
	var classified int
	var confirmed sql.NullInt64
	var emb []byte
	err := sc.Scan(&s.ID, &s.SessionID, &s.Repo, &s.Type, &s.Weight, &ts, &s.Excerpt, &s.Context, &s.Key,
		&s.FileID, &s.FileOffset, &classified, &confirmed, &s.Rule, &s.RemedyHint, &emb)
	if err != nil {
		return s, err
	}
	s.TS = FromMs(ts)
	s.Classified = classified != 0
	if confirmed.Valid {
		b := confirmed.Int64 != 0
		s.Confirmed = &b
	}
	s.Embedding = DecodeVec(emb)
	return s, nil
}

// QuerySignals returns signals matching a WHERE clause (may be empty).
func (s *Store) QuerySignals(where string, args ...any) ([]Signal, error) {
	q := `SELECT ` + signalCols + ` FROM signals`
	if where != "" {
		q += ` WHERE ` + where //nolint:gosec // G202: callers pass constant clauses; values go through args
	}
	q += ` ORDER BY ts, id`
	rows, err := s.DB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Signal
	for rows.Next() {
		sig, err := scanSignal(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sig)
	}
	return out, rows.Err()
}

// Finding is a cluster of signals that share one underlying rule.
type Finding struct {
	ID          string
	Title       string
	Family      string
	Key         string
	RemedyType  string
	Proposal    string
	Target      string
	CoveredBy   string
	Status      string
	Reason      string
	Centroid    []float32
	FirstSeen   time.Time
	LastSeen    time.Time
	UpdatedAt   time.Time
	Synthesized bool
}

// Finding statuses.
const (
	StatusOpen      = "open"
	StatusAccepted  = "accepted"
	StatusDismissed = "dismissed"
)

const findingCols = `id, title, family, key, remedy_type, proposal, target, covered_by, status, reason, centroid, first_seen, last_seen, updated_at, synthesized`

// scanFinding reads one findings row from sc into a Finding, decoding the
// centroid and timestamps. The scan error, if any, is returned with the partly
// filled Finding.
func scanFinding(sc interface{ Scan(...any) error }) (Finding, error) {
	var f Finding
	var c []byte
	var fs, ls, ua int64
	var syn int
	err := sc.Scan(&f.ID, &f.Title, &f.Family, &f.Key, &f.RemedyType, &f.Proposal, &f.Target, &f.CoveredBy,
		&f.Status, &f.Reason, &c, &fs, &ls, &ua, &syn)
	f.Centroid = DecodeVec(c)
	f.FirstSeen, f.LastSeen, f.UpdatedAt = FromMs(fs), FromMs(ls), FromMs(ua)
	f.Synthesized = syn != 0
	return f, err
}

// ErrNotFound is returned when a finding id does not exist.
var ErrNotFound = errors.New("not found")

// GetFinding loads one finding.
func (s *Store) GetFinding(id string) (Finding, error) {
	f, err := scanFinding(s.DB.QueryRow(`SELECT `+findingCols+` FROM findings WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return f, fmt.Errorf("finding %s: %w", id, ErrNotFound)
	}
	return f, err
}

// QueryFindings returns findings matching a WHERE clause (may be empty).
func (s *Store) QueryFindings(where string, args ...any) ([]Finding, error) {
	q := `SELECT ` + findingCols + ` FROM findings`
	if where != "" {
		q += ` WHERE ` + where //nolint:gosec // G202: callers pass constant clauses; values go through args
	}
	q += ` ORDER BY id`
	rows, err := s.DB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Finding
	for rows.Next() {
		f, err := scanFinding(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// Execer is satisfied by *sql.DB and *sql.Tx.
type Execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

// SaveFinding inserts or replaces a finding.
func (s *Store) SaveFinding(f Finding) error { return SaveFinding(s.DB, f) }

// SaveFinding inserts or replaces a finding using e.
func SaveFinding(e Execer, f Finding) error {
	syn := 0
	if f.Synthesized {
		syn = 1
	}
	_, err := e.Exec(`INSERT INTO findings(`+findingCols+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET title=excluded.title, family=excluded.family, key=excluded.key,
 remedy_type=excluded.remedy_type, proposal=excluded.proposal, target=excluded.target,
 covered_by=excluded.covered_by, status=excluded.status, reason=excluded.reason,
 centroid=excluded.centroid, first_seen=excluded.first_seen, last_seen=excluded.last_seen,
 updated_at=excluded.updated_at, synthesized=excluded.synthesized`,
		f.ID, f.Title, f.Family, f.Key, f.RemedyType, f.Proposal, f.Target, f.CoveredBy, f.Status, f.Reason,
		EncodeVec(f.Centroid), Ms(f.FirstSeen), Ms(f.LastSeen), Ms(f.UpdatedAt), syn)
	return err
}

// FindingSignals returns the signals linked to a finding, oldest first.
func (s *Store) FindingSignals(id string) ([]Signal, error) {
	return s.QuerySignals(`id IN (SELECT signal_id FROM finding_signals WHERE finding_id=?)`, id)
}

// Session is per-session metadata.
type Session struct {
	ID      string
	Repo    string
	Branch  string
	Cwd     string
	Started time.Time
	Turns   int
}

// Sessions returns metadata for all sessions keyed by id.
func (s *Store) Sessions() (map[string]Session, error) {
	rows, err := s.DB.Query(`SELECT session_id, repo, branch, cwd, started_at, turns FROM sessions`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]Session{}
	for rows.Next() {
		var ss Session
		var st int64
		if err := rows.Scan(&ss.ID, &ss.Repo, &ss.Branch, &ss.Cwd, &st, &ss.Turns); err != nil {
			return nil, err
		}
		ss.Started = FromMs(st)
		out[ss.ID] = ss
	}
	return out, rows.Err()
}

// Turn is one stored extract of a transcript event (or one content block of
// it).
type Turn struct {
	ID         string
	SessionID  string
	EventUUID  string
	ParentUUID string
	Seq        int64
	TS         time.Time
	Role       string
	Kind       string
	Text       string
	ToolName   string
	ToolKey    string
	ToolUseID  string
	IsError    bool
	Sidechain  bool
	FileID     int64
	FileOffset int64
	Embedding  []float32
}

const turnCols = `id, session_id, event_uuid, parent_uuid, seq, ts, role, kind, text, tool_name, tool_key, tool_use_id, is_error, sidechain, file_id, file_offset, embedding`

// SessionTurns returns a session's turns in transcript order.
func (s *Store) SessionTurns(sessionID string) ([]Turn, error) {
	rows, err := s.DB.Query(`SELECT `+turnCols+` FROM turns WHERE session_id=? ORDER BY ts, file_id, seq, id`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Turn
	for rows.Next() {
		var t Turn
		var ts int64
		var isErr, side int
		var emb []byte
		if err := rows.Scan(&t.ID, &t.SessionID, &t.EventUUID, &t.ParentUUID, &t.Seq, &ts, &t.Role, &t.Kind, &t.Text,
			&t.ToolName, &t.ToolKey, &t.ToolUseID, &isErr, &side, &t.FileID, &t.FileOffset, &emb); err != nil {
			return nil, err
		}
		t.TS = FromMs(ts)
		t.IsError, t.Sidechain = isErr != 0, side != 0
		t.Embedding = DecodeVec(emb)
		out = append(out, t)
	}
	return out, rows.Err()
}
