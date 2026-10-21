package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/rossedman/sminer/internal/config"
)

func newConfigPathCmd(a *app) *cobra.Command {
	return &cobra.Command{
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
}
