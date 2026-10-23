package cli

import (
	"github.com/spf13/cobra"

	"github.com/rossedman/sminer/internal/pipeline"
)

// newAnalyzeCmd builds the analyze command, which finds signals in ingested
// sessions, clusters them into findings and routes each to a remedy.
func newAnalyzeCmd(a *app) *cobra.Command {
	var scope scopeFlags
	cmd := &cobra.Command{
		Use:   "analyze",
		Short: "Find signals, cluster them into findings, and propose remedies",
		Long: `Find signals in ingested sessions, confirm them with the classifier, cluster
them into findings, and route each finding to a remedy.

Classifier results are cached, so re-running only classifies new candidates.
--offline skips the classifier and proposal drafting entirely.`,
		Example: `  sminer analyze
  sminer analyze --offline --repo ~/src/api --min-count 2`,
		Args: args(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			env, err := a.env()
			if err != nil {
				return err
			}
			res, err := pipeline.Analyze(cmd.Context(), env, a.scope(scope))
			if err != nil {
				return err
			}
			if a.g.json {
				return a.printJSON(res)
			}
			a.summarizeAnalyze(res)
			return nil
		},
	}
	scope.register(cmd)
	return cmd
}
