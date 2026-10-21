package transcript

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func TestIsKnownType(t *testing.T) {
	t.Parallel()
	for _, k := range []string{"user", "assistant", "summary", "system", "attachment", "file-history-snapshot", "queue-operation", "progress"} {
		if !IsKnownType(k) {
			t.Errorf("IsKnownType(%q) = false", k)
		}
	}
	for _, k := range []string{"", "weird", "User"} {
		if IsKnownType(k) {
			t.Errorf("IsKnownType(%q) = true", k)
		}
	}
}

func TestDecodeFields(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		line  string
		check func(t *testing.T, ev Event)
	}{
		{
			name: "all top-level fields",
			line: `{"type":"user","uuid":"u1","parentUuid":"p0","sessionId":"s1","timestamp":"2026-09-21T09:00:20.500Z","cwd":"/w","gitBranch":"main","version":"1.0.98","isSidechain":true,"isMeta":true,"summary":"sum","subtype":"st","toolUseResult":{"a":1},"attachment":{"b":2},"message":{"role":"user","content":"hi"}}`,
			check: func(t *testing.T, ev Event) {
				want := time.Date(2026, 9, 21, 9, 0, 20, 500_000_000, time.UTC)
				if !ev.Timestamp.Equal(want) {
					t.Errorf("Timestamp = %v", ev.Timestamp)
				}
				if ev.UUID != "u1" || ev.ParentUUID != "p0" || ev.SessionID != "s1" || ev.Cwd != "/w" ||
					ev.GitBranch != "main" || ev.Version != "1.0.98" || !ev.IsSidechain || !ev.IsMeta ||
					ev.Summary != "sum" || ev.Subtype != "st" || ev.Role != "user" {
					t.Errorf("fields = %+v", ev)
				}
				if string(ev.ToolUseResult) != `{"a":1}` || string(ev.Attachment) != `{"b":2}` {
					t.Errorf("raw = %s / %s", ev.ToolUseResult, ev.Attachment)
				}
			},
		},
		{
			name: "bad timestamp leaves zero time",
			line: `{"type":"user","timestamp":"yesterday"}`,
			check: func(t *testing.T, ev Event) {
				if !ev.Timestamp.IsZero() {
					t.Errorf("Timestamp = %v", ev.Timestamp)
				}
			},
		},
		{
			name: "null parent",
			line: `{"type":"user","parentUuid":null}`,
			check: func(t *testing.T, ev Event) {
				if ev.ParentUUID != "" {
					t.Errorf("ParentUUID = %q", ev.ParentUUID)
				}
			},
		},
		{
			name: "system content string",
			line: `{"type":"system","subtype":"info","content":"hello system"}`,
			check: func(t *testing.T, ev Event) {
				if ev.Text != "hello system" {
					t.Errorf("Text = %q", ev.Text)
				}
			},
		},
		{
			name: "system content blocks join with newline and skip empty",
			line: `{"type":"system","content":[{"type":"text","text":"a"},{"type":"image"},{"type":"text","text":"b"}]}`,
			check: func(t *testing.T, ev Event) {
				if ev.Text != "a\nb" {
					t.Errorf("Text = %q", ev.Text)
				}
			},
		},
		{
			name: "system content malformed blocks",
			line: `{"type":"system","content":[1,2]}`,
			check: func(t *testing.T, ev Event) {
				if ev.Text != "" {
					t.Errorf("Text = %q", ev.Text)
				}
			},
		},
		{
			name: "system content other json kind",
			line: `{"type":"system","content":{"x":1}}`,
			check: func(t *testing.T, ev Event) {
				if ev.Text != "" {
					t.Errorf("Text = %q", ev.Text)
				}
			},
		},
		{
			name: "message as string is ignored",
			line: `{"type":"user","message":"plain"}`,
			check: func(t *testing.T, ev Event) {
				if ev.Role != "" || len(ev.Blocks) != 0 {
					t.Errorf("ev = %+v", ev)
				}
			},
		},
		{
			name: "message object with invalid role type",
			line: `{"type":"user","message":{"role":5,"content":"x"}}`,
			check: func(t *testing.T, ev Event) {
				if ev.Role != "" || len(ev.Blocks) != 0 {
					t.Errorf("ev = %+v", ev)
				}
			},
		},
		{
			name: "thinking block",
			line: `{"type":"assistant","message":{"role":"assistant","content":[{"type":"thinking","thinking":"hmm","text":"ignored"}]}}`,
			check: func(t *testing.T, ev Event) {
				if len(ev.Blocks) != 1 || ev.Blocks[0].Type != "thinking" || ev.Blocks[0].Text != "hmm" {
					t.Errorf("Blocks = %+v", ev.Blocks)
				}
			},
		},
		{
			name: "tool_use block keeps id name and input",
			line: `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"t9","name":"Bash","input":{"command":"ls"}}]}}`,
			check: func(t *testing.T, ev Event) {
				b := ev.Blocks[0]
				if b.ToolUseID != "t9" || b.ToolName != "Bash" || string(b.Input) != `{"command":"ls"}` || b.Text != "" {
					t.Errorf("block = %+v", b)
				}
			},
		},
		{
			name: "tool_result with string content and error flag",
			line: `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t9","is_error":true,"content":"failed"}]}}`,
			check: func(t *testing.T, ev Event) {
				b := ev.Blocks[0]
				if b.ToolUseID != "t9" || !b.IsError || b.Text != "failed" {
					t.Errorf("block = %+v", b)
				}
			},
		},
		{
			name: "odd block does not discard the others",
			line: `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"ok"},{"type":"text","text":7},{"type":"text","text":"also"}]}}`,
			check: func(t *testing.T, ev Event) {
				// Each good block once, in order; the bad one dropped.
				var texts []string
				for _, b := range ev.Blocks {
					texts = append(texts, b.Text)
				}
				if got := strings.Join(texts, ","); got != "ok,also" {
					t.Errorf("block texts = %q, want %q", got, "ok,also")
				}
			},
		},
		{
			name: "content that is neither string nor array",
			line: `{"type":"user","message":{"role":"user","content":{"a":1}}}`,
			check: func(t *testing.T, ev Event) {
				if len(ev.Blocks) != 0 {
					t.Errorf("Blocks = %+v", ev.Blocks)
				}
			},
		},
		{
			name: "empty array content yields no blocks",
			line: `{"type":"user","message":{"role":"user","content":[]}}`,
			check: func(t *testing.T, ev Event) {
				if len(ev.Blocks) != 0 {
					t.Errorf("Blocks = %+v", ev.Blocks)
				}
			},
		},
		{
			name: "surrounding whitespace is trimmed",
			line: "  \t{\"type\":\"summary\",\"summary\":\"s\"}\r\n",
			check: func(t *testing.T, ev Event) {
				if ev.Type != "summary" || ev.Summary != "s" {
					t.Errorf("ev = %+v", ev)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ev, err := Decode([]byte(tt.line))
			if err != nil {
				t.Fatal(err)
			}
			tt.check(t, ev)
		})
	}
}

func TestDecodeEmpty(t *testing.T) {
	t.Parallel()
	for _, in := range []string{"", "   ", "\n", " \t\r\n"} {
		if _, err := Decode([]byte(in)); !errors.Is(err, ErrEmpty) {
			t.Errorf("Decode(%q) err = %v, want ErrEmpty", in, err)
		}
	}
}

func TestScanCallbackError(t *testing.T) {
	t.Parallel()
	in := `{"type":"user","uuid":"a"}` + "\n" + `{"type":"user","uuid":"b"}` + "\n"
	boom := errors.New("boom")
	calls := 0
	end, st, err := Scan(strings.NewReader(in), 0, func(ev Event) error {
		calls++
		if ev.UUID == "b" {
			return boom
		}
		return nil
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if calls != 2 {
		t.Errorf("calls = %d", calls)
	}
	// The failed line is not consumed so it is retried next time.
	if want := int64(len(`{"type":"user","uuid":"a"}` + "\n")); end != want {
		t.Errorf("end = %d, want %d", end, want)
	}
	if st.Lines != 2 {
		t.Errorf("Lines = %d", st.Lines)
	}
}

type errReader struct {
	data string
	err  error
	done bool
}

func (r *errReader) Read(p []byte) (int, error) {
	if r.done {
		return 0, r.err
	}
	r.done = true
	return copy(p, r.data), nil
}

func TestScanReadError(t *testing.T) {
	t.Parallel()
	boom := errors.New("disk gone")
	line := `{"type":"user","uuid":"a"}` + "\n"
	var n int
	end, _, err := Scan(&errReader{data: line + `{"ty`, err: boom}, 0, func(Event) error { n++; return nil })
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if n != 1 || end != int64(len(line)) {
		t.Errorf("n = %d end = %d", n, end)
	}
}

func TestScanOffsetsAndBlankLines(t *testing.T) {
	t.Parallel()
	l1 := `{"type":"user","uuid":"a"}` + "\n"
	l2 := "\n"
	l3 := `{"type":"user","uuid":"b"}` + "\n"
	var offs []int64
	end, st, err := Scan(strings.NewReader(l1+l2+l3), 100, func(ev Event) error {
		offs = append(offs, ev.Offset)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(offs) != 2 || offs[0] != 100 || offs[1] != 100+int64(len(l1)+len(l2)) {
		t.Errorf("offsets = %v", offs)
	}
	if end != 100+int64(len(l1+l2+l3)) {
		t.Errorf("end = %d", end)
	}
	if st.Lines != 3 || st.Malformed != 0 {
		t.Errorf("stats = %+v", st)
	}
}

func TestScanLongLine(t *testing.T) {
	t.Parallel()
	// Longer than the 1 MiB read buffer but under the cap.
	big := strings.Repeat("x", 3<<20)
	l := `{"type":"user","uuid":"big","summary":"` + big + `"}` + "\n"
	var got int
	end, _, err := Scan(strings.NewReader(l), 0, func(ev Event) error {
		got = len(ev.Summary)
		return nil
	})
	if err != nil || got != len(big) || end != int64(len(l)) {
		t.Errorf("err=%v got=%d end=%d", err, got, end)
	}
}

type repeatReader struct{ b byte }

func (r repeatReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = r.b
	}
	return len(p), nil
}

func TestScanOversizedLineIsSkipped(t *testing.T) {
	t.Parallel()
	over := int64(maxLine) + 10
	tail := `{"type":"user","uuid":"after"}` + "\n"
	r := io.MultiReader(io.LimitReader(repeatReader{'x'}, over), strings.NewReader("\n"+tail))
	var seen []string
	end, st, err := Scan(r, 0, func(ev Event) error {
		seen = append(seen, ev.UUID)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 || seen[0] != "after" {
		t.Errorf("seen = %v", seen)
	}
	if st.Lines != 2 || st.Malformed != 1 {
		t.Errorf("stats = %+v", st)
	}
	if want := over + 1 + int64(len(tail)); end != want {
		t.Errorf("end = %d, want %d", end, want)
	}
}

func TestScanNoInput(t *testing.T) {
	t.Parallel()
	end, st, err := Scan(bytes.NewReader(nil), 42, func(Event) error { t.Fatal("called"); return nil })
	if err != nil || end != 42 || st != (ScanStats{}) {
		t.Errorf("end=%d st=%+v err=%v", end, st, err)
	}
}
