package cli

import (
	"github.com/spf13/cobra"

	"github.com/rossedman/sminer/internal/pipeline"
)

// newIngestCmd builds the ingest command, which reads new transcript lines
// into the database incrementally.
func newIngestCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ingest",
		Short: "Read new transcript lines into the database",
		Long: `Read new transcript lines into the database.

Ingest is incremental: each transcript is re-read only from where the last run
stopped, so repeat runs are cheap. Text is redacted before it is stored, and
raw transcripts are never copied.`,
		Example: `  sminer ingest
  sminer ingest --since 30d --root ~/.claude/projects --root /mnt/old/projects`,
		Args: args(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			env, err := a.env()
			if err != nil {
				return err
			}
			res, err := pipeline.Ingest(cmd.Context(), env, a.cfg.Roots, a.cfg.WindowDuration())
			if err != nil {
				return err
			}
			if a.g.json {
				return a.printJSON(res)
			}
			a.summarizeIngest(res)
			return nil
		},
	}
	cmd.Flags().String("since", "90d", "only read transcripts modified within this window")
	bindFlag(cmd.Flags(), "since", "window")
	registerRoots(cmd)
	return cmd
}
