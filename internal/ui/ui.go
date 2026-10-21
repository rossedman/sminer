// Package ui is sminer's terminal look: the brand palette, lipgloss styles
// and the small components commands share (badges, bars, sparklines, tables,
// diffs). Everything here renders strings; whether color reaches the screen
// is decided by the writer (see NewWriter), so a piped or NO_COLOR run gets
// the same text without escape codes.
package ui

import (
	"image/color"
	"io"
	"os"
	"strings"

	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/table"
	"github.com/charmbracelet/colorprofile"
)

// Palette is the brand's colors for one background.
type Palette struct {
	Ore, Gem, Ember, Violet color.Color
	Text, Muted, Faint      color.Color
	Surface                 color.Color // panel background
	Ink                     color.Color // text on a colored background
}

// NewPalette picks the dark- or light-background variant of the brand.
func NewPalette(dark bool) Palette {
	c := lipgloss.LightDark(dark)
	hex := lipgloss.Color
	return Palette{
		Ore:     c(hex("#a8670b"), hex("#f4b443")),
		Gem:     c(hex("#0d8a78"), hex("#6fdccb")),
		Ember:   c(hex("#c2462f"), hex("#f2836f")),
		Violet:  c(hex("#5a49c9"), hex("#a99cf5")),
		Text:    c(hex("#1b1d2e"), hex("#f6efe2")),
		Muted:   c(hex("#5d6178"), hex("#a3a8c3")),
		Faint:   c(hex("#9a9db0"), hex("#5f6380")),
		Surface: c(hex("#f1ece2"), hex("#1a1d33")),
		Ink:     hex("#0e1020"),
	}
}

// Theme is the palette plus the styles built from it.
type Theme struct {
	P    Palette
	Dark bool

	Title, Heading, Text, Muted, Faint, Bold lipgloss.Style
	ID, Key, Value, Code                     lipgloss.Style
	OK, Warn, Err, Accent                    lipgloss.Style
}

// New builds the theme for a background.
func New(dark bool) Theme {
	p := NewPalette(dark)
	s := lipgloss.NewStyle()
	return Theme{
		P:       p,
		Dark:    dark,
		Title:   s.Bold(true).Foreground(p.Text),
		Heading: s.Bold(true).Foreground(p.Ore),
		Text:    s.Foreground(p.Text),
		Muted:   s.Foreground(p.Muted),
		Faint:   s.Foreground(p.Faint),
		Bold:    s.Bold(true),
		ID:      s.Bold(true).Foreground(p.Ore),
		Key:     s.Foreground(p.Muted),
		Value:   s.Foreground(p.Text),
		Code:    s.Foreground(p.Gem),
		OK:      s.Foreground(p.Gem),
		Warn:    s.Foreground(p.Ore),
		Err:     s.Foreground(p.Ember),
		Accent:  s.Foreground(p.Violet),
	}
}

// Dark reports whether the terminal has a dark background. It goes by
// COLORFGBG, which many terminals set ("15;0" is light text on black), and
// assumes dark without it. Asking the terminal directly would mean a
// round-trip that some terminals and pipes never answer.
func Dark() bool {
	v := os.Getenv("COLORFGBG")
	if i := strings.LastIndexByte(v, ';'); i >= 0 {
		switch v[i+1:] {
		case "7", "15":
			return false
		}
	}
	return true
}

// IsTerminal reports whether a stream is a character device (a TTY).
func IsTerminal(stream any) bool {
	f, ok := stream.(*os.File)
	if !ok {
		return false
	}
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

// NewWriter wraps w so styled text is downsampled to what the terminal
// supports, and stripped entirely when w is not a terminal, NO_COLOR is
// set, TERM is dumb, or noColor is true.
func NewWriter(w io.Writer, noColor bool) *colorprofile.Writer {
	cw := colorprofile.NewWriter(w, os.Environ())
	if noColor && cw.Profile > colorprofile.Ascii {
		cw.Profile = colorprofile.Ascii
	}
	return cw
}

// Remedy names, mirrored from the remedy package to keep ui free of
// domain imports.
const (
	remedyHook     = "hook"
	remedyClaudeMD = "claude_md"
	remedySkill    = "skill"
)

// RemedyColor is the color that marks a remedy everywhere.
func (t Theme) RemedyColor(remedy string) color.Color {
	switch remedy {
	case remedyHook:
		return t.P.Violet
	case remedyClaudeMD:
		return t.P.Ore
	case remedySkill:
		return t.P.Gem
	}
	return t.P.Faint
}

// RemedyLabel is the human name of a remedy.
func RemedyLabel(remedy string) string {
	switch remedy {
	case remedyHook:
		return "hook"
	case remedyClaudeMD:
		return "CLAUDE.md"
	case remedySkill:
		return "skill"
	case "":
		return "pending"
	}
	return remedy
}

// Badge is a remedy as a small colored pill.
func (t Theme) Badge(remedy string) string {
	return lipgloss.NewStyle().Bold(true).Padding(0, 1).
		Foreground(t.P.Ink).Background(t.RemedyColor(remedy)).
		Render(RemedyLabel(remedy))
}

// Remedy is a remedy as colored text, for tables.
func (t Theme) Remedy(remedy string) string {
	return lipgloss.NewStyle().Foreground(t.RemedyColor(remedy)).Render("● " + RemedyLabel(remedy))
}

// Status is a finding status with its glyph.
func (t Theme) Status(status string) string {
	switch status {
	case "open":
		return t.Warn.Render("○") + t.Muted.Render(" open")
	case "accepted":
		return t.OK.Render("✓ accepted")
	case "dismissed":
		return t.Faint.Render("✗ dismissed")
	}
	return status
}

// Bar is a horizontal meter of width cells for v out of total.
func (t Theme) Bar(v, total float64, width int, c color.Color) string {
	if total <= 0 {
		total = 1
	}
	n := int(v/total*float64(width) + 0.5)
	n = min(max0(n), width)
	if v > 0 && n == 0 {
		n = 1
	}
	return lipgloss.NewStyle().Foreground(c).Render(strings.Repeat("━", n)) +
		t.Faint.Render(strings.Repeat("─", width-n))
}

func max0(n int) int {
	if n < 0 {
		return 0
	}
	return n
}

var sparks = []rune("▁▂▃▄▅▆▇█")

// Spark renders counts as a sparkline. Cells at index split and later are
// colored after (the weeks since adoption); split < 0 colors none.
func (t Theme) Spark(counts []int, split int, before, after color.Color) string {
	hi := 0
	for _, c := range counts {
		hi = max(hi, c)
	}
	var b, a strings.Builder
	for i, c := range counts {
		r := ' '
		switch {
		case c == 0:
			r = '·'
		case hi > 0:
			r = sparks[min(len(sparks)-1, c*(len(sparks)-1)/hi)]
		}
		if split >= 0 && i >= split {
			a.WriteRune(r)
		} else {
			b.WriteRune(r)
		}
	}
	return lipgloss.NewStyle().Foreground(before).Render(b.String()) +
		lipgloss.NewStyle().Foreground(after).Render(a.String())
}

// Table is a borderless table with a ruled header, the shape every list in
// sminer uses on a terminal. style colors a body cell; nil leaves it plain.
func (t Theme) Table(headers []string, rows [][]string, style func(row, col int) lipgloss.Style) *table.Table {
	return table.New().
		Border(lipgloss.NormalBorder()).
		BorderTop(false).BorderBottom(false).BorderLeft(false).BorderRight(false).
		BorderColumn(false).BorderRow(false).BorderHeader(true).
		BorderStyle(t.Faint).
		Headers(headers...).
		Rows(rows...).
		StyleFunc(func(row, col int) lipgloss.Style {
			base := lipgloss.NewStyle().PaddingRight(2)
			if row == table.HeaderRow {
				return base.Inherit(t.Muted).Bold(true)
			}
			if style != nil {
				return base.Inherit(style(row, col))
			}
			return base
		})
}

// Diff colors a unified diff: additions gem, removals ember, hunks violet,
// file headers bold, anything else (sminer's patch header) muted.
func (t Theme) Diff(diff string) string {
	lines := strings.SplitAfter(diff, "\n")
	var sb strings.Builder
	inBody := false
	for _, l := range lines {
		body := strings.TrimSuffix(l, "\n")
		nl := l[len(body):]
		var st lipgloss.Style
		switch {
		case strings.HasPrefix(body, "+++ ") || strings.HasPrefix(body, "--- ") || strings.HasPrefix(body, "diff --git") || strings.HasPrefix(body, "new file mode"):
			st, inBody = t.Bold, true
		case strings.HasPrefix(body, "@@"):
			st = t.Accent
		case inBody && strings.HasPrefix(body, "+"):
			st = t.OK
		case inBody && strings.HasPrefix(body, "-"):
			st = t.Err
		case inBody:
			st = t.Text
		default:
			st = t.Muted
		}
		if body != "" {
			body = st.Render(body)
		}
		sb.WriteString(body + nl)
	}
	return sb.String()
}

// Rule is a horizontal rule with an optional label, width cells wide.
func (t Theme) Rule(label string, width int) string {
	if label == "" {
		return t.Faint.Render(strings.Repeat("─", width))
	}
	l := " " + label + " "
	rest := max(0, width-lipgloss.Width(l)-2)
	return t.Faint.Render("──") + t.Heading.Render(l) + t.Faint.Render(strings.Repeat("─", rest))
}

// Tilde shortens a path under the home directory to ~/..., for display.
func Tilde(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" || home == "/" {
		return p
	}
	if p == home {
		return "~"
	}
	if strings.HasPrefix(p, home+string(os.PathSeparator)) {
		return "~" + p[len(home):]
	}
	return p
}
