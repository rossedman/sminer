package remedy

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/rossedman/sminer/internal/classify"
	"github.com/rossedman/sminer/internal/store"
	"github.com/rossedman/sminer/internal/textutil"
)

const synthSystem = `You turn a cluster of related corrections from Claude Code sessions into one concrete fix for the developer's setup.

Pick the remedy:
- "hook": the rule can be checked mechanically before a tool call runs, by a regular expression on an Edit/Write file path or on a Bash command. Prefer a hook whenever one fits: deterministic beats instructed.
- "claude_md": the rule needs judgment or is a style preference; it becomes one line in CLAUDE.md.
- "skill": the developer keeps re-explaining a multi-step procedure.
- "ignore": the cluster is one-off or task-specific noise.

Reply with only a JSON object:
{"title": "short finding title, under 90 characters",
 "remedy": "hook"|"claude_md"|"skill"|"ignore",
 "rule": "one imperative sentence suitable for CLAUDE.md",
 "hook": {"matcher": "Edit|Write|MultiEdit" or "Bash", "field": "file_path" or "command", "pattern": "regex"},
 "skill": {"name": "kebab-case-name", "description": "when to use it", "steps": ["..."]}}
Omit "hook" or "skill" when they do not apply.`

type synthesis struct {
	Title    string
	Remedy   string
	Proposal Proposal
}

func synthesize(ctx context.Context, llm classify.LLM, f store.Finding, sigs []store.Signal) (synthesis, error) {
	var sb strings.Builder
	fmt.Fprintf(&sb, "%d occurrences across %d sessions.\n\n", len(sigs), countSessions(sigs))
	n := 0
	for i := len(sigs) - 1; i >= 0 && n < 8; i-- {
		s := sigs[i]
		n++
		fmt.Fprintf(&sb, "<occurrence type=%q>\n", s.Type)
		if s.Rule != "" {
			fmt.Fprintf(&sb, "classifier rule: %s\n", s.Rule)
		}
		fmt.Fprintf(&sb, "developer said: %s\n</occurrence>\n", textutil.Truncate(textutil.OneLine(strings.TrimPrefix(s.Excerpt, "(interrupted) ")), 300))
	}
	reply, err := llm.Complete(ctx, synthSystem, sb.String(), 800)
	if err != nil {
		return synthesis{}, err
	}
	i, j := strings.Index(reply, "{"), strings.LastIndex(reply, "}")
	if i < 0 || j < i {
		return synthesis{}, fmt.Errorf("no JSON in synthesis reply")
	}
	var r struct {
		Title  string `json:"title"`
		Remedy string `json:"remedy"`
		Rule   string `json:"rule"`
		Hook   *struct {
			Matcher string `json:"matcher"`
			Field   string `json:"field"`
			Pattern string `json:"pattern"`
		} `json:"hook"`
		Skill *struct {
			Name        string   `json:"name"`
			Description string   `json:"description"`
			Steps       []string `json:"steps"`
		} `json:"skill"`
	}
	if err := json.Unmarshal([]byte(reply[i:j+1]), &r); err != nil {
		return synthesis{}, fmt.Errorf("bad synthesis JSON: %w", err)
	}
	out := synthesis{Title: r.Title, Remedy: r.Remedy, Proposal: Proposal{Rule: textutil.Truncate(textutil.OneLine(r.Rule), 300)}}
	switch r.Remedy {
	case Hook:
		if r.Hook == nil || r.Hook.Pattern == "" || (r.Hook.Field != "file_path" && r.Hook.Field != "command") {
			out.Remedy = ClaudeMD
			break
		}
		if _, err := regexp.Compile(r.Hook.Pattern); err != nil {
			out.Remedy = ClaudeMD
			break
		}
		if _, err := regexp.Compile(r.Hook.Matcher); err != nil || r.Hook.Matcher == "" {
			r.Hook.Matcher = map[string]string{"file_path": "Edit|Write|MultiEdit", "command": "Bash"}[r.Hook.Field]
		}
		out.Proposal.Matcher, out.Proposal.Field, out.Proposal.Pattern = r.Hook.Matcher, r.Hook.Field, r.Hook.Pattern
	case Skill:
		if r.Skill == nil || len(r.Skill.Steps) == 0 {
			out.Remedy = ClaudeMD
			break
		}
		out.Proposal.SkillName = textutil.Slug(r.Skill.Name, 40)
		out.Proposal.Description = textutil.OneLine(r.Skill.Description)
		out.Proposal.Steps = r.Skill.Steps
	case ClaudeMD, Ignore:
	default:
		out.Remedy = ""
	}
	if out.Proposal.Rule == "" && out.Remedy != Ignore {
		out.Remedy = ""
	}
	return out, nil
}

func countSessions(sigs []store.Signal) int {
	m := map[string]bool{}
	for _, s := range sigs {
		m[s.SessionID] = true
	}
	return len(m)
}
