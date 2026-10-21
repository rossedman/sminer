package remedy

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/rossedman/sminer/internal/classify"
	"github.com/rossedman/sminer/internal/cluster"
	"github.com/rossedman/sminer/internal/store"
)

type replyLLM struct {
	reply string
	err   error
	calls int
	user  string
}

func (r *replyLLM) Complete(_ context.Context, _, user string, _ int) (string, error) {
	r.calls++
	r.user = user
	return r.reply, r.err
}

func TestDecodeProposal(t *testing.T) {
	t.Parallel()
	full := Proposal{Rule: "r", Matcher: "Bash", Field: "command", Pattern: "^x$", SkillName: "s", Description: "d", Steps: []string{"a", "b"},
		Why: "w", Unheeded: &Unheeded{Where: "p:1", Line: "l", Since: 3}}
	if got := DecodeProposal(full.encode()); !reflect.DeepEqual(got, full) {
		t.Errorf("round trip = %+v", got)
	}
	if got := DecodeProposal("Use zerolog everywhere"); got.Rule != "Use zerolog everywhere" || got.Pattern != "" {
		t.Errorf("plain text should become the rule: %+v", got)
	}
	if got := DecodeProposal(""); got.Rule != "" {
		t.Errorf("empty = %+v", got)
	}
	if got := DecodeProposal(`{"rule":"a","pattern":"b"}`); got.Rule != "a" || got.Pattern != "b" {
		t.Errorf("minimal JSON = %+v", got)
	}
	if got := DecodeProposal(`{"rule": 5}`); got.Rule != `{"rule": 5}` {
		t.Errorf("mistyped JSON falls back to text: %+v", got)
	}
	// Empty optional fields are omitted from the encoding.
	if enc := (Proposal{Rule: "r"}).encode(); enc != `{"rule":"r"}` {
		t.Errorf("encode = %s", enc)
	}
}

func FuzzDecodeProposal(f *testing.F) {
	for _, p := range []Proposal{
		{Rule: "Never edit go.sum"},
		{Rule: "r", Matcher: "Edit|Write|MultiEdit", Field: "file_path", Pattern: `(^|/)go\.sum$`},
		{Rule: "r", SkillName: "release", Description: "cut a release", Steps: []string{"tag", "push"}},
		{Rule: "r", Why: "already in CLAUDE.md:3", Unheeded: &Unheeded{Where: "/r/CLAUDE.md:3", Line: "- x", Since: 4}},
	} {
		f.Add(p.encode())
	}
	f.Add("plain text rule")
	f.Add("")
	f.Add("null")
	f.Add(`{"rule":5}`)
	f.Add(`[1,2`)
	f.Fuzz(func(t *testing.T, s string) {
		p := DecodeProposal(s)
		if !reflect.DeepEqual(p, DecodeProposal(s)) {
			t.Fatal("non-deterministic")
		}
		// Whatever came out re-encodes, and valid text survives the round trip.
		if utf8.ValidString(p.Rule + p.Matcher + p.Field + p.Pattern + p.SkillName + p.Description + p.Why + strings.Join(p.Steps, "")) {
			back := DecodeProposal(p.encode())
			if p.Rule != "" && !reflect.DeepEqual(back.Rule, p.Rule) {
				t.Fatalf("rule lost in round trip: %q -> %q", p.Rule, back.Rule)
			}
			if back.Pattern != p.Pattern || back.Matcher != p.Matcher || back.Field != p.Field || back.SkillName != p.SkillName {
				t.Fatalf("round trip changed %+v -> %+v", p, back)
			}
		}
		if !json.Valid([]byte(s)) && p.Rule != s {
			t.Fatalf("non-JSON input must become the rule, got %q for %q", p.Rule, s)
		}
	})
}

func FuzzExtractHook(f *testing.F) {
	for _, s := range []string{
		"Don't edit package-lock.json by hand, run npm install instead.",
		"never touch go.sum manually",
		"never force push: don't run git push --force",
		"don't run `make deploy` from a laptop",
		"Use snake_case for terraform resource names",
		"stop editing config/*.yaml",
		"never run rm -rf in the repo root",
		"",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, text string) {
		p, ok := ExtractHook(text)
		p2, ok2 := ExtractHook(text)
		if ok != ok2 || !reflect.DeepEqual(p, p2) {
			t.Fatal("non-deterministic")
		}
		if !ok {
			if !reflect.DeepEqual(p, Proposal{}) {
				t.Fatalf("failed extraction must be zero, got %+v", p)
			}
			return
		}
		re, err := regexp.Compile(p.Pattern)
		if err != nil {
			t.Fatalf("pattern %q does not compile: %v", p.Pattern, err)
		}
		if p.Pattern == "" {
			t.Fatal("empty pattern would block everything")
		}
		switch p.Field {
		case "file_path":
			if p.Matcher != "Edit|Write|MultiEdit" {
				t.Fatalf("matcher %q for file_path", p.Matcher)
			}
		case "command":
			if p.Matcher != "Bash" {
				t.Fatalf("matcher %q for command", p.Matcher)
			}
		default:
			t.Fatalf("unknown field %q", p.Field)
		}
		if !negRe.MatchString(text) {
			t.Fatalf("hook extracted without any prohibition in %q", text)
		}
		_ = re
	})
}

func BenchmarkExtractHook(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		_, _ = ExtractHook("Don't edit package-lock.json by hand, run npm install instead.")
	}
}

func TestRouteFamilies(t *testing.T) {
	t.Parallel()
	t.Run("hook family", func(t *testing.T) {
		t.Parallel()
		r, p := Route(store.Finding{Family: cluster.Hook, Key: "PreToolUse:Edit g.sh"}, []store.Signal{{Excerpt: "old"}, {Excerpt: "no  lockfiles\nplease"}})
		if r != ClaudeMD || !strings.Contains(p.Rule, "PreToolUse:Edit g.sh hook enforces") || !strings.HasSuffix(p.Rule, "no lockfiles please") {
			t.Errorf("%s %+v", r, p)
		}
		r, p = Route(store.Finding{Family: cluster.Hook, Key: "k"}, nil)
		if r != ClaudeMD || p.Rule == "" {
			t.Errorf("no signals: %s %+v", r, p)
		}
		_, long := Route(store.Finding{Family: cluster.Hook, Key: "k"}, []store.Signal{{Excerpt: strings.Repeat("word ", 200)}})
		if len(long.Rule) > 300 {
			t.Errorf("rule length %d", len(long.Rule))
		}
	})
	t.Run("failure family", func(t *testing.T) {
		t.Parallel()
		_, p := Route(store.Finding{Family: cluster.Failure, Key: "go test"}, []store.Signal{{Excerpt: "`go test ./...` failed 2 times in a row: undefined: foo"}})
		if !strings.Contains(p.Rule, "When `go test` fails") || !strings.Contains(p.Rule, "undefined: foo") {
			t.Errorf("rule = %q", p.Rule)
		}
		_, p = Route(store.Finding{Family: cluster.Failure, Key: "go test"}, []store.Signal{{Excerpt: "no separator"}})
		if !strings.Contains(p.Rule, "failed with: )") {
			t.Errorf("rule = %q", p.Rule)
		}
		_, p = Route(store.Finding{Family: cluster.Failure, Key: "make"}, nil)
		if !strings.Contains(p.Rule, "`make`") {
			t.Errorf("rule = %q", p.Rule)
		}
	})
	t.Run("fixup family", func(t *testing.T) {
		t.Parallel()
		mk := func(keys ...string) []store.Signal {
			var s []store.Signal
			for _, k := range keys {
				s = append(s, store.Signal{Type: store.ManualFixup, Key: k})
			}
			return s
		}
		for keys, want := range map[string]string{
			"":                           "Edits to files are",
			"*.go":                       "Edits to *.go files are",
			"*.go,*.go,go.mod":           "Edits to *.go and go.mod files are",
			"*.go,*.go,*.md,*.md,go.mod": "Edits to *.go, *.md and go.mod files are",
			"a,b,c,d,e":                  "Edits to a, b and c files are",
		} {
			var ks []string
			if keys != "" {
				ks = strings.Split(keys, ",")
			}
			if r, p := Route(store.Finding{Family: cluster.Fixup}, mk(ks...)); r != ClaudeMD || !strings.HasPrefix(p.Rule, want) {
				t.Errorf("keys %q: %s %q, want prefix %q", keys, r, p.Rule, want)
			}
		}
	})
}

func TestRouteBehavior(t *testing.T) {
	t.Parallel()
	f := store.Finding{Family: cluster.Behavior}
	mk := func(text, hint string, n int) []store.Signal {
		s := sigs("s", n, text)
		for i := range s {
			s[i].RemedyHint = hint
		}
		return s
	}
	t.Run("classifier rule wins over excerpts", func(t *testing.T) {
		t.Parallel()
		ss := mk("whatever the user typed", "", 3)
		ss[0].Rule, ss[1].Rule, ss[2].Rule = "Use pnpm", "Use pnpm", "Use yarn"
		r, p := Route(f, ss)
		if r != ClaudeMD || p.Rule != "Use pnpm" {
			t.Errorf("%s %+v", r, p)
		}
	})
	t.Run("skill hint", func(t *testing.T) {
		t.Parallel()
		r, p := Route(f, mk("cut a release for the repo", Skill, 3))
		if r != Skill || p.SkillName == "" || !strings.HasPrefix(p.Description, "Use when asked to: ") {
			t.Errorf("%s %+v", r, p)
		}
	})
	t.Run("procedural text becomes a skill", func(t *testing.T) {
		t.Parallel()
		r, p := Route(f, mk("first run the linter, then run the tests, finally tag the release", "", 3))
		if r != Skill || len(p.Steps) < 3 {
			t.Errorf("%s %+v", r, p)
		}
	})
	t.Run("plain rule", func(t *testing.T) {
		t.Parallel()
		r, p := Route(f, mk("no, prefer zerolog for logging", "", 3))
		if r != ClaudeMD || p.Rule != "Prefer zerolog for logging" {
			t.Errorf("%s %+v", r, p)
		}
	})
	t.Run("no signals", func(t *testing.T) {
		t.Parallel()
		r, p := Route(f, nil)
		if r != ClaudeMD || p.Rule != "" {
			t.Errorf("%s %+v", r, p)
		}
	})
	t.Run("closest to centroid", func(t *testing.T) {
		t.Parallel()
		ss := []store.Signal{
			{Excerpt: "far away text", Embedding: []float32{0, 1}},
			{Excerpt: "(interrupted) close text", Embedding: []float32{1, 0}},
		}
		if got := representativeText(store.Finding{Centroid: []float32{1, 0}}, ss); got != "close text" {
			t.Errorf("got %q", got)
		}
	})
}

func TestMajorityHintAndRule(t *testing.T) {
	t.Parallel()
	mk := func(hints ...string) []store.Signal {
		var s []store.Signal
		for _, h := range hints {
			s = append(s, store.Signal{RemedyHint: h, Rule: h})
		}
		return s
	}
	if got := majorityHint(mk("hook", "skill", "skill")); got != "skill" {
		t.Errorf("hint = %q", got)
	}
	if got := majorityHint(mk("skill", "hook")); got != "hook" {
		t.Errorf("tie goes to alphabetically first, got %q", got)
	}
	if majorityHint(mk("", "")) != "" || majorityHint(nil) != "" {
		t.Error("no hints")
	}
	if got := representativeText(store.Finding{}, mk("b", "a")); got != "a" {
		t.Errorf("rule tie goes to alphabetically first, got %q", got)
	}
}

func TestAsRuleAndGlob(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"no, use pnpm":           "Use pnpm",
		"Nope!! stop that":       "Stop that",
		"  lowercase   rule  ":   "Lowercase rule",
		"":                       "",
		"okay. fine":             "Fine",
		"notable change":         "Notable change",
		strings.Repeat("x", 300): "X" + strings.Repeat("x", 236) + "...",
	} {
		if got := asRule(in); got != want {
			t.Errorf("asRule(%q) = %q, want %q", in, got, want)
		}
	}
	for g, tc := range map[string]struct{ re, yes, no string }{
		"go.sum":        {`(^|/)go\.sum$`, "a/go.sum", "a/ago.sum"},
		"config/*.yaml": {`config/[^/"]*\.yaml$`, "x/config/a.yaml", "config/a/b.yaml"},
		"*.lock":        {`(^|/)[^/"]*\.lock$`, "yarn.lock", "yarn.lock.bak"},
	} {
		if got := globToRegex(g); got != tc.re {
			t.Errorf("globToRegex(%q) = %q, want %q", g, got, tc.re)
		}
		re := regexp.MustCompile(globToRegex(g))
		if !re.MatchString(tc.yes) || re.MatchString(tc.no) {
			t.Errorf("%q: yes=%v no=%v", g, re.MatchString(tc.yes), re.MatchString(tc.no))
		}
	}
}

func TestSkillFrom(t *testing.T) {
	t.Parallel()
	p := skillFrom("1. lint the code\n- run the tests\n* tag a release\nok", nil)
	if !reflect.DeepEqual(p.Steps, []string{"lint the code", "run the tests", "tag a release"}) {
		t.Errorf("list steps = %v", p.Steps)
	}
	p = skillFrom("first lint the code; then run the tests, and then tag a release.", nil)
	if len(p.Steps) < 3 || p.Steps[0][0] < 'A' || p.Steps[0][0] > 'Z' {
		t.Errorf("sentence steps = %v", p.Steps)
	}
	if p.Rule == "" || p.SkillName == "" || len(p.Description) == 0 {
		t.Errorf("skill = %+v", p)
	}
	if skillFrom("", nil).SkillName != "finding" {
		t.Error("empty text still gets a name")
	}
}

func TestLooksProcedural(t *testing.T) {
	t.Parallel()
	rep := func(n int) []store.Signal {
		s := make([]store.Signal, n)
		for i := range s {
			s[i].Type = store.RepeatedInstruction
		}
		return s
	}
	long := "before you commit, run the formatter and the linter, then check the diff carefully for stray files and debugging output"
	if !looksProcedural("first a, then b, finally c", nil) {
		t.Error("three steps")
	}
	if looksProcedural("first a, then b", []store.Signal{{Type: store.Correction}}) {
		t.Error("two steps without repeats")
	}
	if !looksProcedural(long, rep(3)) {
		t.Error("two steps in long repeated text")
	}
	if looksProcedural("first a then b", rep(3)) {
		t.Error("short text needs three steps")
	}
}

func TestBuildPatchKinds(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg := testConfig(dir)
	hookTarget := filepath.Join(cfg.Remedies.HooksDir, "sminer-x.sh")
	hook := store.Finding{ID: "F1", Title: "No go.sum", RemedyType: Hook, Target: hookTarget,
		Proposal: Proposal{Rule: "Don't edit go.sum", Matcher: "Edit", Field: "file_path", Pattern: `go\.sum$`}.encode()}
	p, err := BuildPatch(hook, cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"sminer F1: No go.sum", "Remedy: hook", "new file mode 100755", "+++ b/sminer-x.sh", "edits to files matching",
		`grep -Eq -- 'go\.sum$'`, "Blocked by sminer hook: Don'\\''t edit go.sum", `"matcher": "Edit"`} {
		if !strings.Contains(p.Diff, want) {
			t.Errorf("hook patch missing %q:\n%s", want, p.Diff)
		}
	}
	if p.Base != cfg.Remedies.HooksDir || !strings.Contains(p.Extra, hookTarget) || !strings.Contains(p.Extra, cfg.Remedies.Settings) {
		t.Errorf("base/extra = %q / %q", p.Base, p.Extra)
	}
	cmd := hook
	cmd.Proposal = Proposal{Rule: "r", Matcher: "Bash", Field: "command", Pattern: "^npm "}.encode()
	if p, err := BuildPatch(cmd, cfg); err != nil || !strings.Contains(p.Diff, "shell commands matching") {
		t.Errorf("command hook: %v", err)
	}
	if err := os.MkdirAll(cfg.Remedies.HooksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hookTarget, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := BuildPatch(hook, cfg); !errors.Is(err, ErrTargetExists) {
		t.Errorf("existing hook err = %v", err)
	}

	skillTarget := filepath.Join(cfg.Remedies.SkillsDir, "release", "SKILL.md")
	skill := store.Finding{ID: "F2", Title: "Release", RemedyType: Skill, Target: skillTarget,
		Proposal: Proposal{Rule: "Cut a release", SkillName: "release", Description: "Use when releasing", Steps: []string{"tag", "push"}}.encode()}
	p, err = BuildPatch(skill, cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"+++ b/release/SKILL.md", "+name: release", `+description: "Use when releasing"`, "+# Cut a release", "+1. tag", "+2. push", "new file mode 100644"} {
		if !strings.Contains(p.Diff, want) {
			t.Errorf("skill patch missing %q:\n%s", want, p.Diff)
		}
	}
	if p.Base != cfg.Remedies.SkillsDir {
		t.Errorf("base = %q", p.Base)
	}
	if err := os.MkdirAll(filepath.Dir(skillTarget), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(skillTarget, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := BuildPatch(skill, cfg); !errors.Is(err, ErrTargetExists) {
		t.Errorf("existing skill err = %v", err)
	}

	for _, rt := range []string{"", "weird"} {
		if _, err := BuildPatch(store.Finding{ID: "F3", RemedyType: rt}, cfg); !errors.Is(err, ErrNoRemedy) {
			t.Errorf("remedy %q err = %v", rt, err)
		}
	}
	_, err = BuildPatch(store.Finding{ID: "F4", RemedyType: Ignore, Proposal: Proposal{Why: "covered"}.encode()}, cfg)
	if !errors.Is(err, ErrNotPatchable) || !strings.Contains(err.Error(), "covered") {
		t.Errorf("ignore err = %v", err)
	}
}

func TestClaudeMDPatchCases(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	mk := func(path string) store.Finding {
		return store.Finding{ID: "F", Title: "t", RemedyType: ClaudeMD, Target: path, Proposal: Proposal{Rule: "Use zerolog"}.encode()}
	}
	if _, err := ClaudeMDPatch(nil); !errors.Is(err, ErrNotPatchable) {
		t.Errorf("no findings err = %v", err)
	}
	if _, err := ClaudeMDPatch([]store.Finding{mk("")}); !errors.Is(err, ErrNoRemedy) {
		t.Errorf("no target err = %v", err)
	}
	wrong := mk(filepath.Join(dir, "CLAUDE.md"))
	wrong.RemedyType = Hook
	if _, err := ClaudeMDPatch([]store.Finding{wrong}); !errors.Is(err, ErrNotPatchable) {
		t.Errorf("wrong remedy err = %v", err)
	}
	// An unreadable target (a directory) is an error, not an empty file.
	if _, err := ClaudeMDPatch([]store.Finding{mk(dir)}); err == nil {
		t.Error("directory target should fail")
	}

	tests := []struct {
		name, existing string
		create         bool
		want           []string
		notWant        []string
	}{
		{"new file", "", false, []string{"new file mode 100644", "+## Learned conventions", "+- Use zerolog"}, []string{"@@ -0,0 +1,3 @@\n+## Learned conventions\n+\n+- Use zerolog\n+"}},
		{"empty existing file", "", true, []string{"@@ -0,0 +1,3 @@", "+## Learned conventions"}, []string{"new file mode"}},
		{"existing with section", "# R\n\n## Learned conventions\n\n- old\n", true, []string{"@@ -3,3 +3,4 @@", "+- Use zerolog", " - old"}, []string{"+## Learned conventions"}},
		{"existing without trailing newline", "# R\n- old", true, []string{"-- old\n\\ No newline at end of file\n+- old\n", "+## Learned conventions"}, nil},
		{"existing one line", "# R\n", true, []string{"@@ -1,1 +1,5 @@", " # R"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "CLAUDE.md")
			if tt.create {
				if err := os.WriteFile(path, []byte(tt.existing), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			p, err := ClaudeMDPatch([]store.Finding{mk(path)})
			if err != nil {
				t.Fatal(err)
			}
			for _, w := range tt.want {
				if !strings.Contains(p.Diff, w) {
					t.Errorf("missing %q in:\n%s", w, p.Diff)
				}
			}
			for _, w := range tt.notWant {
				if strings.Contains(p.Diff, w) {
					t.Errorf("unexpected %q in:\n%s", w, p.Diff)
				}
			}
		})
	}
}

func TestBaseFor(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for path, want := range map[string]string{
		filepath.Join(home, ".claude", "CLAUDE.md"):   home,
		filepath.Join(home, "CLAUDE.md"):              home,
		"/work/repo/CLAUDE.md":                        "/work/repo",
		"/work/repo/.claude/CLAUDE.md":                "/work/repo",
		filepath.Join(home, "code", "r", "CLAUDE.md"): filepath.Join(home, "code", "r"),
	} {
		if got := baseFor(path); got != want {
			t.Errorf("baseFor(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestDiffHelpers(t *testing.T) {
	t.Parallel()
	if got := NewFileDiff("a/b.txt", "x\ny\n", true); got != "diff --git a/a/b.txt b/a/b.txt\nnew file mode 100755\n--- /dev/null\n+++ b/a/b.txt\n@@ -0,0 +1,2 @@\n+x\n+y\n" {
		t.Errorf("NewFileDiff:\n%s", got)
	}
	if got := AppendDiff("f", "", []string{"a"}, false); !strings.Contains(got, "new file mode 100644") {
		t.Errorf("missing file -> new file diff:\n%s", got)
	}
	if shellQuote(`it's`) != `'it'\''s'` {
		t.Error("shellQuote")
	}
	if fileExists(filepath.Join(t.TempDir(), "nope")) || !fileExists(t.TempDir()) {
		t.Error("fileExists")
	}
}

func TestTarget(t *testing.T) {
	t.Parallel()
	cfg := testConfig("/cfg")
	if got := target(ClaudeMD, Proposal{}, []string{"/work/api"}, cfg); got != "/work/api/CLAUDE.md" {
		t.Errorf("single repo: %q", got)
	}
	if got := target(ClaudeMD, Proposal{}, []string{"/a", "/b"}, cfg); got != "/cfg/CLAUDE.md" {
		t.Errorf("many repos: %q", got)
	}
	if got := target(ClaudeMD, Proposal{}, nil, cfg); got != "/cfg/CLAUDE.md" {
		t.Errorf("no repos: %q", got)
	}
	if got := target(Hook, Proposal{Rule: "Don't edit go.sum"}, nil, cfg); got != "/cfg/hooks/sminer-don-t-edit-go-sum.sh" {
		t.Errorf("hook: %q", got)
	}
	if got := target(Skill, Proposal{SkillName: "rel"}, nil, cfg); got != "/cfg/skills/rel/SKILL.md" {
		t.Errorf("skill: %q", got)
	}
	if target(Ignore, Proposal{}, nil, cfg) != "" || target("", Proposal{}, nil, cfg) != "" {
		t.Error("other remedies have no target")
	}
}

func TestReposOf(t *testing.T) {
	t.Parallel()
	got := reposOf([]store.Signal{{Repo: "/b"}, {Repo: ""}, {Repo: "/a"}, {Repo: "/b"}})
	if !reflect.DeepEqual(got, []string{"/a", "/b"}) {
		t.Errorf("reposOf = %v", got)
	}
}

func TestLoadExisting(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg := testConfig(dir)
	write := func(rel, body string) string {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	script := write("scripts/guard.sh", "#!/bin/sh\necho no-lockfiles-allowed\n")
	write("settings.json", `{"hooks":{"PreToolUse":[{"matcher":"Edit","hooks":[{"type":"command","command":"bash `+script+`"}]}]}}`)
	write("settings.local.json", `{"hooks":{"PostToolUse":[{"matcher":"Write","hooks":[{"command":"echo local-marker"}]}]}}`)
	write("hooks/check.sh", "grep deploy-guard-marker")
	write("hooks/.git/ignored.sh", "git-internal-marker")
	write("hooks/node_modules/x.sh", "node-modules-marker")
	big := write("hooks/big.sh", strings.Repeat("a", 300<<10)+"huge-marker")
	_ = big
	write("skills/release/SKILL.md", "---\nname: release\ndescription: \"Cut a tagged release of the service\"\n---\n")
	write("skills/nodesc/other.txt", "x")
	write("skills/file.txt", "not a dir")
	write("settings-bad/settings.json", "{not json")
	repo := filepath.Join(dir, "repo")
	write("repo/.claude/settings.json", `{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"command":"repo-hook-marker"}]}]}}`)
	write("repo/.claude/settings.local.json", `{"hooks":{}}`)

	ex := LoadExisting(cfg)
	ex.LoadRepo(repo)
	ex.LoadRepo("")
	ex.loadSettings(filepath.Join(dir, "settings-bad", "settings.json"))
	ex.loadSettings(filepath.Join(dir, "missing.json"))

	tests := []struct {
		literal string
		found   bool
		suffix  string
	}{
		{"no-lockfiles-allowed", true, "guard.sh"},
		{"local-marker", true, "settings.local.json (PostToolUse Write)"},
		{"deploy-guard-marker", true, "check.sh"},
		{"repo-hook-marker", true, "settings.json (PreToolUse Bash)"},
		{"git-internal-marker", false, ""},
		{"node-modules-marker", false, ""},
		{"huge-marker", false, ""},
		{"zz", false, ""},
		{"  ", false, ""},
		{"absent-marker", false, ""},
	}
	for _, tt := range tests {
		got := ex.CoveredByHook(tt.literal)
		if (got != "") != tt.found || !strings.HasSuffix(got, tt.suffix) {
			t.Errorf("CoveredByHook(%q) = %q, found=%v", tt.literal, got, tt.found)
		}
	}
	if got := ex.CoveredBySkill("release", ""); got != filepath.Join(cfg.Remedies.SkillsDir, "release") {
		t.Errorf("by slug = %q", got)
	}
	if got := ex.CoveredBySkill("other", "Cut a tagged release of the service"); got != filepath.Join(cfg.Remedies.SkillsDir, "release") {
		t.Errorf("by description = %q", got)
	}
	if got := ex.CoveredBySkill("other", "Totally different topic here"); got != "" {
		t.Errorf("unrelated = %q", got)
	}
	if _, ok := ex.skills["nodesc"]; !ok || ex.skills["nodesc"] != "" {
		t.Errorf("skill without SKILL.md should be listed with an empty description: %v", ex.skills)
	}
	if _, ok := ex.skills["file.txt"]; ok {
		t.Error("plain files are not skills")
	}
}

func TestClaudeMDCoverage(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg := testConfig(dir)
	repo := filepath.Join(dir, "repo")
	if err := os.MkdirAll(filepath.Join(repo, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	must := func(p, body string) {
		t.Helper()
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must(cfg.Remedies.GlobalClaudeMD, "# Global\n\n- Prefer zerolog for structured logging in services.\n")
	must(filepath.Join(repo, ".claude", "CLAUDE.md"), "- Always wrap errors with context using fmt.Errorf and the percent w verb\n")
	must(filepath.Join(repo, "CLAUDE.local.md"), "- ok\n")
	ex := LoadExisting(cfg)

	files := ex.ClaudeMDFiles([]string{repo, ""})
	if len(files) != 4 || files[0] != cfg.Remedies.GlobalClaudeMD || files[1] != filepath.Join(repo, "CLAUDE.md") {
		t.Errorf("files = %v", files)
	}
	c, ok := ex.ClaudeMDCoverage("Prefer zerolog for logging", nil)
	if !ok || c.Line != 3 || c.Path != cfg.Remedies.GlobalClaudeMD || !strings.HasPrefix(c.Text, "- Prefer zerolog") || !strings.HasSuffix(c.String(), "CLAUDE.md:3") {
		t.Errorf("jaccard match = %+v %v", c, ok)
	}
	// High overlap of the rule's own words counts even when the line is long.
	c, ok = ex.ClaudeMDCoverage("wrap errors with context using fmt.Errorf", []string{repo})
	if !ok || c.Line != 1 || !strings.HasSuffix(c.Path, filepath.Join(".claude", "CLAUDE.md")) {
		t.Errorf("overlap match = %+v %v", c, ok)
	}
	if _, ok := ex.ClaudeMDCoverage("wrap errors with context using fmt.Errorf", nil); ok {
		t.Error("repo files are only searched for that repo")
	}
	if _, ok := ex.ClaudeMDCoverage("single", nil); ok {
		t.Error("rules under two tokens never match")
	}
	if _, ok := ex.ClaudeMDCoverage("totally unrelated sentence about databases", []string{repo}); ok {
		t.Error("unrelated rule matched")
	}
	if got := ex.CoveredByClaudeMD("Prefer zerolog for logging", nil); !strings.HasSuffix(got, "CLAUDE.md:3") {
		t.Errorf("CoveredByClaudeMD = %q", got)
	}
	if ex.CoveredByClaudeMD("nothing matches this at all", nil) != "" {
		t.Error("expected no coverage")
	}
	if !modTime(filepath.Join(repo, "CLAUDE.md")).IsZero() {
		t.Error("missing file has zero mtime")
	}
}

func TestSynthesize(t *testing.T) {
	t.Parallel()
	f := store.Finding{ID: "F", Family: cluster.Behavior}
	ss := []store.Signal{
		{Type: store.Correction, SessionID: "a", Rule: "Use pnpm", Excerpt: "(interrupted) use pnpm\nnow"},
		{Type: store.Rewind, SessionID: "b", Excerpt: "pnpm please"},
	}
	for i := range 10 {
		ss = append(ss, store.Signal{Type: store.Correction, SessionID: "c", Excerpt: strings.Repeat("e", i+1)})
	}
	tests := []struct {
		name    string
		reply   string
		err     error
		want    synthesis
		wantErr string
	}{
		{"hook", "ok {\"title\":\"Use pnpm\",\"remedy\":\"hook\",\"rule\":\"Use pnpm.\",\"hook\":{\"matcher\":\"Bash\",\"field\":\"command\",\"pattern\":\"^npm \"}} done",
			nil, synthesis{"Use pnpm", Hook, Proposal{Rule: "Use pnpm.", Matcher: "Bash", Field: "command", Pattern: "^npm "}}, ""},
		{"hook with default matcher", `{"title":"t","remedy":"hook","rule":"r","hook":{"matcher":"(","field":"file_path","pattern":"go\\.sum$"}}`,
			nil, synthesis{"t", Hook, Proposal{Rule: "r", Matcher: "Edit|Write|MultiEdit", Field: "file_path", Pattern: `go\.sum$`}}, ""},
		{"hook without matcher on command", `{"title":"t","remedy":"hook","rule":"r","hook":{"field":"command","pattern":"x"}}`,
			nil, synthesis{"t", Hook, Proposal{Rule: "r", Matcher: "Bash", Field: "command", Pattern: "x"}}, ""},
		{"hook without body downgrades", `{"title":"t","remedy":"hook","rule":"r"}`, nil, synthesis{"t", ClaudeMD, Proposal{Rule: "r"}}, ""},
		{"hook with bad field downgrades", `{"remedy":"hook","rule":"r","hook":{"field":"url","pattern":"x"}}`, nil, synthesis{"", ClaudeMD, Proposal{Rule: "r"}}, ""},
		{"hook with bad regex downgrades", `{"remedy":"hook","rule":"r","hook":{"field":"command","pattern":"("}}`, nil, synthesis{"", ClaudeMD, Proposal{Rule: "r"}}, ""},
		{"skill", `{"title":"t","remedy":"skill","rule":"r","skill":{"name":"Cut Release!","description":"when  releasing","steps":["a","b"]}}`,
			nil, synthesis{"t", Skill, Proposal{Rule: "r", SkillName: "cut-release", Description: "when releasing", Steps: []string{"a", "b"}}}, ""},
		{"skill without steps downgrades", `{"remedy":"skill","rule":"r","skill":{"name":"x","steps":[]}}`, nil, synthesis{"", ClaudeMD, Proposal{Rule: "r"}}, ""},
		{"skill missing downgrades", `{"remedy":"skill","rule":"r"}`, nil, synthesis{"", ClaudeMD, Proposal{Rule: "r"}}, ""},
		{"claude_md", `{"title":"t","remedy":"claude_md","rule":"Always\n do x"}`, nil, synthesis{"t", ClaudeMD, Proposal{Rule: "Always do x"}}, ""},
		{"ignore without rule", `{"remedy":"ignore"}`, nil, synthesis{"", Ignore, Proposal{}}, ""},
		{"unknown remedy", `{"remedy":"magic","rule":"r"}`, nil, synthesis{"", "", Proposal{Rule: "r"}}, ""},
		{"no rule clears the remedy", `{"remedy":"claude_md"}`, nil, synthesis{"", "", Proposal{}}, ""},
		{"no json", "sorry, I cannot", nil, synthesis{}, "no JSON"},
		{"bad json", `{"remedy": }`, nil, synthesis{}, "bad synthesis JSON"},
		{"llm error", "", errors.New("overloaded"), synthesis{}, "overloaded"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			llm := &replyLLM{reply: tt.reply, err: tt.err}
			got, err := synthesize(context.Background(), llm, f, ss)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got  %+v\nwant %+v", got, tt.want)
			}
			if !strings.HasPrefix(llm.user, "12 occurrences across 3 sessions.") {
				t.Errorf("prompt header = %q", strings.SplitN(llm.user, "\n", 2)[0])
			}
			if n := strings.Count(llm.user, "<occurrence "); n != 8 {
				t.Errorf("prompt has %d occurrences, want the 8 most recent", n)
			}
		})
	}
	// The prompt carries the classifier rule and strips the interrupt marker.
	llm := &replyLLM{reply: `{}`}
	_, _ = synthesize(context.Background(), llm, f, ss[:2])
	if !strings.Contains(llm.user, "classifier rule: Use pnpm") || strings.Contains(llm.user, "(interrupted)") || !strings.Contains(llm.user, "developer said: use pnpm now") {
		t.Errorf("prompt:\n%s", llm.user)
	}
	if countSessions(ss) != 3 {
		t.Error("countSessions")
	}
}

func seedFinding(t *testing.T, st *store.Store, f store.Finding, ss ...store.Signal) {
	t.Helper()
	if err := st.SaveFinding(f); err != nil {
		t.Fatal(err)
	}
	for _, s := range ss {
		if _, err := st.DB.Exec(`INSERT INTO signals(id, session_id, repo, type, weight, ts, excerpt, rule, remedy_hint) VALUES(?,?,?,?,?,?,?,?,?)`,
			s.ID, s.SessionID, s.Repo, s.Type, 3, store.Ms(s.TS), s.Excerpt, s.Rule, s.RemedyHint); err != nil {
			t.Fatal(err)
		}
		if _, err := st.DB.Exec(`INSERT INTO finding_signals VALUES(?,?)`, f.ID, s.ID); err != nil {
			t.Fatal(err)
		}
	}
}

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestRunRoutesAndSynthesizes(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	cfg := testConfig(t.TempDir())
	mk := func(prefix, text string) []store.Signal {
		var out []store.Signal
		for i := range 3 {
			out = append(out, store.Signal{ID: prefix + string(rune('a'+i)), SessionID: prefix + string(rune('a'+i)), Repo: "/work/api", Type: store.Correction, Excerpt: text})
		}
		return out
	}
	seedFinding(t, st, store.Finding{ID: "B1", Family: cluster.Behavior, Status: store.StatusOpen}, mk("b1", "Don't edit go.sum by hand")...)
	seedFinding(t, st, store.Finding{ID: "H1", Family: cluster.Hook, Key: "k", Status: store.StatusOpen}, mk("h1", "blocked")...)
	seedFinding(t, st, store.Finding{ID: "DONE", Family: cluster.Behavior, Status: store.StatusAccepted, Synthesized: true, RemedyType: ClaudeMD, Title: "kept"}, mk("dn", "x")...)

	// Offline: heuristics only.
	stats, err := Run(context.Background(), st, []string{"B1", "H1", "DONE"}, Options{Config: cfg, Existing: LoadExisting(cfg)})
	if err != nil {
		t.Fatal(err)
	}
	if stats.ByType[Hook] != 1 || stats.ByType[ClaudeMD] != 2 || stats.Synthesized != 0 || stats.Errors != 0 {
		t.Errorf("stats = %+v", stats)
	}
	b1, _ := st.GetFinding("B1")
	if b1.RemedyType != Hook || b1.Target == "" || !strings.Contains(b1.Proposal, "go\\\\.sum") {
		t.Errorf("B1 = %+v", b1)
	}
	if d, _ := st.GetFinding("DONE"); d.Title != "kept" || d.Proposal != "" {
		t.Errorf("accepted+synthesized findings are left alone: %+v", d)
	}

	// With an LLM: behavior findings are drafted once, within budget.
	llm := &replyLLM{reply: `{"title":"Never hand-edit go.sum","remedy":"claude_md","rule":"Never hand-edit go.sum; use go mod tidy."}`}
	stats, err = Run(context.Background(), st, []string{"B1", "H1"}, Options{Config: cfg, Existing: LoadExisting(cfg), LLM: llm, Budget: classify.NewBudget(5)})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Synthesized != 1 || llm.calls != 1 {
		t.Errorf("stats = %+v calls = %d", stats, llm.calls)
	}
	b1, _ = st.GetFinding("B1")
	if !b1.Synthesized || b1.Title != "Never hand-edit go.sum" || b1.RemedyType != ClaudeMD {
		t.Errorf("B1 after synth = %+v", b1)
	}
	again, _ := Run(context.Background(), st, []string{"B1"}, Options{Config: cfg, Existing: LoadExisting(cfg), LLM: llm, Budget: classify.NewBudget(5)})
	if llm.calls != 1 || again.Synthesized != 0 {
		t.Errorf("already synthesized findings must not be drafted again (calls %d)", llm.calls)
	}

	// Errors are counted and routing falls back to heuristics.
	st2 := newTestStore(t)
	seedFinding(t, st2, store.Finding{ID: "B2", Family: cluster.Behavior, Status: store.StatusOpen}, mk("b2", "Don't edit go.sum by hand")...)
	failing := &replyLLM{err: errors.New("overloaded")}
	stats, err = Run(context.Background(), st2, []string{"B2"}, Options{Config: cfg, Existing: LoadExisting(cfg), LLM: failing, Budget: classify.NewBudget(5)})
	if err != nil || stats.Errors != 1 || stats.LastError == nil || stats.ByType[Hook] != 1 {
		t.Errorf("stats = %+v err = %v", stats, err)
	}
	// No budget: no calls.
	failing.calls = 0
	_, _ = Run(context.Background(), st2, []string{"B2"}, Options{Config: cfg, Existing: LoadExisting(cfg), LLM: failing})
	if failing.calls != 0 {
		t.Error("a nil budget must refuse calls")
	}
}

func TestRunErrors(t *testing.T) {
	t.Parallel()
	cfg := testConfig(t.TempDir())
	st := newTestStore(t)
	if _, err := Run(context.Background(), st, []string{"missing"}, Options{Config: cfg, Existing: LoadExisting(cfg)}); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("err = %v", err)
	}
	seedFinding(t, st, store.Finding{ID: "F", Family: cluster.Behavior, Status: store.StatusOpen})
	if _, err := st.DB.Exec(`DROP TABLE finding_signals`); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(context.Background(), st, []string{"F"}, Options{Config: cfg, Existing: LoadExisting(cfg)}); err == nil {
		t.Error("missing link table should fail")
	}
	if stats, err := Run(context.Background(), st, nil, Options{Config: cfg}); err != nil || len(stats.ByType) != 0 {
		t.Errorf("no ids: %+v %v", stats, err)
	}
	// A save failure is reported.
	st3 := newTestStore(t)
	seedFinding(t, st3, store.Finding{ID: "F", Family: cluster.Behavior, Status: store.StatusOpen}, sigs("s", 3, "prefer zerolog")...)
	if _, err := st3.DB.Exec(`CREATE TRIGGER no_update BEFORE UPDATE ON findings BEGIN SELECT RAISE(ABORT, 'read only'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(context.Background(), st3, []string{"F"}, Options{Config: cfg, Existing: LoadExisting(cfg)}); err == nil {
		t.Error("save failure should be returned")
	}
}

func TestEscalateExistingHookWins(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfig(dir)
	cfg.Remedies.GlobalClaudeMD = filepath.Join(dir, "CLAUDE.md")
	if err := os.WriteFile(cfg.Remedies.GlobalClaudeMD, []byte("- Don't edit go.sum by hand, run go mod tidy.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cfg.Remedies.HooksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.Remedies.HooksDir, "g.sh"), []byte("grep go.sum"), 0o755); err != nil {
		t.Fatal(err)
	}
	old := sigs("s", 3, "x")
	for i := range old {
		old[i].TS = time.Now().Add(time.Hour) // after the file mtime
		old[i].Rule = "Don't edit go.sum"
	}
	ex := LoadExisting(cfg)
	r, p, where := dedupe(ClaudeMD, Proposal{Rule: "Don't edit go.sum by hand, run go mod tidy"}, store.Finding{Family: cluster.Behavior}, old, nil, ex)
	if r != Ignore || where == "" || !strings.Contains(p.Why, "existing hook") {
		t.Errorf("existing hook should cover the escalated rule: %s %+v %q", r, p, where)
	}
	// Non-behaviour findings never count as unheeded.
	r, p, _ = dedupe(ClaudeMD, Proposal{Rule: "Don't edit go.sum by hand, run go mod tidy"}, store.Finding{Family: cluster.Failure}, old, nil, ex)
	if r != Ignore || p.Unheeded != nil || !strings.HasPrefix(p.Why, "already in ") {
		t.Errorf("failure family: %s %+v", r, p)
	}
	// Skill dedupe.
	if err := os.MkdirAll(filepath.Join(cfg.Remedies.SkillsDir, "release"), 0o755); err != nil {
		t.Fatal(err)
	}
	r, p, where = dedupe(Skill, Proposal{SkillName: "release"}, store.Finding{Family: cluster.Behavior}, sigs("s", 3, "x"), nil, LoadExisting(cfg))
	if r != Ignore || !strings.HasSuffix(where, "release") || p.Why == "" {
		t.Errorf("skill dedupe: %s %+v %q", r, p, where)
	}
}
