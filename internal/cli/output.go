package cli

import (
	"fmt"
	"strings"
	"text/tabwriter"

	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/table"

	"github.com/rossedman/sminer/internal/ui"
)

// isTerminal reports whether a stream is a character device (a TTY).
func isTerminal(stream any) bool { return ui.IsTerminal(stream) }

// fancyOut reports whether stdout is a person at a terminal, which earns
// tables with headers, rendered Markdown and other layout. Pipes get the
// plain, line-oriented form. Color is separate: the writers strip it
// whenever it isn't wanted.
func (a *app) fancyOut() bool { return isTerminal(a.opts.Stdout) }

// fancyErr is the same for stderr: spinners and prompts.
func (a *app) fancyErr() bool { return isTerminal(a.opts.Stderr) && !a.g.quiet }

// theme is the terminal theme, built on first use.
func (a *app) theme() ui.Theme {
	if a.th == nil {
		t := ui.New(ui.Dark())
		a.th = &t
	}
	return *a.th
}

// style renders s in st; the writer strips it when color is off.
func style(st lipgloss.Style, s string) string {
	if s == "" {
		return s
	}
	return st.Render(s)
}

// noColorArg finds --no-color before flags are parsed, so even help and
// usage errors honor it.
func noColorArg(argv []string) bool {
	for _, a := range argv {
		if a == "--" {
			break
		}
		if a == "--no-color" || a == "--no-color=true" {
			return true
		}
	}
	return false
}

// table writes a list. On a terminal it's a lipgloss table with a ruled
// header; piped, it's aligned columns, one record per line, no header.
func (a *app) table(header []string, rows [][]string, cell func(row, col int) lipgloss.Style) error {
	if a.fancyOut() {
		_, err := fmt.Fprintln(a.out, a.fit(a.theme().Table(header, rows, cell)))
		return err
	}
	tw := tabwriter.NewWriter(a.out, 0, 4, 2, ' ', 0)
	for _, r := range rows {
		tw.Write([]byte(strings.Join(r, "\t") + "\n"))
	}
	return tw.Flush()
}

// fit renders a table, wrapping cells only when it would overflow the
// terminal.
func (a *app) fit(t *table.Table) string {
	out := t.String()
	if lipgloss.Width(out) > a.width() {
		out = t.Width(a.width()).String()
	}
	return out
}
