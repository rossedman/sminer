package store

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func openTemp(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "db", "sminer.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestOpenCreatesPrivateFile(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "nested", "dir")
	path := filepath.Join(dir, "s.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("db mode = %v, want 0600", fi.Mode().Perm())
	}
	di, err := os.Stat(dir)
	if err != nil || di.Mode().Perm() != 0o700 {
		t.Errorf("dir mode = %v err=%v, want 0700", di.Mode().Perm(), err)
	}
}

func TestOpenTightensExistingPermissions(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "old.db")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", fi.Mode().Perm())
	}
}

func TestOpenMemory(t *testing.T) {
	t.Parallel()
	st, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SetMeta("k", "v"); err != nil {
		t.Fatal(err)
	}
}

func TestOpenErrors(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	file := filepath.Join(dir, "afile")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Parent is a regular file, so MkdirAll fails.
	if _, err := Open(filepath.Join(file, "sub", "s.db")); err == nil {
		t.Error("want error when parent is a file")
	}
	// The path itself is a directory, so OpenFile fails.
	if _, err := Open(dir); err == nil {
		t.Error("want error when path is a directory")
	}
	// A file that is not a database fails when the schema is applied.
	junk := filepath.Join(dir, "junk.db")
	if err := os.WriteFile(junk, []byte("this is definitely not a sqlite database file, just text padding padding padding"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(junk); err == nil {
		t.Error("want error for a non-database file")
	}
}

func TestMeta(t *testing.T) {
	t.Parallel()
	st := openTemp(t)
	if got := st.Meta("missing"); got != "" {
		t.Errorf("Meta(missing) = %q", got)
	}
	for _, v := range []string{"1", "2"} {
		if err := st.SetMeta("ver", v); err != nil {
			t.Fatal(err)
		}
		if got := st.Meta("ver"); got != v {
			t.Errorf("Meta = %q, want %q", got, v)
		}
	}
	_ = st.Close()
	if err := st.SetMeta("a", "b"); err == nil {
		t.Error("SetMeta on closed store should fail")
	}
}

func TestMsRoundTrip(t *testing.T) {
	t.Parallel()
	if Ms(time.Time{}) != 0 || !FromMs(0).IsZero() {
		t.Error("zero time must map to 0 and back")
	}
	ts := time.Date(2026, 9, 21, 9, 0, 20, 123_000_000, time.UTC)
	if got := Ms(ts); got != ts.UnixMilli() {
		t.Errorf("Ms = %d", got)
	}
	back := FromMs(Ms(ts))
	if !back.Equal(ts) || back.Location() != time.UTC {
		t.Errorf("FromMs(Ms) = %v", back)
	}
	// Sub-millisecond precision is dropped.
	if got := FromMs(Ms(ts.Add(900 * time.Microsecond))); !got.Equal(ts) {
		t.Errorf("sub-ms not truncated: %v", got)
	}
}

func TestVecCodec(t *testing.T) {
	t.Parallel()
	if EncodeVec(nil) != nil {
		t.Error("EncodeVec(nil) != nil")
	}
	if got := EncodeVec([]float32{}); len(got) != 0 {
		t.Errorf("EncodeVec(empty) = %v", got)
	}
	v := []float32{0, 1, -1.5, math.MaxFloat32, math.SmallestNonzeroFloat32}
	b := EncodeVec(v)
	if len(b) != 4*len(v) {
		t.Fatalf("len = %d", len(b))
	}
	if !reflect.DeepEqual(DecodeVec(b), v) {
		t.Errorf("round trip = %v", DecodeVec(b))
	}
	// Little-endian: 1.0 is 0x3f800000.
	if got := EncodeVec([]float32{1}); !reflect.DeepEqual(got, []byte{0, 0, 0x80, 0x3f}) {
		t.Errorf("encoding = %v", got)
	}
	for _, bad := range [][]byte{nil, {}, {1}, {1, 2, 3}, {1, 2, 3, 4, 5}} {
		if bad != nil && len(bad)%4 == 0 {
			continue
		}
		if DecodeVec(bad) != nil {
			t.Errorf("DecodeVec(%v) should be nil", bad)
		}
	}
}

func sampleSignal(id string, ts time.Time) Signal {
	return Signal{ID: id, SessionID: "s1", Repo: "/r", Type: Correction, Weight: High, TS: ts, Excerpt: "no, use X"}
}

func insertSignal(t *testing.T, st *Store, s Signal, classified int, confirmed any, emb []float32) {
	t.Helper()
	_, err := st.DB.Exec(`INSERT INTO signals(`+signalCols+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		s.ID, s.SessionID, s.Repo, s.Type, s.Weight, Ms(s.TS), s.Excerpt, s.Context, s.Key, s.FileID, s.FileOffset,
		classified, confirmed, s.Rule, s.RemedyHint, EncodeVec(emb))
	if err != nil {
		t.Fatal(err)
	}
}

func TestQuerySignals(t *testing.T) {
	t.Parallel()
	st := openTemp(t)
	t0 := time.Date(2026, 9, 21, 9, 0, 0, 0, time.UTC)
	a := sampleSignal("a", t0.Add(2*time.Minute))
	b := sampleSignal("b", t0)
	b.Type, b.Rule, b.RemedyHint, b.FileID, b.FileOffset = Rewind, "use X", "claude_md", 4, 99
	c := sampleSignal("c", t0.Add(time.Minute))
	insertSignal(t, st, a, 0, nil, nil)
	insertSignal(t, st, b, 1, 1, []float32{1, 2})
	insertSignal(t, st, c, 1, 0, nil)

	all, err := st.QuerySignals("")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 || all[0].ID != "b" || all[1].ID != "c" || all[2].ID != "a" {
		t.Fatalf("order by ts: %+v", all)
	}
	gb := all[0]
	if !gb.Classified || gb.Confirmed == nil || !*gb.Confirmed || gb.Rule != "use X" || gb.RemedyHint != "claude_md" ||
		gb.FileID != 4 || gb.FileOffset != 99 || gb.Type != Rewind || !reflect.DeepEqual(gb.Embedding, []float32{1, 2}) || !gb.TS.Equal(t0) {
		t.Errorf("signal b = %+v", gb)
	}
	if all[1].Confirmed == nil || *all[1].Confirmed {
		t.Errorf("c confirmed = %v, want false", all[1].Confirmed)
	}
	if all[2].Confirmed != nil || all[2].Classified {
		t.Errorf("a should be unclassified with nil confirmed: %+v", all[2])
	}

	got, err := st.QuerySignals("type=? AND weight>=?", Rewind, High)
	if err != nil || len(got) != 1 || got[0].ID != "b" {
		t.Errorf("filtered = %+v err=%v", got, err)
	}
	if _, err := st.QuerySignals("nonsense("); err == nil {
		t.Error("bad where clause should fail")
	}
	// A row whose column cannot be scanned surfaces the error.
	if _, err := st.DB.Exec(`UPDATE signals SET weight='heavy' WHERE id='a'`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.QuerySignals(""); err == nil {
		t.Error("unscannable row should fail")
	}
}

func TestFindingRoundTrip(t *testing.T) {
	t.Parallel()
	st := openTemp(t)
	t0 := time.Date(2026, 9, 21, 9, 0, 0, 0, time.UTC)
	f := Finding{
		ID: "f1", Title: "Use just", Family: "build", Key: "make", RemedyType: "claude_md", Proposal: "Use just",
		Target: "CLAUDE.md", CoveredBy: "", Status: StatusOpen, Reason: "", Centroid: []float32{0.5, -0.5},
		FirstSeen: t0, LastSeen: t0.Add(time.Hour), UpdatedAt: t0.Add(2 * time.Hour), Synthesized: true,
	}
	if err := st.SaveFinding(f); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetFinding("f1")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, f) {
		t.Errorf("got %+v\nwant %+v", got, f)
	}

	// Saving again replaces the mutable fields.
	f.Status, f.Reason, f.Synthesized, f.Title = StatusDismissed, "noise", false, "Use just always"
	if err := st.SaveFinding(f); err != nil {
		t.Fatal(err)
	}
	got, _ = st.GetFinding("f1")
	if got.Status != StatusDismissed || got.Reason != "noise" || got.Synthesized || got.Title != "Use just always" {
		t.Errorf("after update: %+v", got)
	}
	all, err := st.QueryFindings("")
	if err != nil || len(all) != 1 {
		t.Errorf("QueryFindings = %+v err=%v", all, err)
	}

	f2 := f
	f2.ID, f2.Status = "f0", StatusOpen
	if err := st.SaveFinding(f2); err != nil {
		t.Fatal(err)
	}
	open, err := st.QueryFindings("status=?", StatusOpen)
	if err != nil || len(open) != 1 || open[0].ID != "f0" {
		t.Errorf("open = %+v err=%v", open, err)
	}
	ordered, _ := st.QueryFindings("")
	if len(ordered) != 2 || ordered[0].ID != "f0" || ordered[1].ID != "f1" {
		t.Errorf("order by id: %+v", ordered)
	}
	if _, err := st.QueryFindings("bogus("); err == nil {
		t.Error("bad where clause should fail")
	}
	if _, err := st.DB.Exec(`UPDATE findings SET first_seen='x' WHERE id='f0'`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.QueryFindings(""); err == nil {
		t.Error("unscannable finding should fail")
	}
}

func TestGetFindingNotFound(t *testing.T) {
	t.Parallel()
	st := openTemp(t)
	_, err := st.GetFinding("nope")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	_ = st.Close()
	_, err = st.GetFinding("nope")
	if err == nil || errors.Is(err, ErrNotFound) {
		t.Errorf("closed store: err = %v, want a non-NotFound error", err)
	}
}

func TestFindingSignals(t *testing.T) {
	t.Parallel()
	st := openTemp(t)
	t0 := time.Date(2026, 9, 21, 9, 0, 0, 0, time.UTC)
	insertSignal(t, st, sampleSignal("s2", t0.Add(time.Minute)), 0, nil, nil)
	insertSignal(t, st, sampleSignal("s1", t0), 0, nil, nil)
	insertSignal(t, st, sampleSignal("s3", t0), 0, nil, nil)
	for _, sid := range []string{"s1", "s2"} {
		if _, err := st.DB.Exec(`INSERT INTO finding_signals VALUES('f1', ?)`, sid); err != nil {
			t.Fatal(err)
		}
	}
	got, err := st.FindingSignals("f1")
	if err != nil || len(got) != 2 || got[0].ID != "s1" || got[1].ID != "s2" {
		t.Errorf("FindingSignals = %+v err=%v", got, err)
	}
	if got, _ := st.FindingSignals("other"); len(got) != 0 {
		t.Errorf("other = %+v", got)
	}
}

func TestSessionsAndTurns(t *testing.T) {
	t.Parallel()
	st := openTemp(t)
	t0 := time.Date(2026, 9, 21, 9, 0, 0, 0, time.UTC)
	if _, err := st.DB.Exec(`INSERT INTO sessions(session_id, repo, branch, cwd, started_at, turns) VALUES('s1','/r','main','/r/sub',?,3)`, Ms(t0)); err != nil {
		t.Fatal(err)
	}
	sess, err := st.Sessions()
	if err != nil {
		t.Fatal(err)
	}
	want := Session{ID: "s1", Repo: "/r", Branch: "main", Cwd: "/r/sub", Started: t0, Turns: 3}
	if !reflect.DeepEqual(sess["s1"], want) || len(sess) != 1 {
		t.Errorf("Sessions = %+v", sess)
	}

	ins := `INSERT INTO turns(id, session_id, event_uuid, parent_uuid, seq, ts, role, kind, text, tool_name, tool_key, tool_use_id, is_error, sidechain, file_id, file_offset, embedding)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`
	rows := [][]any{
		{"t2", "s1", "e2", "e1", 0, Ms(t0.Add(time.Second)), "assistant", "tool_use", "", "Bash", "ls", "tu1", 0, 0, 1, 50, nil},
		{"t1", "s1", "e1", "", 0, Ms(t0), "user", "user_text", "hello", "", "", "", 0, 0, 1, 0, EncodeVec([]float32{1, 2, 3})},
		{"t3", "s1", "e3", "e2", 1, Ms(t0.Add(time.Second)), "user", "tool_result", "boom", "", "", "tu1", 1, 1, 1, 90, nil},
		{"other", "s2", "e9", "", 0, Ms(t0), "user", "user_text", "x", "", "", "", 0, 0, 2, 0, nil},
	}
	for _, r := range rows {
		if _, err := st.DB.Exec(ins, r...); err != nil {
			t.Fatal(err)
		}
	}
	turns, err := st.SessionTurns("s1")
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 3 || turns[0].ID != "t1" || turns[1].ID != "t2" || turns[2].ID != "t3" {
		t.Fatalf("order = %+v", turns)
	}
	if !reflect.DeepEqual(turns[0].Embedding, []float32{1, 2, 3}) || turns[0].Text != "hello" || turns[0].Role != "user" || !turns[0].TS.Equal(t0) {
		t.Errorf("t1 = %+v", turns[0])
	}
	if turns[1].ToolName != "Bash" || turns[1].ToolKey != "ls" || turns[1].ToolUseID != "tu1" || turns[1].FileOffset != 50 || turns[1].ParentUUID != "e1" || turns[1].EventUUID != "e2" || turns[1].IsError || turns[1].Sidechain {
		t.Errorf("t2 = %+v", turns[1])
	}
	if !turns[2].IsError || !turns[2].Sidechain || turns[2].Seq != 1 || turns[2].FileID != 1 {
		t.Errorf("t3 = %+v", turns[2])
	}
	if got, _ := st.SessionTurns("none"); len(got) != 0 {
		t.Errorf("none = %+v", got)
	}

	if _, err := st.DB.Exec(`UPDATE turns SET seq='x' WHERE id='t1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SessionTurns("s1"); err == nil {
		t.Error("unscannable turn should fail")
	}
	if _, err := st.DB.Exec(`UPDATE sessions SET turns='x'`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Sessions(); err == nil {
		t.Error("unscannable session should fail")
	}
}

func TestClosedStoreErrors(t *testing.T) {
	t.Parallel()
	st := openTemp(t)
	_ = st.Close()
	if _, err := st.QuerySignals(""); err == nil {
		t.Error("QuerySignals")
	}
	if _, err := st.QueryFindings(""); err == nil {
		t.Error("QueryFindings")
	}
	if _, err := st.Sessions(); err == nil {
		t.Error("Sessions")
	}
	if _, err := st.SessionTurns("s"); err == nil {
		t.Error("SessionTurns")
	}
	if err := st.SaveFinding(Finding{ID: "x"}); err == nil {
		t.Error("SaveFinding")
	}
}

func TestSaveFindingInTransaction(t *testing.T) {
	t.Parallel()
	st := openTemp(t)
	tx, err := st.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := SaveFinding(tx, Finding{ID: "tx1", Status: StatusOpen}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetFinding("tx1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("rolled back finding visible: %v", err)
	}
}
