package cli

import (
	"fmt"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/rossedman/sminer/internal/report"
)

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
				if a.fancyOut() {
					fmt.Fprint(a.out, a.trendChart(tr))
					return nil
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
			if a.fancyOut() {
				return a.trendTable(trs)
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
			return a.table(nil, rows, nil)
		},
	}
}
