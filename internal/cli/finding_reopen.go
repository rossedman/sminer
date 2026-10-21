package cli

import (
	"github.com/spf13/cobra"
)

// newFindingReopenCmd builds finding reopen, which returns accepted or
// dismissed findings to open. An existing patch file is left in place.
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
