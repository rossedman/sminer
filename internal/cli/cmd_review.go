package cli

import (
	"context"
	"errors"
	"fmt"

	tea "charm.land/bubbletea/v2"
	"github.com/spf13/cobra"

	"github.com/rossedman/sminer/internal/finding"
	"github.com/rossedman/sminer/internal/store"
)

func newReviewCmd(a *app) *cobra.Command {
	var status, dir string
	cmd := &cobra.Command{
		Use:   "review",
		Short: "Work through findings in a full-screen browser",
		Long: `Work through findings in a full-screen browser: the list on the left, the
selected finding on the right, one key per action.

  space  mark a finding (actions apply to every marked one)
  p      show the patch accepting would write
  a      accept: write the patch and record the adoption
  d      dismiss, with an optional reason
  r      reopen
  tab    switch between open, accepted, dismissed and all
  ?      every key

Accepting does what 'sminer finding accept' does: it writes a patch file and
never applies it. CLAUDE.md lines marked together for the same file share
one patch. When you quit, the paths of the patches written go to stdout, one
per line, so they can be piped into patch or a script.

review needs a terminal. In scripts, use 'sminer finding list' and
'sminer finding accept'.`,
		Example: `  sminer review
  sminer review --status all
  for p in $(sminer review); do echo "wrote $p"; done`,
		Args: args(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !validStatus(status) {
				return usageError(cmd, fmt.Errorf("unknown status %q (want one of open, accepted, dismissed, all)", status))
			}
			if !isTerminal(a.opts.Stdin) || !isTerminal(a.opts.Stderr) {
				return withHint(usageError(cmd, errors.New("review is interactive and needs a terminal")),
					"use 'sminer finding list' and 'sminer finding accept <id>' in scripts")
			}
			svc, err := a.findings()
			if err != nil {
				return err
			}
			return a.review(cmd.Context(), svc, dir, status)
		},
	}
	cmd.Flags().StringVar(&status, "status", store.StatusOpen, "findings to start on: open, accepted, dismissed or all")
	cmd.Flags().StringVar(&dir, "patch-dir", "", "where to write patch files (default $XDG_DATA_HOME/sminer/patches)")
	_ = cmd.RegisterFlagCompletionFunc("status", cobra.FixedCompletions(finding.Statuses, cobra.ShellCompDirectiveNoFileComp))
	return cmd
}

func validStatus(s string) bool {
	for _, v := range finding.Statuses {
		if s == v {
			return true
		}
	}
	return false
}

func (a *app) review(ctx context.Context, svc reviewer, dir, status string) error {
	m := newReviewModel(a.theme(), svc, dir, status)
	if m.err != nil {
		return m.err
	}
	p := tea.NewProgram(m, tea.WithContext(ctx), tea.WithInput(a.opts.Stdin), tea.WithOutput(a.opts.Stderr))
	_, runErr := p.Run()
	if err := ctx.Err(); err != nil {
		return err
	}
	if m.interrupted {
		a.reviewSummary(m)
		return context.Canceled
	}
	if runErr != nil && !errors.Is(runErr, tea.ErrProgramKilled) {
		return runErr
	}
	a.reviewSummary(m)
	if a.g.json {
		return a.printJSON(map[string]any{"patches": orEmpty(m.written), "accepted": m.accepted,
			"dismissed": m.dismissed, "reopened": m.reopened})
	}
	for _, p := range m.written {
		fmt.Fprintln(a.out, p)
	}
	return nil
}

func (a *app) reviewSummary(m *reviewModel) {
	if m.accepted+m.dismissed+m.reopened == 0 {
		a.info("%s", style(a.theme().Faint, "no changes"))
		return
	}
	th := a.theme()
	a.info("%s accepted %d · dismissed %d · reopened %d", style(th.OK, "✓"), m.accepted, m.dismissed, m.reopened)
	for _, p := range m.written {
		a.info("  %s %s", style(th.Key, "apply with:"), style(th.Code, tildeIn(m.applies[p])))
	}
}

func orEmpty(ss []string) []string {
	if ss == nil {
		return []string{}
	}
	return ss
}
