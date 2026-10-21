package cli

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/rossedman/sminer/internal/finding"
	"github.com/rossedman/sminer/internal/remedy"
	"github.com/rossedman/sminer/internal/report"
	"github.com/rossedman/sminer/internal/textutil"
	"github.com/rossedman/sminer/internal/ui"
)

// reviewer is what the review screen needs from the finding service.
type reviewer interface {
	List(status string) ([]report.Finding, error)
	Get(id string) (report.Finding, error)
	AcceptMany(ids []string, dir string, preview bool) ([]finding.AcceptResult, error)
	Dismiss(id, reason string) error
	Reopen(id string) error
}

var reviewFilters = []string{"open", "accepted", "dismissed", "all"}

type reviewMode int

const (
	modeCard reviewMode = iota
	modePreview
	modeDismiss
	modeHelp
)

// reviewModel is the full-screen finding browser: a list on the left, the
// selected finding (or its patch) on the right, one key per action.
type reviewModel struct {
	th     ui.Theme
	svc    reviewer
	dir    string // patch directory, "" for the configured one
	filter int    // index into reviewFilters

	items  []report.Finding
	cur    int
	top    int // first visible list row
	marked map[string]bool
	cache  map[string]report.Finding // full findings, every excerpt

	mode  reviewMode
	vp    viewport.Model
	input textinput.Model

	width, height int
	note          string // last action's outcome
	noteErr       bool

	// Session totals, reported on exit.
	written                       []string          // patch files, in order, unique
	applies                       map[string]string // patch file -> command that applies it
	accepted, dismissed, reopened int
	interrupted                   bool
	err                           error // fatal: loading failed
}

// newReviewModel returns a model that starts on the filter named by status and
// loads its findings. Any load error is left in the model's err field.
func newReviewModel(th ui.Theme, svc reviewer, dir, status string) *reviewModel {
	m := &reviewModel{th: th, svc: svc, dir: dir, marked: map[string]bool{}, cache: map[string]report.Finding{},
		applies: map[string]string{},
		width:   100, height: 30}
	for i, f := range reviewFilters {
		if f == status {
			m.filter = i
		}
	}
	m.vp = viewport.New()
	m.input = textinput.New()
	m.input.Prompt = "reason: "
	m.input.Placeholder = "why it isn't worth fixing (optional)"
	m.input.CharLimit = 200
	m.reload()
	return m
}

// Init implements tea.Model. The review screen has no startup command.
func (m *reviewModel) Init() tea.Cmd { return nil }

// reload fetches the list for the current filter, keeping the cursor on
// the same finding when it is still listed.
func (m *reviewModel) reload() {
	keep := ""
	if f, ok := m.current(); ok {
		keep = f.ID
	}
	items, err := m.svc.List(reviewFilters[m.filter])
	if err != nil {
		m.err = err
		return
	}
	m.items, m.cur = items, 0
	for i, f := range items {
		if f.ID == keep {
			m.cur = i
		}
	}
	for id := range m.marked {
		if !m.listed(id) {
			delete(m.marked, id)
		}
	}
	m.refresh()
}

// listed reports whether the finding with the given id is in the current list.
func (m *reviewModel) listed(id string) bool {
	for _, f := range m.items {
		if f.ID == id {
			return true
		}
	}
	return false
}

// current returns the selected finding, or false if the list is empty.
func (m *reviewModel) current() (report.Finding, bool) {
	if m.cur < 0 || m.cur >= len(m.items) {
		return report.Finding{}, false
	}
	return m.items[m.cur], true
}

// targets are the findings an action applies to: the marked ones, or the
// one under the cursor.
func (m *reviewModel) targets() []string {
	var ids []string
	for _, f := range m.items {
		if m.marked[f.ID] {
			ids = append(ids, f.ID)
		}
	}
	if len(ids) == 0 {
		if f, ok := m.current(); ok {
			ids = []string{f.ID}
		}
	}
	return ids
}

// Pane geometry.
func (m *reviewModel) listWidth() int { return max(30, min(52, m.width*2/5)) }

// detailWidth returns the width of the right-hand detail pane, at least 30
// columns.
func (m *reviewModel) detailWidth() int {
	return max(30, m.width-m.listWidth()-3)
}

// bodyHeight returns the number of rows available to the list and detail
// panes, at least 5.
func (m *reviewModel) bodyHeight() int { return max(5, m.height-5) }

// refresh re-renders the right pane for the cursor and mode.
func (m *reviewModel) refresh() {
	m.vp.SetWidth(m.detailWidth())
	m.vp.SetHeight(m.bodyHeight())
	f, ok := m.current()
	if !ok {
		m.vp.SetContent(m.th.Muted.Render(fmt.Sprintf("No %s findings.", reviewFilters[m.filter])) + "\n\n" +
			m.th.Faint.Render("Press tab to look at another status, or run 'sminer run' to look for more."))
		return
	}
	switch m.mode {
	case modePreview:
		ids := m.targets()
		res, err := m.svc.AcceptMany(ids, m.dir, true)
		if err != nil {
			m.vp.SetContent(m.th.Err.Render("Can't build a patch: ") + m.th.Text.Render(err.Error()))
			break
		}
		var b strings.Builder
		seen := map[string]bool{}
		for _, r := range res {
			if seen[r.Diff] {
				continue
			}
			seen[r.Diff] = true
			b.WriteString(m.th.Diff(r.Diff))
			if r.FollowUp != "" {
				b.WriteString("\n" + m.th.Muted.Render(r.FollowUp) + "\n")
			}
			b.WriteString("\n")
		}
		m.vp.SetContent(lipgloss.NewStyle().Width(m.detailWidth()).Render(strings.TrimRight(b.String(), "\n")))
	default:
		full, ok := m.cache[f.ID]
		if !ok {
			var err error
			if full, err = m.svc.Get(f.ID); err != nil {
				full = f
			}
			m.cache[f.ID] = full
		}
		m.vp.SetContent(lipgloss.NewStyle().Width(m.detailWidth()).Render(renderCard(m.th, full, m.detailWidth(), false)))
	}
	m.vp.GotoTop()
}

// say sets the status-line note to the formatted message, shown as an error
// when ok is false.
func (m *reviewModel) say(ok bool, format string, args ...any) {
	m.note, m.noteErr = fmt.Sprintf(format, args...), !ok
}

// Update implements tea.Model. It tracks window size, quits on ctrl+c and
// marks the run interrupted, and sends other keys to the handler for the
// current mode.
func (m *reviewModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.refresh()
		return m, nil
	case tea.KeyPressMsg:
		if msg.String() == "ctrl+c" {
			m.interrupted = true
			return m, tea.Quit
		}
		switch m.mode {
		case modeDismiss:
			return m.updateDismiss(msg)
		case modeHelp:
			m.mode = modeCard
			return m, nil
		}
		return m.updateBrowse(msg)
	}
	return m, nil
}

// updateDismiss handles keys while the dismiss reason is being typed. Esc
// cancels, enter dismisses every target with the typed reason and clears the
// marks, and other keys edit the reason.
func (m *reviewModel) updateDismiss(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = modeCard
		m.input.Blur()
		m.say(true, "dismiss cancelled")
		return m, nil
	case "enter":
		reason := strings.TrimSpace(m.input.Value())
		ids := m.targets()
		m.mode = modeCard
		m.input.Blur()
		m.say(true, "✗ dismissed %s", strings.Join(ids, ", "))
		for _, id := range ids {
			if err := m.svc.Dismiss(id, reason); err != nil {
				m.say(false, "dismiss %s: %v", id, err)
				break
			}
			m.dismissed++
		}
		m.marked = map[string]bool{}
		m.afterChange(ids)
		return m, nil
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

// updateBrowse handles keys in the browse and patch-preview modes: moving,
// scrolling, marking, previewing, accepting, dismissing, reopening, switching
// the status filter, and quitting.
func (m *reviewModel) updateBrowse(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "esc":
		if m.mode == modePreview && msg.String() == "esc" {
			m.mode = modeCard
			m.refresh()
			return m, nil
		}
		return m, tea.Quit
	case "up", "k":
		m.move(-1)
	case "down", "j":
		m.move(1)
	case "home", "g":
		m.move(-len(m.items))
	case "end", "G":
		m.move(len(m.items))
	case "pgdown", "ctrl+d", "J":
		m.vp.HalfPageDown()
	case "pgup", "ctrl+u", "K":
		m.vp.HalfPageUp()
	case "space", "x":
		if f, ok := m.current(); ok {
			if m.marked[f.ID] {
				delete(m.marked, f.ID)
			} else {
				m.marked[f.ID] = true
			}
			m.move(1)
			if m.mode == modePreview {
				m.refresh()
			}
		}
	case "p", "enter":
		if m.mode == modePreview {
			m.mode = modeCard
		} else if len(m.items) > 0 {
			m.mode = modePreview
		}
		m.refresh()
	case "a":
		m.accept()
	case "d":
		if len(m.items) > 0 {
			m.mode = modeDismiss
			m.input.Reset()
			return m, m.input.Focus()
		}
	case "r":
		ids := m.targets()
		for _, id := range ids {
			if err := m.svc.Reopen(id); err != nil {
				m.say(false, "reopen %s: %v", id, err)
				return m, nil
			}
			m.reopened++
		}
		if len(ids) > 0 {
			m.say(true, "○ reopened %s", strings.Join(ids, ", "))
			m.marked = map[string]bool{}
			m.afterChange(ids)
		}
	case "tab", "f":
		m.filter = (m.filter + 1) % len(reviewFilters)
		m.mode = modeCard
		m.reload()
	case "shift+tab", "F":
		m.filter = (m.filter + len(reviewFilters) - 1) % len(reviewFilters)
		m.mode = modeCard
		m.reload()
	case "?":
		m.mode = modeHelp
	}
	return m, nil
}

// move shifts the selection by d rows, clamped to the list, and refreshes the
// detail pane. While previewing a patch for marked findings the preview is
// kept.
func (m *reviewModel) move(d int) {
	if len(m.items) == 0 {
		return
	}
	n := min(max(m.cur+d, 0), len(m.items)-1)
	if n == m.cur {
		return
	}
	m.cur = n
	if m.mode == modePreview && len(m.marked) == 0 {
		m.refresh()
	} else if m.mode != modePreview {
		m.refresh()
	}
}

// accept writes patches for the target findings and records them as adopted.
// Paths and apply commands are kept for the exit summary, and the marks are
// cleared. On error it only sets the status note.
func (m *reviewModel) accept() {
	ids := m.targets()
	if len(ids) == 0 {
		return
	}
	res, err := m.svc.AcceptMany(ids, m.dir, false)
	if err != nil {
		m.say(false, "%v", err)
		return
	}
	m.accepted += len(ids)
	var paths []string
	for _, r := range res {
		if !contains(m.written, r.PatchPath) {
			m.written = append(m.written, r.PatchPath)
		}
		m.applies[r.PatchPath] = r.Apply
		if !contains(paths, r.PatchPath) {
			paths = append(paths, r.PatchPath)
		}
	}
	m.say(true, "✓ accepted %s · wrote %s", strings.Join(ids, ", "), tildeIn(strings.Join(paths, ", ")))
	m.marked = map[string]bool{}
	m.mode = modeCard
	m.afterChange(ids)
}

// afterChange updates the changed findings in place so they stay visible
// with their new status until the filter is reloaded.
func (m *reviewModel) afterChange(ids []string) {
	for _, id := range ids {
		delete(m.cache, id)
		if f, err := m.svc.Get(id); err == nil {
			for i := range m.items {
				if m.items[i].ID == id {
					m.items[i] = f
				}
			}
			m.cache[id] = f
		}
	}
	m.refresh()
}

// contains reports whether s is in ss.
func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// View implements tea.Model, rendering the screen on the alternate screen.
func (m *reviewModel) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	return v
}

// render draws the help screen in help mode, otherwise the header, the list
// and detail panes side by side, and the footer.
func (m *reviewModel) render() string {
	th := m.th
	if m.mode == modeHelp {
		return m.renderHelp()
	}
	header := m.renderHeader()
	list := m.renderList()
	sep := th.Faint.Render(strings.TrimRight(strings.Repeat("│\n", m.bodyHeight()), "\n"))
	body := lipgloss.JoinHorizontal(lipgloss.Top, list, " ", sep, " ", m.vp.View())
	return lipgloss.JoinVertical(lipgloss.Left, header, "", body, "", m.renderFooter())
}

// renderHeader draws the title, the status tabs with the active one
// underlined, the number of findings and, if any are marked, how many.
func (m *reviewModel) renderHeader() string {
	th := m.th
	pill := lipgloss.NewStyle().Bold(true).Padding(0, 1).Foreground(th.P.Ink).Background(th.P.Ore).Render("sminer review")
	var tabs []string
	for i, f := range reviewFilters {
		if i == m.filter {
			tabs = append(tabs, lipgloss.NewStyle().Bold(true).Foreground(th.P.Text).Underline(true).Render(f))
		} else {
			tabs = append(tabs, th.Faint.Render(f))
		}
	}
	right := th.Muted.Render(fmt.Sprintf("%d findings", len(m.items)))
	if n := len(m.marked); n > 0 {
		right = th.Warn.Render(fmt.Sprintf("%d marked", n)) + th.Faint.Render(" · ") + right
	}
	return pill + "  " + strings.Join(tabs, th.Faint.Render("  ")) + "    " + right
}

// renderList draws the visible window of the findings list, scrolling so the
// selection stays in view. Each row shows the mark or cursor, status or remedy
// glyph, id, score and a truncated title.
func (m *reviewModel) renderList() string {
	th := m.th
	w, h := m.listWidth(), m.bodyHeight()
	if m.cur < m.top {
		m.top = m.cur
	}
	if m.cur >= m.top+h {
		m.top = m.cur - h + 1
	}
	var rows []string
	for i := m.top; i < len(m.items) && i < m.top+h; i++ {
		f := m.items[i]
		mark := "  "
		switch {
		case m.marked[f.ID]:
			mark = th.Warn.Render("● ")
		case i == m.cur:
			mark = th.Heading.Render("▸ ")
		}
		glyph := lipgloss.NewStyle().Foreground(th.RemedyColor(f.Remedy)).Render("■")
		switch f.Status {
		case "accepted":
			glyph = th.OK.Render("✓")
		case "dismissed":
			glyph = th.Faint.Render("✗")
		}
		score := fmt.Sprintf("%4.0f", f.Score)
		titleW := max(8, w-lipgloss.Width(mark)-2-8-5-1)
		title := textutil.Truncate(textutil.OneLine(f.Title), titleW)
		var row string
		if i == m.cur {
			sel := lipgloss.NewStyle().Background(th.P.Surface)
			row = mark + glyph + sel.Render(" "+th.ID.Render(f.ID)+" "+th.Muted.Render(score)+" "+th.Title.Render(title))
			row = row + sel.Render(strings.Repeat(" ", max(0, w-lipgloss.Width(row))))
		} else {
			row = mark + glyph + " " + th.Faint.Render(f.ID) + " " + th.Faint.Render(score) + " " + th.Text.Render(title)
		}
		rows = append(rows, row)
	}
	if len(m.items) == 0 {
		rows = append(rows, th.Faint.Render("  nothing here"))
	}
	return lipgloss.NewStyle().Width(w).Height(h).MaxHeight(h).Render(strings.Join(rows, "\n"))
}

// renderFooter draws the status line and the key legend. The status line shows
// the dismiss prompt, the latest note, a patch-preview notice or a hint, in
// that order of priority. Both lines are truncated to the width.
func (m *reviewModel) renderFooter() string {
	th := m.th
	var status string
	switch {
	case m.mode == modeDismiss:
		status = th.Warn.Render("Dismiss "+strings.Join(m.targets(), ", ")) + "  " + m.input.View()
	case m.note != "" && m.noteErr:
		status = th.Err.Render("✗ " + m.note)
	case m.note != "":
		status = th.OK.Render(m.note)
	case m.mode == modePreview:
		status = th.Muted.Render("Previewing the patch for " + strings.Join(m.targets(), ", ") + ". Nothing is written until you accept.")
	default:
		status = th.Faint.Render(m.hint())
	}
	k := func(key, label string) string {
		return th.Heading.Render(key) + " " + th.Faint.Render(label)
	}
	keys := []string{k("↑↓", "move"), k("space", "mark"), k("p", "patch"), k("a", "accept"), k("d", "dismiss"),
		k("r", "reopen"), k("tab", "status"), k("?", "help"), k("q", "quit")}
	if m.mode == modeDismiss {
		keys = []string{k("enter", "dismiss"), k("esc", "cancel")}
	}
	return ansi.Truncate(status, m.width-1, "…") + "\n" + ansi.Truncate(strings.Join(keys, "  "), m.width-1, "…")
}

// hint suggests the obvious next step for the finding under the cursor.
func (m *reviewModel) hint() string {
	f, ok := m.current()
	if !ok {
		return ""
	}
	switch {
	case f.Remedy == remedy.Ignore:
		return "Ignored by routing: " + textutil.Truncate(f.Proposal.Why, 120)
	case f.Status == "accepted":
		return "Accepted. Apply its patch, or press r to reopen it."
	case f.Status == "dismissed":
		return "Dismissed. Press r to reopen it."
	}
	return "Press p to see the patch, a to accept it, d to dismiss."
}

// renderHelp draws the screen listing every key.
func (m *reviewModel) renderHelp() string {
	th := m.th
	rows := [][2]string{
		{"↑ ↓  j k", "move through findings"},
		{"g G", "first, last"},
		{"J K  pgup pgdn", "scroll the right pane"},
		{"space  x", "mark a finding; actions apply to every marked one"},
		{"p  enter", "show the patch accepting would write (nothing is written)"},
		{"a", "accept: write the patch to the patch directory and record the adoption"},
		{"d", "dismiss, with an optional reason"},
		{"r", "reopen an accepted or dismissed finding"},
		{"tab  shift+tab", "switch between open, accepted, dismissed and all"},
		{"q  esc", "quit; the patch paths written go to stdout"},
	}
	var b strings.Builder
	b.WriteString(th.Heading.Render("sminer review") + "\n\n")
	for _, r := range rows {
		fmt.Fprintf(&b, "  %s  %s\n", th.Code.Render(fmt.Sprintf("%-16s", r[0])), th.Text.Render(r[1]))
	}
	b.WriteString("\n" + th.Muted.Render("CLAUDE.md lines marked together for the same file share one patch.") + "\n")
	b.WriteString(th.Muted.Render("sminer never applies a patch: apply it yourself with the command it shows.") + "\n\n")
	b.WriteString(th.Faint.Render("Press any key to go back."))
	return lipgloss.NewStyle().Padding(1, 2).Render(b.String())
}
