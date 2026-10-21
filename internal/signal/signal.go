// Package signal finds friction in stored turns: corrections, interrupts,
// rewinds, repeated instructions, hook blocks, tool failure loops and manual
// fix-ups. Each detector emits a store.Signal with a type, weight and a
// redacted excerpt. Detection is heuristic and cheap; the classify stage
// confirms the ambiguous ones.
package signal

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rossedman/sminer/internal/embed"
	"github.com/rossedman/sminer/internal/ingest"
	"github.com/rossedman/sminer/internal/store"
	"github.com/rossedman/sminer/internal/textutil"
)

// NeedsClassifier reports whether a signal type is confirmed by the LLM
// classifier before it counts. Other types are deterministic.
func NeedsClassifier(s store.Signal) bool {
	switch s.Type {
	case store.Correction, store.Rewind, store.RepeatedInstruction:
		return true
	case store.Interrupt:
		return !strings.HasPrefix(s.Excerpt, interruptedPrefix)
	}
	return false
}

const interruptedPrefix = "(interrupted) "

// DetectorVersion changes whenever Detect would produce different signals
// from the same turns; Run then re-detects every session.
const DetectorVersion = "3"

// IsBareInterrupt reports whether s is an interrupt the user gave no
// reason for. It counts as friction but says nothing to cluster on.
func IsBareInterrupt(s store.Signal) bool {
	return s.Type == store.Interrupt && strings.HasPrefix(s.Excerpt, interruptedPrefix)
}

// IsScratchPath reports whether path is a temp, scratchpad or ~/.claude
// file. Claude's background tasks and the app rewrite these, so a change
// there is not the user fixing Claude's work.
func IsScratchPath(path string) bool {
	p := filepath.Clean(path)
	roots := []string{"/tmp", "/private/tmp", "/var/folders", "/private/var/folders", filepath.Clean(os.TempDir())}
	if home, err := os.UserHomeDir(); err == nil {
		roots = append(roots, filepath.Join(home, ".claude"))
	}
	for _, r := range roots {
		if p == r || strings.HasPrefix(p, r+"/") {
			return true
		}
	}
	return strings.Contains(p, "/scratchpad/") || strings.Contains(p, "/scratch-workspaces/")
}

// Detect runs the per-session detectors over one session's turns.
func Detect(sess store.Session, turns []store.Turn) []store.Signal {
	var out []store.Signal
	emit := func(t store.Turn, typ string, weight int, excerpt, context, key string) {
		out = append(out, store.Signal{
			ID:         textutil.Hash(20, typ, t.ID),
			SessionID:  t.SessionID,
			Repo:       sess.Repo,
			Type:       typ,
			Weight:     weight,
			TS:         t.TS,
			Excerpt:    excerpt,
			Context:    context,
			Key:        key,
			FileID:     t.FileID,
			FileOffset: t.FileOffset,
		})
	}

	var actions []store.Turn // assistant activity since the last user text
	tools := map[string]store.Turn{}
	consumed := map[string]bool{}
	children := map[string][]store.Turn{}
	var failCmd string
	var failStreak int
	var failEmitted bool
	// Files Claude wrote or read, and whether a later Bash command may have
	// rewritten them. Turns ingested before trackFixup knew about Bash
	// still carry fix-ups for those changes.
	bashTouched := map[string]bool{}

	prevKind := ""
	for i, t := range turns {
		if t.Sidechain {
			continue
		}
		if i > 0 {
			prevKind = turns[i-1].Kind //nolint:gosec // G602: guarded by i > 0
		}
		switch t.Kind {
		case ingest.KindToolUse:
			actions = append(actions, t)
			if t.ToolUseID != "" {
				tools[t.ToolUseID] = t
			}
			switch t.ToolName {
			case "Write":
				bashTouched[t.ToolKey] = false
			case "Edit", "MultiEdit", "Read":
				if _, ok := bashTouched[t.ToolKey]; !ok {
					bashTouched[t.ToolKey] = false
				}
			case "Bash":
				for p := range bashTouched {
					if ingest.BashMayWrite(t.ToolKey, p) {
						bashTouched[p] = true
					}
				}
			}
		case ingest.KindAssistantText:
			actions = append(actions, t)
		case ingest.KindToolResult:
			tu, ok := tools[t.ToolUseID]
			if !ok || tu.ToolName != "Bash" || tu.ToolKey == "" {
				continue
			}
			cmd := tu.ToolKey
			if t.IsError {
				if cmd == failCmd {
					failStreak++
				} else {
					failCmd, failStreak, failEmitted = cmd, 1, false
				}
				if failStreak >= 2 && !failEmitted {
					failEmitted = true
					key := CommandKey(cmd)
					excerpt := fmt.Sprintf("`%s` failed %d times in a row: %s", textutil.Truncate(cmd, 200), failStreak,
						textutil.Truncate(textutil.FirstLine(t.Text), 200))
					emit(t, store.ToolFailure, store.Medium, excerpt, "", key)
				}
			} else if cmd == failCmd {
				failCmd, failStreak = "", 0
			}
		case ingest.KindHook:
			// Turns stored before hook detection was tightened.
			if !ingest.IsHook(t.Text) {
				continue
			}
			key, msg := HookKey(t.Text)
			ctx := ""
			if tu, ok := tools[t.ToolUseID]; ok {
				ctx = describeAction(tu)
			}
			emit(t, store.HookBlock, store.Medium, msg, ctx, key)
		case ingest.KindInterrupt, ingest.KindRejected:
			// One interruption is logged twice (a tool_result marker,
			// then a user-text marker); count it once.
			if t.Kind == ingest.KindInterrupt && prevKind == ingest.KindInterrupt {
				continue
			}
			excerpt := ingest.StripInterrupt(t.Text)
			if excerpt == "" {
				for j := i + 1; j < len(turns); j++ {
					n := turns[j]
					if n.Kind == ingest.KindUserText && !ingest.IsHarnessText(n.Text) {
						excerpt = n.Text
						consumed[n.ID] = true
						break
					}
					if n.Role == "assistant" || n.Kind == ingest.KindCommand {
						break
					}
				}
			}
			ctx := describeActions(actions)
			if excerpt == "" {
				last := ""
				if len(actions) > 0 {
					last = describeAction(actions[len(actions)-1])
				}
				excerpt = interruptedPrefix + last
			}
			emit(t, store.Interrupt, store.High, excerpt, ctx, t.ID)
		case ingest.KindUserText:
			if ingest.IsHarnessText(t.Text) {
				continue
			}
			if t.ParentUUID != "" {
				children[t.ParentUUID] = append(children[t.ParentUUID], t)
			}
			if consumed[t.ID] {
				actions = nil
				continue
			}
			if hasToolUse(actions) && IsCorrection(t.Text) {
				emit(t, store.Correction, store.High, t.Text, describeActions(actions), t.ID)
			}
			actions = nil
		case ingest.KindFixup:
			if IsScratchPath(t.ToolKey) {
				continue
			}
			if bashTouched[t.ToolKey] {
				bashTouched[t.ToolKey] = false
				continue
			}
			key := FileKey(t.ToolKey)
			emit(t, store.ManualFixup, store.Low, fmt.Sprintf("%s changed outside Claude right after Claude edited it", t.ToolKey), "", key)
		}
	}

	// Rewinds: one parent with several user-text children means the user
	// went back and retried from that point.
	for _, sibs := range children {
		uniq := dedupeByEvent(sibs)
		for k := 1; k < len(uniq); k++ {
			prev := uniq[k-1]
			ctx := "Earlier attempt from the same point: " + textutil.Truncate(prev.Text, 600)
			emit(uniq[k], store.Rewind, store.Medium, uniq[k].Text, ctx, uniq[k].ID)
		}
	}
	return out
}

func dedupeByEvent(ts []store.Turn) []store.Turn {
	seen := map[string]bool{}
	var out []store.Turn
	for _, t := range ts {
		if seen[t.EventUUID] {
			continue
		}
		seen[t.EventUUID] = true
		out = append(out, t)
	}
	return out
}

func hasToolUse(ts []store.Turn) bool {
	for _, t := range ts {
		if t.Kind == ingest.KindToolUse {
			return true
		}
	}
	return false
}

func describeAction(t store.Turn) string {
	if t.Kind == ingest.KindAssistantText {
		return "Assistant said: " + textutil.Truncate(textutil.OneLine(t.Text), 300)
	}
	s := t.ToolName
	if t.ToolKey != "" {
		s += " " + textutil.Truncate(t.ToolKey, 200)
	}
	if t.Text != "" {
		s += " — " + textutil.Truncate(textutil.OneLine(t.Text), 200)
	}
	return s
}

// describeActions summarises the assistant's last few steps, capped so a
// classifier call stays near 2 KB.
func describeActions(ts []store.Turn) string {
	if len(ts) > 4 {
		ts = ts[len(ts)-4:]
	}
	var parts []string
	for _, t := range ts {
		parts = append(parts, describeAction(t))
	}
	return textutil.Truncate(strings.Join(parts, "\n"), 1400)
}

// Options control the signal stage.
type Options struct {
	Embedder embed.Embedder
	Now      time.Time
	Window   time.Duration
	// MinSessions is how many distinct sessions a near-duplicate instruction
	// must appear in to count as repeated.
	MinSessions int
}

// Stats summarises a signal run.
type Stats struct {
	Sessions int
	ByType   map[string]int
}

// Run recomputes signals for sessions that ingest marked dirty, then runs
// the cross-session repeated-instruction pass. Classifier results on
// signals that survive are kept.
func Run(ctx context.Context, st *store.Store, opt Options) (Stats, error) {
	stats := Stats{ByType: map[string]int{}}
	if opt.MinSessions <= 0 {
		opt.MinSessions = 3
	}
	sessions, err := st.Sessions()
	if err != nil {
		return stats, err
	}
	if st.Meta("detector_version") != DetectorVersion {
		if _, err := st.DB.Exec(`UPDATE sessions SET dirty=1`); err != nil {
			return stats, err
		}
		if err := st.SetMeta("detector_version", DetectorVersion); err != nil {
			return stats, err
		}
	}
	rows, err := st.DB.Query(`SELECT session_id FROM sessions WHERE dirty=1`)
	if err != nil {
		return stats, err
	}
	var dirty []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return stats, err
		}
		dirty = append(dirty, id)
	}
	rows.Close()

	for _, sid := range dirty {
		if err := ctx.Err(); err != nil {
			return stats, err
		}
		turns, err := st.SessionTurns(sid)
		if err != nil {
			return stats, err
		}
		sigs := Detect(sessions[sid], turns)
		if err := replaceSessionSignals(st, sid, sigs); err != nil {
			return stats, err
		}
		stats.Sessions++
	}
	if opt.Embedder != nil {
		if err := repeated(st, opt, sessions); err != nil {
			return stats, fmt.Errorf("repeated instructions: %w", err)
		}
	}
	r, err := st.DB.Query(`SELECT type, COUNT(*) FROM signals GROUP BY type`)
	if err != nil {
		return stats, err
	}
	defer r.Close()
	for r.Next() {
		var t string
		var n int
		if err := r.Scan(&t, &n); err != nil {
			return stats, err
		}
		stats.ByType[t] = n
	}
	return stats, r.Err()
}

func replaceSessionSignals(st *store.Store, sid string, sigs []store.Signal) error {
	tx, err := st.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	keep := map[string]bool{}
	for _, s := range sigs {
		keep[s.ID] = true
		if err := upsertSignal(tx, s); err != nil {
			return err
		}
	}
	rows, err := tx.Query(`SELECT id FROM signals WHERE session_id=? AND type<>?`, sid, store.RepeatedInstruction)
	if err != nil {
		return err
	}
	var stale []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		if !keep[id] {
			stale = append(stale, id)
		}
	}
	rows.Close()
	for _, id := range stale {
		if _, err := tx.Exec(`DELETE FROM signals WHERE id=?`, id); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`UPDATE sessions SET dirty=0 WHERE session_id=?`, sid); err != nil {
		return err
	}
	return tx.Commit()
}

func upsertSignal(tx *sql.Tx, s store.Signal) error {
	_, err := tx.Exec(`INSERT INTO signals(id, session_id, repo, type, weight, ts, excerpt, context, key, file_id, file_offset)
VALUES(?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET session_id=excluded.session_id, repo=excluded.repo, weight=excluded.weight, ts=excluded.ts,
 context=excluded.context, key=excluded.key, file_id=excluded.file_id, file_offset=excluded.file_offset,
 classified=CASE WHEN signals.excerpt=excluded.excerpt THEN signals.classified ELSE 0 END,
 confirmed=CASE WHEN signals.excerpt=excluded.excerpt THEN signals.confirmed ELSE NULL END,
 embedding=CASE WHEN signals.excerpt=excluded.excerpt THEN signals.embedding ELSE NULL END,
 excerpt=excluded.excerpt`,
		s.ID, s.SessionID, s.Repo, s.Type, s.Weight, store.Ms(s.TS), s.Excerpt, s.Context, s.Key, s.FileID, s.FileOffset)
	return err
}
