package transcript

import (
	"strings"
	"testing"
)

func TestDecode(t *testing.T) {
	tests := []struct {
		name      string
		line      string
		wantType  string
		wantText  string
		wantBlock string
		wantErr   bool
	}{
		{
			name:      "string content (1.0.x)",
			line:      `{"type":"user","uuid":"u1","parentUuid":null,"sessionId":"s","timestamp":"2026-09-21T09:00:20.000Z","message":{"role":"user","content":"hello"}}`,
			wantType:  "user",
			wantText:  "hello",
			wantBlock: "text",
		},
		{
			name:      "block content (2.0.x)",
			line:      `{"type":"user","uuid":"u1","sessionId":"s","message":{"role":"user","content":[{"type":"text","text":"hi"}]},"extraField":{"nested":true}}`,
			wantType:  "user",
			wantText:  "hi",
			wantBlock: "text",
		},
		{
			name:      "tool result with block content",
			line:      `{"type":"user","sessionId":"s","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","is_error":true,"content":[{"type":"text","text":"boom"}]}]}}`,
			wantType:  "user",
			wantText:  "boom",
			wantBlock: "tool_result",
		},
		{
			name:     "unknown type keeps its type",
			line:     `{"type":"weird-new-type","sessionId":"s"}`,
			wantType: "weird-new-type",
		},
		{
			name:    "malformed",
			line:    `{nope`,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ev, err := Decode([]byte(tt.line))
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if ev.Type != tt.wantType {
				t.Errorf("type = %q, want %q", ev.Type, tt.wantType)
			}
			if tt.wantBlock != "" {
				if len(ev.Blocks) != 1 {
					t.Fatalf("blocks = %d, want 1", len(ev.Blocks))
				}
				if ev.Blocks[0].Type != tt.wantBlock || ev.Blocks[0].Text != tt.wantText {
					t.Errorf("block = %+v, want %s %q", ev.Blocks[0], tt.wantBlock, tt.wantText)
				}
			}
		})
	}
}

func TestDecodeToolUse(t *testing.T) {
	ev, err := Decode([]byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"ls"}}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	b := ev.Blocks[0]
	if b.ToolName != "Bash" || b.ToolUseID != "t1" || !strings.Contains(string(b.Input), "ls") {
		t.Errorf("tool_use = %+v", b)
	}
}

func TestScanStopsBeforePartialLine(t *testing.T) {
	full := `{"type":"user","sessionId":"s","uuid":"a"}` + "\n" + `{bad}` + "\n" + `{"type":"user","sessionId":"s","uuid":"b"}` + "\n"
	partial := `{"type":"user","sessionId":"s","uu`
	var seen []string
	end, st, err := Scan(strings.NewReader(full+partial), 0, func(ev Event) error {
		seen = append(seen, ev.UUID)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if end != int64(len(full)) {
		t.Errorf("end = %d, want %d (before the partial line)", end, len(full))
	}
	if strings.Join(seen, ",") != "a,b" {
		t.Errorf("seen = %v", seen)
	}
	if st.Lines != 3 || st.Malformed != 1 {
		t.Errorf("stats = %+v", st)
	}

	// Resuming from the returned offset yields only the completed line.
	rest := `id":"c"}` + "\n"
	seen = nil
	end2, _, err := Scan(strings.NewReader(partial+rest), end, func(ev Event) error {
		seen = append(seen, ev.UUID)
		if ev.Offset != end {
			t.Errorf("offset = %d, want %d", ev.Offset, end)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(seen, ",") != "c" || end2 != end+int64(len(partial+rest)) {
		t.Errorf("resume: seen %v end %d", seen, end2)
	}
}

func FuzzDecode(f *testing.F) {
	f.Add([]byte(`{"type":"user","message":{"role":"user","content":"x"}}`))
	f.Add([]byte(`{"type":"assistant","message":{"content":[{"type":"tool_use","input":{}}]}}`))
	f.Add([]byte(`{"type":"user","message":{"content":[{"type":"tool_result","content":[1,2]}]},"toolUseResult":"s"}`))
	f.Add([]byte(`{"message":"str","parentUuid":5}`))
	f.Fuzz(func(t *testing.T, b []byte) {
		_, _ = Decode(b) // must never panic
	})
}
