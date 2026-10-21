package cli

import (
	"github.com/spf13/cobra"

	"github.com/rossedman/sminer/internal/pipeline"
)

func newReportCmd(a *app) *cobra.Command {
	var scope scopeFlags
	var output, format string
	cmd := &cobra.Command{
		Use:   "report",
		Short: "Print the ranked findings report",
		Long: `Print the ranked findings report as Markdown, or JSON with --json.

Findings are ranked by occurrences × weight × repos. With --output, the report
is written to that file (atomically) and findings.json is written beside it.`,
		Example: `  sminer report | less
  sminer report --output report.md
  sminer report --json > findings.json`,
		Args: args(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if format == "json" {
				a.g.json = true
			}
			env, err := a.env()
			if err != nil {
				return err
			}
			r, err := pipeline.Report(env, a.scope(scope))
			if err != nil {
				return err
			}
			return a.emitReport(r, output)
		},
	}
	scope.register(cmd)
	cmd.Flags().StringVarP(&output, "output", "o", "", "write the report to this file instead of stdout")
	cmd.Flags().StringVar(&format, "format", "", "")
	deprecateFlag(cmd, "format", "--json")
	cmd.Flags().StringVar(&output, "out", "", "")
	deprecateFlag(cmd, "out", "--output")
	return cmd
}
