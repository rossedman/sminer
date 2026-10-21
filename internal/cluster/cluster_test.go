package cluster

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/rossedman/sminer/internal/config"
	"github.com/rossedman/sminer/internal/embed"
	"github.com/rossedman/sminer/internal/ingest"
	"github.com/rossedman/sminer/internal/redact"
	"github.com/rossedman/sminer/internal/signal"
	"github.com/rossedman/sminer/internal/store"
)

var now = time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)

func newStore(t testing.TB) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// corpusStore ingests testdata/projects and detects signals in it.
func corpusStore(t testing.TB) *store.Store {
	t.Helper()
	st := newStore(t)
	red, err := redact.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = ingest.Run(context.Background(), st, ingest.Options{
		Roots: []string{filepath.Join("..", "..", "testdata", "projects")}, Since: 3650 * 24 * time.Hour,
		Now: now, Config: config.Default(), Redact: red,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := signal.Run(context.Background(), st, signal.Options{Embedder: embed.Hash{}, Now: now}); err != nil {
		t.Fatal(err)
	}
	return st
}

type sig struct {
	id, typ, excerpt, key, rule, hint, repo string
	ts                                      time.Time
	confirmed                               any
}

func insert(t testing.TB, st *store.Store, ss ...sig) {
	t.Helper()
	for _, s := range ss {
		if s.repo == "" {
			s.repo = "/r"
		}
		if s.ts.IsZero() {
			s.ts = now.Add(-time.Hour)
		}
		_, err := st.DB.Exec(`INSERT INTO signals(id, session_id, repo, type, weight, ts, excerpt, key, rule, remedy_hint, confirmed)
VALUES(?,?,?,?,?,?,?,?,?,?,?)`, s.id, "sess-"+s.id, s.repo, s.typ, 3, store.Ms(s.ts), s.excerpt, s.key, s.rule, s.hint, s.confirmed)
		if err != nil {
			t.Fatal(err)
		}
	}
}

func pnpmSignals(n int, prefix string) []sig {
	var out []sig
	for i := range n {
		out = append(out, sig{id: fmt.Sprintf("%s%d", prefix, i), typ: store.Correction,
			excerpt: "no, use pnpm instead of npm in this repo", ts: now.Add(-time.Duration(i+1) * time.Hour)})
	}
	return out
}

func TestFamilyOfAndText(t *testing.T) {
	t.Parallel()
	for typ, want := range map[string]string{
		store.HookBlock: Hook, store.ToolFailure: Failure, store.ManualFixup: Fixup,
		store.Correction: Behavior, store.Interrupt: Behavior, store.Rewind: Behavior, store.RepeatedInstruction: Behavior, "other": Behavior,
	} {
		if got := FamilyOf(typ); got != want {
			t.Errorf("FamilyOf(%s) = %s, want %s", typ, got, want)
		}
	}
	if SignalText(store.Signal{Rule: "the rule", Excerpt: "x"}) != "the rule" {
		t.Error("rule wins")
	}
	if SignalText(store.Signal{Excerpt: "(interrupted) Bash ls"}) != "Bash ls" {
		t.Error("interrupt prefix trimmed")
	}
	if SignalText(store.Signal{Excerpt: "plain"}) != "plain" {
		t.Error("excerpt fallback")
	}
}

func TestFixupKindsAndSortKinds(t *testing.T) {
	t.Parallel()
	sigs := []store.Signal{
		{Type: store.ManualFixup, Key: "*.go"}, {Type: store.ManualFixup, Key: "*.go"}, {Type: store.ManualFixup, Key: "go.mod"},
		{Type: store.ManualFixup, Key: "*.md"}, {Type: store.ManualFixup, Key: ""}, {Type: store.Correction, Key: "*.go"},
	}
	want := []KindCount{{"*.go", 2}, {"*.md", 1}, {"go.mod", 1}}
	if got := FixupKinds(sigs); !reflect.DeepEqual(got, want) {
		t.Errorf("FixupKinds = %v, want %v", got, want)
	}
	if got := SortKinds(map[string]int{}); len(got) != 0 {
		t.Errorf("empty = %v", got)
	}
}

func TestDefaultTitle(t *testing.T) {
	t.Parallel()
	members := []store.Signal{{Excerpt: "first"}, {Excerpt: "  blocked\nby   hook "}}
	tests := []struct {
		name, family, key string
		members           []store.Signal
		want              string
	}{
		{"hook with message", Hook, "PreToolUse:Edit g.sh", members, "Hook PreToolUse:Edit g.sh keeps blocking actions: blocked by hook"},
		{"hook without message", Hook, "k", []store.Signal{{}}, "Hook k keeps blocking actions"},
		{"hook without members", Hook, "k", nil, "Hook k keeps blocking actions"},
		{"failure", Failure, "go test", members, "`go test` fails repeatedly in a row"},
		{"fixup", Fixup, FixupKey, members, "You often hand-correct files right after Claude edits them"},
	}
	for _, tt := range tests {
		if got := DefaultTitle(tt.family, tt.key, tt.members, nil, nil); got != tt.want {
			t.Errorf("%s: %q, want %q", tt.name, got, tt.want)
		}
	}
	long := DefaultTitle(Hook, "k", []store.Signal{{Excerpt: strings.Repeat("x ", 200)}}, nil, nil)
	if len(long) > 110 {
		t.Errorf("hook title length %d", len(long))
	}
	bm := []store.Signal{{Rule: "far"}, {Rule: "Use pnpm"}}
	vecs := [][]float32{{0, 1}, {1, 0}}
	if got := DefaultTitle(Behavior, "", bm, vecs, []float32{1, 0}); got != "Use pnpm" {
		t.Errorf("behavior title = %q", got)
	}
}

func TestRepresentative(t *testing.T) {
	t.Parallel()
	members := []store.Signal{{ID: "a"}, {ID: "b"}, {ID: "c"}}
	vecs := [][]float32{{0, 1}, {1, 0}, {0.6, 0.8}}
	if got := Representative(members, vecs, []float32{1, 0}); got.ID != "b" {
		t.Errorf("closest member = %s", got.ID)
	}
	if got := Representative(members, nil, nil); got.ID != "a" {
		t.Errorf("no vectors falls back to the first member, got %s", got.ID)
	}
}

func TestRunGroupsByFamily(t *testing.T) {
	t.Parallel()
	st := newStore(t)
	sigs := pnpmSignals(3, "p")
	sigs = append(sigs,
		sig{id: "o1", typ: store.Correction, excerpt: "terraform resource names must be snake_case"},
		sig{id: "h1", typ: store.HookBlock, key: "PreToolUse:Edit g.sh", excerpt: "no lockfiles"},
		sig{id: "h2", typ: store.HookBlock, key: "PreToolUse:Edit g.sh", excerpt: "no lockfiles"},
		sig{id: "h3", typ: store.HookBlock, key: "PreToolUse:Bash other.sh", excerpt: "x"},
		sig{id: "f1", typ: store.ToolFailure, key: "go test", excerpt: "x"},
		sig{id: "f2", typ: store.ToolFailure, key: "go test", excerpt: "x"},
		sig{id: "x1", typ: store.ManualFixup, key: "*.go"},
		sig{id: "x2", typ: store.ManualFixup, key: "go.mod"},
	)
	insert(t, st, sigs...)
	stats, err := Run(st, Options{Embedder: embed.Hash{}, MinCount: 2, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Signals != len(sigs) {
		t.Errorf("Signals = %d, want %d", stats.Signals, len(sigs))
	}
	// pnpm, other-behavior, 2 hook keys, 1 failure key, 1 fixup group.
	if stats.Clusters != 6 {
		t.Errorf("Clusters = %d, want 6", stats.Clusters)
	}
	// Groups below MinCount produce no finding: other-behavior and the single hook.
	if len(stats.Findings) != 4 || stats.New != 4 {
		t.Fatalf("findings = %v new = %d", stats.Findings, stats.New)
	}
	fs, err := st.QueryFindings("")
	if err != nil {
		t.Fatal(err)
	}
	byFamily := map[string]store.Finding{}
	for _, f := range fs {
		byFamily[f.Family] = f
		if f.Status != store.StatusOpen || !strings.HasPrefix(f.ID, "F") || f.UpdatedAt != now || f.FirstSeen.IsZero() || f.LastSeen.Before(f.FirstSeen) {
			t.Errorf("finding = %+v", f)
		}
	}
	if len(byFamily) != 4 {
		t.Fatalf("families = %v", byFamily)
	}
	if f := byFamily[Hook]; f.Key != "PreToolUse:Edit g.sh" || !strings.Contains(f.Title, "no lockfiles") {
		t.Errorf("hook finding = %+v", f)
	}
	if f := byFamily[Failure]; f.Key != "go test" || f.Title != "`go test` fails repeatedly in a row" {
		t.Errorf("failure finding = %+v", f)
	}
	if f := byFamily[Fixup]; f.Key != FixupKey || len(mustSignals(t, st, f.ID)) != 2 {
		t.Errorf("fixup finding = %+v", f)
	}
	b := byFamily[Behavior]
	if len(b.Centroid) == 0 || b.Key != "" || len(mustSignals(t, st, b.ID)) != 3 || !strings.Contains(b.Title, "pnpm") {
		t.Errorf("behavior finding = %+v", b)
	}
	// Largest group first.
	if stats.Findings[0] != b.ID {
		t.Errorf("findings not ordered by size: %v", stats.Findings)
	}
}

func mustSignals(t testing.TB, st *store.Store, id string) []store.Signal {
	t.Helper()
	s, err := st.FindingSignals(id)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestRunFiltersSignals(t *testing.T) {
	t.Parallel()
	st := newStore(t)
	ss := pnpmSignals(3, "keep")
	ss = append(ss,
		sig{id: "rej", typ: store.Correction, excerpt: "no, use pnpm instead of npm in this repo", confirmed: 0},
		sig{id: "ign", typ: store.Correction, excerpt: "no, use pnpm instead of npm in this repo", hint: "ignore"},
		sig{id: "bare", typ: store.Interrupt, excerpt: "(interrupted) Bash npm install"},
		sig{id: "conf", typ: store.Correction, excerpt: "no, use pnpm instead of npm in this repo", confirmed: 1},
		sig{id: "old", typ: store.Correction, excerpt: "no, use pnpm instead of npm in this repo", ts: now.Add(-100 * 24 * time.Hour)},
		sig{id: "other", typ: store.Correction, excerpt: "no, use pnpm instead of npm in this repo", repo: "/elsewhere"},
	)
	insert(t, st, ss...)
	stats, err := Run(st, Options{Embedder: embed.Hash{}, MinCount: 1, Now: now, Since: now.Add(-30 * 24 * time.Hour), Repo: "/r"})
	if err != nil {
		t.Fatal(err)
	}
	// keep0..2 and conf only.
	if stats.Signals != 4 {
		t.Errorf("Signals = %d, want 4", stats.Signals)
	}
	linked := 0
	for _, id := range stats.Findings {
		linked += len(mustSignals(t, st, id))
	}
	if linked != 4 {
		t.Errorf("linked = %d", linked)
	}
}

func TestRunDefaults(t *testing.T) {
	t.Parallel()
	st := newStore(t)
	insert(t, st, pnpmSignals(3, "p")...)
	// Zero MinCount means 3, zero Now means the wall clock, zero Threshold
	// the embedder's.
	stats, err := Run(st, Options{Embedder: embed.Hash{}})
	if err != nil || len(stats.Findings) != 1 {
		t.Fatalf("stats = %+v err = %v", stats, err)
	}
	f, _ := st.GetFinding(stats.Findings[0])
	if time.Since(f.UpdatedAt) > time.Minute {
		t.Errorf("UpdatedAt = %v", f.UpdatedAt)
	}
	st2 := newStore(t)
	insert(t, st2, pnpmSignals(2, "p")...)
	stats, _ = Run(st2, Options{Embedder: embed.Hash{}, Now: now})
	if len(stats.Findings) != 0 {
		t.Error("two signals are below the default minimum of 3")
	}
}

func TestRunKeepsFindingIdentityAcrossRuns(t *testing.T) {
	t.Parallel()
	st := newStore(t)
	insert(t, st, pnpmSignals(3, "p")...)
	insert(t, st, sig{id: "h1", typ: store.HookBlock, key: "k", excerpt: "x"}, sig{id: "h2", typ: store.HookBlock, key: "k", excerpt: "x"})
	opt := Options{Embedder: embed.Hash{}, MinCount: 2, Now: now}
	first, err := Run(st, opt)
	if err != nil {
		t.Fatal(err)
	}
	// Mark the behavior finding accepted and synthesized with a custom title.
	for _, id := range first.Findings {
		f, _ := st.GetFinding(id)
		if f.Family == Behavior {
			f.Status, f.Synthesized, f.Title = store.StatusAccepted, true, "Custom title"
			if err := st.SaveFinding(f); err != nil {
				t.Fatal(err)
			}
		}
	}
	insert(t, st, sig{id: "p9", typ: store.Correction, excerpt: "no, use pnpm instead of npm in this repo"})
	second, err := Run(st, opt)
	if err != nil {
		t.Fatal(err)
	}
	if second.New != 0 || !reflect.DeepEqual(sorted(first.Findings), sorted(second.Findings)) {
		t.Errorf("identity changed: %v -> %v (new %d)", first.Findings, second.Findings, second.New)
	}
	for _, id := range second.Findings {
		f, _ := st.GetFinding(id)
		if f.Family != Behavior {
			continue
		}
		if f.Status != store.StatusAccepted || f.Title != "Custom title" {
			t.Errorf("status/title not preserved: %+v", f)
		}
		if got := len(mustSignals(t, st, id)); got != 4 {
			t.Errorf("behavior finding has %d signals, want 4", got)
		}
	}
}

func sorted(s []string) []string {
	out := append([]string(nil), s...)
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

func TestRunRelinksStaleSignals(t *testing.T) {
	t.Parallel()
	st := newStore(t)
	insert(t, st, pnpmSignals(3, "p")...)
	opt := Options{Embedder: embed.Hash{}, MinCount: 1, Now: now}
	first, err := Run(st, opt)
	if err != nil || len(first.Findings) != 1 {
		t.Fatalf("%+v %v", first, err)
	}
	if got := len(mustSignals(t, st, first.Findings[0])); got != 3 {
		t.Fatalf("linked = %d, want 3", got)
	}
	// The classifier now calls one of them a one-off: it stops counting.
	if _, err := st.DB.Exec(`UPDATE signals SET remedy_hint='ignore' WHERE id='p0'`); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(st, opt); err != nil {
		t.Fatal(err)
	}
	if got := len(mustSignals(t, st, first.Findings[0])); got != 2 {
		t.Errorf("linked after ignore = %d, want 2", got)
	}
}

func TestRunNewIDCollision(t *testing.T) {
	t.Parallel()
	st := newStore(t)
	insert(t, st, pnpmSignals(2, "p")...)
	// p1 is the oldest signal, so it seeds the new finding's id. Take that id.
	base := "F" + hash6(Behavior, "p1")
	if err := st.SaveFinding(store.Finding{ID: base, Family: Hook, Key: "unrelated", Status: store.StatusOpen}); err != nil {
		t.Fatal(err)
	}
	stats, err := Run(st, Options{Embedder: embed.Hash{}, MinCount: 1, Now: now})
	if err != nil || len(stats.Findings) != 1 {
		t.Fatalf("%+v %v", stats, err)
	}
	if stats.Findings[0] != base+"-2" {
		t.Errorf("id = %q, want %q", stats.Findings[0], base+"-2")
	}
	old, err := st.GetFinding(base)
	if err != nil || old.Family != Hook {
		t.Errorf("existing finding clobbered: %+v %v", old, err)
	}
}

func hash6(family, seed string) string {
	g := &group{family: family, members: []store.Signal{{ID: seed}}}
	id := newID(g, map[string]bool{})
	return strings.TrimPrefix(id, "F")
}

func TestMatchBehaviorByCentroid(t *testing.T) {
	t.Parallel()
	g := &group{family: Behavior, cent: []float32{1, 0}}
	existing := []store.Finding{
		{ID: "wrongfam", Family: Hook, Centroid: []float32{1, 0}},
		{ID: "taken", Family: Behavior, Centroid: []float32{1, 0}},
		{ID: "far", Family: Behavior, Centroid: []float32{0, 1}},
		{ID: "near", Family: Behavior, Centroid: []float32{0.9, 0.1}},
		{ID: "nearest", Family: Behavior, Centroid: []float32{1, 0}},
	}
	f, ok := match(g, existing, map[string]bool{"taken": true}, 0.8)
	if !ok || f.ID != "nearest" {
		t.Errorf("match = %v %v", f.ID, ok)
	}
	if _, ok := match(g, existing[:3], map[string]bool{"taken": true}, 0.8); ok {
		t.Error("nothing similar enough should not match")
	}
	hg := &group{family: Hook, key: "k"}
	f, ok = match(hg, []store.Finding{{ID: "a", Family: Hook, Key: "z"}, {ID: "b", Family: Hook, Key: "k"}}, map[string]bool{}, 0.5)
	if !ok || f.ID != "b" {
		t.Errorf("keyed match = %v %v", f.ID, ok)
	}
	if _, ok := match(hg, []store.Finding{{ID: "b", Family: Hook, Key: "k"}}, map[string]bool{"b": true}, 0.5); ok {
		t.Error("taken finding must not match twice")
	}
}

type countingEmbedder struct {
	embed.Hash
	name  string
	calls int
	err   error
}

func (c *countingEmbedder) Name() string { return c.name }
func (c *countingEmbedder) Embed(texts []string) ([][]float32, error) {
	c.calls++
	if c.err != nil {
		return nil, c.err
	}
	return c.Hash.Embed(texts)
}

func TestEnsureEmbeddings(t *testing.T) {
	t.Parallel()
	st := newStore(t)
	insert(t, st, pnpmSignals(3, "p")...)
	insert(t, st, sig{id: "h1", typ: store.HookBlock, key: "k", excerpt: "x"})
	e := &countingEmbedder{name: "one"}
	opt := Options{Embedder: e, MinCount: 1, Now: now}
	if _, err := Run(st, opt); err != nil {
		t.Fatal(err)
	}
	if e.calls != 1 || st.Meta("signal_embedder") != "one" {
		t.Errorf("calls = %d meta = %q", e.calls, st.Meta("signal_embedder"))
	}
	var withEmb, hookEmb int
	_ = st.DB.QueryRow(`SELECT COUNT(*) FROM signals WHERE embedding IS NOT NULL AND type='correction'`).Scan(&withEmb)
	_ = st.DB.QueryRow(`SELECT COUNT(*) FROM signals WHERE embedding IS NOT NULL AND type='hook_block'`).Scan(&hookEmb)
	if withEmb != 3 || hookEmb != 0 {
		t.Errorf("stored embeddings: behavior %d, keyed %d", withEmb, hookEmb)
	}
	if _, err := Run(st, opt); err != nil {
		t.Fatal(err)
	}
	if e.calls != 1 {
		t.Errorf("cached embeddings were recomputed (%d calls)", e.calls)
	}
	e2 := &countingEmbedder{name: "two"}
	if _, err := Run(st, Options{Embedder: e2, MinCount: 1, Now: now}); err != nil {
		t.Fatal(err)
	}
	if e2.calls != 1 {
		t.Errorf("a new embedder must re-embed, calls = %d", e2.calls)
	}
	boom := errors.New("embedder down")
	e3 := &countingEmbedder{name: "three", err: boom}
	if _, err := Run(st, Options{Embedder: e3, MinCount: 1, Now: now}); !errors.Is(err, boom) {
		t.Errorf("err = %v", err)
	}
}

func TestRunStoreErrors(t *testing.T) {
	t.Parallel()
	opt := Options{Embedder: embed.Hash{}, MinCount: 1, Now: now}
	for _, drop := range []string{"signals", "findings", "finding_signals", "meta"} {
		t.Run(drop, func(t *testing.T) {
			t.Parallel()
			st := newStore(t)
			insert(t, st, pnpmSignals(3, "p")...)
			if _, err := st.DB.Exec(`DROP TABLE ` + drop); err != nil {
				t.Fatal(err)
			}
			if _, err := Run(st, opt); err == nil {
				t.Error("want error")
			}
		})
	}
	t.Run("closed", func(t *testing.T) {
		t.Parallel()
		st := newStore(t)
		_ = st.Close()
		if _, err := Run(st, opt); err == nil {
			t.Error("want error")
		}
	})
}

func TestRunOnCorpus(t *testing.T) {
	t.Parallel()
	st := corpusStore(t)
	stats, err := Run(st, Options{Embedder: embed.Hash{}, MinCount: 1, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Signals == 0 || stats.Clusters == 0 || len(stats.Findings) == 0 || stats.New != len(stats.Findings) {
		t.Errorf("stats = %+v", stats)
	}
	again, err := Run(st, Options{Embedder: embed.Hash{}, MinCount: 1, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if again.New != 0 || !reflect.DeepEqual(sorted(again.Findings), sorted(stats.Findings)) {
		t.Errorf("not stable: %+v vs %+v", stats, again)
	}
}

func BenchmarkRun(b *testing.B) {
	st := corpusStore(b)
	opt := Options{Embedder: embed.Hash{}, MinCount: 1, Now: now}
	if _, err := Run(st, opt); err != nil { // warm the embedding cache
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := Run(st, opt); err != nil {
			b.Fatal(err)
		}
	}
}
