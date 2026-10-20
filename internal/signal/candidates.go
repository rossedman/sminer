package signal

import (
	"github.com/rossedman/sminer/internal/ingest"
	"github.com/rossedman/sminer/internal/store"
	"github.com/rossedman/sminer/internal/textutil"
)

// Candidate is a developer message that follows assistant tool use: the
// turns correction detection decides on. Labelling them is how the
// heuristic and classifier are measured.
type Candidate struct {
	Turn      string `json:"turn"`
	SessionID string `json:"session_id"`
	Action    string `json:"action"`
	Message   string `json:"message"`
	// Heuristic is what IsCorrection says about Message.
	Heuristic bool `json:"heuristic"`
}

// Candidates lists a session's candidate turns in transcript order. Turn
// text is already redacted at ingest.
func Candidates(turns []store.Turn) []Candidate {
	var out []Candidate
	var actions []store.Turn
	for _, t := range turns {
		if t.Sidechain {
			continue
		}
		switch t.Kind {
		case ingest.KindToolUse, ingest.KindAssistantText:
			actions = append(actions, t)
		case ingest.KindInterrupt, ingest.KindRejected:
			actions = nil
		case ingest.KindUserText:
			if hasToolUse(actions) && t.Text != "" {
				out = append(out, Candidate{
					Turn:      t.ID,
					SessionID: t.SessionID,
					Action:    describeActions(actions),
					Message:   textutil.Truncate(t.Text, 1000),
					Heuristic: IsCorrection(t.Text),
				})
			}
			actions = nil
		}
	}
	return out
}
