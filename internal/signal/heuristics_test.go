package signal

import (
	"testing"
	"time"

	"github.com/rossedman/sminer/internal/ingest"
	"github.com/rossedman/sminer/internal/store"
)

func TestIsCorrection(t *testing.T) {
	tests := []struct {
		text string
		want bool
	}{
		{"No, don't use testify/assert.", true},
		{"revert that change", true},
		{"I said snake_case", true},
		{"use pnpm instead", true},
		{"why did you delete the vendor dir?", true},
		{"you forgot to update the docs", true},
		{"we use zerolog here", true},
		{"No problem, thanks!", false},
		{"no worries, go ahead", false},
		{"I don't know, what do you think?", false},
		{"looks good, commit it", false},
		{"/review", false},
		{"Add a handler for GET /widgets", false},
		{"Actually, please use the v2 client", true}, // two weak cues
		{"actually looks fine", false},
	}
	for _, tt := range tests {
		if got := IsCorrection(tt.text); got != tt.want {
			t.Errorf("IsCorrection(%q) = %v, want %v", tt.text, got, tt.want)
		}
	}
}

func TestHookKey(t *testing.T) {
	key, msg := HookKey("PreToolUse:Edit hook error: [~/src/claude-hooks/no-lockfiles.sh]: Editing lockfiles is not allowed")
	if key != "PreToolUse:Edit no-lockfiles.sh" || msg != "Editing lockfiles is not allowed" {
		t.Errorf("got %q %q", key, msg)
	}
	k1, _ := HookKey("PreToolUse:Bash hook blocked: line 12 of /tmp/a.sh is bad")
	k2, _ := HookKey("PreToolUse:Bash hook blocked: line 99 of /var/b.sh is bad")
	if k1 != k2 {
		t.Errorf("variable parts not masked: %q vs %q", k1, k2)
	}
}

func TestCommandKey(t *testing.T) {
	tests := map[string]string{
		"go test ./...":                   "go test",
		"cd api && FOO=1 go test -v ./x":  "go test",
		"npm run build -- --prod":         "npm run",
		"make":                            "make",
		"/usr/bin/python3 -m pytest":      "python3",
		"terraform plan -out=plan.tfplan": "terraform plan",
	}
	for in, want := range tests {
		if got := CommandKey(in); got != want {
			t.Errorf("CommandKey(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDetect(t *testing.T) {
	ts := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	n := 0
	turn := func(kind, role, text string, mod func(*store.Turn)) store.Turn {
		n++
		t := store.Turn{ID: string(rune('a'+n)) + "#0", EventUUID: string(rune('a' + n)), SessionID: "s1",
			Kind: kind, Role: role, Text: text, TS: ts.Add(time.Duration(n) * time.Second)}
		if mod != nil {
			mod(&t)
		}
		return t
	}
	bash := func(id, cmd string) func(*store.Turn) {
		return func(t *store.Turn) { t.ToolName, t.ToolKey, t.ToolUseID = "Bash", cmd, id }
	}
	errResult := func(id string) func(*store.Turn) {
		return func(t *store.Turn) { t.ToolUseID, t.IsError = id, true }
	}
	turns := []store.Turn{
		turn(ingest.KindUserText, "user", "set up the build", func(t *store.Turn) { t.ParentUUID = "root" }),
		turn(ingest.KindToolUse, "assistant", "", bash("t1", "make build")),
		turn(ingest.KindToolResult, "user", "make: *** No rule", errResult("t1")),
		turn(ingest.KindToolUse, "assistant", "", bash("t2", "make build")),
		turn(ingest.KindToolResult, "user", "make: *** No rule", errResult("t2")),
		turn(ingest.KindUserText, "user", "no, don't use make here, use just", nil),
		turn(ingest.KindToolUse, "assistant", "", bash("t3", "rm -rf vendor")),
		turn(ingest.KindInterrupt, "user", "", nil),
		turn(ingest.KindUserText, "user", "never delete vendor", nil),
		turn(ingest.KindHook, "user", "PreToolUse:Bash hook error: [guard.sh]: no rm -rf", func(t *store.Turn) { t.ToolUseID = "t3" }),
		turn(ingest.KindUserText, "user", "set up the build with just", func(t *store.Turn) { t.ParentUUID = "root" }),
		turn(ingest.KindFixup, "", "", func(t *store.Turn) { t.ToolKey = "/r/main.go" }),
	}
	sigs := Detect(store.Session{ID: "s1", Repo: "/r"}, turns)
	got := map[string]int{}
	for _, s := range sigs {
		got[s.Type]++
		if s.Repo != "/r" {
			t.Errorf("repo not attributed: %+v", s)
		}
		if s.Type == store.Interrupt && s.Excerpt != "never delete vendor" {
			t.Errorf("interrupt excerpt = %q", s.Excerpt)
		}
		if s.Type == store.ToolFailure && s.Key != "make build" {
			t.Errorf("failure key = %q", s.Key)
		}
	}
	want := map[string]int{
		store.ToolFailure: 1, store.Correction: 1, store.Interrupt: 1,
		store.HookBlock: 1, store.Rewind: 1, store.ManualFixup: 1,
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %d, want %d (all: %v)", k, got[k], v, got)
		}
	}
	// IDs are deterministic so classifier results survive re-detection.
	again := Detect(store.Session{ID: "s1", Repo: "/r"}, turns)
	for i := range sigs {
		if sigs[i].ID != again[i].ID {
			t.Fatal("signal ids not stable")
		}
	}
}

func TestDetectCountsOneInterruptOnce(t *testing.T) {
	turns := []store.Turn{
		{ID: "1#0", Kind: ingest.KindUserText, Role: "user", Text: "clean up"},
		{ID: "2#0", Kind: ingest.KindToolUse, Role: "assistant", ToolName: "Bash", ToolKey: "rm -rf vendor", ToolUseID: "t"},
		{ID: "3#0", Kind: ingest.KindInterrupt, Role: "user", ToolUseID: "t"},
		{ID: "4#0", Kind: ingest.KindInterrupt, Role: "user"},
		{ID: "5#0", Kind: ingest.KindUserText, Role: "user", Text: "keep vendor"},
	}
	sigs := Detect(store.Session{}, turns)
	if len(sigs) != 1 || sigs[0].Type != store.Interrupt || sigs[0].Excerpt != "keep vendor" {
		t.Errorf("sigs = %+v", sigs)
	}
}
