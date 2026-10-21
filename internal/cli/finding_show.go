package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/rossedman/sminer/internal/remedy"
	"github.com/rossedman/sminer/internal/report"
)

func newFindingShowCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:               "show <id>",
		Short:             "Show a finding's proposal and every excerpt behind it",
		Example:           `  sminer finding show F1a2b3c`,
		Args:              args(cobra.ExactArgs(1)),
		ValidArgsFunction: completeIDs(a, "all", false),
		RunE: func(cmd *cobra.Command, argv []string) error {
			svc, err := a.findings()
			if err != nil {
				return err
			}
			f, err := svc.Get(argv[0])
			if err != nil {
				return err
			}
			if a.g.json {
				return a.printJSON(f)
			}
			a.writeFinding(f)
			return nil
		},
	}
}

func (a *app) writeFinding(f report.Finding) {
	if a.fancyOut() {
		fmt.Fprint(a.out, a.findingCard(f))
		return
	}
	w := a.out
	p := f.Proposal
	fmt.Fprintf(w, "%s  %s\n", f.ID, f.Title)
	fmt.Fprintf(w, "status %s · remedy %s · %d occurrences in %d sessions · score %.0f\n", f.Status, f.Remedy, f.Occurrences, f.Sessions, f.Score)
	if len(f.Repos) > 0 {
		fmt.Fprintf(w, "repos %s\n", strings.Join(f.Repos, ", "))
	}
	if f.Target != "" {
		fmt.Fprintf(w, "target %s\n", f.Target)
	}
	if f.AdoptedAt != nil {
		fmt.Fprintf(w, "adopted %s\n", f.AdoptedAt.Format("2006-01-02"))
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, proposalText(f.Remedy, p))
	fmt.Fprintln(w)
	for _, e := range f.Excerpts {
		fmt.Fprintf(w, "%s  %-20s %s\n    %s\n    %s\n", e.TS.Format("2006-01-02 15:04"), e.Type, e.Repo, e.Text, e.Resume)
	}
}

// proposalText describes a proposal in a line or a short list.
func proposalText(r string, p remedy.Proposal) string {
	switch r {
	case remedy.Hook:
		s := fmt.Sprintf("proposal: PreToolUse hook on %s, block when %s matches %s\n  %s", p.Matcher, p.Field, p.Pattern, p.Rule)
		if u := p.Unheeded; u != nil {
			s = fmt.Sprintf("escalated: %s is ignored (corrected %d times since)\n", u.Where, u.Since) + s
		}
		return s
	case remedy.Skill:
		s := fmt.Sprintf("proposal: skill %s (%s)", p.SkillName, p.Description)
		for i, st := range p.Steps {
			s += fmt.Sprintf("\n  %d. %s", i+1, st)
		}
		return s
	case remedy.Ignore:
		return "ignored: " + p.Why
	}
	return "proposal: " + p.Rule
}
