// Package classify confirms heuristic candidates with an LLM. It is the only
// stage that touches the network, and it sends only the candidate user turn
// plus a truncated summary of the preceding assistant action (about 2 KB per
// call): no file contents and no tool output bodies.
package classify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/anthropics/anthropic-sdk-go"

	"github.com/rossedman/sminer/internal/signal"
	"github.com/rossedman/sminer/internal/store"
	"github.com/rossedman/sminer/internal/textutil"
)

// LLM is the one call sminer makes. Tests substitute a fake.
type LLM interface {
	Complete(ctx context.Context, system, user string, maxTokens int) (string, error)
}

// Anthropic calls the Messages API with the official SDK. Credentials are
// resolved by the SDK (ANTHROPIC_API_KEY and friends).
type Anthropic struct {
	Model  string
	client anthropic.Client
}

// NewAnthropic builds a client for model.
func NewAnthropic(model string) *Anthropic {
	return &Anthropic{Model: model, client: anthropic.NewClient()}
}

// Complete sends one single-turn request and returns its text.
func (a *Anthropic) Complete(ctx context.Context, system, user string, maxTokens int) (string, error) {
	resp, err := a.client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     anthropic.Model(a.Model),
		MaxTokens: int64(maxTokens),
		System:    []anthropic.TextBlockParam{{Text: system}},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(user)),
		},
		OutputConfig: anthropic.OutputConfigParam{Effort: anthropic.OutputConfigEffortLow},
	})
	if err != nil {
		return "", err
	}
	if resp.StopReason == anthropic.StopReasonRefusal {
		return "", errors.New("model declined the request")
	}
	var sb strings.Builder
	for _, b := range resp.Content {
		if t, ok := b.AsAny().(anthropic.TextBlock); ok {
			sb.WriteString(t.Text)
		}
	}
	return sb.String(), nil
}

// Budget caps LLM calls across the classify and remedy stages of one run.
type Budget struct {
	max  int64
	used atomic.Int64
}

// NewBudget allows up to max calls.
func NewBudget(max int) *Budget { return &Budget{max: int64(max)} }

// Take reserves one call, reporting false when the budget is spent.
func (b *Budget) Take() bool {
	if b == nil {
		return false
	}
	if b.used.Add(1) > b.max {
		b.used.Add(-1)
		return false
	}
	return true
}

// Used is the number of calls made.
func (b *Budget) Used() int {
	if b == nil {
		return 0
	}
	return int(b.used.Load())
}

// Verdict is the classifier's answer for one candidate.
type Verdict struct {
	Correction bool    `json:"correction"`
	Rule       string  `json:"rule"`
	Remedy     string  `json:"remedy"`
	Confidence float64 `json:"confidence"`
}

// SystemPrompt instructs the classifier.
const SystemPrompt = `You review excerpts from Claude Code sessions to find corrections a developer keeps having to make.

You get the assistant's most recent actions and the developer's next message. Decide whether that message is a correction: pushing back on what the assistant just did, telling it to do something differently, or restating a convention it broke. Approvals, questions, new tasks, status updates and neutral follow-ups are not corrections.

Some messages are instead flagged because the developer sent near-identical text in several sessions. For those, answer "correction": true only if the message states a standing convention or a procedure they keep re-explaining (something worth writing down once), and false if it is an ordinary task request that happens to repeat.

When it is a correction, also give:
- rule: the general, reusable instruction the developer is implying, as one short imperative sentence that would make sense in a CLAUDE.md file and in other sessions (for example "Write table-driven tests with the standard library, not testify/assert"). Leave out details that only apply to this one task.
- remedy: "hook" if the rule could be enforced mechanically by matching a file path, a shell command or a linter result; "skill" if it describes a multi-step procedure; "claude_md" if it needs judgment or is a style preference; "ignore" if it only makes sense for this one task.

Reply with only a JSON object, no prose:
{"correction": true|false, "rule": "...", "remedy": "hook"|"claude_md"|"skill"|"ignore", "confidence": 0.0-1.0}`

// UserPrompt builds the per-candidate message, capped near 2 KB.
func UserPrompt(s store.Signal) string {
	action := textutil.Truncate(s.Context, 1200)
	if action == "" {
		action = "(no recorded action)"
	}
	msg := textutil.Truncate(strings.TrimPrefix(s.Excerpt, "(interrupted) "), 700)
	lead := "The developer's next message"
	switch s.Type {
	case store.Interrupt:
		lead = "The developer interrupted the assistant, then wrote"
	case store.Rewind:
		lead = "The developer rewound the conversation and re-sent this instead"
	case store.RepeatedInstruction:
		lead = "The developer has sent near-identical messages in several separate sessions. One of them"
		action = "(" + s.Context + ")"
	}
	return fmt.Sprintf("<assistant_actions>\n%s\n</assistant_actions>\n\n%s:\n<message>\n%s\n</message>", action, lead, msg)
}

// ParseVerdict extracts the JSON object from a reply.
func ParseVerdict(s string) (Verdict, error) {
	var v Verdict
	i, j := strings.Index(s, "{"), strings.LastIndex(s, "}")
	if i < 0 || j < i {
		return v, fmt.Errorf("no JSON object in reply: %q", textutil.Truncate(s, 120))
	}
	if err := json.Unmarshal([]byte(s[i:j+1]), &v); err != nil {
		return v, fmt.Errorf("bad verdict JSON: %w", err)
	}
	switch v.Remedy {
	case "hook", "claude_md", "skill", "ignore":
	default:
		v.Remedy = ""
	}
	v.Rule = textutil.Truncate(textutil.OneLine(v.Rule), 300)
	return v, nil
}

// Options control the classify stage.
type Options struct {
	LLM         LLM
	Budget      *Budget
	Concurrency int
	Since       time.Time
	Repo        string
	Timeout     time.Duration
}

// Stats summarises a classify run.
type Stats struct {
	Pending   int
	Calls     int
	Confirmed int
	Rejected  int
	Errors    int
	LastError error
	Aborted   bool
}

// Run classifies unclassified candidates, newest first, within the budget.
func Run(ctx context.Context, st *store.Store, opt Options) (Stats, error) {
	var stats Stats
	where := `classified=0 AND type IN ('correction','interrupt','rewind','repeated_instruction')`
	args := []any{}
	if !opt.Since.IsZero() {
		where += ` AND ts >= ?`
		args = append(args, store.Ms(opt.Since))
	}
	if opt.Repo != "" {
		where += ` AND repo = ?`
		args = append(args, opt.Repo)
	}
	sigs, err := st.QuerySignals(where, args...)
	if err != nil {
		return stats, err
	}
	var todo []store.Signal
	for i := len(sigs) - 1; i >= 0; i-- {
		if signal.NeedsClassifier(sigs[i]) {
			todo = append(todo, sigs[i])
		}
	}
	stats.Pending = len(todo)
	if opt.Concurrency <= 0 {
		opt.Concurrency = 4
	}
	if opt.Timeout <= 0 {
		opt.Timeout = 60 * time.Second
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	work := make(chan store.Signal)
	var mu sync.Mutex
	var wg sync.WaitGroup
	var consecutiveErrs, successes atomic.Int64
	for range opt.Concurrency {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for s := range work {
				cctx, ccancel := context.WithTimeout(ctx, opt.Timeout)
				reply, err := opt.LLM.Complete(cctx, SystemPrompt, UserPrompt(s), 300)
				ccancel()
				var v Verdict
				if err == nil {
					v, err = ParseVerdict(reply)
				}
				mu.Lock()
				stats.Calls++
				if err != nil {
					stats.Errors++
					stats.LastError = err
					mu.Unlock()
					// Stop early when nothing works (bad credentials,
					// no network) instead of burning through the queue.
					if consecutiveErrs.Add(1) >= 5 && successes.Load() == 0 {
						cancel()
					}
					continue
				}
				consecutiveErrs.Store(0)
				successes.Add(1)
				if v.Correction {
					stats.Confirmed++
				} else {
					stats.Rejected++
				}
				_, werr := st.DB.Exec(`UPDATE signals SET classified=1, confirmed=?, rule=?, remedy_hint=?, embedding=NULL WHERE id=?`,
					boolInt(v.Correction), v.Rule, v.Remedy, s.ID)
				if werr != nil {
					stats.Errors++
					stats.LastError = werr
				}
				mu.Unlock()
			}
		}()
	}
loop:
	for _, s := range todo {
		if !opt.Budget.Take() {
			break
		}
		select {
		case work <- s:
		case <-ctx.Done():
			break loop
		}
	}
	close(work)
	wg.Wait()
	if ctx.Err() != nil && successes.Load() == 0 && stats.Errors > 0 {
		stats.Aborted = true
	}
	return stats, nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
