package cli

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
	"github.com/rossedman/sminer/internal/finding"
	"github.com/rossedman/sminer/internal/report"
)

var corpus, _ = filepath.Abs("../../testdata/projects")

type harness struct {
	t   *testing.T
	dir string
	llm *recordingLLM
}

type result struct {
	code           int
	stdout, stderr string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	dir := t.TempDir()
	// Keep every default path inside the test directory.
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "data"))
	t.Setenv("NO_COLOR", "1")
	cfg := `roots: [` + corpus + `]
window: 90d
embeddings: {provider: hash}
classifier: {model: fake, max_calls_per_run: 200, concurrency: 2}
remedies:
  hooks_dir: ` + dir + `/hooks
  global_claude_md: ` + dir + `/CLAUDE.md
  skills_dir: ` + dir + `/skills
  settings: ` + dir + `/settings.json
`
	os.MkdirAll(filepath.Join(dir, "config", "sminer"), 0o700)
	if err := os.WriteFile(filepath.Join(dir, "config", "sminer", "config.yaml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	return &harness{t: t, dir: dir, llm: &recordingLLM{}}
}

func (h *harness) run(argv ...string) result {
	h.t.Helper()
	return h.runWith(Options{}, argv...)
}

// typing runs with stdin as an interactive terminal fed input.
func (h *harness) typing(input string, argv ...string) result {
	h.t.Helper()
	return h.runWith(Options{Stdin: strings.NewReader(input), ForceInteractive: true}, argv...)
}

func (h *harness) runWith(opts Options, argv ...string) result {
	h.t.Helper()
	var out, errb bytes.Buffer
	opts.Stdout, opts.Stderr = &out, &errb
	opts.Now = func() time.Time { return time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC) }
	opts.NewLLM = func(string) classify.LLM { return h.llm }
	if opts.Stdin == nil {
		opts.Stdin = strings.NewReader("")
	}
	code := Run(context.Background(), argv, opts)
	return result{code, out.String(), errb.String()}
}

// ok runs and fails the test on a non-zero exit.
func (h *harness) ok(argv ...string) result {
	h.t.Helper()
	r := h.run(argv...)
	if r.code != 0 {
		h.t.Fatalf("sminer %v exited %d\nstdout:\n%s\nstderr:\n%s", argv, r.code, r.stdout, r.stderr)
	}
	return r
}

func (h *harness) list(status string) []report.Finding {
	h.t.Helper()
	var fs []report.Finding
	if err := json.Unmarshal([]byte(h.ok("finding", "list", "--status", status, "--json").stdout), &fs); err != nil {
		h.t.Fatal(err)
	}
	return fs
}

func byTitle(fs []report.Finding, sub string) *report.Finding {
	for i := range fs {
		if strings.Contains(fs[i].Title, sub) {
			return &fs[i]
		}
	}
	return nil
}

func TestHelpAndUsageErrors(t *testing.T) {
	h := newHarness(t)
	tests := []struct {
		argv     []string
		code     int
		inStdout string
		inStderr string
	}{
		{nil, ExitOK, "Exit codes:", ""},
		{[]string{"--help"}, ExitOK, "ACTING ON FINDINGS", ""},
		{[]string{"help", "finding"}, ExitOK, "sminer finding list", ""},
		{[]string{"finding", "accept", "-h"}, ExitOK, "--preview", ""},
		{[]string{"review"}, ExitUsage, "", "sminer finding list"},
		{[]string{"review", "--status", "bogus"}, ExitUsage, "", "unknown status"},
		{[]string{"analyz"}, ExitUsage, "", "Did you mean this?"},
		{[]string{"run", "--bogus"}, ExitUsage, "", "run 'sminer run --help'"},
		{[]string{"finding", "show"}, ExitUsage, "", "accepts 1 arg"},
		{[]string{"report", "--quiet", "--verbose"}, ExitUsage, "", "none of the others can be"},
		{[]string{"--config", "/does/not/exist.yaml", "finding", "list"}, ExitUsage, "", "config file not found"},
		{[]string{"finding", "list", "--status", "bogus"}, ExitUsage, "", "unknown status"},
		{[]string{"run", "--since", "soon"}, ExitUsage, "", "bad duration"},
		{[]string{"report", "--min-count", "0"}, ExitUsage, "", "at least 1"},
	}
	for _, tt := range tests {
		r := h.run(tt.argv...)
		if r.code != tt.code {
			t.Errorf("%v: exit %d, want %d\n%s%s", tt.argv, r.code, tt.code, r.stdout, r.stderr)
		}
		if !strings.Contains(r.stdout, tt.inStdout) || !strings.Contains(r.stderr, tt.inStderr) {
			t.Errorf("%v: stdout %q / stderr %q missing %q / %q", tt.argv, r.stdout, r.stderr, tt.inStdout, tt.inStderr)
		}
	}
}

func TestErrorsAndEmptyResults(t *testing.T) {
	h := newHarness(t)
	r := h.run("finding", "list")
	if r.code != 0 || r.stdout != "" {
		t.Errorf("empty list should print nothing and exit 0: %+v", r)
	}
	if r := h.ok("finding", "list", "--json"); strings.TrimSpace(r.stdout) != "[]" {
		t.Errorf("empty JSON list = %q", r.stdout)
	}

	r = h.run("finding", "show", "F000000")
	if r.code != ExitNotFound || !strings.HasPrefix(r.stderr, "error: finding F000000") || !strings.Contains(r.stderr, "hint:") {
		t.Errorf("not found: %+v", r)
	}
	r = h.run("finding", "show", "F000000", "--json")
	var e struct {
		Error struct {
			Code string `json:"code"`
			Exit int    `json:"exit"`
			Hint string `json:"hint"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(r.stderr), &e); err != nil || e.Error.Code != "not_found" || e.Error.Exit != 4 || e.Error.Hint == "" {
		t.Errorf("JSON error = %q (%v)", r.stderr, err)
	}
	if r.stdout != "" {
		t.Errorf("error leaked onto stdout: %q", r.stdout)
	}
}

func TestRunOffline(t *testing.T) {
	h := newHarness(t)
	r := h.ok("run", "--offline")
	if len(h.llm.prompts) != 0 {
		t.Errorf("--offline made %d LLM calls", len(h.llm.prompts))
	}
	for _, want := range []string{"# sminer report", "## Findings", "## Guardrail activity", "claude --resume", "weird-new-type"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("report on stdout missing %q", want)
		}
	}
	if strings.Contains(r.stdout, "ingested") || !strings.Contains(r.stderr, "ingested") {
		t.Error("progress belongs on stderr, not stdout")
	}
	if strings.Contains(r.stdout+r.stderr, "\x1b") {
		t.Error("escape codes in output that isn't a terminal")
	}
	if strings.Contains(r.stdout, "ghp_") {
		t.Error("secret leaked into report")
	}

	fs := h.list("open")
	want := map[string]string{
		"testify":         "claude_md",
		"package-lock":    "hook",
		"`go test`":       "claude_md",
		"hand-correct":    "claude_md",
		"Before commit":   "skill",
		"no-lockfiles.sh": "claude_md",
	}
	for sub, remedy := range want {
		f := byTitle(fs, sub)
		if f == nil {
			t.Errorf("no finding titled like %q", sub)
			continue
		}
		if f.Remedy != remedy {
			t.Errorf("%q remedy = %s, want %s", sub, f.Remedy, remedy)
		}
	}
	for i := 1; i < len(fs); i++ {
		if fs[i].Score > fs[i-1].Score {
			t.Error("findings not ranked by score")
		}
	}

	// The plain list is line-oriented: one finding per line, no header
	// when stdout is not a terminal.
	plain := h.ok("finding", "list").stdout
	if n := strings.Count(plain, "\n"); n != len(fs) || strings.Contains(plain, "TITLE") {
		t.Errorf("plain list has %d lines for %d findings:\n%s", n, len(fs), plain)
	}

	// Lifecycle: preview changes nothing; accept, dismiss and reopen
	// survive re-analysis with stable ids.
	hook := byTitle(fs, "package-lock")
	tf := byTitle(fs, "`go test`")
	pr := h.ok("finding", "accept", hook.ID, "--preview")
	if !strings.Contains(pr.stdout, "+++ b/sminer-") || byTitle(h.list("accepted"), "package-lock") != nil {
		t.Errorf("preview should print the diff and change nothing:\n%s", pr.stdout)
	}
	acc := h.ok("finding", "accept", hook.ID)
	patch := strings.TrimSpace(acc.stdout)
	if _, err := os.Stat(patch); err != nil || !strings.HasPrefix(patch, filepath.Join(h.dir, "data", "sminer", "patches")) {
		t.Errorf("accept should print the patch path on stdout: %q (%v)", acc.stdout, err)
	}
	if !strings.Contains(acc.stderr, "undo with: sminer finding reopen "+hook.ID) || !strings.Contains(acc.stderr, `"matcher": "Edit|Write|MultiEdit"`) {
		t.Errorf("accept stderr:\n%s", acc.stderr)
	}
	d := h.ok("finding", "dismiss", tf.ID, "--reason", "flaky env")
	if !strings.Contains(d.stderr, "undo with") || d.stdout != "" {
		t.Errorf("dismiss output: %+v", d)
	}

	h.ok("analyze", "--offline")
	ids := map[string]bool{}
	for _, f := range fs {
		ids[f.ID] = true
	}
	for _, f := range h.list("all") {
		if !ids[f.ID] {
			t.Errorf("new id %s (%s) after re-analysis", f.ID, f.Title)
		}
	}
	if f := byTitle(h.list("accepted"), "package-lock"); f == nil || f.AdoptedAt == nil {
		t.Errorf("accepted finding = %+v", f)
	}
	if byTitle(h.list("open"), "`go test`") != nil {
		t.Error("dismissed finding listed as open")
	}
	if strings.Contains(h.ok("report").stdout, tf.Title) {
		t.Error("dismissed finding still in report")
	}
	h.ok("finding", "reopen", tf.ID, hook.ID)
	if len(h.list("open")) != len(fs) {
		t.Error("reopen did not restore findings")
	}

	// Accepting a hook whose script already exists is a precondition failure.
	os.MkdirAll(filepath.Join(h.dir, "hooks"), 0o755)
	h.ok("finding", "accept", hook.ID) // writes the patch, not the script
	var res []finding.AcceptResult
	json.Unmarshal([]byte(h.ok("finding", "accept", hook.ID, "--preview", "--json").stdout), &res)
	os.WriteFile(res[0].Target, []byte("#!/bin/sh\n"), 0o755)
	if r := h.run("finding", "accept", hook.ID); r.code != ExitConflict || !strings.Contains(r.stderr, "hint:") {
		t.Errorf("existing target: %+v", r)
	}

	show := h.ok("finding", "show", hook.ID).stdout
	if strings.Count(show, "claude --resume") != 3 {
		t.Errorf("show should list all 3 occurrences:\n%s", show)
	}
	if tr := h.ok("finding", "trend", hook.ID).stdout; !strings.Contains(tr, "Before adoption") {
		t.Errorf("trend:\n%s", tr)
	}
	if !strings.Contains(h.ok("finding", "trend").stdout, hook.ID) {
		t.Error("trend table missing finding")
	}
}

func TestReportOutputFiles(t *testing.T) {
	h := newHarness(t)
	h.ok("ingest")
	h.ok("analyze", "--offline")
	out := filepath.Join(h.dir, "out", "report.md")
	r := h.ok("report", "--output", out)
	if r.stdout != "" {
		t.Errorf("--output should keep stdout empty: %q", r.stdout)
	}
	for _, p := range []string{out, filepath.Join(h.dir, "out", "findings.json")} {
		if _, err := os.Stat(p); err != nil {
			t.Error(err)
		}
	}
	var rep report.Report
	if err := json.Unmarshal([]byte(h.ok("report", "--json").stdout), &rep); err != nil || len(rep.Findings) == 0 {
		t.Errorf("report --json: %v", err)
	}
	if r := h.ok("report", "--quiet", "--output", out); r.stderr != "" {
		t.Errorf("--quiet printed: %q", r.stderr)
	}
}

func TestMachineOutput(t *testing.T) {
	h := newHarness(t)
	var ing map[string]any
	if err := json.Unmarshal([]byte(h.ok("ingest", "--json").stdout), &ing); err != nil || ing["files"].(float64) != 11 {
		t.Errorf("ingest --json: %v %v", ing, err)
	}
	var an map[string]any
	if err := json.Unmarshal([]byte(h.ok("analyze", "--offline", "--json").stdout), &an); err != nil || an["embedder"] == "" {
		t.Errorf("analyze --json: %v %v", an, err)
	}
	var v struct {
		Version  string   `json:"version"`
		Commands []string `json:"commands"`
	}
	if err := json.Unmarshal([]byte(h.ok("version", "--json").stdout), &v); err != nil || v.Version == "" || len(v.Commands) < 10 {
		t.Errorf("version --json: %+v %v", v, err)
	}
	for _, c := range v.Commands {
		if c == "show" {
			t.Error("deprecated alias listed in command tree")
		}
	}
}

func TestConfigPrecedence(t *testing.T) {
	h := newHarness(t)
	t.Setenv("SMINER_WINDOW", "30d")
	var cfg map[string]struct {
		Value  any    `json:"value"`
		Source string `json:"source"`
	}
	if err := json.Unmarshal([]byte(h.ok("config", "show", "--offline", "--json").stdout), &cfg); err != nil {
		t.Fatal(err)
	}
	checks := map[string]string{
		"window":           "env SMINER_WINDOW",
		"offline":          "flag --offline",
		"classifier.model": "file ",
		"min_count":        "default",
	}
	for k, src := range checks {
		if !strings.HasPrefix(cfg[k].Source, src) {
			t.Errorf("%s source = %q, want %q", k, cfg[k].Source, src)
		}
	}
	if cfg["window"].Value != "30d" {
		t.Errorf("window = %v", cfg["window"].Value)
	}
	if p := strings.TrimSpace(h.ok("config", "path").stdout); p != filepath.Join(h.dir, "config", "sminer", "config.yaml") {
		t.Errorf("config path = %q", p)
	}
	// The flag beats the environment.
	if err := json.Unmarshal([]byte(h.ok("config", "show", "--json", "--offline=false").stdout), &cfg); err != nil || cfg["offline"].Value != false {
		t.Errorf("--offline=false: %v", cfg["offline"])
	}
}

func TestOutputFlagsFromEnv(t *testing.T) {
	h := newHarness(t)
	t.Setenv("SMINER_JSON", "1")
	var list []any
	if err := json.Unmarshal([]byte(h.ok("finding", "list").stdout), &list); err != nil {
		t.Errorf("SMINER_JSON=1 finding list is not JSON: %v", err)
	}

	t.Setenv("SMINER_QUIET", "1")
	t.Setenv("SMINER_VERBOSE", "1")
	if r := h.run("ingest"); r.code != ExitUsage {
		t.Errorf("SMINER_QUIET and SMINER_VERBOSE together: exit %d, stderr %q", r.code, r.stderr)
	}
	// A flag settles the conflict.
	if r := h.run("ingest", "--quiet"); r.code != ExitOK || r.stderr != "" {
		t.Errorf("--quiet over SMINER_VERBOSE: exit %d, stderr %q", r.code, r.stderr)
	}
}

func TestDeprecatedNamesStillWork(t *testing.T) {
	h := newHarness(t)
	h.ok("run", "--no-llm", "--quiet")
	if len(h.llm.prompts) != 0 {
		t.Error("--no-llm should still mean offline")
	}
	id := h.list("open")[0].ID
	r := h.ok("show", id)
	if !strings.Contains(r.stdout, id) || !strings.Contains(r.stderr, "deprecated") || !strings.Contains(r.stderr, removalDate) {
		t.Errorf("show alias: %+v", r)
	}
	if r := h.ok("report", "--format", "json"); !json.Valid([]byte(r.stdout)) || !strings.Contains(r.stderr, "deprecated") {
		t.Errorf("--format json: %q", r.stderr)
	}
}

// recordingLLM fakes the classifier and synthesis calls and keeps every
// prompt so tests can check what would leave the machine.
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
	h := newHarness(t)
	r := h.ok("run", "--json")
	var rep report.Report
	if err := json.Unmarshal([]byte(r.stdout), &rep); err != nil {
		t.Fatalf("run --json stdout is not JSON: %v\n%s", err, r.stdout)
	}
	if !strings.Contains(r.stderr, "classifying candidates with fake") {
		t.Errorf("stderr:\n%s", r.stderr)
	}
	fs := h.list("open")
	if f := byTitle(fs, "Synthesized: prefer stdlib"); f == nil {
		t.Error("synthesized title missing")
	}
	if f := byTitle(fs, "Synthesized: pre-commit"); f == nil || f.Remedy != "skill" || len(f.Proposal.Steps) != 3 {
		t.Errorf("skill finding = %+v", f)
	}
	if f := byTitle(fs, "Synthesized: snake_case"); f == nil || f.Occurrences != 3 {
		t.Errorf("terraform finding = %+v", f)
	}
	if f := byTitle(fs, "Synthesized: package-lock"); f == nil || f.Remedy != "hook" {
		t.Errorf("lockfile finding = %+v", f)
	}
	for _, p := range h.llm.prompts {
		if len(p) > 2600 {
			t.Errorf("prompt is %d bytes", len(p))
		}
		for _, leak := range []string{"ghp_", "FAIL\tapi", "undefined: Widget", "package api", "Editing lockfiles is not allowed"} {
			if strings.Contains(p, leak) {
				t.Errorf("prompt leaked %q:\n%s", leak, p)
			}
		}
	}

	// Classifier results are cached: re-analysis makes no classifier calls.
	before := len(h.llm.prompts)
	var an struct {
		Classifier struct {
			Pending int `json:"pending"`
		} `json:"classifier"`
	}
	json.Unmarshal([]byte(h.ok("analyze", "--json").stdout), &an)
	if an.Classifier.Pending != 0 || len(h.llm.prompts) != before {
		t.Errorf("re-classified: pending %d, %d new calls", an.Classifier.Pending, len(h.llm.prompts)-before)
	}

	// SMINER_OFFLINE disables the network too.
	t.Setenv("SMINER_OFFLINE", "true")
	before = len(h.llm.prompts)
	h.ok("analyze", "--quiet")
	if len(h.llm.prompts) != before {
		t.Error("SMINER_OFFLINE=true still called the LLM")
	}
}

func TestInterrupted(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out, errb bytes.Buffer
	code := Run(ctx, []string{"run", "--offline"}, Options{Stdout: &out, Stderr: &errb, NewLLM: func(string) classify.LLM { return h.llm }})
	if code != ExitInterrupted || !strings.Contains(errb.String(), "interrupted") {
		t.Errorf("exit %d, stderr %q", code, errb.String())
	}
}

func TestAcceptCombinesClaudeMDLines(t *testing.T) {
	h := newHarness(t)
	h.ok("run", "--offline")
	fs := h.list("open")
	byTarget := map[string][]string{}
	for _, f := range fs {
		if f.Remedy == "claude_md" {
			byTarget[f.Target] = append(byTarget[f.Target], f.ID)
		}
	}
	var ids []string
	for _, g := range byTarget {
		if len(g) >= 2 {
			ids = g[:2]
			break
		}
	}
	if ids == nil {
		t.Fatalf("corpus has no two CLAUDE.md findings for one file: %v", byTarget)
	}
	hook := byTitle(fs, "package-lock")

	// A finding that can't be patched aborts the batch before anything is written.
	os.MkdirAll(filepath.Join(h.dir, "hooks"), 0o755)
	os.WriteFile(hook.Target, []byte("#!/bin/sh\n"), 0o755)
	if r := h.run("finding", "accept", ids[0], hook.ID); r.code != ExitConflict {
		t.Fatalf("batch with an unpatchable finding: %+v", r)
	}
	if len(h.list("accepted")) != 0 {
		t.Error("a failed batch accepted findings")
	}
	if ents, _ := os.ReadDir(filepath.Join(h.dir, "data", "sminer", "patches")); len(ents) != 0 {
		t.Errorf("a failed batch wrote %d patch file(s)", len(ents))
	}

	r := h.ok("finding", "accept", ids[0], ids[1], ids[0])
	paths := strings.Fields(r.stdout)
	if len(paths) != 1 || filepath.Base(paths[0]) != ids[0]+"+"+ids[1]+".patch" {
		t.Fatalf("want one combined patch path, got %q", r.stdout)
	}
	b, _ := os.ReadFile(paths[0])
	if strings.Count(string(b), "\n+- ") != 2 || strings.Count(string(b), "+## Learned conventions") != 1 {
		t.Errorf("combined patch:\n%s", b)
	}
	if !strings.Contains(r.stderr, "reopen "+ids[0]+" "+ids[1]) {
		t.Errorf("stderr:\n%s", r.stderr)
	}
	var res []finding.AcceptResult
	h.ok("finding", "reopen", ids[0], ids[1])
	json.Unmarshal([]byte(h.ok("finding", "accept", ids[0], ids[1], "--preview", "--json").stdout), &res)
	if len(res) != 2 || len(res[0].Combined) != 1 || res[0].Combined[0] != ids[1] || res[0].Diff != res[1].Diff {
		t.Errorf("json results = %+v", res)
	}
}

func TestLabel(t *testing.T) {
	h := newHarness(t)
	if r := h.run("eval"); r.code == 0 || !strings.Contains(r.stderr, "sminer label") {
		t.Errorf("eval with no labels: %+v", r)
	}
	h.ok("ingest")
	if r := h.run("label"); r.code != ExitUsage || !strings.Contains(r.stderr, "--export") {
		t.Errorf("label without a terminal: %+v", r)
	}

	// --export: JSONL on stdout with null labels, nothing written.
	exp := h.ok("label", "--export", "--seed", "1", "--limit", "4")
	lines := strings.Split(strings.TrimSpace(exp.stdout), "\n")
	if len(lines) == 0 || len(lines) > 4 || !strings.Contains(lines[0], `"correction":null`) {
		t.Fatalf("export:\n%s", exp.stdout)
	}
	if again := h.ok("label", "--export", "--seed", "1", "--limit", "4"); again.stdout != exp.stdout {
		t.Error("same seed, different sample")
	}

	// Interactive: y, n, s, then q. Two answers are saved.
	out := filepath.Join(h.dir, "data", "sminer", "labels.jsonl")
	r := h.typing("y\nn\nwhat\ns\nq\n", "label", "--seed", "1", "--limit", "4")
	if r.code != 0 || strings.TrimSpace(r.stdout) != out || !strings.Contains(r.stderr, "labelled 2 (1 corrections, 1 not), skipped 1") {
		t.Fatalf("label: %+v", r)
	}
	if st, err := os.Stat(out); err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("labels file mode: %v %v", st, err)
	}
	ev := h.ok("eval", "--json")
	var scores map[string]struct{ TP, FP, FN, TN int }
	json.Unmarshal([]byte(ev.stdout), &scores)
	if s := scores["heuristic"]; s.TP+s.FP+s.FN+s.TN != 2 {
		t.Errorf("eval read %+v from %s", s, out)
	}

	// Labelled turns are not offered again.
	next := h.ok("label", "--export", "--seed", "1", "--limit", "50")
	b, _ := os.ReadFile(out)
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var it struct{ Message string }
		json.Unmarshal([]byte(l), &it)
		if strings.Contains(next.stdout, strings.TrimSuffix(strings.TrimPrefix(mustJSON(it.Message), `"`), `"`)) {
			t.Errorf("labelled message offered again: %q", it.Message)
		}
	}
	// Exported, still-null lines are skipped by eval.
	batch := filepath.Join(h.dir, "batch.jsonl")
	os.WriteFile(batch, []byte(next.stdout), 0o600)
	if r := h.run("eval", "--labels", batch); r.code == 0 {
		t.Errorf("eval of an unlabelled batch succeeded: %+v", r)
	}
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
