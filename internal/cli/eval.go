package cli

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/rossedman/sminer/internal/config"
	"github.com/rossedman/sminer/internal/eval"
)

func newEvalCmd(a *app) *cobra.Command {
	var labels string
	var live bool
	cmd := &cobra.Command{
		Use:   "eval",
		Short: "Score correction detection against a labelled set",
		Long: `Score correction detection against a hand-labelled set of candidate turns
(JSONL: {"action": ..., "message": ..., "correction": true|false}).

By default it reads the labels 'sminer label' wrote from your own sessions.
Lines whose correction is null are skipped. The heuristic score runs
offline. --live also scores the classifier, which makes one API call per
labelled turn.`,
		Example: `  sminer eval
  sminer eval --labels testdata/labels/candidates.jsonl
  sminer eval --labels mine.jsonl --live --json`,
		Args: args(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if labels == "" {
				labels = defaultLabels()
			}
			set, err := eval.Load(config.Expand(labels))
			if errors.Is(err, os.ErrNotExist) && !cmd.Flags().Changed("labels") {
				return withHint(fmt.Errorf("no labels yet at %s", labels),
					"label some of your turns with 'sminer label', or pass --labels testdata/labels/candidates.jsonl")
			}
			if err != nil {
				return err
			}
			if len(set) == 0 {
				return fmt.Errorf("%s has no labelled turns", labels)
			}
			type score struct {
				Precision float64 `json:"precision"`
				Recall    float64 `json:"recall"`
				eval.Score
			}
			mk := func(s eval.Score) score { return score{s.Precision(), s.Recall(), s} }
			out := map[string]score{"heuristic": mk(eval.Heuristic(set))}
			if live {
				if a.cfg.Offline {
					return usageError(cmd, fmt.Errorf("--live needs the network; drop --offline"))
				}
				a.progress("classifying %d labelled turns with %s", len(set), a.cfg.Classifier.Model)
				c, err := eval.Classifier(cmd.Context(), set, a.opts.NewLLM(a.cfg.Classifier.Model))
				if err != nil {
					return err
				}
				out["classifier"] = mk(c.Score)
				out["pipeline"] = mk(eval.Pipeline(set, c.Verdicts))
			}
			if a.g.json {
				return a.printJSON(out)
			}
			for _, k := range []string{"heuristic", "classifier", "pipeline"} {
				if s, ok := out[k]; ok {
					fmt.Fprintf(a.out, "%-10s %s\n", k, s.Score)
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&labels, "labels", "", "labelled candidate turns, JSONL (default $XDG_DATA_HOME/sminer/labels.jsonl)")
	cmd.Flags().BoolVar(&live, "live", false, "also score the live classifier (makes API calls)")
	return cmd
}
