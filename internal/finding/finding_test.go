package finding

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rossedman/sminer/internal/classify"
	"github.com/rossedman/sminer/internal/cluster"
	"github.com/rossedman/sminer/internal/config"
	"github.com/rossedman/sminer/internal/embed"
	"github.com/rossedman/sminer/internal/ingest"
	"github.com/rossedman/sminer/internal/redact"
	"github.com/rossedman/sminer/internal/remedy"
	"github.com/rossedman/sminer/internal/signal"
	"github.com/rossedman/sminer/internal/store"
)

var now = time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)

// llm drafts a CLAUDE.md line for every behavioural finding, so several
// findings target the same file.
type llm struct{}

func (llm) Complete(_ context.Context, system, user string, _ int) (string, error) {
	if system == classify.SystemPrompt {
		return `{"correction": true, "rule": "x"}`, nil
	}
	n := strings.Count(user, "\n")
	return `{"title": "Rule ` + string(rune('A'+n%20)) + `", "remedy": "claude_md", "rule": "Always follow rule ` + string(rune('A'+n%20)) + `."}`, nil
}

type env struct {
	svc Service
	cfg config.Config
	dir string
}

func setup(t testing.TB, useLLM bool) env {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	dir := t.TempDir()
	cfg := config.Default()
	cfg.PatchDir = filepath.Join(dir, "patches")
	cfg.Remedies = config.Remedies{
		HooksDir: filepath.Join(dir, "hooks"), GlobalClaudeMD: filepath.Join(dir, "CLAUDE.md"),
		SkillsDir: filepath.Join(dir, "skills"), Settings: filepath.Join(dir, "settings.json"),
	}
	red, _ := redact.New(nil)
	ctx := context.Background()
	if _, err := ingest.Run(ctx, st, ingest.Options{Roots: []string{filepath.Join("..", "..", "testdata", "projects")},
		Since: 3650 * 24 * time.Hour, Now: now, Config: cfg, Redact: red}); err != nil {
		t.Fatal(err)
	}
	if _, err := signal.Run(ctx, st, signal.Options{Embedder: embed.Hash{}, Now: now}); err != nil {
		t.Fatal(err)
	}
	cs, err := cluster.Run(st, cluster.Options{Embedder: embed.Hash{}, MinCount: 1, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	opt := remedy.Options{Config: cfg, Existing: remedy.LoadExisting(cfg)}
	if useLLM {
		opt.LLM, opt.Budget = llm{}, classify.NewBudget(100)
	}
	if _, err := remedy.Run(ctx, st, cs.Findings, opt); err != nil {
		t.Fatal(err)
	}
	return env{svc: Service{Store: st, Config: cfg, Now: now}, cfg: cfg, dir: dir}
}

func TestListFilters(t *testing.T) {
	t.Parallel()
	e := setup(t, false)
	all, err := e.svc.List("all")
	if err != nil || len(all) == 0 {
		t.Fatalf("all = %v %v", len(all), err)
	}
	if blank, _ := e.svc.List(""); len(blank) != len(all) {
		t.Errorf(`"" should mean all: %d vs %d`, len(blank), len(all))
	}
	for i := 1; i < len(all); i++ {
		if all[i-1].Score < all[i].Score {
			t.Errorf("not ranked by score at %d", i)
		}
	}
	if _, err := e.svc.List("bogus"); err == nil || !strings.Contains(err.Error(), "bogus") {
		t.Errorf("unknown status err = %v", err)
	}
	open, _ := e.svc.List(store.StatusOpen)
	if len(open) != len(all) {
		t.Errorf("all findings start open: %d vs %d", len(open), len(all))
	}
	if acc, _ := e.svc.List(store.StatusAccepted); len(acc) != 0 {
		t.Errorf("accepted = %d", len(acc))
	}

	id := all[0].ID
	if err := e.svc.Dismiss(id, "noise"); err != nil {
		t.Fatal(err)
	}
	dis, _ := e.svc.List(store.StatusDismissed)
	if len(dis) != 1 || dis[0].ID != id || dis[0].Status != store.StatusDismissed {
		t.Errorf("dismissed = %+v", dis)
	}
	f, _ := e.svc.Store.GetFinding(id)
	if f.Reason != "noise" {
		t.Errorf("reason = %q", f.Reason)
	}
	if open, _ := e.svc.List(store.StatusOpen); len(open) != len(all)-1 {
		t.Errorf("open after dismiss = %d", len(open))
	}

	if err := e.svc.Reopen(id); err != nil {
		t.Fatal(err)
	}
	f, _ = e.svc.Store.GetFinding(id)
	if f.Status != store.StatusOpen || f.Reason != "" {
		t.Errorf("after reopen: %+v", f)
	}
}

func TestListSkipsEmptyOpenFindings(t *testing.T) {
	t.Parallel()
	e := setup(t, false)
	if err := e.svc.Store.SaveFinding(store.Finding{ID: "EMPTY", Title: "orphan", Status: store.StatusOpen}); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Store.SaveFinding(store.Finding{ID: "EMPTYDIS", Title: "orphan", Status: store.StatusDismissed}); err != nil {
		t.Fatal(err)
	}
	all, _ := e.svc.List("all")
	var ids []string
	for _, f := range all {
		ids = append(ids, f.ID)
	}
	if slices.Contains(ids, "EMPTY") || !slices.Contains(ids, "EMPTYDIS") {
		t.Errorf("open orphan must be skipped, dismissed kept: %v", ids)
	}
}

func TestGetAndIDs(t *testing.T) {
	t.Parallel()
	e := setup(t, false)
	all, _ := e.svc.List("all")
	f, err := e.svc.Get(all[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if f.ID != all[0].ID || len(f.Excerpts) < len(all[0].Excerpts) || len(f.Excerpts) > f.Occurrences {
		t.Errorf("Get returns at least the list excerpts: %d vs %d (occurrences %d)", len(f.Excerpts), len(all[0].Excerpts), f.Occurrences)
	}
	if _, err := e.svc.Get("nope"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("err = %v", err)
	}
	ids, err := e.svc.IDs("all")
	if err != nil || len(ids) != len(all) || !strings.Contains(ids[0], "\t"+all[0].Title) || !strings.HasPrefix(ids[0], all[0].ID) {
		t.Errorf("ids = %v %v", ids, err)
	}
	if _, err := e.svc.IDs("bogus"); err == nil {
		t.Error("IDs should surface List errors")
	}
	for _, f := range all {
		if len(f.Excerpts) > 3 {
			t.Errorf("List caps excerpts at 3, got %d", len(f.Excerpts))
		}
	}
}

func TestAcceptPreviewChangesNothing(t *testing.T) {
	t.Parallel()
	e := setup(t, false)
	all, _ := e.svc.List("all")
	res, err := e.svc.Accept(all[0].ID, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Preview || res.PatchPath != "" || res.Diff == "" || res.ID != all[0].ID || !strings.HasPrefix(res.Apply, "cd ") || strings.Contains(res.Apply, "<") {
		t.Errorf("preview = %+v", res)
	}
	if _, err := os.Stat(e.cfg.PatchDir); !os.IsNotExist(err) {
		t.Errorf("preview wrote %s", e.cfg.PatchDir)
	}
	f, _ := e.svc.Store.GetFinding(all[0].ID)
	if f.Status != store.StatusOpen {
		t.Errorf("status = %s", f.Status)
	}
}

func TestAcceptWritesPatchAndRecordsAdoption(t *testing.T) {
	t.Parallel()
	e := setup(t, false)
	all, _ := e.svc.List("all")
	id := all[0].ID
	res, err := e.svc.Accept(id, "", false)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(e.cfg.PatchDir, id+".patch")
	if res.Preview || res.PatchPath != want || !strings.HasSuffix(res.Apply, "< "+want) {
		t.Errorf("result = %+v", res)
	}
	b, err := os.ReadFile(want)
	if err != nil || string(b) != res.Diff {
		t.Errorf("patch file: %v", err)
	}
	if fi, _ := os.Stat(want); fi.Mode().Perm() != 0o600 {
		t.Errorf("patch mode = %v", fi.Mode().Perm())
	}
	f, _ := e.svc.Store.GetFinding(id)
	if f.Status != store.StatusAccepted {
		t.Errorf("status = %s", f.Status)
	}
	view, _ := e.svc.Get(id)
	if view.AdoptedAt == nil || !view.AdoptedAt.Equal(now) {
		t.Errorf("AdoptedAt = %v", view.AdoptedAt)
	}
	var patch string
	_ = e.svc.Store.DB.QueryRow(`SELECT patch_path FROM adoptions WHERE finding_id=?`, id).Scan(&patch)
	if patch != want {
		t.Errorf("adoption patch_path = %q", patch)
	}

	tr, err := e.svc.Trend(id)
	if err != nil || tr.AdoptedAt == nil {
		t.Errorf("trend = %+v %v", tr, err)
	}
	trs, err := e.svc.Trends()
	if err != nil || len(trs) == 0 {
		t.Fatalf("trends = %v %v", len(trs), err)
	}
	for i := 1; i < len(trs); i++ {
		if trs[i-1].BeforeRate < trs[i].BeforeRate {
			t.Errorf("trends not sorted by rate")
		}
	}

	// Reopening drops the adoption record but leaves the patch file.
	if err := e.svc.Reopen(id); err != nil {
		t.Fatal(err)
	}
	view, _ = e.svc.Get(id)
	if view.AdoptedAt != nil {
		t.Error("adoption should be removed on reopen")
	}
	if _, err := os.Stat(want); err != nil {
		t.Errorf("patch file removed: %v", err)
	}
}

func TestAcceptManyCombinesClaudeMDLines(t *testing.T) {
	t.Parallel()
	e := setup(t, true)
	all, _ := e.svc.List("all")
	var mdIDs []string
	for _, f := range all {
		if f.Remedy == remedy.ClaudeMD && f.Target != "" {
			mdIDs = append(mdIDs, f.ID)
		}
	}
	if len(mdIDs) < 2 {
		t.Fatalf("corpus yields %d CLAUDE.md findings, need 2", len(mdIDs))
	}
	ids := append([]string{mdIDs[0], mdIDs[0]}, mdIDs[1:]...) // duplicates collapse
	res, err := e.svc.AcceptMany(ids, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != len(mdIDs) {
		t.Fatalf("results = %d, want %d", len(res), len(mdIDs))
	}
	targets := map[string]bool{}
	for _, r := range res {
		targets[r.Target] = true
	}
	files, _ := filepath.Glob(filepath.Join(e.cfg.PatchDir, "*.patch"))
	if len(files) != len(targets) {
		t.Errorf("patch files = %d, targets = %d", len(files), len(targets))
	}
	if len(targets) == len(res) {
		t.Fatal("the corpus findings share no CLAUDE.md target; combining was not exercised")
	}
	for _, r := range res {
		shared := 0
		for _, o := range res {
			if o.Target == r.Target && o.ID != r.ID {
				shared++
			}
		}
		if len(r.Combined) != shared {
			t.Errorf("%s combined = %v, shares a file with %d", r.ID, r.Combined, shared)
		}
	}
}

func TestAcceptErrors(t *testing.T) {
	t.Parallel()
	e := setup(t, false)
	if _, err := e.svc.Accept("nope", "", true); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("err = %v", err)
	}
	if _, err := e.svc.AcceptMany([]string{}, "", true); err != nil {
		t.Errorf("no ids is a no-op, got %v", err)
	}
	all, _ := e.svc.List("all")
	// One bad id means nothing is written.
	if _, err := e.svc.AcceptMany([]string{all[0].ID, "nope"}, "", false); err == nil {
		t.Error("want error")
	}
	if _, err := os.Stat(e.cfg.PatchDir); !os.IsNotExist(err) {
		t.Error("nothing may be written when one finding fails")
	}
	// The patch dir is a file, so writing fails and the finding stays open.
	blocker := filepath.Join(e.dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Accept(all[0].ID, filepath.Join(blocker, "sub"), false); err == nil {
		t.Error("unwritable dir should fail")
	}
	if f, _ := e.svc.Store.GetFinding(all[0].ID); f.Status != store.StatusOpen {
		t.Errorf("status = %s after failed accept", f.Status)
	}
	// A finding that cannot be patched.
	if err := e.svc.Store.SaveFinding(store.Finding{ID: "BAD", RemedyType: "bogus", Status: store.StatusOpen}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Accept("BAD", "", true); err == nil {
		t.Error("unpatchable finding should fail")
	}
}

func TestStatusErrors(t *testing.T) {
	t.Parallel()
	e := setup(t, false)
	if err := e.svc.Dismiss("nope", "r"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Dismiss err = %v", err)
	}
	if err := e.svc.Reopen("nope"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Reopen err = %v", err)
	}
	if _, err := e.svc.Trend("nope"); err == nil {
		t.Error("Trend of unknown finding")
	}
}

func TestStoreFailures(t *testing.T) {
	t.Parallel()
	for _, drop := range []string{"findings", "sessions", "finding_signals", "signals"} {
		t.Run(drop, func(t *testing.T) {
			t.Parallel()
			e := setup(t, false)
			if _, err := e.svc.Store.DB.Exec(`DROP TABLE ` + drop); err != nil {
				t.Fatal(err)
			}
			if _, err := e.svc.List("all"); err == nil {
				t.Error("List")
			}
			if _, err := e.svc.IDs("all"); err == nil {
				t.Error("IDs")
			}
			if _, err := e.svc.Trends(); err == nil && drop != "sessions" {
				t.Error("Trends")
			}
		})
	}
	t.Run("adoptions", func(t *testing.T) {
		t.Parallel()
		e := setup(t, false)
		all, _ := e.svc.List("all")
		if _, err := e.svc.Store.DB.Exec(`DROP TABLE adoptions`); err != nil {
			t.Fatal(err)
		}
		if _, err := e.svc.Accept(all[0].ID, "", false); err == nil {
			t.Error("recording the adoption must fail the accept")
		}
		if err := e.svc.Reopen(all[0].ID); err == nil {
			t.Error("Reopen must report the failed adoption delete")
		}
		// Missing adoption data just means "not adopted".
		if v, err := e.svc.Get(all[0].ID); err != nil || v.AdoptedAt != nil {
			t.Errorf("Get = %v %v", v.AdoptedAt, err)
		}
	})
	t.Run("get with missing sessions", func(t *testing.T) {
		t.Parallel()
		e := setup(t, false)
		all, _ := e.svc.List("all")
		_, _ = e.svc.Store.DB.Exec(`DROP TABLE sessions`)
		if _, err := e.svc.Get(all[0].ID); err == nil {
			t.Error("Get")
		}
	})
}

func BenchmarkList(b *testing.B) {
	e := setup(b, false)
	b.ReportAllocs()
	for b.Loop() {
		if _, err := e.svc.List("all"); err != nil {
			b.Fatal(err)
		}
	}
}
