package cli

import (
	"github.com/spf13/cobra"
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
	cmd.AddCommand(newConfigShowCmd(a), newConfigPathCmd(a))
	return cmd
}
