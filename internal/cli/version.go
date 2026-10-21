package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/rossedman/sminer/internal/version"
)

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
