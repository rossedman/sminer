package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rossedman/sminer/internal/classify"
	"github.com/rossedman/sminer/internal/config"
	"github.com/rossedman/sminer/internal/store"
)

var now = time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)

var corpus = filepath.Join("..", "..", "testdata", "projects")

func testEnv(t testing.TB) Env {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Embeddings.Provider = "hash"
	cfg.Remedies = config.Remedies{
		HooksDir:       filepath.Join(dir, "hooks"),
		GlobalClaudeMD: filepath.Join(dir, "CLAUDE.md"),
		SkillsDir:      filepath.Join(dir, "skills"),
		Settings:       filepath.Join(dir, "settings.json"),
	}
	cfg.PatchDir = filepath.Join(dir, "patches")
	return Env{Store: st, Config: cfg, Now: now}
}

var scope = Scope{Window: 3650 * 24 * time.Hour, MinCount: 1}

// fakeLLM answers classifier and drafting requests.
type fakeLLM struct {
	mu       sync.Mutex
	classify int
	draft    int
	failAll  error
}

func (f *fakeLLM) Complete(_ context.Context, system, user string, _ int) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failAll != nil {
		return "", f.failAll
	}
	if system == classify.SystemPrompt {
		f.classify++
		return `{"correction": true, "rule": "Use pnpm instead of npm", "remedy": "hook", "confidence": 0.9}`, nil
	}
	f.draft++
	return `{"title": "Use pnpm", "remedy": "hook", "rule": "Use pnpm, not npm.", "hook": {"matcher": "Bash", "field": "command", "pattern": "^npm "}}`, nil
}

func TestIngestAnalyzeReportOffline(t *testing.T) {
	t.Parallel()
	env := testEnv(t)
	var msgs []string
	env.Progress = func(format string, args ...any) { msgs = append(msgs, format) }

	ing, err := Ingest(t.Context(), env, []string{corpus}, scope.Window)
	if err != nil {
		t.Fatal(err)
	}
	if ing.Files == 0 || ing.Turns == 0 || ing.Sessions == 0 || ing.UnknownTypes == nil || ing.Versions == nil {
		t.Errorf("ingest = %+v", ing)
	}
	an, err := Analyze(t.Context(), env, scope)
	if err != nil {
		t.Fatal(err)
	}
	if an.Embedder != "hash-v4" || an.Classifier != nil || an.LLMCalls != 0 || an.SessionsRescanned == 0 {
		t.Errorf("analyze = %+v", an)
	}
	if len(an.Signals) == 0 || an.Findings == nil || an.Remedies == nil || an.Warnings == nil {
		t.Errorf("result maps must be non-nil: %+v", an)
	}
	if an.Clusters == 0 || an.NewFindings == 0 || len(an.Findings) == 0 {
		t.Errorf("no findings from the corpus: %+v", an)
	}
	if len(msgs) < 4 {
		t.Errorf("progress messages = %v", msgs)
	}

	rep, err := Report(env, scope)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Findings) == 0 {
		t.Error("report has no findings")
	}
	b, err := json.Marshal(an)
	if err != nil || !strings.Contains(string(b), `"elapsed":"`) {
		t.Errorf("json = %s (%v)", b, err)
	}

	// Re-running is idempotent: same findings, none new.
	again, err := Analyze(t.Context(), env, scope)
	if err != nil {
		t.Fatal(err)
	}
	if again.NewFindings != 0 || again.SessionsRescanned != 0 || len(again.Findings) != len(an.Findings) {
		t.Errorf("re-run = %+v", again)
	}
}

func TestAnalyzeWithClassifier(t *testing.T) {
	t.Parallel()
	env := testEnv(t)
	if _, err := Ingest(t.Context(), env, []string{corpus}, scope.Window); err != nil {
		t.Fatal(err)
	}
	llm := &fakeLLM{}
	var model string
	env.NewLLM = func(m string) classify.LLM { model = m; return llm }
	an, err := Analyze(t.Context(), env, scope)
	if err != nil {
		t.Fatal(err)
	}
	if model != env.Config.Classifier.Model {
		t.Errorf("model = %q", model)
	}
	if an.Classifier == nil || an.Classifier.Calls == 0 || an.Classifier.Confirmed == 0 || an.Classifier.Model != model || an.Classifier.Pending == 0 {
		t.Fatalf("classifier = %+v", an.Classifier)
	}
	if llm.classify == 0 || llm.draft == 0 || an.Synthesized == 0 {
		t.Errorf("classify=%d draft=%d synthesized=%d", llm.classify, llm.draft, an.Synthesized)
	}
	if an.LLMCalls != llm.classify+llm.draft {
		t.Errorf("LLMCalls = %d, want %d", an.LLMCalls, llm.classify+llm.draft)
	}
	if len(an.Warnings) != 0 {
		t.Errorf("warnings = %v", an.Warnings)
	}
}

func TestAnalyzeClassifierFailureFallsBack(t *testing.T) {
	t.Parallel()
	env := testEnv(t)
	if _, err := Ingest(t.Context(), env, []string{corpus}, scope.Window); err != nil {
		t.Fatal(err)
	}
	llm := &fakeLLM{failAll: errors.New("401 invalid x-api-key")}
	env.Config.Classifier.Concurrency = 1
	env.NewLLM = func(string) classify.LLM { return llm }
	an, err := Analyze(t.Context(), env, scope)
	if err != nil {
		t.Fatal(err)
	}
	if len(an.Warnings) == 0 {
		t.Fatalf("want a warning about the classifier: %+v", an)
	}
	w := strings.Join(an.Warnings, "\n")
	if !strings.Contains(w, "classifier") {
		t.Errorf("warnings = %q", w)
	}
	if an.Classifier == nil || an.Classifier.Errors == 0 {
		t.Errorf("classifier = %+v", an.Classifier)
	}
	if an.Synthesized != 0 {
		t.Error("an aborted classifier must disable drafting too")
	}
}

func TestAnalyzeOfflineAndExcludedRepoSkipClassifier(t *testing.T) {
	t.Parallel()
	for name, mutate := range map[string]func(*Env, *Scope){
		"offline":       func(e *Env, _ *Scope) { e.Config.Offline = true },
		"excluded repo": func(e *Env, s *Scope) { s.Repo = "/work/api"; e.Config.ExcludeRepos = []string{"/work/api"} },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			env := testEnv(t)
			sc := scope
			mutate(&env, &sc)
			if _, err := Ingest(t.Context(), env, []string{corpus}, sc.Window); err != nil {
				t.Fatal(err)
			}
			llm := &fakeLLM{}
			env.NewLLM = func(string) classify.LLM { return llm }
			an, err := Analyze(t.Context(), env, sc)
			if err != nil {
				t.Fatal(err)
			}
			if llm.classify+llm.draft != 0 || an.Classifier != nil {
				t.Errorf("LLM was used: %+v", an)
			}
		})
	}
}

func TestAnalyzeScopeRepo(t *testing.T) {
	t.Parallel()
	env := testEnv(t)
	if _, err := Ingest(t.Context(), env, []string{corpus}, scope.Window); err != nil {
		t.Fatal(err)
	}
	sc := scope
	sc.Repo = "/no/such/repo"
	an, err := Analyze(t.Context(), env, sc)
	if err != nil {
		t.Fatal(err)
	}
	if an.Clusters != 0 || len(an.Findings) != 0 {
		t.Errorf("a repo with no signals should yield no findings: %+v", an)
	}
}

func TestErrors(t *testing.T) {
	t.Parallel()
	t.Run("bad redact pattern", func(t *testing.T) {
		t.Parallel()
		env := testEnv(t)
		env.Config.Redact.Patterns = []string{"("}
		if _, err := Ingest(t.Context(), env, []string{corpus}, scope.Window); err == nil {
			t.Error("want error")
		}
	})
	t.Run("missing root", func(t *testing.T) {
		t.Parallel()
		env := testEnv(t)
		res, err := Ingest(t.Context(), env, []string{filepath.Join(t.TempDir(), "nope")}, scope.Window)
		if err == nil {
			t.Error("want error")
		}
		if res.UnknownTypes == nil || res.Versions == nil {
			t.Error("maps must be non-nil even on error")
		}
	})
	t.Run("unknown embedder", func(t *testing.T) {
		t.Parallel()
		env := testEnv(t)
		env.Config.Embeddings.Provider = "nope"
		if _, err := Analyze(t.Context(), env, scope); err == nil {
			t.Error("want error")
		}
	})
	t.Run("closed store", func(t *testing.T) {
		t.Parallel()
		env := testEnv(t)
		_ = env.Store.Close()
		if _, err := Analyze(t.Context(), env, scope); err == nil {
			t.Error("analyze")
		}
		if _, err := Report(env, scope); err == nil {
			t.Error("report")
		}
	})
	for _, table := range []string{"findings", "signals", "finding_signals"} {
		t.Run("missing "+table, func(t *testing.T) {
			t.Parallel()
			env := testEnv(t)
			if _, err := Ingest(t.Context(), env, []string{corpus}, scope.Window); err != nil {
				t.Fatal(err)
			}
			if _, err := env.Store.DB.Exec(`DROP TABLE ` + table); err != nil {
				t.Fatal(err)
			}
			if _, err := Analyze(t.Context(), env, scope); err == nil {
				t.Error("want error")
			}
		})
	}
	t.Run("cancelled before ingest completes", func(t *testing.T) {
		t.Parallel()
		env := testEnv(t)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if _, err := Ingest(ctx, env, []string{corpus}, scope.Window); err == nil {
			t.Error("want error")
		}
	})
	t.Run("cancelled after classify", func(t *testing.T) {
		t.Parallel()
		env := testEnv(t)
		if _, err := Ingest(t.Context(), env, []string{corpus}, scope.Window); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		env.NewLLM = func(string) classify.LLM { return cancelLLM{cancel} }
		if _, err := Analyze(ctx, env, scope); !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
	})
}

// cancelLLM cancels the run on its first call.
type cancelLLM struct{ cancel func() }

func (c cancelLLM) Complete(context.Context, string, string, int) (string, error) {
	c.cancel()
	return `{"correction": true, "rule": "r"}`, nil
}

func TestDraftingErrorsBecomeWarnings(t *testing.T) {
	t.Parallel()
	env := testEnv(t)
	if _, err := Ingest(t.Context(), env, []string{corpus}, scope.Window); err != nil {
		t.Fatal(err)
	}
	env.NewLLM = func(string) classify.LLM { return draftFails{} }
	an, err := Analyze(t.Context(), env, scope)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(an.Warnings, "\n"), "proposal drafting") {
		t.Errorf("warnings = %v", an.Warnings)
	}
}

type draftFails struct{}

func (draftFails) Complete(_ context.Context, system, _ string, _ int) (string, error) {
	if system == classify.SystemPrompt {
		return `{"correction": true, "rule": "Use pnpm instead of npm"}`, nil
	}
	return "", errors.New("overloaded")
}

func TestDuration(t *testing.T) {
	t.Parallel()
	d := Duration(1234567 * time.Microsecond)
	if d.String() != "1.235s" {
		t.Errorf("String = %q", d.String())
	}
	b, err := d.MarshalText()
	if err != nil || string(b) != "1.235s" {
		t.Errorf("MarshalText = %q %v", b, err)
	}
	out, _ := json.Marshal(map[string]Duration{"e": d})
	if string(out) != `{"e":"1.235s"}` {
		t.Errorf("json = %s", out)
	}
}

func TestNonNilAndScope(t *testing.T) {
	t.Parallel()
	if m := nonNil(nil); m == nil || len(m) != 0 {
		t.Errorf("nonNil(nil) = %v", m)
	}
	in := map[string]int{"a": 1}
	if m := nonNil(in); m["a"] != 1 {
		t.Errorf("nonNil kept map = %v", m)
	}
	s := Scope{Window: 48 * time.Hour}
	if got := s.since(now); !got.Equal(now.Add(-48 * time.Hour)) {
		t.Errorf("since = %v", got)
	}
	var e Env
	e.progress("no callback is fine %d", 1) // must not panic
}

func BenchmarkAnalyze(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		b.StopTimer()
		env := testEnv(b)
		if _, err := Ingest(context.Background(), env, []string{corpus}, scope.Window); err != nil {
			b.Fatal(err)
		}
		b.StartTimer()
		if _, err := Analyze(context.Background(), env, scope); err != nil {
			b.Fatal(err)
		}
	}
}
