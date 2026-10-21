package cli

import (
	"strconv"

	"github.com/spf13/cobra"

	"github.com/rossedman/sminer/internal/finding"
	"github.com/rossedman/sminer/internal/store"
)

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
			if len(fs) == 0 && a.fancyOut() {
				a.info("no %s findings; run 'sminer run' to look for some", status)
			}
			if a.fancyOut() {
				return a.findingTable(fs)
			}
			rows := make([][]string, len(fs))
			for i, f := range fs {
				rows[i] = []string{f.ID, f.Status, f.Remedy, strconv.Itoa(f.Occurrences), strconv.Itoa(f.Sessions),
					strconv.FormatFloat(f.Score, 'f', 0, 64), f.Title}
			}
			return a.table(nil, rows, nil)
		},
	}
	cmd.Flags().StringVar(&status, "status", store.StatusOpen, "open, accepted, dismissed or all")
	_ = cmd.RegisterFlagCompletionFunc("status", cobra.FixedCompletions(finding.Statuses, cobra.ShellCompDirectiveNoFileComp))
	return cmd
}
