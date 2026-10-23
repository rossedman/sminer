package signal

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/rossedman/sminer/internal/store"
)

type sessionTurns struct {
	sess  store.Session
	turns []store.Turn
}

func corpusSessions(tb testing.TB) []sessionTurns {
	tb.Helper()
	st := ingestedStore(tb)
	sessions, err := st.Sessions()
	if err != nil {
		tb.Fatal(err)
	}
	var out []sessionTurns
	for id, s := range sessions {
		turns, err := st.SessionTurns(id)
		if err != nil {
			tb.Fatal(err)
		}
		out = append(out, sessionTurns{s, turns})
	}
	if len(out) == 0 {
		tb.Fatal("empty corpus")
	}
	return out
}

func BenchmarkDetect(b *testing.B) {
	sessions := corpusSessions(b)
	b.ReportAllocs()
	for b.Loop() {
		n := 0
		for _, s := range sessions {
			n += len(Detect(s.sess, s.turns))
		}
		if n == 0 {
			b.Fatal("no signals")
		}
	}
}

var correctionTexts = []string{
	"No, don't use testify/assert; use the standard library.",
	"Add a handler for GET /widgets with tests",
	"actually, please use the v2 client and always wrap errors",
	"looks good, commit it",
	"why did you delete the vendor directory? I said never touch it.",
	strings.Repeat("Implement pagination for the widgets endpoint and update the docs. ", 20),
}

func BenchmarkIsCorrection(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		for _, t := range correctionTexts {
			_ = IsCorrection(t)
		}
	}
}

func FuzzIsCorrection(f *testing.F) {
	for _, t := range correctionTexts {
		f.Add(t)
	}
	f.Add("/review")
	f.Add("no problem, but don't touch the schema")
	f.Add("")
	f.Fuzz(func(t *testing.T, text string) {
		got := IsCorrection(text)
		if IsCorrection(text) != got {
			t.Fatal("non-deterministic")
		}
		// Only the trimmed text matters.
		if padded := IsCorrection("  \n\t" + text + " \r\n"); padded != got {
			t.Fatalf("surrounding whitespace changed the verdict: %v vs %v for %q", got, padded, text)
		}
		trimmed := strings.TrimSpace(text)
		if (trimmed == "" || strings.HasPrefix(trimmed, "/")) && got {
			t.Fatalf("empty or slash-command text flagged: %q", text)
		}
	})
}

var hookSeeds = []string{
	"PreToolUse:Edit hook error: [~/src/claude-hooks/no-lockfiles.sh]: Editing lockfiles is not allowed",
	"PreToolUse:Bash hook blocked: line 12 of /tmp/a.sh is bad",
	"PostToolUse:Edit hook error: [python3 /hooks/lint.py]: lint failed",
	"UserPromptSubmit hook error: something 42 times 'quoted'",
	"hook error: İstanbul",
	"",
}

func FuzzHookKey(f *testing.F) {
	for _, s := range hookSeeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, text string) {
		key, msg := HookKey(text)
		key2, msg2 := HookKey(text)
		if key != key2 || msg != msg2 {
			t.Fatal("non-deterministic")
		}
		if key == "" {
			t.Fatalf("empty key for %q", text)
		}
		if strings.Contains(msg, "\n") || len(msg) > 200 {
			t.Fatalf("message %q must be one line of at most 200 bytes", msg)
		}
		if utf8.ValidString(text) && !utf8.ValidString(msg) {
			t.Fatalf("message %q is not valid UTF-8 for valid input %q", msg, text)
		}
		// Surrounding whitespace never changes the identity.
		k3, m3 := HookKey("  \n" + text + "\n  ")
		if k3 != key || m3 != msg {
			t.Fatalf("whitespace changed result: %q/%q vs %q/%q", key, msg, k3, m3)
		}
	})
}

var commandSeeds = []string{
	"go test ./...",
	"cd api && FOO=1 go test -v ./x",
	"npm run build -- --prod",
	"git status | head -5",
	"terraform plan -out=plan.tfplan",
	"/usr/bin/python3 -m pytest",
	"",
}

func FuzzCommandKey(f *testing.F) {
	for _, s := range commandSeeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, cmd string) {
		got := CommandKey(cmd)
		if CommandKey(cmd) != got {
			t.Fatal("non-deterministic")
		}
		if padded := CommandKey("  \t" + cmd + " \n "); padded != got {
			t.Fatalf("whitespace changed key: %q vs %q for %q", got, padded, cmd)
		}
		if got != strings.TrimSpace(got) || strings.ContainsAny(got, "\n\t") || strings.Count(got, " ") > 1 {
			t.Fatalf("key %q must be at most a program and a subcommand", got)
		}
	})
}
