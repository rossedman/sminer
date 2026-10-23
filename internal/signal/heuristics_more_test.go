package signal

import (
	"strings"
	"testing"
	"time"

	"github.com/rossedman/sminer/internal/ingest"
	"github.com/rossedman/sminer/internal/store"
)

func TestIsCorrectionBoundaries(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("Add the widget handler and wire it into the router. ", 20)
	tests := []struct {
		name string
		text string
		want bool
	}{
		{"empty", "", false},
		{"whitespace", "  \n\t ", false},
		{"slash command with cue", "/clear don't", false},
		{"opening cue in a long prompt counts", "No, don't do that. " + long, true},
		{"cue buried in a long prompt is ignored", long + " never do this", false},
		{"long prompt without cue", long, false},
		{"benign opener then strong cue", "no problem, but don't touch the schema", true},
		{"benign opener alone", "no worries at all", false},
		{"never mind", "never mind, carry on", false},
		{"single weak cue", "remember the tests", false},
		{"two weak cues", "please use zerolog and always wrap errors", true},
		{"instead", "pnpm instead", true},
		{"revert", "please revert it", true},
		{"again broken", "it is still completely broken", true},
		{"use not", "use pnpm, not npm", true},
		{"plain request", "Implement pagination for the widgets endpoint", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := IsCorrection(tt.text); got != tt.want {
				t.Errorf("IsCorrection(%q) = %v, want %v", tt.text, got, tt.want)
			}
		})
	}
}

func TestHookKeyVariants(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		in      string
		wantKey string
		wantMsg string
	}{
		{"script by absolute path", "PreToolUse:Bash hook error: [/home/me/hooks/guard.sh]: no rm -rf", "PreToolUse:Bash guard.sh", "no rm -rf"},
		{"interpreter takes last field", "PostToolUse:Edit hook error: [python3 /hooks/lint.py]: lint failed", "PostToolUse:Edit lint.py", "lint failed"},
		{"interpreter with only itself", "PreToolUse hook error: [bash]: nope", "PreToolUse bash", "nope"},
		{"no script masks variable parts", "UserPromptSubmit hook blocked: /tmp/x.sh exited 2 for 'abc'", "UserPromptSubmit userpromptsubmit hook blocked: <path> exited # for <s>", "UserPromptSubmit hook blocked: /tmp/x.sh exited 2 for 'abc'"},
		{"no label falls back to hook", "hook error: something happened 42 times", "hook something happened # times", "something happened 42 times"},
		{"empty brackets ignored", "PreCompact hook error: [ ]: x", "PreCompact x", "x"},
		{"multi-line message uses first line", "PreToolUse:Bash hook error: [g.sh]: first\nsecond", "PreToolUse:Bash g.sh", "first"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			key, msg := HookKey(tt.in)
			if key != tt.wantKey || msg != tt.wantMsg {
				t.Errorf("HookKey(%q) = %q, %q; want %q, %q", tt.in, key, msg, tt.wantKey, tt.wantMsg)
			}
		})
	}
	_, msg := HookKey("PreToolUse hook error: [a.sh]: " + strings.Repeat("x", 500))
	if len(msg) != 200 || !strings.HasSuffix(msg, "...") {
		t.Errorf("long message not truncated to 200: len %d", len(msg))
	}
}

func TestCommandKeyVariants(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"":                               "",
		"   ":                            "",
		"cd api":                         "",
		"cd api && ":                     "",
		"cd":                             "",
		"FOO=1 BAR=2":                    "",
		"FOO=1 BAR=2 make test":          "make test",
		"git status | head -5":           "git status",
		"echo hi; ls -la":                "echo hi",
		"true || go build ./...":         "true",
		"git -C repo log":                "git",
		"ls ./dir":                       "ls",
		`grep "x" file`:                  "grep",
		"kubectl get pods -n prod":       "kubectl get",
		"  go   test  ":                  "go test",
		"cd a && cd b && npm install -g": "npm install",
	}
	for in, want := range tests {
		if got := CommandKey(in); got != want {
			t.Errorf("CommandKey(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFileKey(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"/r/main.go":               "*.go",
		"/r/go.mod":                "go.mod",
		"/r/go.sum":                "go.sum",
		"/r/pkg/foo_test.go":       "*.go",
		"/r/Makefile":              "Makefile",
		"/r/.gitignore":            ".gitignore",
		"/r/README.md":             "README.md",
		"/r/docs/guide.md":         "*.md",
		"/r/web/package-lock.json": "package-lock.json",
		"/r/a.verylongextension":   "a.verylongextension",
		"/r/x.tar.gz":              "*.gz",
		"/r/CLAUDE.md":             "CLAUDE.md",
		"/r/deploy/Cargo.toml":     "Cargo.toml",
	}
	for in, want := range tests {
		if got := FileKey(in); got != want {
			t.Errorf("FileKey(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNeedsClassifier(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		sig  store.Signal
		want bool
	}{
		{"correction", store.Signal{Type: store.Correction}, true},
		{"rewind", store.Signal{Type: store.Rewind}, true},
		{"repeated", store.Signal{Type: store.RepeatedInstruction}, true},
		{"interrupt with reason", store.Signal{Type: store.Interrupt, Excerpt: "keep vendor"}, true},
		{"bare interrupt", store.Signal{Type: store.Interrupt, Excerpt: "(interrupted) Bash ls"}, false},
		{"hook", store.Signal{Type: store.HookBlock}, false},
		{"tool failure", store.Signal{Type: store.ToolFailure}, false},
		{"fixup", store.Signal{Type: store.ManualFixup}, false},
		{"unknown", store.Signal{Type: "other"}, false},
	}
	for _, tt := range tests {
		if got := NeedsClassifier(tt.sig); got != tt.want {
			t.Errorf("%s: NeedsClassifier = %v, want %v", tt.name, got, tt.want)
		}
	}
	if IsBareInterrupt(store.Signal{Type: store.Correction, Excerpt: "(interrupted) x"}) {
		t.Error("only interrupts can be bare")
	}
}

func TestCandidates(t *testing.T) {
	t.Parallel()
	turns := []store.Turn{
		{ID: "1", SessionID: "s", Kind: ingest.KindUserText, Text: "start: no tools yet, so not a candidate"},
		{ID: "2", SessionID: "s", Kind: ingest.KindAssistantText, Text: "I'll edit it"},
		{ID: "3", SessionID: "s", Kind: ingest.KindToolUse, ToolName: "Edit", ToolKey: "/r/a.go"},
		{ID: "4", SessionID: "s", Kind: ingest.KindUserText, Text: "no, don't edit that file"},
		{ID: "5", SessionID: "s", Kind: ingest.KindToolUse, ToolName: "Bash", ToolKey: "ls", Sidechain: true},
		{ID: "6", SessionID: "s", Kind: ingest.KindUserText, Text: "sidechain tool use does not count"},
		{ID: "7", SessionID: "s", Kind: ingest.KindToolUse, ToolName: "Bash", ToolKey: "go test"},
		{ID: "8", SessionID: "s", Kind: ingest.KindInterrupt},
		{ID: "9", SessionID: "s", Kind: ingest.KindUserText, Text: "after interrupt resets actions"},
		{ID: "10", SessionID: "s", Kind: ingest.KindToolUse, ToolName: "Bash", ToolKey: "go vet"},
		{ID: "11", SessionID: "s", Kind: ingest.KindUserText, Text: ""},
		{ID: "12", SessionID: "s", Kind: ingest.KindToolUse, ToolName: "Read", ToolKey: "/r/b.go"},
		{ID: "13", SessionID: "s", Kind: ingest.KindRejected},
		{ID: "14", SessionID: "s", Kind: ingest.KindUserText, Text: "after rejection resets actions"},
		{ID: "15", SessionID: "s", Kind: ingest.KindToolUse, ToolName: "Write", ToolKey: "/r/c.go"},
		{ID: "16", SessionID: "s", Kind: ingest.KindUserText, Text: strings.Repeat("y", 1500)},
	}
	got := Candidates(turns)
	if len(got) != 2 {
		t.Fatalf("got %d candidates: %+v", len(got), got)
	}
	c := got[0]
	if c.Turn != "4" || c.SessionID != "s" || !c.Heuristic || c.Message != "no, don't edit that file" ||
		!strings.Contains(c.Action, "Assistant said: I'll edit it") || !strings.Contains(c.Action, "Edit /r/a.go") {
		t.Errorf("first = %+v", c)
	}
	if got[1].Turn != "16" || len(got[1].Message) != 1000 || got[1].Heuristic {
		t.Errorf("second = %+v (message len %d)", got[1], len(got[1].Message))
	}
	if len(Candidates(nil)) != 0 {
		t.Error("nil turns should give no candidates")
	}
}

func TestIsScratchPathHome(t *testing.T) {
	home := "/home/fake-user"
	t.Setenv("HOME", home)
	if !IsScratchPath(home + "/.claude/projects/x.jsonl") {
		t.Error("~/.claude is scratch")
	}
	if IsScratchPath(home + "/code/app/main.go") {
		t.Error("ordinary file under home is not scratch")
	}
	if !IsScratchPath("/work/x/scratch-workspaces/a/b.go") {
		t.Error("scratch-workspaces is scratch")
	}
	if !IsScratchPath("/tmp") {
		t.Error("the temp root itself is scratch")
	}
	if IsScratchPath("/tmpfoo/x.go") {
		t.Error("prefix match must respect path boundaries")
	}
}

func TestDetectMoreBranches(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	turns := []store.Turn{
		{ID: "1#0", EventUUID: "1", Kind: ingest.KindUserText, Role: "user", Text: "run the build"},
		{ID: "2#0", EventUUID: "2", Kind: ingest.KindToolUse, Role: "assistant", ToolName: "Bash", ToolKey: "make", ToolUseID: "t1", TS: at},
		{ID: "3#0", EventUUID: "3", Kind: ingest.KindToolResult, Role: "user", ToolUseID: "t1", IsError: true, Text: "boom", TS: at},
		// A different failing command resets the streak, so no failure loop.
		{ID: "4#0", EventUUID: "4", Kind: ingest.KindToolUse, Role: "assistant", ToolName: "Bash", ToolKey: "make lint", ToolUseID: "t2"},
		{ID: "5#0", EventUUID: "5", Kind: ingest.KindToolResult, Role: "user", ToolUseID: "t2", IsError: true, Text: "boom"},
		// A success clears the pending failure.
		{ID: "6#0", EventUUID: "6", Kind: ingest.KindToolUse, Role: "assistant", ToolName: "Bash", ToolKey: "make lint", ToolUseID: "t3"},
		{ID: "7#0", EventUUID: "7", Kind: ingest.KindToolResult, Role: "user", ToolUseID: "t3", Text: "ok"},
		{ID: "8#0", EventUUID: "8", Kind: ingest.KindToolUse, Role: "assistant", ToolName: "Bash", ToolKey: "make lint", ToolUseID: "t4"},
		{ID: "9#0", EventUUID: "9", Kind: ingest.KindToolResult, Role: "user", ToolUseID: "t4", IsError: true, Text: "boom"},
		// Results for unknown tools or non-Bash tools are ignored.
		{ID: "10#0", EventUUID: "10", Kind: ingest.KindToolResult, Role: "user", ToolUseID: "nope", IsError: true},
		{ID: "11#0", EventUUID: "11", Kind: ingest.KindToolUse, Role: "assistant", ToolName: "Read", ToolKey: "/r/a.go", ToolUseID: "t5"},
		{ID: "12#0", EventUUID: "12", Kind: ingest.KindToolResult, Role: "user", ToolUseID: "t5", IsError: true},
		{ID: "13#0", EventUUID: "13", Kind: ingest.KindToolResult, Role: "user", ToolUseID: "t5", IsError: true},
		// Sidechain turns are skipped entirely.
		{ID: "14#0", EventUUID: "14", Kind: ingest.KindUserText, Role: "user", Text: "no, don't", Sidechain: true},
		// Harness text is not a user correction.
		{ID: "15#0", EventUUID: "15", Kind: ingest.KindUserText, Role: "user", Text: "<task-notification> no, don't </task-notification>"},
	}
	sigs := Detect(store.Session{ID: "s"}, turns)
	for _, s := range sigs {
		t.Errorf("unexpected signal %s (%q)", s.Type, s.Excerpt)
	}
}

func TestDetectToolFailureLoop(t *testing.T) {
	t.Parallel()
	mk := func(i int, cmd string) []store.Turn {
		id := string(rune('a' + i))
		return []store.Turn{
			{ID: id + "#0", Kind: ingest.KindToolUse, Role: "assistant", ToolName: "Bash", ToolKey: cmd, ToolUseID: id},
			{ID: id + "#1", Kind: ingest.KindToolResult, Role: "user", ToolUseID: id, IsError: true, Text: "line1\nline2"},
		}
	}
	var turns []store.Turn
	for i := range 4 {
		turns = append(turns, mk(i, "go test ./...")...)
	}
	sigs := Detect(store.Session{ID: "s", Repo: "/r"}, turns)
	if len(sigs) != 1 {
		t.Fatalf("a long streak should emit once, got %d", len(sigs))
	}
	s := sigs[0]
	if s.Type != store.ToolFailure || s.Key != "go test" || s.Weight != store.Medium ||
		!strings.Contains(s.Excerpt, "`go test ./...` failed 2 times in a row: line1") {
		t.Errorf("signal = %+v", s)
	}
}

func TestDetectHookContext(t *testing.T) {
	t.Parallel()
	turns := []store.Turn{
		{ID: "1#0", Kind: ingest.KindToolUse, Role: "assistant", ToolName: "Edit", ToolKey: "/r/go.sum", ToolUseID: "t1", Text: "edit\nit"},
		{ID: "2#0", Kind: ingest.KindHook, Role: "user", ToolUseID: "t1", Text: "PreToolUse:Edit hook error: [~/h/no-lock.sh]: lockfiles are generated"},
		{ID: "3#0", Kind: ingest.KindHook, Role: "user", ToolUseID: "unknown", Text: "PreToolUse:Edit hook error: [~/h/no-lock.sh]: again"},
	}
	sigs := Detect(store.Session{ID: "s"}, turns)
	if len(sigs) != 2 {
		t.Fatalf("sigs = %+v", sigs)
	}
	if sigs[0].Context != "Edit /r/go.sum — edit it" || sigs[0].Key != "PreToolUse:Edit no-lock.sh" || sigs[0].Excerpt != "lockfiles are generated" {
		t.Errorf("first = %+v", sigs[0])
	}
	if sigs[1].Context != "" {
		t.Errorf("hook with unknown tool should have no context: %+v", sigs[1])
	}
}

func TestDescribeActionsCaps(t *testing.T) {
	t.Parallel()
	var ts []store.Turn
	for i := range 6 {
		ts = append(ts, store.Turn{Kind: ingest.KindToolUse, ToolName: "Bash", ToolKey: strings.Repeat("k", 300) + string(rune('a'+i))})
	}
	got := describeActions(ts)
	if len(got) > 1400 {
		t.Errorf("len = %d, want <= 1400", len(got))
	}
	if strings.Count(got, "Bash ") != 4 && !strings.HasSuffix(got, "...") {
		t.Errorf("only the last four actions should be described: %q", got)
	}
	if describeActions(nil) != "" {
		t.Error("no actions -> empty description")
	}
}
