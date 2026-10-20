package cli

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/rossedman/sminer/internal/config"
	"github.com/rossedman/sminer/internal/eval"
	"github.com/rossedman/sminer/internal/version"
)

func newConfigCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Show the effective configuration and where it lives",
		Long: `Show the effective configuration and where each value came from.

Precedence, highest first: flags, SMINER_* environment variables, the config
file, built-in defaults. Every key can be set by environment: classifier.model
is SMINER_CLASSIFIER_MODEL.`,
		Args: args(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	show := &cobra.Command{
		Use:   "show",
		Short: "Print every setting with the layer that set it",
		Example: `  sminer config show
  SMINER_WINDOW=30d sminer config show --json | jq .window`,
		Args: args(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			type entry struct {
				Value  any    `json:"value"`
				Source string `json:"source"`
			}
			out := map[string]entry{}
			var rows [][]string
			for _, k := range config.Keys() {
				e := entry{Value: a.v.Get(k), Source: a.source(cmd, k)}
				out[k] = e
				rows = append(rows, []string{k, fmt.Sprint(e.Value), e.Source})
			}
			if a.g.json {
				return a.printJSON(out)
			}
			return a.table([]string{"KEY", "VALUE", "SOURCE"}, rows)
		},
	}
	path := &cobra.Command{
		Use:   "path",
		Short: "Print the config file path",
		Long: `Print the path of the config file sminer reads. The file is optional; with
--json, also report whether it exists and where data is kept.`,
		Example:     `  $EDITOR "$(sminer config path)"`,
		Args:        args(cobra.NoArgs),
		Annotations: map[string]string{"config": "skip"},
		RunE: func(cmd *cobra.Command, _ []string) error {
			file := config.Expand(or(a.g.configFile, config.DefaultFile()))
			_, statErr := os.Stat(file)
			if a.g.json {
				return a.printJSON(map[string]any{"config_file": file, "exists": statErr == nil, "data_dir": config.DataDir()})
			}
			fmt.Fprintln(a.out, file)
			return nil
		},
	}
	cmd.AddCommand(show, path)
	return cmd
}

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

func newVersionCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version (--json adds build details and the command tree)",
		Example: `  sminer version
  sminer version --json | jq '.commands[]'`,
		Args:        args(cobra.NoArgs),
		Annotations: map[string]string{"config": "skip"},
		RunE: func(cmd *cobra.Command, _ []string) error {
			info := version.Get()
			if a.g.json {
				return a.printJSON(struct {
					version.Info
					Commands []string `json:"commands"`
				}{info, commandTree(cmd.Root())})
			}
			fmt.Fprintf(a.out, "sminer %s", info.Version)
			if info.Commit != "" {
				fmt.Fprintf(a.out, " (%s)", info.Commit)
			}
			fmt.Fprintf(a.out, " %s %s/%s\n", info.Go, info.OS, info.Arch)
			return nil
		},
	}
}

// commandTree lists every visible command path, for tools that introspect.
func commandTree(c *cobra.Command) []string {
	var out []string
	var walk func(*cobra.Command)
	walk = func(c *cobra.Command) {
		for _, sub := range c.Commands() {
			if sub.Hidden || !sub.IsAvailableCommand() {
				continue
			}
			out = append(out, strings.TrimPrefix(sub.CommandPath(), "sminer "))
			walk(sub)
		}
	}
	walk(c)
	return out
}
