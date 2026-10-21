package cli

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/rossedman/sminer/internal/signal"
	"github.com/rossedman/sminer/internal/textutil"
	"github.com/rossedman/sminer/internal/ui"
)

// labelModel is the full-screen labeller: one candidate at a time, one key
// per answer, saved as it's given.
type labelModel struct {
	th          ui.Theme
	items       []signal.Candidate
	i           int
	save        func(signal.Candidate, bool) error
	t           *tally
	width       int
	last        string // what the previous key did
	err         error
	interrupted bool
}

// Init implements tea.Model. The label screen has no startup command.
func (m *labelModel) Init() tea.Cmd { return nil }

// Update implements tea.Model. It tracks the terminal width and handles one
// key per candidate: y and n save an answer, s skips, and q or esc quits. A
// failed save or ctrl+c ends the program; ctrl+c also marks it interrupted.
// Other keys are ignored.
func (m *labelModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = min(msg.Width, 120)
	case tea.KeyPressMsg:
		if m.i >= len(m.items) {
			return m, tea.Quit
		}
		c := m.items[m.i]
		switch msg.String() {
		case "y":
			if m.err = m.save(c, true); m.err != nil {
				return m, tea.Quit
			}
			m.last = m.th.OK.Render("✓ marked as a correction")
		case "n":
			if m.err = m.save(c, false); m.err != nil {
				return m, tea.Quit
			}
			m.last = m.th.Accent.Render("✗ marked as not a correction")
		case "s", "space", "right":
			m.t.skipped++
			m.last = m.th.Faint.Render("↷ skipped")
		case "q", "esc":
			return m, tea.Quit
		case "ctrl+c":
			m.interrupted = true
			return m, tea.Quit
		default:
			return m, nil
		}
		m.i++
		if m.i >= len(m.items) {
			return m, tea.Quit
		}
	}
	return m, nil
}

// View implements tea.Model, rendering the current candidate on the alternate
// screen.
func (m *labelModel) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	return v
}

// render draws the header with progress and tallies, the action Claude took,
// the message the user sent, and the key legend. It returns an empty string
// once every candidate has been answered.
func (m *labelModel) render() string {
	th := m.th
	w := max(40, m.width)
	inner := min(w-4, 96)
	if m.i >= len(m.items) {
		return ""
	}
	c := m.items[m.i]

	pill := lipgloss.NewStyle().Bold(true).Padding(0, 1).Foreground(th.P.Ink).Background(th.P.Ore).Render("sminer label")
	count := th.Muted.Render(fmt.Sprintf(" %d of %d ", m.i+1, len(m.items)))
	bar := th.Bar(float64(m.i), float64(len(m.items)), 24, th.P.Ore)
	tl := fmt.Sprintf("   %s %d  %s %d  %s %d", th.OK.Render("✓"), m.t.yes, th.Accent.Render("✗"), m.t.no, th.Faint.Render("↷"), m.t.skipped)
	header := pill + count + bar + tl

	var action strings.Builder
	for _, l := range strings.Split(c.Action, "\n") {
		action.WriteString(th.Faint.Render("▸ ") + th.Muted.Render(textutil.Truncate(l, inner-4)) + "\n")
	}
	said := lipgloss.NewStyle().Border(lipgloss.ThickBorder(), false, false, false, true).
		BorderForeground(th.P.Ember).PaddingLeft(1).Width(inner).
		Render(th.Title.Render(textutil.Truncate(c.Message, 600)))

	key := func(k, label string, col lipgloss.Style) string {
		return lipgloss.NewStyle().Bold(true).Padding(0, 1).Foreground(th.P.Ink).Background(col.GetForeground()).Render(k) + " " + th.Muted.Render(label)
	}
	keys := strings.Join([]string{
		key("y", "correction", th.OK),
		key("n", "not a correction", th.Accent),
		key("s", "skip", th.Faint),
		key("q", "quit", th.Faint),
	}, "   ")

	parts := []string{
		header, "",
		th.Heading.Render("Claude had just done"),
		strings.TrimRight(action.String(), "\n"), "",
		th.Heading.Render("Then you said"),
		said, "",
		th.Text.Render("Is this a correction? ") + th.Faint.Render("It pushes back on what Claude did, or restates a rule it broke."), "",
		keys,
	}
	if m.last != "" {
		parts = append(parts, "", m.last)
	}
	return lipgloss.NewStyle().Padding(1, 2).Render(strings.Join(parts, "\n"))
}
