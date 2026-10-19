package classify

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rossedman/sminer/internal/store"
)

// fakeLLM answers from a function and records prompt sizes.
type fakeLLM struct {
	fn      func(user string) (string, error)
	calls   atomic.Int64
	maxSize atomic.Int64
}

func (f *fakeLLM) Complete(_ context.Context, _, user string, _ int) (string, error) {
	f.calls.Add(1)
	if n := int64(len(user)); n > f.maxSize.Load() {
		f.maxSize.Store(n)
	}
	return f.fn(user)
}

func TestParseVerdict(t *testing.T) {
	v, err := ParseVerdict("Sure:\n```json\n{\"correction\": true, \"rule\": \"Use  zerolog\\n\", \"remedy\": \"claude_md\", \"confidence\": 0.9}\n```")
	if err != nil || !v.Correction || v.Rule != "Use zerolog" || v.Remedy != "claude_md" {
		t.Errorf("got %+v %v", v, err)
	}
	v, _ = ParseVerdict(`{"correction": false, "remedy": "bogus"}`)
	if v.Remedy != "" {
		t.Errorf("bad remedy kept: %q", v.Remedy)
	}
	if _, err := ParseVerdict("no json"); err == nil {
		t.Error("expected error")
	}
}

func TestUserPromptCapped(t *testing.T) {
	s := store.Signal{Type: store.Correction, Context: strings.Repeat("ctx ", 2000), Excerpt: strings.Repeat("msg ", 2000)}
	if n := len(UserPrompt(s)); n > 2200 {
		t.Errorf("prompt is %d bytes, want about 2 KB", n)
	}
}

func seed(t *testing.T, n int) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	for i := range n {
		excerpt := "no, use zerolog"
		if i%2 == 1 {
			excerpt = "what does this do?"
		}
		_, err := st.DB.Exec(`INSERT INTO signals(id, session_id, type, weight, ts, excerpt, context) VALUES(?,?,?,?,?,?,?)`,
			string(rune('a'+i)), "s", store.Correction, 3, store.Ms(time.Now()), excerpt, "Edit main.go")
		if err != nil {
			t.Fatal(err)
		}
	}
	// Deterministic types are never sent.
	st.DB.Exec(`INSERT INTO signals(id, session_id, type, weight, ts, excerpt) VALUES('hook','s','hook_block',2,1,'blocked')`)
	return st
}

func TestRunConfirmsAndCaches(t *testing.T) {
	st := seed(t, 6)
	llm := &fakeLLM{fn: func(user string) (string, error) {
		if strings.Contains(user, "zerolog") {
			return `{"correction": true, "rule": "Use zerolog for logging", "remedy": "claude_md"}`, nil
		}
		return `{"correction": false}`, nil
	}}
	stats, err := Run(context.Background(), st, Options{LLM: llm, Budget: NewBudget(100), Concurrency: 3})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Calls != 6 || stats.Confirmed != 3 || stats.Rejected != 3 || stats.Errors != 0 {
		t.Errorf("stats = %+v", stats)
	}
	var rule string
	st.DB.QueryRow(`SELECT rule FROM signals WHERE id='a' AND confirmed=1`).Scan(&rule)
	if rule != "Use zerolog for logging" {
		t.Errorf("rule = %q", rule)
	}
	// A second run sends nothing: results are cached on the signals.
	stats, _ = Run(context.Background(), st, Options{LLM: llm, Budget: NewBudget(100)})
	if stats.Calls != 0 || llm.calls.Load() != 6 {
		t.Errorf("re-run made calls: %+v", stats)
	}
	if llm.maxSize.Load() > 2200 {
		t.Errorf("prompt too large: %d", llm.maxSize.Load())
	}
}

func TestRunRespectsBudget(t *testing.T) {
	st := seed(t, 10)
	llm := &fakeLLM{fn: func(string) (string, error) { return `{"correction": true, "rule": "x"}`, nil }}
	b := NewBudget(4)
	stats, _ := Run(context.Background(), st, Options{LLM: llm, Budget: b, Concurrency: 2})
	if stats.Calls != 4 || b.Used() != 4 {
		t.Errorf("calls = %d used = %d, want 4", stats.Calls, b.Used())
	}
}

func TestRunAbortsWhenNothingWorks(t *testing.T) {
	st := seed(t, 20)
	llm := &fakeLLM{fn: func(string) (string, error) { return "", errors.New("401 invalid x-api-key") }}
	stats, _ := Run(context.Background(), st, Options{LLM: llm, Budget: NewBudget(100), Concurrency: 1})
	if !stats.Aborted || stats.Calls >= 20 {
		t.Errorf("did not abort early: %+v", stats)
	}
}
