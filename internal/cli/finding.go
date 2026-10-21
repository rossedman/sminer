package cli

import (
	"github.com/spf13/cobra"
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
