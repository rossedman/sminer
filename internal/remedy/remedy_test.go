package remedy

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/rossedman/sminer/internal/cluster"
	"github.com/rossedman/sminer/internal/config"
	"github.com/rossedman/sminer/internal/store"
)

func TestExtractHook(t *testing.T) {
	tests := []struct {
		text, field, matches string
		ok                   bool
	}{
		{"Don't edit package-lock.json by hand, run npm install instead.", "file_path", "/a/web/package-lock.json", true},
		{"never touch go.sum manually", "file_path", "go.sum", true},
		{"never force push: don't run git push --force", "command", "git push --force origin main", true},
		{"don't run `make deploy` from a laptop", "command", "make deploy", true},
		{"don't use testify, use table-driven tests", "", "", false},
		{"Use snake_case for terraform resource names", "", "", false},
	}
	for _, tt := range tests {
		p, ok := ExtractHook(tt.text)
		if ok != tt.ok {
			t.Errorf("ExtractHook(%q) ok = %v, want %v (%+v)", tt.text, ok, tt.ok, p)
			continue
		}
		if !ok {
			continue
		}
		if p.Field != tt.field {
			t.Errorf("%q: field %q, want %q", tt.text, p.Field, tt.field)
		}
		if !regexp.MustCompile(p.Pattern).MatchString(tt.matches) {
			t.Errorf("%q: pattern %q does not match %q", tt.text, p.Pattern, tt.matches)
		}
	}
	p, _ := ExtractHook("Don't edit package-lock.json by hand")
	if regexp.MustCompile(p.Pattern).MatchString("/a/package-lock.json.bak") {
		t.Errorf("pattern %q too loose", p.Pattern)
	}
}

func TestAppendDiffApplies(t *testing.T) {
	if _, err := exec.LookPath("patch"); err != nil {
		t.Skip("patch not installed")
	}
	for name, old := range map[string]string{
		"trailing newline": "# Rules\n\n- one\n- two\n",
		"no newline":       "# Rules\n- one",
		"empty":            "",
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "CLAUDE.md")
			if err := os.WriteFile(path, []byte(old), 0o644); err != nil {
				t.Fatal(err)
			}
			diff := AppendDiff("CLAUDE.md", old, []string{"", "- three"}, true)
			cmd := exec.Command("patch", "-p1")
			cmd.Dir = dir
			cmd.Stdin = strings.NewReader(diff)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("patch failed: %v\n%s\n%s", err, out, diff)
			}
			got, _ := os.ReadFile(path)
			if !strings.HasSuffix(string(got), "\n- three\n") || !strings.HasPrefix(string(got), old[:min(len(old), 7)]) {
				t.Errorf("result:\n%s", got)
			}
		})
	}
}

func testConfig(dir string) config.Config {
	cfg := config.Default()
	cfg.Remedies = config.Remedies{
		HooksDir:       filepath.Join(dir, "hooks"),
		GlobalClaudeMD: filepath.Join(dir, "CLAUDE.md"),
		SkillsDir:      filepath.Join(dir, "skills"),
		Settings:       filepath.Join(dir, "settings.json"),
	}
	return cfg
}

func sigs(session string, n int, text string) []store.Signal {
	var out []store.Signal
	for i := range n {
		out = append(out, store.Signal{ID: text + string(rune('a'+i)), SessionID: session + string(rune('a'+i)), Type: store.Correction, Weight: 3, Excerpt: text, Repo: "/r"})
	}
	return out
}

func TestDedupe(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfig(dir)
	os.WriteFile(cfg.Remedies.GlobalClaudeMD, []byte("# Me\n\n- Write table-driven tests with the standard library, not testify.\n"), 0o644)
	os.MkdirAll(cfg.Remedies.HooksDir, 0o755)
	os.WriteFile(filepath.Join(cfg.Remedies.HooksDir, "locks.sh"), []byte("grep -q 'package-lock.json'"), 0o755)
	ex := LoadExisting(cfg)
	f := store.Finding{Family: cluster.Behavior}

	r, p, where := dedupe(ClaudeMD, Proposal{Rule: "Use table-driven tests with the standard library, not testify"}, f, sigs("s", 3, "x"), []string{"/r"}, ex)
	if r != Ignore || !strings.Contains(where, "CLAUDE.md:3") {
		t.Errorf("claude_md not deduped: %s %+v %q", r, p, where)
	}
	hp, _ := ExtractHook("don't edit package-lock.json")
	r, _, where = dedupe(Hook, hp, f, sigs("s", 3, "x"), nil, ex)
	if r != Ignore || !strings.HasSuffix(where, "locks.sh") {
		t.Errorf("hook not deduped: %s %q", r, where)
	}
	r, p, _ = dedupe(ClaudeMD, Proposal{Rule: "Prefer zerolog"}, f, sigs("same", 1, "x"), nil, ex)
	if r != Ignore || !strings.Contains(p.Why, "one-off") {
		t.Errorf("single-session finding not ignored: %s %+v", r, p)
	}
	r, _, _ = dedupe(ClaudeMD, Proposal{Rule: "Prefer zerolog for logging"}, f, sigs("s", 3, "x"), nil, ex)
	if r != ClaudeMD {
		t.Errorf("novel rule ignored: %s", r)
	}
}

func TestRoutePrefersHook(t *testing.T) {
	f := store.Finding{Family: cluster.Behavior}
	ss := sigs("s", 3, "stop editing yarn.lock directly")
	for i := range ss {
		ss[i].RemedyHint = ClaudeMD // even when the classifier leans CLAUDE.md
	}
	r, p := Route(f, ss)
	if r != Hook || p.Field != "file_path" {
		t.Errorf("route = %s %+v", r, p)
	}
}

func TestBuildPatchClaudeMD(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfig(dir)
	f := store.Finding{ID: "F1", Title: "t", RemedyType: ClaudeMD, Target: filepath.Join(dir, "repo", "CLAUDE.md"), Proposal: Proposal{Rule: "Use zerolog"}.encode()}
	p, err := BuildPatch(f, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if p.Base != filepath.Join(dir, "repo") || !strings.Contains(p.Diff, "+- Use zerolog") || !strings.Contains(p.Diff, "+++ b/CLAUDE.md") {
		t.Errorf("patch:\n%s", p.Diff)
	}
	f.RemedyType = Ignore
	if _, err := BuildPatch(f, cfg); err == nil {
		t.Error("ignored finding produced a patch")
	}
}
