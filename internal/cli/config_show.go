package cli

import (
	"fmt"

	"charm.land/lipgloss/v2"
	"github.com/spf13/cobra"

	"github.com/rossedman/sminer/internal/config"
)

// newConfigShowCmd builds config show, which prints every setting with its
// value and the layer that set it: flag, environment, file or default.
func newConfigShowCmd(a *app) *cobra.Command {
	return &cobra.Command{
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
			if a.fancyOut() {
				for _, r := range rows {
					r[1], r[2] = tildeIn(r[1]), tildeIn(r[2])
				}
			}
			th := a.theme()
			return a.table([]string{"KEY", "VALUE", "SOURCE"}, rows, func(row, col int) lipgloss.Style {
				switch col {
				case 0:
					return th.Key
				case 2:
					return sourceStyle(th, rows[row][2])
				}
				return th.Value
			})
		},
	}
}
