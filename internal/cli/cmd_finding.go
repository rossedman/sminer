package cli

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/rossedman/sminer/internal/finding"
	"github.com/rossedman/sminer/internal/remedy"
	"github.com/rossedman/sminer/internal/report"
	"github.com/rossedman/sminer/internal/store"
)

func newFindingCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "finding",
		Aliases: []string{"findings"},
		Short:   "List, inspect, accept, dismiss and reopen findings",
		Long: `A finding is one rule you keep correcting, backed by the signals that show it.

Accepting writes a patch for its remedy and records the adoption date, so
'sminer finding trend' can show whether the fix worked. Dismissing hides it
from reports. Both are undone with 'sminer finding reopen'.`,
		Example: `  sminer finding list
  sminer finding show F1a2b3c
  sminer finding accept F1a2b3c --preview`,
		Args: args(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	cmd.AddCommand(newFindingListCmd(a), newFindingShowCmd(a), newFindingAcceptCmd(a),
		newFindingDismissCmd(a), newFindingReopenCmd(a), newFindingTrendCmd(a))
	return cmd
}

// completeIDs completes finding ids (with titles) from the database.
func completeIDs(a *app, status string, many bool) cobra.CompletionFunc {
	return func(cmd *cobra.Command, done []string, _ string) ([]cobra.Completion, cobra.ShellCompDirective) {
		if !many && len(done) > 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		if err := a.load(cmd); err != nil {
			return nil, cobra.ShellCompDirectiveError
		}
		svc, err := a.findings()
		if err != nil {
			return nil, cobra.ShellCompDirectiveError
		}
		ids, err := svc.IDs(status)
		if err != nil {
			return nil, cobra.ShellCompDirectiveError
		}
		return ids, cobra.ShellCompDirectiveNoFileComp
	}
}

func newFindingListCmd(a *app) *cobra.Command {
	var status string
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List findings, highest score first",
		Long: `List findings, highest score first, one per line:

  ID  STATUS  REMEDY  OCCURRENCES  SESSIONS  SCORE  TITLE

No findings is not an error: the command prints nothing and exits 0.`,
		Example: `  sminer finding list
  sminer finding list --status accepted
  sminer finding list --json | jq -r '.[] | select(.remedy == "hook") | .id'`,
		Args: args(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			svc, err := a.findings()
			if err != nil {
				return err
			}
			fs, err := svc.List(status)
			if err != nil {
				return usageError(cmd, err)
			}
			if a.g.json {
				return a.printJSON(fs)
			}
			if len(fs) == 0 && isTerminal(a.out) {
				a.info("no %s findings; run 'sminer run' to look for some", status)
			}
			rows := make([][]string, len(fs))
			for i, f := range fs {
				rows[i] = []string{f.ID, f.Status, f.Remedy, strconv.Itoa(f.Occurrences), strconv.Itoa(f.Sessions),
					strconv.FormatFloat(f.Score, 'f', 0, 64), f.Title}
			}
			return a.table([]string{"ID", "STATUS", "REMEDY", "OCCURRENCES", "SESSIONS", "SCORE", "TITLE"}, rows)
		},
	}
	cmd.Flags().StringVar(&status, "status", store.StatusOpen, "open, accepted, dismissed or all")
	_ = cmd.RegisterFlagCompletionFunc("status", cobra.FixedCompletions(finding.Statuses, cobra.ShellCompDirectiveNoFileComp))
	return cmd
}

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
	w := a.out
	p := f.Proposal
	fmt.Fprintf(w, "%s  %s\n", a.styleOut(bold, f.ID), f.Title)
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
	switch f.Remedy {
	case remedy.Hook:
		fmt.Fprintf(w, "proposal: PreToolUse hook on %s, block when %s matches %s\n  %s\n", p.Matcher, p.Field, p.Pattern, p.Rule)
	case remedy.Skill:
		fmt.Fprintf(w, "proposal: skill %s (%s)\n", p.SkillName, p.Description)
		for i, s := range p.Steps {
			fmt.Fprintf(w, "  %d. %s\n", i+1, s)
		}
	case remedy.Ignore:
		fmt.Fprintf(w, "ignored: %s\n", p.Why)
	default:
		fmt.Fprintf(w, "proposal: %s\n", p.Rule)
	}
	fmt.Fprintln(w)
	for _, e := range f.Excerpts {
		fmt.Fprintf(w, "%s  %-20s %s\n    %s\n    %s\n", e.TS.Format("2006-01-02 15:04"), e.Type, e.Repo, e.Text, a.styleOut(dim, e.Resume))
	}
}

func newFindingAcceptCmd(a *app) *cobra.Command {
	var preview bool
	var dir string
	cmd := &cobra.Command{
		Use:   "accept <id>...",
		Short: "Write the patch for findings and mark them adopted",
		Long: `Write the patch for each finding and mark it adopted.

Patches are rendered against the target files as they are now and written
atomically to the patch directory; each patch path goes to stdout once.
Nothing else is modified: apply the patches yourself. --preview prints the
patches instead and changes nothing. Undo with 'sminer finding reopen <id>'.

CLAUDE.md lines accepted together for the same file share one patch (named
like F1a2b3c+F4d5e6f.patch), so they apply cleanly together. If any finding
can't be patched, nothing is written.`,
		Example: `  sminer finding accept F1a2b3c --preview
  sminer finding accept F1a2b3c F4d5e6f
  patch -d ~ -p1 < "$(sminer finding accept F1a2b3c)"`,
		Args:              args(cobra.MinimumNArgs(1)),
		ValidArgsFunction: completeIDs(a, store.StatusOpen, true),
		RunE: func(cmd *cobra.Command, ids []string) error {
			svc, err := a.findings()
			if err != nil {
				return err
			}
			results, err := svc.AcceptMany(ids, dir, preview)
			if err != nil {
				return err
			}
			if a.g.json {
				return a.printJSON(results)
			}
			// Results sharing a patch are adjacent; report each patch once.
			for i := 0; i < len(results); {
				res := results[i]
				group := append([]string{res.ID}, res.Combined...)
				i += len(group)
				if preview {
					fmt.Fprint(a.out, res.Diff)
					continue
				}
				fmt.Fprintln(a.out, res.PatchPath)
				a.info("accepted %s; apply with: %s", strings.Join(group, ", "), res.Apply)
				if res.FollowUp != "" {
					a.info("%s", res.FollowUp)
				}
				a.info("undo with: sminer finding reopen %s", strings.Join(group, " "))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&preview, "preview", false, "print the patch without writing it or changing the finding")
	cmd.Flags().StringVar(&dir, "patch-dir", "", "where to write patch files (default $XDG_DATA_HOME/sminer/patches)")
	return cmd
}

func newFindingDismissCmd(a *app) *cobra.Command {
	var reason string
	cmd := &cobra.Command{
		Use:               "dismiss <id>...",
		Short:             "Hide findings that are not worth fixing",
		Example:           `  sminer finding dismiss F9d8e7f --reason "intentional in this repo"`,
		Args:              args(cobra.MinimumNArgs(1)),
		ValidArgsFunction: completeIDs(a, store.StatusOpen, true),
		RunE: func(cmd *cobra.Command, ids []string) error {
			svc, err := a.findings()
			if err != nil {
				return err
			}
			for _, id := range ids {
				if err := svc.Dismiss(id, reason); err != nil {
					return err
				}
				a.info("dismissed %s; undo with: sminer finding reopen %s", id, id)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&reason, "reason", "", "why it is not worth fixing (kept for your records)")
	return cmd
}

func newFindingReopenCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "reopen <id>...",
		Short: "Undo accept or dismiss",
		Long: `Return findings to open. For an accepted finding this also forgets the
adoption date; the patch file is left in place.`,
		Example:           `  sminer finding reopen F9d8e7f`,
		Args:              args(cobra.MinimumNArgs(1)),
		ValidArgsFunction: completeIDs(a, "all", true),
		RunE: func(cmd *cobra.Command, ids []string) error {
			svc, err := a.findings()
			if err != nil {
				return err
			}
			for _, id := range ids {
				if err := svc.Reopen(id); err != nil {
					return err
				}
				a.info("reopened %s", id)
			}
			return nil
		},
	}
}

func newFindingTrendCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "trend [<id>]",
		Short: "Show weekly occurrence rates, before and after adoption",
		Long: `Show how often findings occur per week. For an accepted finding the rate is
split at the adoption date, so you can see whether the fix worked.

With an id, print that finding's week-by-week counts.`,
		Example: `  sminer finding trend
  sminer finding trend F1a2b3c`,
		Args:              args(cobra.MaximumNArgs(1)),
		ValidArgsFunction: completeIDs(a, "all", false),
		RunE: func(cmd *cobra.Command, argv []string) error {
			svc, err := a.findings()
			if err != nil {
				return err
			}
			if len(argv) == 1 {
				tr, err := svc.Trend(argv[0])
				if err != nil {
					return err
				}
				if a.g.json {
					return a.printJSON(tr)
				}
				report.WriteTrend(a.out, tr)
				return nil
			}
			trs, err := svc.Trends()
			if err != nil {
				return err
			}
			if a.g.json {
				return a.printJSON(trs)
			}
			rows := make([][]string, len(trs))
			for i, tr := range trs {
				after := "-"
				if tr.AdoptedAt != nil {
					after = strconv.FormatFloat(tr.AfterRate, 'f', 2, 64)
				}
				ws := tr.Weeks
				if len(ws) > 24 {
					ws = ws[len(ws)-24:]
				}
				rows[i] = []string{tr.ID, strconv.FormatFloat(tr.BeforeRate, 'f', 2, 64), after, report.Sparkline(ws), tr.Title}
			}
			return a.table([]string{"ID", "BEFORE/WK", "AFTER/WK", "WEEKLY", "TITLE"}, rows)
		},
	}
}
