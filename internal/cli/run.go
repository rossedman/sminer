package cli

import (
	"github.com/spf13/cobra"

	"github.com/rossedman/sminer/internal/pipeline"
)

func newRunCmd(a *app) *cobra.Command {
	var scope scopeFlags
	var output string
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Ingest new transcripts, analyze them, and print the report",
		Long: `Run the whole pipeline: ingest, analyze, report.

The report goes to stdout as Markdown (or JSON with --json); progress goes to
stderr. With --output, the report is written to that file and findings.json
is written beside it.`,
		Example: `  sminer run --offline
  sminer run --output ~/notes/sminer.md
  sminer run --json | jq '.findings[].title'`,
		Args: args(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			env, err := a.env()
			if err != nil {
				return err
			}
			ir, err := pipeline.Ingest(cmd.Context(), env, a.cfg.Roots, a.cfg.WindowDuration())
			if err != nil {
				return err
			}
			a.summarizeIngest(ir)
			ar, err := pipeline.Analyze(cmd.Context(), env, a.scope(scope))
			if err != nil {
				return err
			}
			a.summarizeAnalyze(ar)
			r, err := pipeline.Report(env, a.scope(scope))
			if err != nil {
				return err
			}
			return a.emitReport(r, output)
		},
	}
	scope.register(cmd)
	registerRoots(cmd)
	cmd.Flags().StringVarP(&output, "output", "o", "", "write the report to this file instead of stdout")
	return cmd
}
