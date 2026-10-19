// Package transcript decodes Claude Code session transcripts.
//
// The JSONL schema is undocumented and changes between releases, so the
// decoder is deliberately tolerant: unknown fields are ignored, content may be
// a string or an array of blocks, and events of unknown type are returned with
// their Type set so callers can count and skip them.
package transcript

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"
)

// Known event types. Anything else is reported as unknown by ingest.
var knownTypes = map[string]bool{
	"user":                  true,
	"assistant":             true,
	"summary":               true,
	"system":                true,
	"attachment":            true,
	"file-history-snapshot": true,
	"queue-operation":       true,
	"progress":              true,
}

// IsKnownType reports whether an event type is one sminer understands or
// deliberately ignores.
func IsKnownType(t string) bool { return knownTypes[t] }

// Event is one decoded transcript line.
type Event struct {
	Type        string
	UUID        string
	ParentUUID  string
	SessionID   string
	Timestamp   time.Time
	Cwd         string
	GitBranch   string
	Version     string
	IsSidechain bool
	IsMeta      bool
	Role        string
	Blocks      []Block
	Summary     string
	Subtype     string
	// Text holds the content of system events.
	Text string
	// ToolUseResult is the structured result Claude Code attaches to
	// tool_result user events. Its shape varies by tool.
	ToolUseResult json.RawMessage
	Attachment    json.RawMessage
	// Offset is the byte offset of the line within its file.
	Offset int64
}

// Block is one element of message.content.
type Block struct {
	Type      string // text, tool_use, tool_result, thinking, image, ...
	Text      string
	ToolName  string
	ToolUseID string
	Input     json.RawMessage
	IsError   bool
}

type rawEvent struct {
	Type        string          `json:"type"`
	UUID        string          `json:"uuid"`
	ParentUUID  *string         `json:"parentUuid"`
	SessionID   string          `json:"sessionId"`
	Timestamp   string          `json:"timestamp"`
	Cwd         string          `json:"cwd"`
	GitBranch   string          `json:"gitBranch"`
	Version     string          `json:"version"`
	IsSidechain bool            `json:"isSidechain"`
	IsMeta      bool            `json:"isMeta"`
	Message     json.RawMessage `json:"message"`
	Summary     string          `json:"summary"`
	Subtype     string          `json:"subtype"`
	Content     json.RawMessage `json:"content"`
	ToolUse     json.RawMessage `json:"toolUseResult"`
	Attachment  json.RawMessage `json:"attachment"`
}

type rawMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type rawBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	Name      string          `json:"name"`
	ID        string          `json:"id"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

// ErrEmpty is returned for blank lines.
var ErrEmpty = errors.New("empty line")

// Decode parses a single JSONL line.
func Decode(line []byte) (Event, error) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return Event{}, ErrEmpty
	}
	var r rawEvent
	if err := json.Unmarshal(line, &r); err != nil {
		return Event{}, err
	}
	ev := Event{
		Type:          r.Type,
		UUID:          r.UUID,
		SessionID:     r.SessionID,
		Cwd:           r.Cwd,
		GitBranch:     r.GitBranch,
		Version:       r.Version,
		IsSidechain:   r.IsSidechain,
		IsMeta:        r.IsMeta,
		Summary:       r.Summary,
		Subtype:       r.Subtype,
		ToolUseResult: r.ToolUse,
		Attachment:    r.Attachment,
	}
	if r.ParentUUID != nil {
		ev.ParentUUID = *r.ParentUUID
	}
	if r.Timestamp != "" {
		if t, err := time.Parse(time.RFC3339Nano, r.Timestamp); err == nil {
			ev.Timestamp = t
		}
	}
	if len(r.Content) > 0 {
		ev.Text = contentText(r.Content)
	}
	if len(r.Message) > 0 && r.Message[0] == '{' {
		var m rawMessage
		if err := json.Unmarshal(r.Message, &m); err == nil {
			ev.Role = m.Role
			ev.Blocks = decodeBlocks(m.Content)
		}
	}
	return ev, nil
}

func decodeBlocks(raw json.RawMessage) []Block {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return nil
	}
	switch raw[0] {
	case '"':
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return []Block{{Type: "text", Text: s}}
		}
	case '[':
		var rbs []rawBlock
		if json.Unmarshal(raw, &rbs) != nil {
			// Fall back to decoding element by element so one odd block
			// does not discard the whole message.
			var elems []json.RawMessage
			if json.Unmarshal(raw, &elems) != nil {
				return nil
			}
			for _, e := range elems {
				var rb rawBlock
				if json.Unmarshal(e, &rb) == nil {
					rbs = append(rbs, rb)
				}
			}
		}
		out := make([]Block, 0, len(rbs))
		for _, rb := range rbs {
			b := Block{Type: rb.Type, ToolName: rb.Name, Input: rb.Input, IsError: rb.IsError}
			switch rb.Type {
			case "text":
				b.Text = rb.Text
			case "thinking":
				b.Text = rb.Thinking
			case "tool_use":
				b.ToolUseID = rb.ID
			case "tool_result":
				b.ToolUseID = rb.ToolUseID
				b.Text = contentText(rb.Content)
			}
			out = append(out, b)
		}
		return out
	}
	return nil
}

// contentText flattens a string-or-blocks content value to text.
func contentText(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return ""
	}
	switch raw[0] {
	case '"':
		var s string
		_ = json.Unmarshal(raw, &s)
		return s
	case '[':
		var parts []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(raw, &parts) != nil {
			return ""
		}
		var sb strings.Builder
		for _, p := range parts {
			if p.Text == "" {
				continue
			}
			if sb.Len() > 0 {
				sb.WriteByte('\n')
			}
			sb.WriteString(p.Text)
		}
		return sb.String()
	}
	return ""
}

// ScanStats summarises one scan.
type ScanStats struct {
	Lines     int
	Malformed int
}

// maxLine bounds a single transcript line. Longer lines (huge tool output)
// are skipped and counted as malformed rather than buffered without limit.
const maxLine = 64 << 20

// Scan reads complete lines from r, which must be positioned at offset, and
// calls fn for each decoded event. A trailing line without a newline is
// treated as still being written: it is not decoded and the returned offset
// stops before it, so the next incremental scan picks it up.
func Scan(r io.Reader, offset int64, fn func(Event) error) (int64, ScanStats, error) {
	br := bufio.NewReaderSize(r, 1<<20)
	var st ScanStats
	lineStart, pos := offset, offset
	var buf []byte
	oversized := false
	for {
		chunk, err := br.ReadSlice('\n')
		pos += int64(len(chunk))
		if !oversized {
			if len(buf)+len(chunk) > maxLine {
				oversized, buf = true, buf[:0]
			} else {
				buf = append(buf, chunk...)
			}
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if err == io.EOF {
			// Nothing left, or a partial line still being written.
			return lineStart, st, nil
		}
		if err != nil {
			return lineStart, st, err
		}
		st.Lines++
		if oversized {
			st.Malformed++
		} else if ev, derr := Decode(buf); derr != nil {
			if !errors.Is(derr, ErrEmpty) {
				st.Malformed++
			}
		} else {
			ev.Offset = lineStart
			if err := fn(ev); err != nil {
				return lineStart, st, err
			}
		}
		lineStart, buf, oversized = pos, buf[:0], false
	}
}
