package cli

import (
	"github.com/spf13/cobra"

	"github.com/rossedman/sminer/internal/store"
)

// newFindingDismissCmd builds finding dismiss, which hides findings from
// reports and keeps an optional reason.
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
