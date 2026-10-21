package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/rossedman/sminer/internal/classify"
	"github.com/rossedman/sminer/internal/config"
	"github.com/rossedman/sminer/internal/remedy"
	"github.com/rossedman/sminer/internal/store"
	"github.com/rossedman/sminer/internal/version"
)

var candidateLabels, _ = filepath.Abs("../../testdata/labels/candidates.jsonl")

// seeded returns a harness whose database already holds the offline findings.
func seeded(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t)
	h.ok("run", "--offline", "--quiet")
	return h
}

func TestVersionCommand(t *testing.T) {
	h := newHarness(t)
	r := h.ok("version")
	if !strings.HasPrefix(r.stdout, "sminer ") || !strings.Contains(r.stdout, "go") || strings.Count(r.stdout, "\n") != 1 {
		t.Errorf("version = %q", r.stdout)
	}
	var v struct {
		Version  string   `json:"version"`
		Go       string   `json:"go"`
		Commands []string `json:"commands"`
	}
	if err := json.Unmarshal([]byte(h.ok("version", "--json").stdout), &v); err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, c := range v.Commands {
		have[c] = true
	}
	for _, want := range []string{"run", "finding accept", "finding trend", "config show", "config path", "label", "eval", "review"} {
		if !have[want] {
			t.Errorf("command tree missing %q: %v", want, v.Commands)
		}
	}
	for _, hidden := range []string{"show", "accept", "dismiss", "trend"} {
		if have[hidden] {
			t.Errorf("deprecated alias %q should be hidden", hidden)
		}
	}
	if r := h.ok("--version"); !strings.HasPrefix(r.stdout, "sminer ") {
		t.Errorf("--version = %q", r.stdout)
	}
}

func TestVersionStampedBuild(t *testing.T) {
	// Not parallel: sets the ldflags-stamped package variables.
	oldV, oldC, oldD := version.Version, version.Commit, version.Date
	t.Cleanup(func() { version.Version, version.Commit, version.Date = oldV, oldC, oldD })
	h := newHarness(t)

	version.Version, version.Commit, version.Date = "v9.9.9", "0123456789ab", "2026-10-20T15:03:39Z"
	if r := h.ok("version"); !strings.HasPrefix(r.stdout, "sminer v9.9.9 (0123456789ab, 2026-10-20T15:03:39Z) go") {
		t.Errorf("version = %q", r.stdout)
	}
	var v struct {
		Date string `json:"date"`
	}
	if err := json.Unmarshal([]byte(h.ok("version", "--json").stdout), &v); err != nil || v.Date != "2026-10-20T15:03:39Z" {
		t.Errorf("version --json date = %q (%v)", v.Date, err)
	}

	version.Date = ""
	if r := h.ok("version"); !strings.HasPrefix(r.stdout, "sminer v9.9.9 (0123456789ab) go") {
		t.Errorf("version without date = %q", r.stdout)
	}
}

func TestBareGroupsPrintHelp(t *testing.T) {
	h := newHarness(t)
	for argv, want := range map[string]string{"config": "Print every setting with the layer", "finding": "Hide findings that are not worth fixing"} {
		r := h.ok(argv)
		if !strings.Contains(r.stdout, want) {
			t.Errorf("sminer %s help missing %q:\n%s", argv, want, r.stdout)
		}
	}
	if r := h.run("config", "extra"); r.code != ExitUsage {
		t.Errorf("config extra: %+v", r)
	}
}

func TestConfigPath(t *testing.T) {
	h := newHarness(t)
	want := filepath.Join(h.dir, "config", "sminer", "config.yaml")
	var j struct {
		ConfigFile string `json:"config_file"`
		Exists     bool   `json:"exists"`
		DataDir    string `json:"data_dir"`
	}
	if err := json.Unmarshal([]byte(h.ok("config", "path", "--json").stdout), &j); err != nil {
		t.Fatal(err)
	}
	if j.ConfigFile != want || !j.Exists || j.DataDir != filepath.Join(h.dir, "data", "sminer") {
		t.Errorf("config path --json = %+v", j)
	}
	missing := filepath.Join(h.dir, "nope.yaml")
	if err := json.Unmarshal([]byte(h.ok("config", "path", "--json", "--config", missing).stdout), &j); err != nil || j.ConfigFile != missing || j.Exists {
		t.Errorf("--config missing: %+v %v", j, err)
	}
	if r := h.ok("config", "path", "--config", missing); strings.TrimSpace(r.stdout) != missing {
		t.Errorf("path = %q", r.stdout)
	}
}

func TestConfigShowPlain(t *testing.T) {
	h := newHarness(t)
	t.Setenv("SMINER_MIN_COUNT", "7")
	r := h.ok("config", "show", "--db", filepath.Join(h.dir, "x.db"))
	rows := map[string][]string{}
	for _, l := range strings.Split(strings.TrimSpace(r.stdout), "\n") {
		f := strings.Fields(l)
		rows[f[0]] = f
	}
	if f := rows["min_count"]; len(f) < 3 || f[1] != "7" || f[2] != "env" {
		t.Errorf("min_count row = %v", f)
	}
	if f := rows["db"]; len(f) < 4 || f[3] != "--db" {
		t.Errorf("db row = %v", f)
	}
	if len(rows) != len(config.Keys()) {
		t.Errorf("%d rows for %d keys", len(rows), len(config.Keys()))
	}
}

func TestOutputConfigFromEnv(t *testing.T) {
	h := newHarness(t)
	t.Setenv("SMINER_NO_COLOR", "1")
	t.Setenv("SMINER_QUIET", "1")
	r := h.ok("run", "--offline")
	if strings.Contains(r.stderr, "ingested") || strings.Contains(r.stderr, "analyzed") {
		t.Errorf("SMINER_QUIET=1 still printed progress: %q", r.stderr)
	}
	if strings.Contains(r.stdout, "\x1b") {
		t.Error("color with SMINER_NO_COLOR")
	}
}

func TestVerboseDetail(t *testing.T) {
	h := newHarness(t)
	r := h.ok("run", "--offline", "--verbose")
	for _, want := range []string{"signals:", "embedder hash", "Claude Code versions seen", "unknown event types"} {
		if !strings.Contains(r.stderr, want) {
			t.Errorf("--verbose stderr missing %q:\n%s", want, r.stderr)
		}
	}
	quiet := h.ok("run", "--offline")
	if strings.Contains(quiet.stderr, "signals:") {
		t.Error("detail printed without --verbose")
	}
	r = h.ok("run", "--verbose")
	if !strings.Contains(r.stderr, "classifier fake:") || !strings.Contains(r.stderr, "LLM call(s)") {
		t.Errorf("classifier detail missing:\n%s", r.stderr)
	}
}

func TestNoColorArg(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		argv []string
		want bool
	}{
		{nil, false},
		{[]string{"run", "--no-color"}, true},
		{[]string{"--no-color=true"}, true},
		{[]string{"--no-color=false"}, false},
		{[]string{"--", "--no-color"}, false},
		{[]string{"run", "--", "x", "--no-color"}, false},
	} {
		if got := noColorArg(tt.argv); got != tt.want {
			t.Errorf("noColorArg(%v) = %v", tt.argv, got)
		}
	}
}

func TestFindingLifecycle(t *testing.T) {
	h := seeded(t)
	open := h.list("open")
	if len(open) < 3 {
		t.Fatalf("want several findings, got %d", len(open))
	}
	a, b := open[0].ID, open[1].ID

	r := h.ok("finding", "dismiss", a, "--reason", "intentional")
	if !strings.Contains(r.stderr, "dismissed "+a) || !strings.Contains(r.stderr, "reopen "+a) {
		t.Errorf("dismiss stderr = %q", r.stderr)
	}
	if got := h.list("dismissed"); len(got) != 1 || got[0].ID != a {
		t.Errorf("dismissed = %+v", got)
	}
	if r := h.ok("finding", "dismiss", b, "--quiet"); r.stderr != "" {
		t.Errorf("--quiet dismiss printed %q", r.stderr)
	}
	if len(h.list("all")) != len(open) {
		t.Error("status all should include dismissed findings")
	}

	r = h.ok("finding", "reopen", a, b)
	if !strings.Contains(r.stderr, "reopened "+a) || !strings.Contains(r.stderr, "reopened "+b) {
		t.Errorf("reopen stderr = %q", r.stderr)
	}
	if len(h.list("dismissed")) != 0 || len(h.list("open")) != len(open) {
		t.Error("reopen did not restore findings")
	}

	// Plain list: one tab-aligned line per finding, status then remedy.
	plain := h.ok("finding", "list", "--status", "all").stdout
	lines := strings.Split(strings.TrimSpace(plain), "\n")
	if len(lines) != len(open) {
		t.Fatalf("list has %d lines for %d findings", len(lines), len(open))
	}
	if f := strings.Fields(lines[0]); f[0] != open[0].ID || f[1] != "open" || f[2] != open[0].Remedy {
		t.Errorf("first row = %q", lines[0])
	}
	if r := h.ok("findings", "ls"); r.stdout != h.ok("finding", "list").stdout {
		t.Error("findings ls is not an alias of finding list")
	}

	// Plain show names the proposal and every excerpt.
	show := h.ok("finding", "show", a).stdout
	for _, w := range []string{a + "  ", "status open", "occurrences in", "proposal:", "claude --resume"} {
		if !strings.Contains(show, w) {
			t.Errorf("show missing %q:\n%s", w, show)
		}
	}
}

func TestFindingNotFound(t *testing.T) {
	h := seeded(t)
	for _, argv := range [][]string{
		{"finding", "dismiss", "F000000"},
		{"finding", "reopen", "F000000"},
		{"finding", "accept", "F000000"},
		{"finding", "accept", "F000000", "--preview"},
		{"finding", "trend", "F000000"},
		{"dismiss", "F000000"},
		{"accept", "F000000"},
		{"trend", "F000000"},
	} {
		if r := h.run(argv...); r.code != ExitNotFound || !strings.Contains(r.stderr, "hint:") {
			t.Errorf("%v: %+v", argv, r)
		}
	}
	for _, argv := range [][]string{{"finding", "accept"}, {"finding", "dismiss"}, {"finding", "reopen"}, {"finding", "trend", "a", "b"}} {
		if r := h.run(argv...); r.code != ExitUsage {
			t.Errorf("%v: exit %d, want usage", argv, r.code)
		}
	}
}

func TestAcceptReopenTrendFlow(t *testing.T) {
	h := seeded(t)
	hook := byTitle(h.list("open"), "package-lock")
	if hook == nil {
		t.Fatal("no hook finding")
	}
	patches := filepath.Join(h.dir, "patches")
	r := h.ok("finding", "accept", hook.ID, "--patch-dir", patches)
	patch := strings.TrimSpace(r.stdout)
	if filepath.Dir(patch) != patches {
		t.Fatalf("patch path %q not in %s", patch, patches)
	}
	if b, err := os.ReadFile(patch); err != nil || !strings.Contains(string(b), "+++ ") {
		t.Errorf("patch file: %v\n%s", err, b)
	}
	for _, w := range []string{"accepted " + hook.ID, "apply with:", "undo with: sminer finding reopen " + hook.ID} {
		if !strings.Contains(r.stderr, w) {
			t.Errorf("accept stderr missing %q:\n%s", w, r.stderr)
		}
	}
	if q := h.ok("finding", "reopen", hook.ID, "--quiet"); q.stderr != "" {
		t.Errorf("quiet reopen printed %q", q.stderr)
	}

	// trend, plain and JSON, before and after adoption.
	var tr struct {
		ID        string          `json:"id"`
		AdoptedAt *time.Time      `json:"adopted_at"`
		Weeks     json.RawMessage `json:"weeks"`
	}
	if err := json.Unmarshal([]byte(h.ok("finding", "trend", hook.ID, "--json").stdout), &tr); err != nil || tr.ID != hook.ID || tr.AdoptedAt != nil {
		t.Errorf("trend json before adoption: %+v %v", tr, err)
	}
	h.ok("finding", "accept", hook.ID, "--patch-dir", patches, "--quiet")
	if err := json.Unmarshal([]byte(h.ok("finding", "trend", hook.ID, "--json").stdout), &tr); err != nil || tr.AdoptedAt == nil {
		t.Errorf("trend json after adoption: %+v %v", tr, err)
	}
	var all []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(h.ok("finding", "trend", "--json").stdout), &all); err != nil || len(all) == 0 {
		t.Errorf("trend --json: %v", err)
	}
	rows := strings.Split(strings.TrimSpace(h.ok("finding", "trend").stdout), "\n")
	for _, l := range rows {
		f := strings.Fields(l)
		if f[0] == hook.ID && f[2] == "-" {
			t.Errorf("adopted finding has no after rate: %q", l)
		}
	}
	if show := h.ok("finding", "show", hook.ID).stdout; !strings.Contains(show, "adopted 2026-10-19") {
		t.Errorf("show after adoption:\n%s", show)
	}

	// Compat aliases print the same data and warn.
	for _, name := range []string{"dismiss", "accept", "show", "trend"} {
		args := []string{name, hook.ID}
		if name == "accept" {
			args = append(args, "--preview")
		}
		r := h.run(args...)
		if !strings.Contains(r.stderr, "'"+name+"' is deprecated; use 'sminer finding "+name+"'") {
			t.Errorf("%s alias: no deprecation notice in %q (exit %d)", name, r.stderr, r.code)
		}
	}
}

func TestAcceptPreviewPlainAndJSON(t *testing.T) {
	h := seeded(t)
	f := byTitle(h.list("open"), "testify")
	if f == nil {
		t.Fatal("no testify finding")
	}
	r := h.ok("finding", "accept", f.ID, "--preview")
	if !strings.Contains(r.stdout, "+++ ") || r.stderr != "" {
		t.Errorf("preview: %+v", r)
	}
	if got := h.list("accepted"); len(got) != 0 {
		t.Error("--preview must not change the finding")
	}
	var res []struct {
		ID   string `json:"id"`
		Diff string `json:"diff"`
	}
	if err := json.Unmarshal([]byte(h.ok("finding", "accept", f.ID, "--preview", "--json").stdout), &res); err != nil || len(res) != 1 || res[0].ID != f.ID || res[0].Diff == "" {
		t.Errorf("preview json: %+v %v", res, err)
	}
}

func TestListStatusErrors(t *testing.T) {
	h := seeded(t)
	r := h.run("finding", "list", "--status", "nonsense")
	if r.code != ExitUsage || !strings.Contains(r.stderr, "unknown status") {
		t.Errorf("%+v", r)
	}
	if r := h.run("review", "--status", "nonsense"); r.code != ExitUsage {
		t.Errorf("review bad status: %+v", r)
	}
	if r := h.run("review", "--status", "all"); r.code != ExitUsage || !strings.Contains(r.stderr, "needs a terminal") {
		t.Errorf("review without terminal: %+v", r)
	}
}

func TestCompletions(t *testing.T) {
	h := seeded(t)
	open := h.list("open")
	h.ok("finding", "dismiss", open[0].ID, "--quiet")

	complete := func(argv ...string) []string {
		r := h.ok(append([]string{"__complete"}, argv...)...)
		var out []string
		for _, l := range strings.Split(strings.TrimSpace(r.stdout), "\n") {
			if strings.HasPrefix(l, ":") {
				continue
			}
			out = append(out, strings.Fields(l)[0])
		}
		return out
	}
	has := func(ids []string, id string) bool {
		for _, x := range ids {
			if x == id {
				return true
			}
		}
		return false
	}
	if got := complete("finding", "show", ""); !has(got, open[0].ID) || !has(got, open[1].ID) {
		t.Errorf("show completes all statuses: %v", got)
	}
	if got := complete("finding", "accept", ""); has(got, open[0].ID) || !has(got, open[1].ID) {
		t.Errorf("accept completes open only: %v", got)
	}
	if got := complete("finding", "accept", open[1].ID, ""); !has(got, open[2].ID) {
		t.Errorf("accept takes several ids: %v", got)
	}
	if got := complete("finding", "dismiss", ""); has(got, open[0].ID) {
		t.Errorf("dismiss offers dismissed: %v", got)
	}
	if got := complete("finding", "reopen", ""); !has(got, open[0].ID) {
		t.Errorf("reopen: %v", got)
	}
	// A single-id command offers nothing after its id.
	if got := complete("finding", "show", open[1].ID, ""); has(got, open[1].ID) || has(got, open[2].ID) {
		t.Errorf("show after id: %v", got)
	}
	if got := complete("finding", "list", "--status", ""); !has(got, "open") || !has(got, "dismissed") || !has(got, "all") {
		t.Errorf("status completion: %v", got)
	}

	// A database that can't be opened completes to nothing instead of failing.
	blocker := filepath.Join(h.dir, "blocker")
	os.WriteFile(blocker, nil, 0o600)
	t.Setenv("SMINER_DB", filepath.Join(blocker, "x.db"))
	r := h.ok("__complete", "finding", "show", "")
	if strings.Contains(r.stdout, "\t") {
		t.Errorf("completion with unusable db: %q", r.stdout)
	}
	if r := h.ok("completion", "bash"); !strings.Contains(r.stdout, "bash completion") {
		t.Errorf("completion bash: %.80q", r.stdout)
	}
	for _, sh := range []string{"zsh", "fish", "powershell"} {
		if r := h.ok("completion", sh); r.stdout == "" {
			t.Errorf("completion %s printed nothing", sh)
		}
	}
}

func TestDatabaseUnavailable(t *testing.T) {
	h := newHarness(t)
	blocker := filepath.Join(h.dir, "blocker")
	os.WriteFile(blocker, nil, 0o600)
	db := filepath.Join(blocker, "sminer.db")
	for _, argv := range [][]string{
		{"finding", "list"}, {"finding", "show", "F1"}, {"finding", "accept", "F1"}, {"finding", "dismiss", "F1"},
		{"finding", "reopen", "F1"}, {"finding", "trend"}, {"run", "--offline"}, {"ingest"}, {"analyze", "--offline"},
		{"report"}, {"label", "--export"},
	} {
		r := h.run(append(argv, "--db", db)...)
		if r.code != ExitFailure || !strings.Contains(r.stderr, "open database") {
			t.Errorf("%v: exit %d, stderr %q", argv, r.code, r.stderr)
		}
	}
	r := h.run("finding", "list", "--db", db, "--json")
	var e struct {
		Error struct{ Code string } `json:"error"`
	}
	if json.Unmarshal([]byte(r.stderr), &e) != nil || e.Error.Code != "failure" {
		t.Errorf("json error = %q", r.stderr)
	}
}

func TestEval(t *testing.T) {
	h := newHarness(t)
	r := h.ok("eval", "--labels", candidateLabels)
	if !strings.HasPrefix(r.stdout, "heuristic") || strings.Contains(r.stdout, "classifier") {
		t.Errorf("offline eval = %q", r.stdout)
	}

	var out map[string]struct {
		Precision float64 `json:"precision"`
		Recall    float64 `json:"recall"`
		TP, FP    int
	}
	live := h.ok("eval", "--labels", candidateLabels, "--live", "--json")
	if err := json.Unmarshal([]byte(live.stdout), &out); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"heuristic", "classifier", "pipeline"} {
		if _, ok := out[k]; !ok {
			t.Errorf("live eval missing %q: %s", k, live.stdout)
		}
	}
	if len(h.llm.prompts) == 0 || !strings.Contains(live.stderr, "classifying") {
		t.Errorf("--live made %d calls; stderr %q", len(h.llm.prompts), live.stderr)
	}
	plain := h.ok("eval", "--labels", candidateLabels, "--live")
	for _, k := range []string{"heuristic", "classifier", "pipeline"} {
		if !strings.Contains(plain.stdout, k) {
			t.Errorf("plain live eval missing %q:\n%s", k, plain.stdout)
		}
	}

	if r := h.run("eval", "--labels", candidateLabels, "--live", "--offline"); r.code != ExitUsage || !strings.Contains(r.stderr, "--live needs the network") {
		t.Errorf("--live --offline: %+v", r)
	}
	if r := h.run("eval", "--labels", filepath.Join(h.dir, "missing.jsonl")); r.code != ExitFailure || strings.Contains(r.stderr, "hint: label some") {
		t.Errorf("explicit missing labels: %+v", r)
	}
	empty := filepath.Join(h.dir, "empty.jsonl")
	os.WriteFile(empty, nil, 0o600)
	if r := h.run("eval", "--labels", empty); r.code != ExitFailure || !strings.Contains(r.stderr, "no labelled turns") {
		t.Errorf("empty labels: %+v", r)
	}
}

func TestLabelErrors(t *testing.T) {
	h := newHarness(t)
	if r := h.run("label", "--limit", "0", "--export"); r.code != ExitUsage || !strings.Contains(r.stderr, "--limit must be positive") {
		t.Errorf("limit 0: %+v", r)
	}
	if r := h.run("label", "--export"); r.code != ExitFailure || !strings.Contains(r.stderr, "no candidate turns") || !strings.Contains(r.stderr, "sminer ingest") {
		t.Errorf("no data: %+v", r)
	}
	h.ok("ingest", "--quiet")
	out := filepath.Join(h.dir, "labels.jsonl")

	// Answer everything on offer, then nothing is left to label.
	h.ok("label", "--export", "--limit", "10000", "--out", out)
	r := h.typing(strings.Repeat("y\n", 10000), "label", "--limit", "10000", "--out", out, "--json", "--seed", "3")
	var res struct {
		Out         string `json:"out"`
		Labelled    int    `json:"labelled"`
		Corrections int    `json:"corrections"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &res); err != nil || res.Labelled == 0 || res.Corrections != res.Labelled || res.Out != out {
		t.Fatalf("label --json: %v\n%+v", err, r)
	}
	r = h.run("label", "--export", "--limit", "10000", "--out", out)
	if r.code != 0 || r.stdout != "" || !strings.Contains(r.stderr, "already labelled") {
		t.Errorf("all labelled: %+v", r)
	}
	if r := h.typing("", "label", "--limit", "5", "--out", out); r.code != 0 || r.stdout != "" {
		t.Errorf("nothing left to label: %+v", r)
	}
}

func TestLabelLinesPrompt(t *testing.T) {
	h := newHarness(t)
	h.ok("ingest", "--quiet")
	out := filepath.Join(h.dir, "l.jsonl")

	// "no" and "skip" word forms, then EOF ends the session cleanly.
	r := h.typing("no\nskip\nyes\n", "label", "--seed", "1", "--limit", "5", "--out", out)
	if r.code != 0 {
		t.Fatalf("exit %d: %s", r.code, r.stderr)
	}
	for _, w := range []string{"[1/", "Claude had just done:", "You said:", "correction? [y]es", "labelled 2 (1 corrections, 1 not), skipped 1"} {
		if !strings.Contains(r.stderr, w) {
			t.Errorf("stderr missing %q:\n%s", w, r.stderr)
		}
	}
	b, _ := os.ReadFile(out)
	if n := strings.Count(string(b), "\n"); n != 2 {
		t.Errorf("%d labels written, want 2:\n%s", n, b)
	}
	if !strings.Contains(string(b), `"correction":false`) || !strings.Contains(string(b), `"correction":true`) {
		t.Errorf("labels = %s", b)
	}

	// An unwritable labels file fails the session but still reports the tally.
	bad := filepath.Join(h.dir, "dir-as-file")
	os.Mkdir(bad, 0o700)
	r = h.typing("y\n", "label", "--seed", "2", "--limit", "2", "--out", bad)
	if r.code == 0 {
		t.Errorf("writing to a directory succeeded: %+v", r)
	}

	// Cancelling mid-prompt returns the context error.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var so, se bytes.Buffer
	code := Run(ctx, []string{"label", "--seed", "1", "--limit", "2", "--out", filepath.Join(h.dir, "c.jsonl")}, Options{
		Stdin: blockingReader{}, Stdout: &so, Stderr: &se, ForceInteractive: true,
		NewLLM: func(string) classify.LLM { return h.llm },
	})
	if code != ExitInterrupted {
		t.Errorf("cancelled label exit %d: %s", code, se.String())
	}
}

// blockingReader never returns data until the test process exits.
type blockingReader struct{}

func (blockingReader) Read([]byte) (int, error) { select {} }

func TestRunOptions(t *testing.T) {
	h := newHarness(t)
	repo := filepath.Join(corpus, "does-not-matter")
	for _, argv := range [][]string{
		{"run", "--offline", "--since", "2w", "--min-count", "1", "--repo", repo, "--quiet"},
		{"run", "--offline", "--root", corpus, "--quiet", "--output", "-"},
		{"ingest", "--root", corpus, "--quiet"},
		{"analyze", "--offline", "--since", "30d", "--quiet"},
	} {
		if r := h.run(argv...); r.code != 0 {
			t.Errorf("%v: %+v", argv, r)
		}
	}
	out := filepath.Join(h.dir, "j", "r.json")
	r := h.ok("run", "--offline", "--quiet", "--json", "--output", out)
	if r.stdout != "" {
		t.Errorf("stdout = %q", r.stdout)
	}
	if _, err := os.Stat(filepath.Join(h.dir, "j", "findings.json")); err == nil {
		t.Error("--json --output must not write a second findings.json")
	}
	var rep map[string]any
	if b, _ := os.ReadFile(out); json.Unmarshal(b, &rep) != nil {
		t.Error("--json --output file is not JSON")
	}
	r = h.ok("report", "--out", filepath.Join(h.dir, "o.md"))
	if !strings.Contains(r.stderr, "--out is deprecated; use --output") {
		t.Errorf("--out notice: %q", r.stderr)
	}
	if r := h.ok("report", "--output", filepath.Join(h.dir, "p.md")); !strings.Contains(r.stderr, "wrote ") || !strings.Contains(r.stderr, "findings.json") {
		t.Errorf("report --output summary: %q", r.stderr)
	}
	if r := h.run("report", "--output", filepath.Join(h.dir, "nested", "x", "r.md")); r.code != 0 {
		t.Errorf("nested output dir should be created: %+v", r)
	}
	blocker := filepath.Join(h.dir, "file")
	os.WriteFile(blocker, nil, 0o600)
	if r := h.run("report", "--output", filepath.Join(blocker, "r.md")); r.code != ExitFailure {
		t.Errorf("unwritable output: %+v", r)
	}
}

func TestCrashIsReported(t *testing.T) {
	newHarness(t)
	var so, se bytes.Buffer
	code := Run(context.Background(), []string{"analyze"}, Options{
		Stdout: &so, Stderr: &se, Stdin: strings.NewReader(""),
		Now:    func() time.Time { return time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC) },
		NewLLM: func(string) classify.LLM { panic("kaboom") },
	})
	if code != ExitFailure || !strings.Contains(se.String(), "sminer crashed: kaboom") || !strings.Contains(se.String(), issuesURL) {
		t.Errorf("crash: exit %d\n%s", code, se.String())
	}
}

func TestExecuteUsesProcessArgs(t *testing.T) {
	newHarness(t) // isolates XDG dirs
	old := os.Args
	t.Cleanup(func() { os.Args = old })
	os.Args = []string{"sminer", "bogus-command"}
	if code := Execute(); code != ExitUsage {
		t.Errorf("Execute() = %d, want usage error", code)
	}
}

func TestResolve(t *testing.T) {
	t.Parallel()
	wrap := func(err error) error { return errors.Join(errors.New("ctx"), err) }
	tests := []struct {
		name     string
		err      error
		exit     int
		code     string
		hintPart string
	}{
		{"plain", errors.New("boom"), ExitFailure, "failure", ""},
		{"canceled", wrap(context.Canceled), ExitInterrupted, "interrupted", ""},
		{"not found", wrap(store.ErrNotFound), ExitNotFound, "not_found", "finding list"},
		{"no remedy", wrap(remedy.ErrNoRemedy), ExitConflict, "no_remedy", "sminer analyze"},
		{"not patchable", wrap(remedy.ErrNotPatchable), ExitConflict, "not_patchable", "finding show"},
		{"target exists", wrap(remedy.ErrTargetExists), ExitConflict, "target_exists", "move the existing file"},
		{"invalid config", wrap(config.ErrInvalid), ExitUsage, "config", "config show"},
		{"no config file", wrap(config.ErrNoFile), ExitUsage, "config", "config path"},
		{"cobra", errors.New(`unknown command "x" for "sminer"`), ExitUsage, "usage", "--help"},
		{"explicit hint kept", withHint(store.ErrNotFound, "custom"), ExitNotFound, "not_found", "custom"},
		{"usage error", usageError(newRootCmd(newApp(Options{})), errors.New("bad")), ExitUsage, "usage", "sminer --help"},
		{"hint on plain error", withHint(errors.New("x"), "try this"), ExitFailure, "failure", "try this"},
	}
	for _, tt := range tests {
		exit, code, hint := resolve(tt.err)
		if exit != tt.exit || code != tt.code || !strings.Contains(hint, tt.hintPart) {
			t.Errorf("%s: got (%d, %q, %q)", tt.name, exit, code, hint)
		}
	}
	if !isCobraUsage(errors.New("required flag(s) \"x\" not set")) || isCobraUsage(errors.New("disk full")) {
		t.Error("isCobraUsage")
	}
	if got := withHint(&cliError{exit: ExitUsage, err: errors.New("e")}, "h"); got.(*cliError).hint != "h" || got.(*cliError).exit != ExitUsage {
		t.Errorf("withHint on cliError = %+v", got)
	}
}

func TestPrintError(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	a := newApp(Options{Stdout: &bytes.Buffer{}, Stderr: &buf})
	if code := a.printError(context.Canceled); code != ExitInterrupted || buf.String() != "interrupted\n" {
		t.Errorf("canceled: %d %q", code, buf.String())
	}
	buf.Reset()
	if code := a.printError(withHint(errors.New("disk full"), "free some space")); code != ExitFailure || buf.String() != "error: disk full\nhint: free some space\n" {
		t.Errorf("plain: %d %q", code, buf.String())
	}
	buf.Reset()
	if code := a.printError(errors.New("bare")); code != ExitFailure || buf.String() != "error: bare\n" {
		t.Errorf("no hint: %d %q", code, buf.String())
	}
	buf.Reset()
	a.g.json = true
	a.printError(withHint(store.ErrNotFound, "h"))
	var e struct {
		Error struct {
			Code    string `json:"code"`
			Exit    int    `json:"exit"`
			Message string `json:"message"`
			Hint    string `json:"hint"`
		} `json:"error"`
	}
	if err := json.Unmarshal(buf.Bytes(), &e); err != nil || e.Error.Code != "not_found" || e.Error.Exit != ExitNotFound || e.Error.Hint != "h" || e.Error.Message == "" {
		t.Errorf("json: %v %q", err, buf.String())
	}
}

func TestOutputHelpers(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	a := newApp(Options{Stdout: &out, Stderr: &bytes.Buffer{}})
	if a.fancyOut() || a.fancyErr() {
		t.Error("buffers are not terminals")
	}
	if err := a.table(nil, [][]string{{"a", "long"}, {"bbbb", "x"}}, nil); err != nil {
		t.Fatal(err)
	}
	if out.String() != "a     long\nbbbb  x\n" {
		t.Errorf("table = %q", out.String())
	}
	if style(a.theme().Faint, "") != "" {
		t.Error("style of empty string should stay empty")
	}
	a.theme()
	cached := a.th
	a.theme()
	if a.th != cached {
		t.Error("theme should be built once")
	}
	out.Reset()
	if err := a.printJSON(map[string]int{"a": 1}); err != nil || out.String() != "{\n  \"a\": 1\n}\n" {
		t.Errorf("printJSON = %q (%v)", out.String(), err)
	}
	out.Reset()
	if err := a.printLine(map[string]int{"a": 1}); err != nil || out.String() != "{\"a\":1}\n" {
		t.Errorf("printLine = %q (%v)", out.String(), err)
	}
	if err := a.printLine(make(chan int)); err == nil {
		t.Error("printLine of an unencodable value should fail")
	}
}

func TestAppSource(t *testing.T) {
	h := newHarness(t)
	t.Setenv("SMINER_MIN_COUNT", "5")
	var cfg map[string]struct{ Source string }
	if err := json.Unmarshal([]byte(h.ok("config", "show", "--json", "--db", filepath.Join(h.dir, "z.db")).stdout), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg["db"].Source != "flag --db" || cfg["min_count"].Source != "env SMINER_MIN_COUNT" || !strings.HasPrefix(cfg["roots"].Source, "file ") || cfg["window"].Source == "" {
		t.Errorf("sources = %+v", cfg)
	}
}

func TestCompatPanicsOnMissingCommand(t *testing.T) {
	t.Parallel()
	defer func() {
		if r := recover(); r == nil || !strings.Contains(r.(string), "no command nope") {
			t.Errorf("recover = %v", r)
		}
	}()
	findSub(newRootCmd(newApp(Options{})), "nope")
}

func TestSpinnerPlainSteps(t *testing.T) {
	t.Parallel()
	var errb bytes.Buffer
	a := newApp(Options{Stdout: &bytes.Buffer{}, Stderr: &errb})
	a.progress("ingesting %d files", 3)
	a.done("done plain", "Done", "detail")
	a.warn("careful")
	a.debugf("hidden")
	got := ansi.Strip(errb.String())
	if got != "· ingesting 3 files\ndone plain\nwarning: careful\n" {
		t.Errorf("stderr = %q", got)
	}
	a.g.verbose = true
	a.debugf("shown %d", 1)
	if !strings.HasSuffix(ansi.Strip(errb.String()), "shown 1\n") {
		t.Errorf("verbose debugf: %q", errb.String())
	}
	errb.Reset()
	a.g.quiet = true
	a.progress("x")
	a.info("x")
	a.done("x", "x", "x")
	if errb.Len() != 0 {
		t.Errorf("--quiet printed %q", errb.String())
	}
	a.warn("still shown")
	if !strings.Contains(errb.String(), "still shown") {
		t.Error("warnings survive --quiet")
	}
}
