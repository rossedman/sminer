package signal

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rossedman/sminer/internal/config"
	"github.com/rossedman/sminer/internal/embed"
	"github.com/rossedman/sminer/internal/ingest"
	"github.com/rossedman/sminer/internal/redact"
	"github.com/rossedman/sminer/internal/store"
)

var corpusNow = time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)

func newStore(t testing.TB) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// ingestedStore returns a store holding testdata/projects.
func ingestedStore(t testing.TB) *store.Store {
	t.Helper()
	st := newStore(t)
	red, err := redact.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = ingest.Run(context.Background(), st, ingest.Options{
		Roots:  []string{filepath.Join("..", "..", "testdata", "projects")},
		Since:  10 * 365 * 24 * time.Hour,
		Now:    corpusNow,
		Config: config.Default(),
		Redact: red,
	})
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestRunOnCorpus(t *testing.T) {
	t.Parallel()
	st := ingestedStore(t)
	stats, err := Run(context.Background(), st, Options{Embedder: embed.Hash{}, Now: corpusNow, MinSessions: 2})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Sessions == 0 {
		t.Fatal("no sessions processed")
	}
	total := 0
	for typ, n := range stats.ByType {
		total += n
		var got int
		if err := st.DB.QueryRow(`SELECT COUNT(*) FROM signals WHERE type=?`, typ).Scan(&got); err != nil || got != n {
			t.Errorf("ByType[%s] = %d, table has %d (%v)", typ, n, got, err)
		}
	}
	if total == 0 {
		t.Fatal("corpus produced no signals")
	}
	if st.Meta("detector_version") != DetectorVersion {
		t.Errorf("detector_version = %q", st.Meta("detector_version"))
	}
	var dirty int
	if err := st.DB.QueryRow(`SELECT COUNT(*) FROM sessions WHERE dirty=1`).Scan(&dirty); err != nil || dirty != 0 {
		t.Errorf("dirty sessions = %d (%v)", dirty, err)
	}

	// A second run has nothing dirty and keeps the same signals.
	again, err := Run(context.Background(), st, Options{Embedder: embed.Hash{}, Now: corpusNow, MinSessions: 2})
	if err != nil {
		t.Fatal(err)
	}
	if again.Sessions != 0 {
		t.Errorf("second run re-detected %d sessions", again.Sessions)
	}
	for typ, n := range stats.ByType {
		if again.ByType[typ] != n {
			t.Errorf("second run ByType[%s] = %d, want %d", typ, again.ByType[typ], n)
		}
	}
}

func TestRunKeepsClassificationUnlessExcerptChanges(t *testing.T) {
	t.Parallel()
	st := ingestedStore(t)
	if _, err := Run(context.Background(), st, Options{}); err != nil {
		t.Fatal(err)
	}
	sigs, err := st.QuerySignals("type=?", store.Correction)
	if err != nil || len(sigs) == 0 {
		t.Fatalf("no correction signals: %v", err)
	}
	keep, change := sigs[0], sigs[len(sigs)-1]
	if keep.ID == change.ID {
		t.Fatal("need two distinct corrections in the corpus")
	}
	for _, id := range []string{keep.ID, change.ID} {
		if _, err := st.DB.Exec(`UPDATE signals SET classified=1, confirmed=1, rule='r', remedy_hint='h', embedding=x'00000000' WHERE id=?`, id); err != nil {
			t.Fatal(err)
		}
	}
	// Pretend the stored excerpt differed so re-detection sees a change.
	if _, err := st.DB.Exec(`UPDATE signals SET excerpt='stale' WHERE id=?`, change.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.Exec(`UPDATE sessions SET dirty=1`); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(context.Background(), st, Options{}); err != nil {
		t.Fatal(err)
	}
	got, err := st.QuerySignals("id=?", keep.ID)
	if err != nil || len(got) != 1 {
		t.Fatal(err)
	}
	if !got[0].Classified || got[0].Confirmed == nil || !*got[0].Confirmed || got[0].Rule != "r" {
		t.Errorf("unchanged excerpt lost classification: %+v", got[0])
	}
	got, _ = st.QuerySignals("id=?", change.ID)
	if len(got) != 1 || got[0].Classified || got[0].Confirmed != nil || got[0].Excerpt != change.Excerpt {
		t.Errorf("changed excerpt should reset classification: %+v", got)
	}
}

func TestRunRemovesStaleSignals(t *testing.T) {
	t.Parallel()
	st := ingestedStore(t)
	if _, err := Run(context.Background(), st, Options{}); err != nil {
		t.Fatal(err)
	}
	sid := "a1111111-0000-4000-8000-000000000001"
	if _, err := st.DB.Exec(`INSERT INTO signals(id, session_id, type, weight, ts, excerpt) VALUES('stale1', ?, 'correction', 3, 1, 'old')`, sid); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.Exec(`INSERT INTO signals(id, session_id, type, weight, ts, excerpt) VALUES('rep1', ?, 'repeated_instruction', 3, 1, 'kept')`, sid); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.Exec(`UPDATE sessions SET dirty=1`); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(context.Background(), st, Options{}); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.QuerySignals("id='stale1'"); len(got) != 0 {
		t.Error("stale signal not deleted")
	}
	if got, _ := st.QuerySignals("id='rep1'"); len(got) != 1 {
		t.Error("repeated-instruction signal must be left to the repeated pass")
	}
}

func TestRunRedetectsOnDetectorVersionChange(t *testing.T) {
	t.Parallel()
	st := ingestedStore(t)
	first, err := Run(context.Background(), st, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetMeta("detector_version", "0"); err != nil {
		t.Fatal(err)
	}
	second, err := Run(context.Background(), st, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if second.Sessions != first.Sessions {
		t.Errorf("re-detected %d sessions, want %d", second.Sessions, first.Sessions)
	}
}

func TestRunCancelled(t *testing.T) {
	t.Parallel()
	st := ingestedStore(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := Run(ctx, st, Options{}); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func TestRunErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		stmt string
	}{
		{"no sessions table", `DROP TABLE sessions`},
		{"no signals table", `DROP TABLE signals`},
		{"no turns table", `DROP TABLE turns`},
		{"no meta table", `DROP TABLE meta`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			st := ingestedStore(t)
			if _, err := st.DB.Exec(tt.stmt); err != nil {
				t.Fatal(err)
			}
			if _, err := Run(context.Background(), st, Options{}); err == nil {
				t.Error("want error")
			}
		})
	}
}

func TestRunClosedStore(t *testing.T) {
	t.Parallel()
	st := newStore(t)
	_ = st.Close()
	if _, err := Run(context.Background(), st, Options{}); err == nil {
		t.Error("want error")
	}
}

type fakeEmbedder struct {
	name  string
	err   error
	calls int
	texts []string
}

func (f *fakeEmbedder) Name() string       { return f.name }
func (f *fakeEmbedder) Threshold() float64 { return 0.45 }
func (f *fakeEmbedder) Embed(texts []string) ([][]float32, error) {
	f.calls++
	f.texts = append(f.texts, texts...)
	if f.err != nil {
		return nil, f.err
	}
	out, _ := embed.Hash{}.Embed(texts)
	return out, nil
}

// seedPrompts inserts one user_text turn per (session, text) pair.
func seedPrompts(t *testing.T, st *store.Store, base time.Time, prompts map[string]string) {
	t.Helper()
	i := 0
	for sid, text := range prompts {
		i++
		ts := store.Ms(base.Add(time.Duration(i) * time.Minute))
		if _, err := st.DB.Exec(`INSERT INTO sessions(session_id, repo, started_at, turns, dirty) VALUES(?, '/repo', ?, 1, 1)`, sid, ts); err != nil {
			t.Fatal(err)
		}
		if _, err := st.DB.Exec(`INSERT INTO turns(id, session_id, event_uuid, ts, role, kind, text) VALUES(?,?,?,?, 'user', 'user_text', ?)`,
			sid+"#0", sid, sid+"-e", ts, text); err != nil {
			t.Fatal(err)
		}
	}
}

const guidance = "Always run go vet and go test before committing any change to this repo"

func TestRepeatedInstructions(t *testing.T) {
	t.Parallel()
	st := newStore(t)
	seedPrompts(t, st, corpusNow, map[string]string{
		"s1": guidance,
		"s2": guidance + ".",
		"s3": "Always run go vet and go test before committing any change in this repo",
		"s4": "Terraform resource names are snake_case everywhere in the modules directory",
		"s5": "short",
	})
	emb := &fakeEmbedder{name: "fake"}
	stats, err := Run(context.Background(), st, Options{Embedder: emb, MinSessions: 3, Now: corpusNow})
	if err != nil {
		t.Fatal(err)
	}
	if got := stats.ByType[store.RepeatedInstruction]; got != 3 {
		t.Fatalf("repeated signals = %d, want 3 (%v)", got, stats.ByType)
	}
	sigs, _ := st.QuerySignals("type=?", store.RepeatedInstruction)
	for _, s := range sigs {
		if s.Weight != store.High || s.Repo != "/repo" || !strings.Contains(s.Context, "3 sessions") || s.Key != s.SessionID+"#0" {
			t.Errorf("bad signal %+v", s)
		}
		if !strings.Contains(s.Excerpt, "go vet") {
			t.Errorf("wrong cluster member %q", s.Excerpt)
		}
	}
	// Short prompts and prompts without guidance wording are not embedded.
	for _, tx := range emb.texts {
		if tx == "short" || strings.Contains(tx, "Terraform") {
			t.Errorf("embedded %q", tx)
		}
	}
	if st.Meta("turn_embedder") != "fake" {
		t.Errorf("turn_embedder = %q", st.Meta("turn_embedder"))
	}

	// Vectors are cached: a second run embeds nothing new.
	before := emb.calls
	if _, err := Run(context.Background(), st, Options{Embedder: emb, MinSessions: 3, Now: corpusNow}); err != nil {
		t.Fatal(err)
	}
	if emb.calls != before {
		t.Errorf("embedder called again (%d -> %d calls)", before, emb.calls)
	}

	// A different embedder name discards cached vectors.
	emb2 := &fakeEmbedder{name: "fake2"}
	if _, err := Run(context.Background(), st, Options{Embedder: emb2, MinSessions: 3, Now: corpusNow}); err != nil {
		t.Fatal(err)
	}
	if emb2.calls == 0 {
		t.Error("changing embedder name should re-embed")
	}

	// Raising MinSessions drops the signals again.
	stats, err = Run(context.Background(), st, Options{Embedder: emb2, MinSessions: 4, Now: corpusNow})
	if err != nil {
		t.Fatal(err)
	}
	if stats.ByType[store.RepeatedInstruction] != 0 {
		t.Errorf("stale repeated signals kept: %v", stats.ByType)
	}
}

func TestRepeatedWindow(t *testing.T) {
	t.Parallel()
	st := newStore(t)
	seedPrompts(t, st, corpusNow.Add(-48*time.Hour), map[string]string{"s1": guidance, "s2": guidance})
	seedPrompts(t, st, corpusNow.Add(-2*time.Hour), map[string]string{"s3": guidance})
	all, err := Run(context.Background(), st, Options{Embedder: embed.Hash{}, MinSessions: 3})
	if err != nil || all.ByType[store.RepeatedInstruction] != 3 {
		t.Fatalf("no window: %v %v", all.ByType, err)
	}
	win, err := Run(context.Background(), st, Options{Embedder: embed.Hash{}, MinSessions: 3, Now: corpusNow, Window: 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if win.ByType[store.RepeatedInstruction] != 0 {
		t.Errorf("window should exclude older prompts: %v", win.ByType)
	}
}

func TestRepeatedSkipsPromptsAlreadyExplained(t *testing.T) {
	t.Parallel()
	st := newStore(t)
	seedPrompts(t, st, corpusNow, map[string]string{"s1": guidance, "s2": guidance, "s3": guidance})
	if _, err := st.DB.Exec(`INSERT INTO signals(id, session_id, type, weight, ts, key) VALUES('c1','s1','correction',3,1,'s1#0')`); err != nil {
		t.Fatal(err)
	}
	// Mark sessions clean so only the cross-session pass runs.
	if _, err := st.DB.Exec(`UPDATE sessions SET dirty=0`); err != nil {
		t.Fatal(err)
	}
	if err := st.SetMeta("detector_version", DetectorVersion); err != nil {
		t.Fatal(err)
	}
	stats, err := Run(context.Background(), st, Options{Embedder: embed.Hash{}, MinSessions: 3})
	if err != nil {
		t.Fatal(err)
	}
	if stats.ByType[store.RepeatedInstruction] != 0 {
		t.Errorf("a turn already explained by a correction is not an instruction: %v", stats.ByType)
	}
}

func TestRepeatedEmbedderFailure(t *testing.T) {
	t.Parallel()
	boom := errors.New("ollama down")
	st := newStore(t)
	seedPrompts(t, st, corpusNow, map[string]string{"s1": guidance, "s2": guidance + " ok", "s3": guidance + " yes"})
	_, err := Run(context.Background(), st, Options{Embedder: &fakeEmbedder{name: "x", err: boom}, MinSessions: 3})
	if !errors.Is(err, boom) {
		t.Errorf("err = %v, want it to wrap %v", err, boom)
	}
}

func TestRepeatedBatches(t *testing.T) {
	t.Parallel()
	st := newStore(t)
	prompts := map[string]string{}
	for i := range 300 {
		prompts[fmt.Sprintf("s%03d", i)] = fmt.Sprintf("Always run linter %d before committing the change to the repo", i)
	}
	seedPrompts(t, st, corpusNow, prompts)
	emb := &fakeEmbedder{name: "batch"}
	if _, err := Run(context.Background(), st, Options{Embedder: emb, MinSessions: 1000}); err != nil {
		t.Fatal(err)
	}
	if emb.calls != 2 || len(emb.texts) != 300 {
		t.Errorf("calls = %d texts = %d, want 2 batches of 256 + 44", emb.calls, len(emb.texts))
	}
}

func TestRepeatedBrokenTables(t *testing.T) {
	t.Parallel()
	for _, drop := range []string{"signals", "turns"} {
		t.Run(drop, func(t *testing.T) {
			t.Parallel()
			st := newStore(t)
			seedPrompts(t, st, corpusNow, map[string]string{"s1": guidance})
			if _, err := st.DB.Exec(`DROP TABLE ` + drop); err != nil {
				t.Fatal(err)
			}
			if err := repeated(st, Options{Embedder: embed.Hash{}, MinSessions: 1}, nil); err == nil {
				t.Error("want error")
			}
		})
	}
}
