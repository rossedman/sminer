package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/rossedman/sminer/internal/store"
)

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
					if a.fancyOut() {
						fmt.Fprint(a.out, a.theme().Diff(res.Diff))
					} else {
						fmt.Fprint(a.out, res.Diff)
					}
					continue
				}
				fmt.Fprintln(a.out, res.PatchPath)
				a.acceptedNote(group, res)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&preview, "preview", false, "print the patch without writing it or changing the finding")
	cmd.Flags().StringVar(&dir, "patch-dir", "", "where to write patch files (default $XDG_DATA_HOME/sminer/patches)")
	return cmd
}
