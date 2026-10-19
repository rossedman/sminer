package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rossedman/sminer/internal/classify"
	"github.com/rossedman/sminer/internal/report"
)

var corpus, _ = filepath.Abs("../../testdata/projects")

type env struct {
	dir  string
	args []string
}

func newEnv(t *testing.T) env {
	t.Helper()
	nowFunc = func() time.Time { return time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC) }
	t.Cleanup(func() { nowFunc = time.Now })
	dir := t.TempDir()
	cfg := `roots: [` + corpus + `]
window: 90d
embeddings: {provider: hash}
classifier: {model: fake, max_calls_per_run: 200, concurrency: 2}
remedies:
  hooks_dir: ` + dir + `/hooks
  global_claude_md: ` + dir + `/CLAUDE.md
  skills_dir: ` + dir + `/skills
  settings: ` + dir + `/settings.json
patch_dir: ` + dir + `/patches
`
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	return env{dir: dir, args: []string{"--config", filepath.Join(dir, "config.yaml"), "--db", filepath.Join(dir, "s.db")}}
}

func (e env) run(t *testing.T, args ...string) string {
	t.Helper()
	var out, errb bytes.Buffer
	if err := run(context.Background(), append(args, e.args...), &out, &errb); err != nil {
		t.Fatalf("sminer %v: %v\n%s%s", args, err, out.String(), errb.String())
	}
	return out.String()
}

func (e env) findings(t *testing.T) report.Report {
	t.Helper()
	var r report.Report
	if err := json.Unmarshal([]byte(e.run(t, "report", "--format", "json", "--out", "-")), &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func byTitle(r report.Report, sub string) *report.Finding {
	for i := range r.Findings {
		if strings.Contains(r.Findings[i].Title, sub) {
			return &r.Findings[i]
		}
	}
	return nil
}

func TestRunOffline(t *testing.T) {
	e := newEnv(t)
	out := e.run(t, "run", "--no-llm", "--out", filepath.Join(e.dir, "report.md"))
	if strings.Contains(out, "classify:") {
		t.Errorf("--no-llm still classified:\n%s", out)
	}
	md, err := os.ReadFile(filepath.Join(e.dir, "report.md"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(e.dir, "findings.json")); err != nil {
		t.Error("findings.json not written beside the report")
	}
	for _, want := range []string{"## Findings", "## Guardrail activity", "PreToolUse:Edit no-lockfiles.sh", "claude --resume", "weird-new-type"} {
		if !strings.Contains(string(md), want) {
			t.Errorf("report missing %q", want)
		}
	}

	r := e.findings(t)
	want := map[string]string{
		"testify":         "claude_md",
		"package-lock":    "hook",
		"`go test`":       "claude_md",
		"*.go files":      "claude_md",
		"Before commit":   "skill",
		"no-lockfiles.sh": "claude_md",
	}
	for sub, remedy := range want {
		f := byTitle(r, sub)
		if f == nil {
			t.Errorf("no finding titled like %q", sub)
			continue
		}
		if f.Remedy != remedy {
			t.Errorf("%q remedy = %s, want %s", sub, f.Remedy, remedy)
		}
		if len(f.Excerpts) == 0 || len(f.Excerpts) > 3 {
			t.Errorf("%q has %d excerpts", sub, len(f.Excerpts))
		}
	}
	for i := 1; i < len(r.Findings); i++ {
		if r.Findings[i].Score > r.Findings[i-1].Score {
			t.Error("findings not ranked by score")
		}
	}
	for _, f := range r.Findings {
		for _, ex := range f.Excerpts {
			if strings.Contains(ex.Text, "ghp_") {
				t.Error("secret leaked into report")
			}
		}
	}

	// Stable IDs across a re-run, and dismiss/accept survive re-analysis.
	ids := map[string]bool{}
	for _, f := range r.Findings {
		ids[f.ID] = true
	}
	tf := byTitle(r, "`go test`")
	hook := byTitle(r, "package-lock")
	e.run(t, "dismiss", tf.ID, "--reason", "flaky env")
	acc := e.run(t, "accept", hook.ID)
	if !strings.Contains(acc, "patch written") || !strings.Contains(acc, `"matcher": "Edit|Write|MultiEdit"`) {
		t.Errorf("accept output:\n%s", acc)
	}
	if _, err := os.Stat(filepath.Join(e.dir, "patches", hook.ID+".patch")); err != nil {
		t.Error(err)
	}
	e.run(t, "analyze", "--no-llm")
	r2 := e.findings(t)
	for _, f := range r2.Findings {
		if !ids[f.ID] {
			t.Errorf("new id %s (%s) after re-run", f.ID, f.Title)
		}
		if f.ID == tf.ID {
			t.Error("dismissed finding still reported")
		}
		if f.ID == hook.ID && (f.Status != "accepted" || f.AdoptedAt == nil) {
			t.Errorf("accepted finding = %+v", f)
		}
	}

	show := e.run(t, "show", hook.ID)
	if strings.Count(show, "claude --resume") != 3 {
		t.Errorf("show should list all 3 occurrences:\n%s", show)
	}
	trend := e.run(t, "trend", hook.ID)
	if !strings.Contains(trend, "Before adoption") {
		t.Errorf("trend:\n%s", trend)
	}
	if !strings.Contains(e.run(t, "trend"), hook.ID) {
		t.Error("trend table missing finding")
	}
}

// recordingLLM fakes the classifier and synthesis calls and keeps every
// prompt so the test can check what would leave the machine.
type recordingLLM struct {
	mu      sync.Mutex
	prompts []string
}

func (l *recordingLLM) Complete(_ context.Context, system, user string, _ int) (string, error) {
	l.mu.Lock()
	l.prompts = append(l.prompts, user)
	l.mu.Unlock()
	if system != classify.SystemPrompt {
		switch {
		case strings.Contains(user, "testify"):
			return `{"title": "Synthesized: prefer stdlib table tests", "remedy": "claude_md", "rule": "Write table-driven tests with the standard library; do not use testify."}`, nil
		case strings.Contains(user, "package-lock"):
			return `{"title": "Synthesized: package-lock edits", "remedy": "hook", "rule": "Never hand-edit package-lock.json", "hook": {"matcher": "Edit|Write|MultiEdit", "field": "file_path", "pattern": "package-lock\\.json$"}}`, nil
		case strings.Contains(user, "snake_case"):
			return `{"title": "Synthesized: snake_case Terraform names", "remedy": "claude_md", "rule": "Use snake_case for Terraform resource names."}`, nil
		case strings.Contains(user, "Before committing"):
			return `{"title": "Synthesized: pre-commit routine", "remedy": "skill", "rule": "Run lint and tests before committing", "skill": {"name": "pre-commit", "description": "Before any commit", "steps": ["make lint", "make test", "conventional commit"]}}`, nil
		}
		return `{"title": "noise", "remedy": "ignore", "rule": ""}`, nil
	}
	switch {
	case strings.Contains(user, "testify"):
		return `{"correction": true, "rule": "Write table-driven tests with the standard library, not testify/assert", "remedy": "claude_md"}`, nil
	case strings.Contains(user, "package-lock"):
		return `{"correction": true, "rule": "Never hand-edit package-lock.json; regenerate it with npm install", "remedy": "hook"}`, nil
	case strings.Contains(user, "snake_case"):
		return `{"correction": true, "rule": "Use snake_case for Terraform resource names", "remedy": "claude_md"}`, nil
	case strings.Contains(user, "Before committing"):
		return `{"correction": true, "rule": "Before committing, run make lint and make test, then write a conventional commit message", "remedy": "skill"}`, nil
	}
	return `{"correction": false}`, nil
}

func TestRunWithClassifier(t *testing.T) {
	e := newEnv(t)
	llm := &recordingLLM{}
	LLMFactory = func(string) classify.LLM { return llm }
	t.Cleanup(func() { LLMFactory = func(m string) classify.LLM { return classify.NewAnthropic(m) } })

	out := e.run(t, "run", "--out", filepath.Join(e.dir, "report.md"))
	if !strings.Contains(out, "classify:") || !strings.Contains(out, "synthesized") {
		t.Errorf("output:\n%s", out)
	}
	r := e.findings(t)
	if f := byTitle(r, "Synthesized: prefer stdlib"); f == nil {
		t.Error("synthesized title missing")
	}
	if f := byTitle(r, "Synthesized: pre-commit"); f == nil || f.Remedy != "skill" || len(f.Proposal.Steps) != 3 {
		t.Errorf("skill finding = %+v", f)
	}
	// With classifier rules, all three Terraform corrections cluster.
	if f := byTitle(r, "Synthesized: snake_case"); f == nil || f.Occurrences != 3 {
		t.Errorf("terraform finding = %+v", f)
	}
	if f := byTitle(r, "Synthesized: package-lock"); f == nil || f.Remedy != "hook" {
		t.Errorf("lockfile finding = %+v", f)
	}

	for _, p := range llm.prompts {
		if len(p) > 2600 {
			t.Errorf("prompt is %d bytes", len(p))
		}
		for _, leak := range []string{"ghp_", "FAIL\tapi", "undefined: Widget", "package api", "Editing lockfiles is not allowed"} {
			if strings.Contains(p, leak) {
				t.Errorf("prompt leaked %q:\n%s", leak, p)
			}
		}
	}

	// Classifier results are cached: a second analyze makes no new
	// classification calls.
	before := len(llm.prompts)
	out = e.run(t, "analyze")
	if !strings.Contains(out, "0 pending") {
		t.Errorf("re-classified:\n%s", out)
	}
	if len(llm.prompts) != before {
		t.Errorf("made %d new calls", len(llm.prompts)-before)
	}
}
