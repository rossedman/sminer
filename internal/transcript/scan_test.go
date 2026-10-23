package transcript

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixtureLines returns every non-empty line of the committed sample projects.
func fixtureLines(tb testing.TB) [][]byte {
	tb.Helper()
	files, err := filepath.Glob(filepath.Join("..", "..", "testdata", "projects", "*", "*.jsonl"))
	if err != nil || len(files) == 0 {
		tb.Fatalf("no fixtures: %v", err)
	}
	var out [][]byte
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			tb.Fatal(err)
		}
		for _, l := range bytes.Split(b, []byte("\n")) {
			if len(bytes.TrimSpace(l)) > 0 {
				out = append(out, l)
			}
		}
	}
	return out
}

func fixtureFile(tb testing.TB) []byte {
	tb.Helper()
	return append(bytes.Join(fixtureLines(tb), []byte("\n")), '\n')
}

func BenchmarkDecode(b *testing.B) {
	var lines [][]byte
	for _, l := range fixtureLines(b) {
		if _, err := Decode(l); err == nil { // the fixtures include deliberately malformed lines
			lines = append(lines, l)
		}
	}
	b.ReportAllocs()
	var n int64
	for _, l := range lines {
		n += int64(len(l))
	}
	b.SetBytes(n)
	for b.Loop() {
		for _, l := range lines {
			if _, err := Decode(l); err != nil {
				b.Fatal(err)
			}
		}
	}
}

func BenchmarkScan(b *testing.B) {
	data := fixtureFile(b)
	b.ReportAllocs()
	b.SetBytes(int64(len(data)))
	for b.Loop() {
		n := 0
		_, _, err := Scan(bytes.NewReader(data), 0, func(Event) error { n++; return nil })
		if err != nil || n == 0 {
			b.Fatalf("n=%d err=%v", n, err)
		}
	}
}

func FuzzScan(f *testing.F) {
	for _, l := range fixtureLines(f) {
		f.Add(append(append([]byte{}, l...), '\n'), uint8(0))
	}
	f.Add(fixtureFile(f), uint8(3))
	f.Add([]byte(`{"type":"user","uuid":"a"}`+"\n"+`{bad}`+"\n"+`{"type":"user"`), uint8(0))
	f.Add([]byte("\n\n \n"), uint8(7))
	f.Fuzz(func(t *testing.T, data []byte, start uint8) {
		off := int64(start)
		var events []Event
		end, st, err := Scan(bytes.NewReader(data), off, func(ev Event) error {
			events = append(events, ev)
			return nil
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		consumed := end - off
		if consumed < 0 || consumed > int64(len(data)) {
			t.Fatalf("offset %d outside input of %d bytes (start %d)", end, len(data), off)
		}
		// The offset stops right after the last newline, never inside a line.
		last := bytes.LastIndexByte(data, '\n')
		if want := int64(last + 1); consumed != want {
			t.Fatalf("consumed %d bytes, want %d (last newline at %d)", consumed, want, last)
		}
		complete := data[:consumed]
		if consumed > 0 && complete[consumed-1] != '\n' {
			t.Fatalf("consumed region does not end at a line boundary")
		}
		// Event count matches the complete lines that decode.
		wantEvents, wantLines, wantBad := 0, 0, 0
		pos := int64(0)
		var wantOffsets []int64
		for _, l := range strings.SplitAfter(string(complete), "\n") {
			if l == "" {
				continue
			}
			wantLines++
			if _, derr := Decode([]byte(l)); derr == nil {
				wantEvents++
				wantOffsets = append(wantOffsets, off+pos)
			} else if derr != ErrEmpty {
				wantBad++
			}
			pos += int64(len(l))
		}
		if len(events) != wantEvents || st.Lines != wantLines || st.Malformed != wantBad {
			t.Fatalf("events=%d lines=%d bad=%d, want %d/%d/%d", len(events), st.Lines, st.Malformed, wantEvents, wantLines, wantBad)
		}
		for i, ev := range events {
			if ev.Offset != wantOffsets[i] {
				t.Fatalf("event %d offset %d, want %d", i, ev.Offset, wantOffsets[i])
			}
		}
	})
}

func FuzzDecodeProperties(f *testing.F) {
	for _, l := range fixtureLines(f) {
		f.Add(l)
	}
	f.Fuzz(func(t *testing.T, line []byte) {
		a, errA := Decode(line)
		b, errB := Decode(line)
		if (errA == nil) != (errB == nil) {
			t.Fatalf("non-deterministic error: %v vs %v", errA, errB)
		}
		if errA != nil {
			return
		}
		if a.Type != b.Type || a.UUID != b.UUID || len(a.Blocks) != len(b.Blocks) || a.Text != b.Text {
			t.Fatalf("non-deterministic decode")
		}
		// Whitespace around the line never changes the result.
		c, errC := Decode(append(append([]byte(" \t"), line...), " \r\n"...))
		if errC != nil || c.Type != a.Type || c.UUID != a.UUID || len(c.Blocks) != len(a.Blocks) {
			t.Fatalf("whitespace changed decode: %v", errC)
		}
	})
}
