package ingest

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rossedman/sminer/internal/config"
	"github.com/rossedman/sminer/internal/redact"
	"github.com/rossedman/sminer/internal/store"
)

var now = time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)

// copyCorpus copies testdata/projects so tests can append to it.
func copyCorpus(t *testing.T) string {
	t.Helper()
	src := filepath.Join("..", "..", "testdata", "projects")
	dst := t.TempDir()
	err := filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if info.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	return dst
}

func open(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func runIngest(t *testing.T, st *store.Store, root string, cfg config.Config) Stats {
	t.Helper()
	red, _ := redact.New(nil)
	stats, err := Run(context.Background(), st, Options{Roots: []string{root}, Since: 90 * 24 * time.Hour, Now: now, Config: cfg, Redact: red})
	if err != nil {
		t.Fatal(err)
	}
	return stats
}

func count(t *testing.T, st *store.Store, q string, args ...any) int {
	t.Helper()
	var n int
	if err := st.DB.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestIngestCorpus(t *testing.T) {
	root := copyCorpus(t)
	st := open(t)
	stats := runIngest(t, st, root, config.Default())

	if stats.Files != 11 || stats.Sessions != 11 || stats.Malformed != 1 {
		t.Errorf("stats = %+v", stats)
	}
	if stats.UnknownTypes["weird-new-type"] != 1 || len(stats.UnknownTypes) != 1 {
		t.Errorf("unknown types = %v", stats.UnknownTypes)
	}
	if stats.Versions["1.0.98"] == 0 || stats.Versions["2.0.14"] == 0 {
		t.Errorf("versions = %v (both schema versions should be covered)", stats.Versions)
	}
	kinds := map[string]int{
		KindInterrupt: 2, // one tool_result marker, one user text marker
		KindRejected:  1,
		KindHook:      3,
		KindFixup:     5, // four Read-based, one attachment
	}
	for k, want := range kinds {
		if got := count(t, st, `SELECT COUNT(*) FROM turns WHERE kind=?`, k); got != want {
			t.Errorf("kind %s = %d, want %d", k, got, want)
		}
	}
	if got := count(t, st, `SELECT COUNT(*) FROM turns WHERE text LIKE '%ghp_%'`); got != 0 {
		t.Errorf("token not redacted in %d turns", got)
	}
	var feedback string
	st.DB.QueryRow(`SELECT text FROM turns WHERE kind=?`, KindRejected).Scan(&feedback)
	if feedback != "never force push to main, open a PR instead" {
		t.Errorf("rejection feedback = %q", feedback)
	}
	if got := count(t, st, `SELECT COUNT(*) FROM sessions WHERE repo='/work/web'`); got != 4 {
		t.Errorf("web sessions = %d", got)
	}
}

func TestIngestIncremental(t *testing.T) {
	root := copyCorpus(t)
	st := open(t)
	first := runIngest(t, st, root, config.Default())

	again := runIngest(t, st, root, config.Default())
	if again.Unchanged != 11 || again.Turns != 0 || again.Lines != 0 {
		t.Errorf("re-run reparsed: %+v", again)
	}

	// Finish the partial trailing line in d1111111's transcript.
	path := filepath.Join(root, "-work-api", "d1111111-0000-4000-8000-000000000001.jsonl")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(` line finished"},"uuid":"late-1","timestamp":"2026-10-12T10:00:00.000Z","cwd":"/work/api"}` + "\n")
	f.Close()
	future := now.Add(time.Minute)
	os.Chtimes(path, future, future)

	third := runIngest(t, st, root, config.Default())
	if third.Unchanged != 10 || third.Lines != 1 || third.Turns != 1 {
		t.Errorf("incremental = %+v", third)
	}
	var text string
	st.DB.QueryRow(`SELECT text FROM turns WHERE event_uuid='late-1'`).Scan(&text)
	if text != "partial line finished" {
		t.Errorf("late turn text = %q", text)
	}
	if got := count(t, st, `SELECT COUNT(*) FROM turns`); got != first.Turns+1 {
		t.Errorf("turns = %d, want %d", got, first.Turns+1)
	}
	if got := count(t, st, `SELECT dirty FROM sessions WHERE session_id='d1111111-0000-4000-8000-000000000001'`); got != 1 {
		t.Error("session not marked dirty")
	}
}

func TestIngestExcludeRepos(t *testing.T) {
	root := copyCorpus(t)
	st := open(t)
	cfg := config.Default()
	cfg.ExcludeRepos = []string{"/work/web"}
	runIngest(t, st, root, cfg)
	if got := count(t, st, `SELECT COUNT(*) FROM sessions WHERE repo LIKE '/work/web%'`); got != 0 {
		t.Errorf("excluded repo ingested: %d sessions", got)
	}
	if got := count(t, st, `SELECT COUNT(*) FROM turns t JOIN sessions s USING(session_id) WHERE s.repo='/work/api'`); got == 0 {
		t.Error("other repos missing")
	}
}

func TestIngestWindow(t *testing.T) {
	root := copyCorpus(t)
	old := now.Add(-200 * 24 * time.Hour)
	filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if strings.HasSuffix(p, ".jsonl") {
			os.Chtimes(p, old, old)
		}
		return nil
	})
	st := open(t)
	stats := runIngest(t, st, root, config.Default())
	if stats.Skipped != 11 || stats.Turns != 0 {
		t.Errorf("window not applied: %+v", stats)
	}
}

func TestDBPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x", "s.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("db mode = %v, want 0600", info.Mode().Perm())
	}
}

// fixupSession writes a one-session transcript from steps of the form
// {"write", path, content}, {"bash", command} and {"read", path, content},
// ingests it and returns the number of fix-up turns.
func fixupSession(t *testing.T, steps ...[]string) int {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "-work-api")
	os.MkdirAll(dir, 0o755)
	const sid = "f0000000-0000-4000-8000-000000000001"
	var b strings.Builder
	ts := time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)
	line := func(typ, uuid string, msg, result any) {
		ev := map[string]any{"type": typ, "uuid": uuid, "sessionId": sid, "cwd": "/work/api", "version": "2.0.14",
			"timestamp": ts.Format(time.RFC3339Nano), "message": msg}
		if result != nil {
			ev["toolUseResult"] = result
		}
		ts = ts.Add(time.Second)
		j, _ := json.Marshal(ev)
		b.Write(j)
		b.WriteByte('\n')
	}
	for i, s := range steps {
		id := fmt.Sprintf("toolu_%d", i)
		var name string
		var input map[string]any
		var result any
		switch s[0] {
		case "write":
			name, input = "Write", map[string]any{"file_path": s[1], "content": s[2]}
		case "bash":
			name, input = "Bash", map[string]any{"command": s[1]}
		case "read":
			n := strings.Count(s[2], "\n")
			name, input = "Read", map[string]any{"file_path": s[1]}
			result = map[string]any{"type": "text", "file": map[string]any{"filePath": s[1], "content": s[2], "numLines": n, "startLine": 1, "totalLines": n}}
		}
		line("assistant", fmt.Sprintf("a-%d", i), map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "tool_use", "id": id, "name": name, "input": input}}}, nil)
		line("user", fmt.Sprintf("u-%d", i), map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "tool_result", "tool_use_id": id, "content": "ok", "is_error": false}}}, result)
	}
	path := filepath.Join(dir, sid+".jsonl")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	os.Chtimes(path, now, now)
	st := open(t)
	runIngest(t, st, root, config.Default())
	return count(t, st, `SELECT COUNT(*) FROM turns WHERE kind=?`, KindFixup)
}

func TestFixupBashWrites(t *testing.T) {
	const p = "/work/api/internal/cluster/cluster.go"
	orig := "package cluster\n\nfunc A() int { return 1 }\n"
	changed := "package cluster\n\nfunc A() int { return 2 }\n"
	tests := []struct {
		name  string
		steps [][]string
		want  int
	}{
		{"sed -i on the file", [][]string{{"write", p, orig}, {"bash", `sed -i '' 's/return 1/return 2/' internal/cluster/cluster.go`}, {"read", p, changed}}, 0},
		{"python heredoc names the file", [][]string{{"write", p, orig}, {"bash", "python3 - <<'EOF'\np='internal/cluster/cluster.go'\nopen(p,'w').write(s)\nEOF"}, {"read", p, changed}}, 0},
		{"gofmt -w ./...", [][]string{{"write", p, orig}, {"bash", "gofmt -w ./..."}, {"read", p, changed}}, 0},
		{"gofmt -w over a parent dir", [][]string{{"write", p, orig}, {"bash", "gofmt -l -w internal/"}, {"read", p, changed}}, 0},
		{"rewrite after re-baseline", [][]string{{"write", p, orig}, {"bash", "gofmt -w ."}, {"read", p, changed}, {"read", p, orig}}, 1},
		{"unrelated bash", [][]string{{"write", p, orig}, {"bash", "go test ./..."}, {"read", p, changed}}, 1},
		{"formatter over another dir", [][]string{{"write", p, orig}, {"bash", "gofmt -w cmd/"}, {"read", p, changed}}, 1},
		{"sed -i on another file", [][]string{{"write", p, orig}, {"bash", `sed -i '' 's/a/b/' main.go`}, {"read", p, changed}}, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := fixupSession(t, tt.steps...); got != tt.want {
				t.Errorf("fix-ups = %d, want %d", got, tt.want)
			}
		})
	}
}
