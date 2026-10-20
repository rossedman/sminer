package cli

import (
	"io"
	"os"
	"text/tabwriter"
)

// ANSI styles. Color only ever decorates text that already says the same
// thing, so stripping it loses nothing.
const (
	red    = "31"
	yellow = "33"
	green  = "32"
	dim    = "2"
	bold   = "1"
)

// isTerminal reports whether a stream is a character device (a TTY).
func isTerminal(stream any) bool {
	f, ok := stream.(*os.File)
	if !ok {
		return false
	}
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

// colorOn decides color for one stream: never with --no-color, NO_COLOR,
// TERM=dumb, or when the stream is not a terminal.
func (a *app) colorOn(w io.Writer) bool {
	if a.g.noColor || os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return false
	}
	return isTerminal(w)
}

// style colors s for stderr.
func (a *app) style(code, s string) string { return paint(a.colorOn(a.err), code, s) }

// styleOut colors s for stdout.
func (a *app) styleOut(code, s string) string { return paint(a.colorOn(a.out), code, s) }

func paint(on bool, code, s string) string {
	if !on || s == "" {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

// table writes aligned, line-oriented columns: one record per line, stable
// field order, a header only when a person is reading.
func (a *app) table(header []string, rows [][]string) error {
	tw := tabwriter.NewWriter(a.out, 0, 4, 2, ' ', 0)
	write := func(cols []string) {
		for i, c := range cols {
			if i > 0 {
				tw.Write([]byte{'\t'})
			}
			tw.Write([]byte(c))
		}
		tw.Write([]byte{'\n'})
	}
	if isTerminal(a.out) {
		write(header)
	}
	for _, r := range rows {
		write(r)
	}
	return tw.Flush()
}
