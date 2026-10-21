package ingest

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rossedman/sminer/internal/config"
	"github.com/rossedman/sminer/internal/redact"
	"github.com/rossedman/sminer/internal/store"
	"github.com/rossedman/sminer/internal/transcript"
)

func newExtractor(t testing.TB) *extractor {
	t.Helper()
	red, err := redact.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &extractor{red: red, repos: newRepoResolver(), fixups: map[string]*fileState{}, pending: map[string]pendingWrite{}}
}

func decode(t testing.TB, line string) transcript.Event {
	t.Helper()
	ev, err := transcript.Decode([]byte(line))
	if err != nil {
		t.Fatalf("decode %s: %v", line, err)
	}
	return ev
}

func j(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func userText(text string, extra map[string]any) string {
	m := map[string]any{"type": "user", "uuid": "u1", "sessionId": "s", "message": map[string]any{"role": "user", "content": text}}
	for k, v := range extra {
		m[k] = v
	}
	return j(m)
}

func TestExtractUser(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		line     string
		wantKind []string
		check    func(t *testing.T, turns []store.Turn)
	}{
		{"plain text", userText("add a handler", nil), []string{KindUserText}, func(t *testing.T, ts []store.Turn) {
			if ts[0].Text != "add a handler" || ts[0].Role != "user" || ts[0].ID != "u1#0" || ts[0].SessionID != "s" {
				t.Errorf("turn = %+v", ts[0])
			}
		}},
		{"system reminder stripped", userText("hi <system-reminder>secret stuff</system-reminder>", nil), []string{KindUserText}, func(t *testing.T, ts []store.Turn) {
			if ts[0].Text != "hi" {
				t.Errorf("text = %q", ts[0].Text)
			}
		}},
		{"only a reminder", userText("<system-reminder>x</system-reminder>", nil), nil, nil},
		{"meta messages are dropped", userText("hello", map[string]any{"isMeta": true}), nil, nil},
		{"local command output dropped", userText("<local-command-stdout>ok</local-command-stdout>", nil), nil, nil},
		{"caveat dropped", userText("Caveat: the messages below were generated", nil), nil, nil},
		{"harness text dropped", userText("<task-notification>done</task-notification>", nil), nil, nil},
		{"slash command", userText("ran <command-name>/review</command-name><command-args>x</command-args>", nil), []string{KindCommand}, func(t *testing.T, ts []store.Turn) {
			if ts[0].ToolKey != "/review" || ts[0].Text != "" {
				t.Errorf("turn = %+v", ts[0])
			}
		}},
		{"interrupt with reason", userText("[Request interrupted by user]\nkeep vendor", nil), []string{KindInterrupt}, func(t *testing.T, ts []store.Turn) {
			if ts[0].Text != "keep vendor" {
				t.Errorf("text = %q", ts[0].Text)
			}
		}},
		{"modified note makes a fixup", userText("Note: /r/a.go was modified, either by the user or by a linter. The change is intentional.", nil), []string{KindFixup, KindUserText}, func(t *testing.T, ts []store.Turn) {
			if ts[0].ToolKey != "/r/a.go" || ts[0].ID != "u1#100" {
				t.Errorf("fixup = %+v", ts[0])
			}
		}},
		{"modified note in a meta message", userText("Note: /r/a.go was modified, either by the user or by a linter.", map[string]any{"isMeta": true}), []string{KindFixup}, nil},
		{"secrets are redacted", userText("export TOKEN=ghp_abcdefghijklmnopqrstuvwxyz0123456789AB now", nil), []string{KindUserText}, func(t *testing.T, ts []store.Turn) {
			if strings.Contains(ts[0].Text, "ghp_") {
				t.Errorf("secret kept: %q", ts[0].Text)
			}
		}},
		{"long text truncated", userText(strings.Repeat("word ", 1000), nil), []string{KindUserText}, func(t *testing.T, ts []store.Turn) {
			if len(ts[0].Text) != maxUserText {
				t.Errorf("len = %d", len(ts[0].Text))
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			x := newExtractor(t)
			turns := x.extract(decode(t, tt.line))
			var kinds []string
			for _, tu := range turns {
				kinds = append(kinds, tu.Kind)
			}
			if strings.Join(kinds, ",") != strings.Join(tt.wantKind, ",") {
				t.Fatalf("kinds = %v, want %v", kinds, tt.wantKind)
			}
			if tt.check != nil {
				tt.check(t, turns)
			}
			if x.seq != 1 {
				t.Errorf("seq = %d, want 1", x.seq)
			}
		})
	}
}

func TestExtractAssistantAndSystem(t *testing.T) {
	t.Parallel()
	x := newExtractor(t)
	asst := j(map[string]any{"type": "assistant", "sessionId": "s", "timestamp": "2026-09-21T09:00:20Z", "isSidechain": true,
		"message": map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "text", "text": "  "},
			map[string]any{"type": "text", "text": "I'll edit it"},
			map[string]any{"type": "tool_use", "id": "tu1", "name": "Write", "input": map[string]any{"file_path": "/r/a.go", "content": "package a"}},
			map[string]any{"type": "tool_use", "id": "tu2", "name": "Grep", "input": map[string]any{"pattern": "foo", "path": "/r"}},
		}}})
	turns := x.extract(decode(t, asst))
	if len(turns) != 3 {
		t.Fatalf("turns = %+v", turns)
	}
	if turns[0].Kind != KindAssistantText || turns[0].Text != "I'll edit it" || turns[0].Role != "assistant" || !turns[0].Sidechain {
		t.Errorf("text turn = %+v", turns[0])
	}
	// Event without uuid: id is a hash of session and offset.
	if !strings.HasSuffix(turns[0].ID, "#1") || strings.HasPrefix(turns[0].ID, "#") {
		t.Errorf("id = %q", turns[0].ID)
	}
	if turns[1].ToolName != "Write" || turns[1].ToolKey != "/r/a.go" || turns[1].Text != "write 9 bytes" || turns[1].ToolUseID != "tu1" {
		t.Errorf("write turn = %+v", turns[1])
	}
	if turns[2].ToolKey != "foo" || turns[2].Text != "/r" {
		t.Errorf("grep turn = %+v", turns[2])
	}
	if _, ok := x.pending["tu1"]; !ok {
		t.Error("write not tracked for fix-up detection")
	}
	if _, ok := x.pending["tu2"]; ok {
		t.Error("read-only tool tracked")
	}

	hook := j(map[string]any{"type": "system", "uuid": "h1", "sessionId": "s", "content": "PreToolUse:Bash hook error: [g.sh]: blocked"})
	ht := x.extract(decode(t, hook))
	if len(ht) != 1 || ht[0].Kind != KindHook || !ht[0].IsError || ht[0].Role != "system" || ht[0].ID != "h1#0" {
		t.Errorf("hook = %+v", ht)
	}
	plain := j(map[string]any{"type": "system", "uuid": "h2", "sessionId": "s", "content": "compacted conversation"})
	if got := x.extract(decode(t, plain)); len(got) != 0 {
		t.Errorf("plain system event produced %+v", got)
	}
	if got := x.extract(decode(t, `{"type":"summary","sessionId":"s","summary":"x"}`)); len(got) != 0 {
		t.Errorf("summary produced %+v", got)
	}
}

func TestExtractAttachment(t *testing.T) {
	t.Parallel()
	x := newExtractor(t)
	tests := []struct {
		att  string
		want int
	}{
		{`{"type":"edited_text_file","filename":"/r/a.go","snippet":"x"}`, 1},
		{`{"type":"edited_text_file"}`, 0},
		{`{"type":"todo","filename":"/r/a.go"}`, 0},
		{`"not an object"`, 0},
	}
	for _, tt := range tests {
		line := `{"type":"attachment","uuid":"a1","sessionId":"s","attachment":` + tt.att + `}`
		got := x.extract(decode(t, line))
		if len(got) != tt.want {
			t.Errorf("%s -> %d turns, want %d", tt.att, len(got), tt.want)
		}
		if tt.want == 1 && (got[0].Kind != KindFixup || got[0].ToolKey != "/r/a.go") {
			t.Errorf("fixup = %+v", got[0])
		}
	}
}

func toolResultEvent(t testing.TB, id, content string, isErr bool, result any) transcript.Event {
	t.Helper()
	m := map[string]any{"type": "user", "uuid": "r-" + id, "sessionId": "s", "message": map[string]any{"role": "user", "content": []any{
		map[string]any{"type": "tool_result", "tool_use_id": id, "content": content, "is_error": isErr}}}}
	if result != nil {
		m["toolUseResult"] = result
	}
	return decode(t, j(m))
}

func TestToolResultKinds(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		content  string
		isErr    bool
		wantKind string
		wantText string
		wantErr  bool
	}{
		{"success output dropped", "all good", false, KindToolResult, "", false},
		{"error output kept", "exit status 1: boom", true, KindToolResult, "exit status 1: boom", true},
		{"rejection with feedback", rejectPrefix + " (eg. a file edit). the user said:\n use a PR", true, KindRejected, "use a PR", true},
		{"rejection without feedback", rejectPrefix + ".", true, KindRejected, "", true},
		{"interrupt", "[Request interrupted by user for tool use]", true, KindInterrupt, "", true},
		{"hook output", "PreToolUse:Bash hook error: [g.sh]: blocked", true, KindHook, "PreToolUse:Bash hook error: [g.sh]: blocked", true},
		{"hook text on a success result", "PostToolUse:Edit hook blocked this", false, KindHook, "PostToolUse:Edit hook blocked this", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			x := newExtractor(t)
			turns := x.extract(toolResultEvent(t, "tu", tt.content, tt.isErr, nil))
			if len(turns) != 1 {
				t.Fatalf("turns = %+v", turns)
			}
			got := turns[0]
			if got.Kind != tt.wantKind || got.Text != tt.wantText || got.IsError != tt.wantErr || got.ToolUseID != "tu" || got.Role != "user" {
				t.Errorf("turn = %+v", got)
			}
		})
	}
}

func readResult(path, content string, start, num, total int) map[string]any {
	return map[string]any{"type": "text", "file": map[string]any{"filePath": path, "content": content, "numLines": num, "startLine": start, "totalLines": total}}
}

func TestTrackFixup(t *testing.T) {
	t.Parallel()
	const p = "/r/a.go"
	write := func(x *extractor, id, tool string, in map[string]any) {
		in["file_path"] = p
		x.pending[id] = pendingWrite{tool: tool, input: json.RawMessage(j(in))}
	}
	fixups := func(turns []store.Turn) int {
		n := 0
		for _, tu := range turns {
			if tu.Kind == KindFixup {
				n++
			}
		}
		return n
	}
	t.Run("write then identical read is not a fixup", func(t *testing.T) {
		t.Parallel()
		x := newExtractor(t)
		write(x, "w", "Write", map[string]any{"content": "package a\n"})
		x.extract(toolResultEvent(t, "w", "ok", false, nil))
		got := x.extract(toolResultEvent(t, "r", "ok", false, readResult(p, "package a\n", 1, 1, 1)))
		if fixups(got) != 0 {
			t.Errorf("got %+v", got)
		}
	})
	t.Run("changed content is a fixup and rebaselines", func(t *testing.T) {
		t.Parallel()
		x := newExtractor(t)
		write(x, "w", "Write", map[string]any{"content": "package a\n"})
		x.extract(toolResultEvent(t, "w", "ok", false, nil))
		got := x.extract(toolResultEvent(t, "r", "ok", false, readResult(p, "package b\n", 1, 1, 1)))
		if fixups(got) != 1 || got[1].ToolKey != p || got[1].ID != "r-r#200" {
			t.Errorf("got %+v", got)
		}
		again := x.extract(toolResultEvent(t, "r2", "ok", false, readResult(p, "package b\n", 1, 1, 1)))
		if fixups(again) != 0 {
			t.Error("same content twice must not repeat the fix-up")
		}
	})
	t.Run("whitespace-only change ignored", func(t *testing.T) {
		t.Parallel()
		x := newExtractor(t)
		write(x, "w", "Write", map[string]any{"content": "a  b\n"})
		x.extract(toolResultEvent(t, "w", "ok", false, nil))
		got := x.extract(toolResultEvent(t, "r", "ok", false, readResult(p, "a b", 1, 1, 1)))
		if fixups(got) != 0 {
			t.Error("formatting is not a fix-up")
		}
	})
	t.Run("partial read is not compared", func(t *testing.T) {
		t.Parallel()
		x := newExtractor(t)
		write(x, "w", "Write", map[string]any{"content": "one\ntwo\n"})
		x.extract(toolResultEvent(t, "w", "ok", false, nil))
		got := x.extract(toolResultEvent(t, "r", "ok", false, readResult(p, "zzz", 5, 1, 10)))
		if fixups(got) != 0 {
			t.Error("partial read compared")
		}
		if x.fixups[p].content != "one\ntwo\n" {
			t.Error("partial read must not rebaseline")
		}
	})
	t.Run("edit on known content", func(t *testing.T) {
		t.Parallel()
		x := newExtractor(t)
		write(x, "w", "Write", map[string]any{"content": "x := 1\ny := 1\n"})
		x.extract(toolResultEvent(t, "w", "ok", false, nil))
		write(x, "e", "Edit", map[string]any{"old_string": "1", "new_string": "2", "replace_all": true})
		x.extract(toolResultEvent(t, "e", "ok", false, nil))
		write(x, "m", "MultiEdit", map[string]any{"edits": []any{map[string]any{"old_string": "x", "new_string": "z"}}})
		x.extract(toolResultEvent(t, "m", "ok", false, nil))
		if got := x.fixups[p].content; got != "z := 2\ny := 2\n" {
			t.Errorf("content = %q", got)
		}
	})
	t.Run("edit on unknown content expects the new text", func(t *testing.T) {
		t.Parallel()
		x := newExtractor(t)
		write(x, "e", "Edit", map[string]any{"old_string": "a", "new_string": "return computeTheAnswer()"})
		x.extract(toolResultEvent(t, "e", "ok", false, nil))
		write(x, "e2", "Edit", map[string]any{"old_string": "b", "new_string": "short"})
		x.extract(toolResultEvent(t, "e2", "ok", false, nil))
		if len(x.fixups[p].expected) != 1 {
			t.Fatalf("expected = %v (short replacements are not tracked)", x.fixups[p].expected)
		}
		ok := x.extract(toolResultEvent(t, "r", "ok", false, readResult(p, "func f() { return computeTheAnswer() }", 1, 1, 1)))
		if fixups(ok) != 0 {
			t.Error("expected text present: no fix-up")
		}
		x.fixups[p] = &fileState{expected: []string{"return computeTheAnswer()"}}
		bad := x.extract(toolResultEvent(t, "r2", "ok", false, readResult(p, "func f() {}", 1, 1, 1)))
		if fixups(bad) != 1 {
			t.Error("expected text missing: fix-up")
		}
	})
	t.Run("failed write is ignored", func(t *testing.T) {
		t.Parallel()
		x := newExtractor(t)
		write(x, "w", "Write", map[string]any{"content": "package a\n"})
		x.extract(toolResultEvent(t, "w", "denied", true, nil))
		if len(x.fixups) != 0 {
			t.Error("failed write tracked")
		}
	})
	t.Run("write without a path or with bad input is ignored", func(t *testing.T) {
		t.Parallel()
		x := newExtractor(t)
		x.pending["a"] = pendingWrite{tool: "Write", input: json.RawMessage(`{"content":"x"}`)}
		x.pending["b"] = pendingWrite{tool: "Write", input: json.RawMessage(`not json`)}
		x.extract(toolResultEvent(t, "a", "ok", false, nil))
		x.extract(toolResultEvent(t, "b", "ok", false, nil))
		if len(x.fixups) != 0 {
			t.Error("tracked a write with no usable path")
		}
	})
	t.Run("bash forgets files it may have rewritten", func(t *testing.T) {
		t.Parallel()
		x := newExtractor(t)
		write(x, "w", "Write", map[string]any{"content": "package a\n"})
		x.extract(toolResultEvent(t, "w", "ok", false, nil))
		x.pending["b"] = pendingWrite{tool: "Bash", input: json.RawMessage(`{"command":"gofmt -w /r/a.go"}`)}
		x.extract(toolResultEvent(t, "b", "boom", true, nil))
		if len(x.fixups) != 0 {
			t.Error("bash rewrite should drop the model")
		}
		x.pending["b2"] = pendingWrite{tool: "Bash", input: json.RawMessage(`garbage`)}
		x.extract(toolResultEvent(t, "b2", "ok", false, nil))
	})
	t.Run("non-object results and malformed file info are ignored", func(t *testing.T) {
		t.Parallel()
		x := newExtractor(t)
		for _, res := range []any{"a string", map[string]any{"file": map[string]any{"filePath": ""}}, map[string]any{"other": 1}, map[string]any{"file": "x"}} {
			if got := x.extract(toolResultEvent(t, "r", "ok", false, res)); fixups(got) != 0 {
				t.Errorf("result %v produced a fix-up", res)
			}
		}
	})
}

func TestToolInputSummary(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, tool, in, wantKey, wantText string
	}{
		{"bash", "Bash", `{"command":"go  test\n./...","description":"run tests"}`, "go test ./...", "run tests"},
		{"edit", "Edit", `{"file_path":"/r/a.go","new_string":"x"}`, "/r/a.go", "x"},
		{"multiedit", "MultiEdit", `{"file_path":"/r/a.go"}`, "/r/a.go", ""},
		{"write", "Write", `{"file_path":"/r/a.go","content":"abc"}`, "/r/a.go", "write 3 bytes"},
		{"read", "Read", `{"file_path":"/r/a.go"}`, "/r/a.go", ""},
		{"notebook", "NotebookEdit", `{"notebook_path":"/r/n.ipynb"}`, "/r/n.ipynb", ""},
		{"glob", "Glob", `{"pattern":"**/*.go","path":"/r"}`, "**/*.go", "/r"},
		{"webfetch", "WebFetch", `{"url":"https://x.dev"}`, "https://x.dev", ""},
		{"task", "Task", `{"description":"explore","prompt":"look around"}`, "explore", "look around"},
		{"agent", "Agent", `{"description":"d","prompt":"p"}`, "d", "p"},
		{"unknown tool with path", "Custom", `{"path":"/x","query":"q"}`, "/x", ""},
		{"unknown tool with query", "Custom", `{"query":"q"}`, "q", ""},
		{"unknown tool nothing", "Custom", `{"n":1}`, "", ""},
		{"bad json", "Bash", `nope`, "", ""},
	}
	for _, tt := range tests {
		key, text := toolInputSummary(tt.tool, json.RawMessage(tt.in))
		if key != tt.wantKey || text != tt.wantText {
			t.Errorf("%s: got %q, %q; want %q, %q", tt.name, key, text, tt.wantKey, tt.wantText)
		}
	}
}

func TestBashMayWrite(t *testing.T) {
	t.Parallel()
	const p = "/work/api/internal/cluster/cluster.go"
	tests := []struct {
		name string
		cmd  string
		path string
		want bool
	}{
		{"empty command", "", p, false},
		{"empty path", "gofmt -w .", "", false},
		{"full path named", "vim " + p, p, true},
		{"basename named", "sed -i s/a/b/ cluster.go", p, true},
		{"unrelated command", "go test ./...", p, false},
		{"gofmt with no path args runs over the cwd", "gofmt -w", p, true},
		{"gofmt over dot", "gofmt -l -w .", p, true},
		{"gofmt over the tree", "gofmt -w ./...", p, true},
		{"gofmt over parent dir", "gofmt -w internal/", p, true},
		{"gofmt over other dir", "gofmt -w cmd/", p, false},
		{"glob in the directory", "prettier --write internal/cluster/*.go", p, true},
		{"git checkout may rewrite", "git checkout -- .", p, true},
		{"read only git", "git status", p, false},
		{"go vet is not a rewriter", "go vet ./...", p, false},
		{"parent traversal", "gofmt -w ../x", p, true},
		{"redirect target is not a path arg", "black --line-length=100 other/dir/", p, false},
	}
	for _, tt := range tests {
		if got := BashMayWrite(tt.cmd, tt.path); got != tt.want {
			t.Errorf("%s: BashMayWrite(%q, %q) = %v, want %v", tt.name, tt.cmd, tt.path, got, tt.want)
		}
	}
}

func FuzzBashMayWrite(f *testing.F) {
	for _, s := range []string{
		"gofmt -w ./...", "sed -i '' 's/a/b/' internal/cluster/cluster.go", "go test ./...", "git checkout -- .",
		"python3 - <<'EOF'\np='internal/cluster/cluster.go'\nEOF", "cd api && make fmt", "ls -la", "",
	} {
		f.Add(s, "/work/api/internal/cluster/cluster.go")
	}
	f.Add("cat a", "")
	f.Add("cat /", "/")
	f.Fuzz(func(t *testing.T, cmd, path string) {
		got := BashMayWrite(cmd, path)
		if BashMayWrite(cmd, path) != got {
			t.Fatal("non-deterministic")
		}
		if (cmd == "" || path == "") && got {
			t.Fatalf("empty input must be false: %q %q", cmd, path)
		}
		// Naming the file always counts, so the answer errs towards yes.
		if path != "" && strings.Contains(cmd, path) && !got {
			t.Fatalf("command names the path but was not flagged: %q %q", cmd, path)
		}
		// A rewriting tool with no path arguments runs over the working
		// directory, which holds the file.
		if got2 := BashMayWrite("gofmt -w", "/a/b/c.go"); !got2 {
			t.Fatal("bare gofmt -w must count")
		}
	})
}

func BenchmarkBashMayWrite(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		BashMayWrite("cd api && gofmt -l -w internal/ && go test ./...", "/work/api/internal/cluster/cluster.go")
	}
}

func TestRepoResolver(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "proj", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	deep := filepath.Join(root, "proj", "a", "b")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	r := newRepoResolver()
	if got := r.resolve(deep); got != filepath.Join(root, "proj") {
		t.Errorf("resolve(deep) = %q", got)
	}
	if got := r.resolve(deep); got != filepath.Join(root, "proj") {
		t.Errorf("cached resolve = %q", got)
	}
	plain := filepath.Join(root, "other")
	if got := r.resolve(plain); got != plain {
		t.Errorf("no repo: resolve = %q, want the cwd", got)
	}
	if got := r.resolve("/nonexistent/x"); got != "/nonexistent/x" {
		t.Errorf("missing dir: %q", got)
	}
}

func TestHelpers(t *testing.T) {
	t.Parallel()
	if head(strings.Repeat("x", 5000)) != strings.Repeat("x", 4096) || head("abc") != "abc" {
		t.Error("head")
	}
	if !mentionsHook("a Hook b") || !mentionsHook("HOOK") || !mentionsHook("hook") || mentionsHook("nothing here") {
		t.Error("mentionsHook")
	}
	if !IsHook("PreToolUse:Bash hook error: x") || IsHook("see the hooks docs") || IsHook("") || !IsHook("Operation blocked by hook\nmore") {
		t.Error("IsHook")
	}
	if !isWriteTool("Write") || !isWriteTool("Edit") || !isWriteTool("MultiEdit") || isWriteTool("Read") {
		t.Error("isWriteTool")
	}
	if b2i(true) != 1 || b2i(false) != 0 {
		t.Error("b2i")
	}
	if squash(" a\n b\t c ") != "abc" {
		t.Error("squash")
	}
	x := newExtractor(t)
	long := strings.Repeat("a", 5000) + " ghp_abcdefghijklmnopqrstuvwxyz0123456789AB"
	if got := x.clean(long, 100); len(got) != 100 || !strings.HasSuffix(got, "...") {
		t.Errorf("clean len = %d", len(got))
	}
	straddle := strings.Repeat("a", 90) + " ghp_abcdefghijklmnopqrstuvwxyz0123456789AB " + strings.Repeat("b", 3000)
	if got := x.clean(straddle, 100); strings.Contains(got, "ghp_") {
		t.Errorf("secret straddling the cut leaked: %q", got)
	}
}

// ---- Run-level behaviour ----

func writeSession(t *testing.T, root, name string, lines ...string) string {
	t.Helper()
	dir := filepath.Join(root, "-work-api")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name+".jsonl")
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, now, now); err != nil {
		t.Fatal(err)
	}
	return p
}

func evLine(uuid, sid, typ, text string) string {
	return j(map[string]any{"type": typ, "uuid": uuid, "sessionId": sid, "cwd": "/work/api", "version": "2.0.1", "gitBranch": "main",
		"timestamp": "2026-10-18T09:00:00Z", "message": map[string]any{"role": typ, "content": text}})
}

func TestRunMissingRoot(t *testing.T) {
	t.Parallel()
	st := open(t)
	red, _ := redact.New(nil)
	_, err := Run(context.Background(), st, Options{Roots: []string{filepath.Join(t.TempDir(), "nope")}, Now: now, Redact: red})
	if err == nil || !strings.Contains(err.Error(), "walk") {
		t.Errorf("err = %v", err)
	}
}

func TestRunStatsAndSessions(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeSession(t, root, "s1",
		evLine("u1", "s1", "user", "first prompt"),
		`{bad json`,
		`{"type":"brand-new","sessionId":"s1"}`,
		`{"type":"user","uuid":"nos","message":{"role":"user","content":"no session id"}}`,
		evLine("u2", "s1", "user", "second prompt"),
	)
	st := open(t)
	red, _ := redact.New(nil)
	stats, err := Run(context.Background(), st, Options{Roots: []string{root}, Now: now, Redact: red, Workers: 1})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Lines != 5 || stats.Malformed != 1 || stats.Turns != 2 || stats.Sessions != 1 || stats.Files != 1 {
		t.Errorf("stats = %+v", stats)
	}
	if stats.UnknownTypes["brand-new"] != 1 || stats.Versions["2.0.1"] != 1 {
		t.Errorf("types=%v versions=%v", stats.UnknownTypes, stats.Versions)
	}
	var branch, cwd string
	var turns int
	if err := st.DB.QueryRow(`SELECT branch, cwd, turns FROM sessions WHERE session_id='s1'`).Scan(&branch, &cwd, &turns); err != nil {
		t.Fatal(err)
	}
	if branch != "main" || cwd != "/work/api" || turns != 2 {
		t.Errorf("session = %q %q %d", branch, cwd, turns)
	}
	if got := count(t, st, `SELECT malformed FROM files`); got != 1 {
		t.Errorf("malformed = %d", got)
	}
	if got := count(t, st, `SELECT count FROM event_types WHERE type='brand-new' AND known=0`); got != 1 {
		t.Errorf("event_types count = %d", got)
	}
}

func TestRunResetsTruncatedFile(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	long := writeSession(t, root, "s1",
		evLine("u1", "s1", "user", "first prompt that is rather long"),
		evLine("u2", "s1", "user", "second prompt that is rather long"),
	)
	st := open(t)
	red, _ := redact.New(nil)
	opts := Options{Roots: []string{root}, Now: now, Redact: red}
	if _, err := Run(context.Background(), st, opts); err != nil {
		t.Fatal(err)
	}
	// Rewrite the file shorter than the recorded offset.
	if err := os.WriteFile(long, []byte(evLine("u9", "s1", "user", "x")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	later := now.Add(time.Hour)
	_ = os.Chtimes(long, later, later)
	stats, err := Run(context.Background(), st, opts)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Turns != 1 {
		t.Errorf("stats = %+v", stats)
	}
	if got := count(t, st, `SELECT COUNT(*) FROM turns`); got != 1 {
		t.Errorf("turns after reset = %d, want 1 (old turns removed)", got)
	}
	if got := count(t, st, `SELECT lines FROM files`); got != 1 {
		t.Errorf("lines = %d, want reset to 1", got)
	}
}

func TestRunCancelled(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeSession(t, root, "s1", evLine("u1", "s1", "user", "hello there"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	st := open(t)
	red, _ := redact.New(nil)
	_, err := Run(ctx, st, Options{Roots: []string{root}, Now: now, Redact: red})
	if err == nil {
		t.Error("cancelled run should report the context error")
	}
}

func TestRunStoreErrors(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeSession(t, root, "s1", evLine("u1", "s1", "user", "hello there"))
	red, _ := redact.New(nil)
	opts := Options{Roots: []string{root}, Now: now, Redact: red}

	closed := open(t)
	_ = closed.Close()
	if _, err := Run(context.Background(), closed, opts); err == nil {
		t.Error("closed store")
	}
	for _, drop := range []string{"turns", "sessions", "event_types"} {
		st := open(t)
		if _, err := st.DB.Exec(`DROP TABLE ` + drop); err != nil {
			t.Fatal(err)
		}
		if _, err := Run(context.Background(), st, opts); err == nil {
			t.Errorf("dropping %s should fail the run", drop)
		}
	}
}

func TestRunUnreadableFile(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	p := writeSession(t, root, "s1", evLine("u1", "s1", "user", "hello there"))
	if err := os.Chmod(p, 0); err != nil {
		t.Fatal(err)
	}
	if f, err := os.Open(p); err == nil {
		f.Close()
		t.Log("running as a user that ignores file modes; expecting success")
		return
	}
	st := open(t)
	red, _ := redact.New(nil)
	if _, err := Run(context.Background(), st, Options{Roots: []string{root}, Now: now, Redact: red}); err == nil {
		t.Error("unreadable file should fail the run")
	}
}

func TestRunMultipleRootsAndNonJSONL(t *testing.T) {
	t.Parallel()
	r1, r2 := t.TempDir(), t.TempDir()
	writeSession(t, r1, "s1", evLine("u1", "s1", "user", "hello from root one"))
	writeSession(t, r2, "s2", evLine("u2", "s2", "user", "hello from root two"))
	if err := os.WriteFile(filepath.Join(r1, "notes.txt"), []byte("ignored"), 0o644); err != nil {
		t.Fatal(err)
	}
	st := open(t)
	red, _ := redact.New(nil)
	stats, err := Run(context.Background(), st, Options{Roots: []string{r1, r2}, Now: now, Redact: red, Config: config.Default()})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Files != 2 || stats.Sessions != 2 || stats.Turns != 2 {
		t.Errorf("stats = %+v", stats)
	}
}

func BenchmarkRun(b *testing.B) {
	red, _ := redact.New(nil)
	src := filepath.Join("..", "..", "testdata", "projects")
	b.ReportAllocs()
	for b.Loop() {
		st, err := store.Open(filepath.Join(b.TempDir(), "s.db"))
		if err != nil {
			b.Fatal(err)
		}
		_, err = Run(context.Background(), st, Options{Roots: []string{src}, Now: now, Redact: red, Config: config.Default()})
		if err != nil {
			b.Fatal(err)
		}
		_ = st.Close()
	}
}
