package classify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rossedman/sminer/internal/store"
)

func messageJSON(stop, text string) string {
	b, _ := json.Marshal(map[string]any{
		"id": "msg_1", "type": "message", "role": "assistant", "model": "claude-test",
		"content":     []any{map[string]any{"type": "text", "text": text}, map[string]any{"type": "text", "text": "!"}},
		"stop_reason": stop, "stop_sequence": nil,
		"usage": map[string]any{"input_tokens": 1, "output_tokens": 1},
	})
	return string(b)
}

func TestAnthropicComplete(t *testing.T) {
	var gotBody atomic.Value
	var gotPath atomic.Value
	status := http.StatusOK
	reply := messageJSON("end_turn", `{"correction": true}`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody.Store(string(b))
		gotPath.Store(r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, reply)
	}))
	defer srv.Close()
	t.Setenv("ANTHROPIC_BASE_URL", srv.URL)
	t.Setenv("ANTHROPIC_API_KEY", "test-key")

	a := NewAnthropic("claude-test")
	if a.Model != "claude-test" {
		t.Fatalf("Model = %q", a.Model)
	}
	got, err := a.Complete(t.Context(), "SYS PROMPT", "USER PROMPT", 123)
	if err != nil {
		t.Fatal(err)
	}
	if got != `{"correction": true}!` {
		t.Errorf("text blocks not concatenated: %q", got)
	}
	body, _ := gotBody.Load().(string)
	for _, want := range []string{`"model":"claude-test"`, `"max_tokens":123`, "SYS PROMPT", "USER PROMPT", `"effort":"low"`} {
		if !strings.Contains(body, want) {
			t.Errorf("request body missing %s: %s", want, body)
		}
	}
	if p, _ := gotPath.Load().(string); p != "/v1/messages" {
		t.Errorf("path = %q", p)
	}

	reply = messageJSON("refusal", "no")
	if _, err := a.Complete(t.Context(), "s", "u", 10); err == nil || !strings.Contains(err.Error(), "declined") {
		t.Errorf("refusal err = %v", err)
	}

	status, reply = http.StatusBadRequest, `{"type":"error","error":{"type":"invalid_request_error","message":"bad"}}`
	if _, err := a.Complete(t.Context(), "s", "u", 10); err == nil {
		t.Error("HTTP error should be returned")
	}
}

func TestBudgetNilAndLimits(t *testing.T) {
	t.Parallel()
	var nilBudget *Budget
	if nilBudget.Take() {
		t.Error("nil budget must refuse")
	}
	if nilBudget.Used() != 0 {
		t.Error("nil budget used != 0")
	}
	b := NewBudget(2)
	for i, want := range []bool{true, true, false, false} {
		if got := b.Take(); got != want {
			t.Errorf("Take #%d = %v, want %v", i, got, want)
		}
	}
	if b.Used() != 2 {
		t.Errorf("Used = %d, want 2 (refused takes are not counted)", b.Used())
	}
	if NewBudget(0).Take() {
		t.Error("zero budget must refuse")
	}
}

func TestParseVerdictTable(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		in      string
		want    Verdict
		wantErr bool
	}{
		{"plain", `{"correction":true,"rule":"r","remedy":"hook","confidence":0.5}`, Verdict{true, "r", "hook", 0.5}, false},
		{"fenced with prose", "ok:\n```json\n{\"correction\":false,\"remedy\":\"skill\"}\n```\nbye", Verdict{false, "", "skill", 0}, false},
		{"all remedies", `{"remedy":"ignore"}`, Verdict{Remedy: "ignore"}, false},
		{"unknown remedy cleared", `{"remedy":"magic"}`, Verdict{}, false},
		{"rule whitespace collapsed", `{"rule":"  a \n b\t c "}`, Verdict{Rule: "a b c"}, false},
		{"nested braces", `{"rule":"use {x}","correction":true}`, Verdict{Correction: true, Rule: "use {x}"}, false},
		{"no braces", `nothing`, Verdict{}, true},
		{"only close", `} {`, Verdict{}, true},
		{"invalid json", `{"correction": tru}`, Verdict{}, true},
		{"wrong type", `{"correction": "yes"}`, Verdict{}, true},
		{"empty", ``, Verdict{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseVerdict(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
	v, _ := ParseVerdict(`{"rule":"` + strings.Repeat("x", 1000) + `"}`)
	if len(v.Rule) != 300 {
		t.Errorf("rule length = %d, want 300", len(v.Rule))
	}
}

func TestUserPromptVariants(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		sig      store.Signal
		contains []string
		absent   []string
	}{
		{"correction", store.Signal{Type: store.Correction, Context: "Edit a.go", Excerpt: "no, don't"},
			[]string{"<assistant_actions>\nEdit a.go\n</assistant_actions>", "The developer's next message:", "<message>\nno, don't\n</message>"}, nil},
		{"no context", store.Signal{Type: store.Correction, Excerpt: "x"}, []string{"(no recorded action)"}, nil},
		{"interrupt strips marker", store.Signal{Type: store.Interrupt, Context: "Bash ls", Excerpt: "(interrupted) Bash ls"},
			[]string{"The developer interrupted the assistant, then wrote:", "<message>\nBash ls\n</message>"}, []string{"(interrupted)"}},
		{"rewind", store.Signal{Type: store.Rewind, Context: "Earlier attempt", Excerpt: "retry"},
			[]string{"rewound the conversation"}, nil},
		{"repeated wraps context", store.Signal{Type: store.RepeatedInstruction, Context: "Similar instruction given in 3 sessions", Excerpt: "always vet"},
			[]string{"near-identical messages", "(Similar instruction given in 3 sessions)"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := UserPrompt(tt.sig)
			for _, c := range tt.contains {
				if !strings.Contains(got, c) {
					t.Errorf("prompt missing %q:\n%s", c, got)
				}
			}
			for _, c := range tt.absent {
				if strings.Contains(got, c) {
					t.Errorf("prompt should not contain %q:\n%s", c, got)
				}
			}
		})
	}
}

func TestRunFilters(t *testing.T) {
	t.Parallel()
	st := seed(t, 4)
	old := time.Now().Add(-48 * time.Hour)
	// a and b are old; c and d keep "now"; b and d belong to repo /x.
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := st.DB.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	mustExec(`UPDATE signals SET ts=? WHERE id IN ('a','b')`, store.Ms(old))
	mustExec(`UPDATE signals SET repo='/x' WHERE id IN ('b','d')`)
	var seen []string
	llm := &fakeLLM{fn: func(user string) (string, error) {
		seen = append(seen, user)
		return `{"correction": true, "rule": "r"}`, nil
	}}
	stats, err := Run(context.Background(), st, Options{LLM: llm, Budget: NewBudget(10), Concurrency: 1,
		Since: time.Now().Add(-time.Hour), Repo: "/x"})
	if err != nil {
		t.Fatal(err)
	}
	// Only d is recent and in /x; d is index 3 so its excerpt is "what does this do?".
	if stats.Pending != 1 || stats.Calls != 1 || len(seen) != 1 || !strings.Contains(seen[0], "what does this do?") {
		t.Errorf("stats = %+v seen = %v", stats, seen)
	}
}

func TestRunSkipsBareInterrupts(t *testing.T) {
	t.Parallel()
	st := seed(t, 0)
	_, err := st.DB.Exec(`INSERT INTO signals(id, session_id, type, weight, ts, excerpt) VALUES('i1','s','interrupt',3,1,'(interrupted) Bash ls'),('i2','s','interrupt',3,2,'keep vendor')`)
	if err != nil {
		t.Fatal(err)
	}
	llm := &fakeLLM{fn: func(string) (string, error) { return `{"correction": true}`, nil }}
	stats, err := Run(context.Background(), st, Options{LLM: llm, Budget: NewBudget(10)})
	if err != nil || stats.Pending != 1 || stats.Calls != 1 {
		t.Errorf("stats = %+v err = %v", stats, err)
	}
}

func TestRunStoreErrors(t *testing.T) {
	t.Parallel()
	st := seed(t, 1)
	_ = st.Close()
	llm := &fakeLLM{fn: func(string) (string, error) { return `{}`, nil }}
	if _, err := Run(context.Background(), st, Options{LLM: llm, Budget: NewBudget(1)}); err == nil {
		t.Error("closed store should fail")
	}
}

func TestRunWriteFailureIsCounted(t *testing.T) {
	t.Parallel()
	st := seed(t, 1)
	llm := &fakeLLM{fn: func(string) (string, error) {
		// Break the table the verdict is written to.
		if _, err := st.DB.Exec(`DROP TABLE signals`); err != nil {
			t.Error(err)
		}
		return `{"correction": true, "rule": "r"}`, nil
	}}
	stats, err := Run(context.Background(), st, Options{LLM: llm, Budget: NewBudget(1)})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Errors != 1 || stats.LastError == nil {
		t.Errorf("stats = %+v", stats)
	}
}

func TestRunUnparseableReply(t *testing.T) {
	t.Parallel()
	st := seed(t, 2)
	llm := &fakeLLM{fn: func(string) (string, error) { return "I cannot help", nil }}
	stats, err := Run(context.Background(), st, Options{LLM: llm, Budget: NewBudget(10), Concurrency: 1})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Errors != 2 || stats.Confirmed != 0 || stats.LastError == nil || stats.Aborted {
		t.Errorf("stats = %+v", stats)
	}
	var n int
	_ = st.DB.QueryRow(`SELECT COUNT(*) FROM signals WHERE classified=1`).Scan(&n)
	if n != 0 {
		t.Errorf("failed replies must stay unclassified, %d classified", n)
	}
}

func TestRunStoresRuleAndRemedy(t *testing.T) {
	t.Parallel()
	st := seed(t, 1)
	llm := &fakeLLM{fn: func(string) (string, error) {
		return `{"correction": true, "rule": "Use zerolog", "remedy": "hook"}`, nil
	}}
	if _, err := Run(context.Background(), st, Options{LLM: llm, Budget: NewBudget(1)}); err != nil {
		t.Fatal(err)
	}
	sigs, _ := st.QuerySignals("id='a'")
	if len(sigs) != 1 || !sigs[0].Classified || sigs[0].Confirmed == nil || !*sigs[0].Confirmed ||
		sigs[0].Rule != "Use zerolog" || sigs[0].RemedyHint != "hook" {
		t.Errorf("signal = %+v", sigs)
	}
}

func FuzzParseVerdict(f *testing.F) {
	for _, s := range []string{
		`{"correction": true, "rule": "Write table-driven tests with the standard library", "remedy": "claude_md", "confidence": 0.9}`,
		"Sure, here you go:\n```json\n{\"correction\": false, \"rule\": \"\", \"remedy\": \"ignore\", \"confidence\": 0.2}\n```",
		`{"correction": true, "rule": "x", "remedy": "bogus"}`,
		`{{}}`, `}{`, `{"rule": 5}`, ``,
	} {
		f.Add(s)
	}
	valid := map[string]bool{"": true, "hook": true, "claude_md": true, "skill": true, "ignore": true}
	f.Fuzz(func(t *testing.T, reply string) {
		v, err := ParseVerdict(reply)
		v2, err2 := ParseVerdict(reply)
		if v != v2 || (err == nil) != (err2 == nil) {
			t.Fatal("non-deterministic")
		}
		if err != nil {
			return
		}
		if !valid[v.Remedy] {
			t.Fatalf("remedy %q outside the documented set", v.Remedy)
		}
		if len(v.Rule) > 300 || strings.ContainsAny(v.Rule, "\n\r\t") || v.Rule != strings.TrimSpace(v.Rule) {
			t.Fatalf("rule %q must be a trimmed single line of at most 300 bytes", v.Rule)
		}
	})
}

func BenchmarkParseVerdict(b *testing.B) {
	reply := "Here is my answer:\n```json\n" + `{"correction": true, "rule": "Write table-driven tests with the standard library, not testify/assert", "remedy": "claude_md", "confidence": 0.93}` + "\n```"
	b.ReportAllocs()
	for b.Loop() {
		if _, err := ParseVerdict(reply); err != nil {
			b.Fatal(err)
		}
	}
}
