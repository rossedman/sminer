package cli

import (
	"fmt"
	"image/color"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/rossedman/sminer/internal/cluster"
	"github.com/rossedman/sminer/internal/remedy"
	"github.com/rossedman/sminer/internal/report"
	"github.com/rossedman/sminer/internal/store"
	"github.com/rossedman/sminer/internal/ui"
)

// renderReport draws the report for a person at a terminal: a header,
// a row of stat tiles, a ranked table, then one card per finding. Evidence
// lives in 'finding show' and 'review'; the Markdown form keeps it for
// files and pipes.
func renderReport(th ui.Theme, r report.Report, width int) string {
	width = min(width, 100)
	sections := []string{
		reportHeader(th, r, width),
		statTiles(th, r.SignalCounts, width),
	}
	if len(r.Findings) == 0 {
		sections = append(sections, lipgloss.NewStyle().Padding(0, 2).Render(
			th.Text.Render("Nothing recurring at this threshold.")+"\n"+
				th.Muted.Render("Try a lower --min-count or a longer --since.")))
	} else {
		sections = append(sections,
			th.Rule(fmt.Sprintf("Findings (%d)", len(r.Findings)), width)+"\n\n"+summaryTable(th, r.Findings, width))
		for i, f := range r.Findings {
			sections = append(sections, findingPanel(th, i+1, f, width))
		}
	}
	if n := reportNotes(th, r); n != "" {
		sections = append(sections, n)
	}
	if len(r.Findings) > 0 {
		sections = append(sections, helpBar(th,
			[2]string{"sminer review", "walk through findings"},
			[2]string{"sminer finding show <id>", "evidence & sessions"},
			[2]string{"sminer finding accept <id> --preview", "see the patch"}))
	}
	return "\n" + strings.Join(sections, "\n\n") + "\n"
}

// reportHeader is a full-width panel: the name on the left, the scope on
// the right.
func reportHeader(th ui.Theme, r report.Report, width int) string {
	scope := "all repos"
	if r.Repo != "" {
		scope = report.ShortRepo(r.Repo)
	}
	end := r.GeneratedAt
	if end.IsZero() {
		end = time.Now()
	}
	left := th.Heading.Render("⛏  sminer report")
	right := th.Muted.Render(fmt.Sprintf("%d sessions · %s – %s · %s",
		r.Sessions, r.Since.Format("Jan 2"), end.Format("Jan 2"), scope))
	inner := width - 4 // border and padding
	gap := max(2, inner-lipgloss.Width(left)-lipgloss.Width(right))
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(th.P.Ore).
		Padding(0, 1).Width(width).
		Render(left + strings.Repeat(" ", gap) + right)
}

// signalColor gives each kind of friction its own color in the tiles.
func signalColor(th ui.Theme, typ string) color.Color {
	switch typ {
	case store.Correction, store.Rewind:
		return th.P.Ember
	case store.Interrupt, store.ToolFailure:
		return th.P.Ore
	case store.RepeatedInstruction:
		return th.P.Gem
	case store.HookBlock:
		return th.P.Violet
	}
	return th.P.Muted
}

// statTiles shows each kind of friction as a small bordered tile: the count
// big and colored, the label under it. One row fills the terminal width when
// the tiles fit; otherwise they wrap at their natural width.
func statTiles(th ui.Theme, counts map[string]int, width int) string {
	fr := report.Friction(counts)
	if len(fr) == 0 {
		return th.Muted.Render("No friction found in this window.")
	}
	// Width counts border and padding, so a tile is its label plus 4.
	widths := make([]int, len(fr))
	total := len(fr) - 1
	for i, f := range fr {
		widths[i] = max(12, lipgloss.Width(f.Label)+4)
		total += widths[i]
	}
	if total <= width {
		extra := width - total
		for i := range widths {
			widths[i] += extra / len(widths)
			if i < extra%len(widths) {
				widths[i]++
			}
		}
	}
	var tiles []string
	for i, f := range fr {
		num := lipgloss.NewStyle().Bold(true).Foreground(signalColor(th, f.Type)).Render(fmt.Sprint(f.Count))
		tiles = append(tiles, lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(th.P.Faint).
			Padding(0, 1).Width(widths[i]).Render(num+"\n"+th.Muted.Render(f.Label)))
	}
	var rows []string
	for i := 0; i < len(tiles); {
		used, j := 0, i
		for j < len(tiles) && (j == i || used+1+widths[j] <= width) {
			if j > i {
				used++
			}
			used += widths[j]
			j++
		}
		row := tiles[i:j]
		i = j
		spaced := make([]string, 0, 2*len(row))
		for j, t := range row {
			if j > 0 {
				spaced = append(spaced, " ")
			}
			spaced = append(spaced, t)
		}
		rows = append(rows, lipgloss.JoinHorizontal(lipgloss.Top, spaced...))
	}
	return lipgloss.JoinVertical(lipgloss.Left, rows...)
}

// summaryTable ranks the findings in one glance: number, fix, a bar for how
// often, and the title.
func summaryTable(th ui.Theme, fs []report.Finding, width int) string {
	top := 0
	for _, f := range fs {
		top = max(top, f.Occurrences)
	}
	titleW := max(20, width-52)
	rows := make([][]string, len(fs))
	for i, f := range fs {
		rows[i] = []string{
			fmt.Sprint(i + 1),
			th.Remedy(f.Remedy),
			th.Bar(float64(f.Occurrences), float64(top), 10, th.RemedyColor(f.Remedy)) + " " + fmt.Sprintf("%d×", f.Occurrences),
			headline(f.Title, titleW),
			f.ID,
		}
	}
	t := th.Table([]string{"#", "FIX", "HOW OFTEN", "FINDING", "ID"}, rows, func(row, col int) lipgloss.Style {
		switch col {
		case 0:
			return th.ID
		case 3:
			return th.Text
		case 4:
			return th.Faint
		}
		return lipgloss.NewStyle()
	})
	return t.String()
}

// findingPanel is one finding as a card bordered in its remedy's color.
func findingPanel(th ui.Theme, n int, f report.Finding, width int) string {
	inner := width - 4
	accent := th.RemedyColor(f.Remedy)

	// Title line: number and badge, the title, the ID on the right.
	head := th.ID.Render(fmt.Sprint(n)) + "  " + th.Badge(f.Remedy) + "  "
	id := th.Faint.Render(f.ID)
	titleW := inner - lipgloss.Width(head) - lipgloss.Width(id) - 2
	title := th.Title.Render(headline(f.Title, titleW))
	gap := max(2, inner-lipgloss.Width(head)-lipgloss.Width(title)-lipgloss.Width(id))
	lines := []string{head + title + strings.Repeat(" ", gap) + id}

	meta := fmt.Sprintf("%d× in %d sessions", f.Occurrences, f.Sessions)
	if len(f.Repos) > 1 {
		meta += fmt.Sprintf(" · %d repos", len(f.Repos))
	}
	if first, last := f.FirstSeen.Format("Jan 2"), f.LastSeen.Format("Jan 2"); first == last {
		meta += " · " + first
	} else {
		meta += " · " + first + " → " + last
	}
	metaLine := th.Muted.Render(meta)
	if f.Status == store.StatusAccepted {
		metaLine += th.Faint.Render(" · ") + th.Status(f.Status)
	}
	lines = append(lines, metaLine, "")

	// Labelled rows; values wrap under themselves, not under the label.
	const keyW = 8
	valW := inner - keyW
	row := func(k, v string) {
		key := th.Key.Width(keyW).Render(k)
		lines = append(lines, lipgloss.JoinHorizontal(lipgloss.Top, key, lipgloss.NewStyle().Width(valW).Render(v)))
	}
	row("change", panelProposal(th, f, valW))
	if f.Target != "" {
		row("where", th.Code.Render(ui.Tilde(f.Target)))
	}
	if len(f.FileKinds) > 0 {
		row("files", fileKindBars(th, f.FileKinds, accent))
	}
	if len(f.Repos) > 0 {
		row("repos", repoList(th, f.Repos, valW))
	}

	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(accent).
		Padding(0, 1).Width(width).Render(strings.Join(lines, "\n"))
}

// panelProposal is the change itself.
func panelProposal(th ui.Theme, f report.Finding, w int) string {
	p := f.Proposal
	switch f.Remedy {
	case remedy.Hook:
		s := th.Text.Render(fmt.Sprintf("block %s when %s matches ", p.Matcher, p.Field)) + th.Code.Render(p.Pattern)
		if p.Rule != "" {
			s += "\n" + th.Muted.Width(w).Render(p.Rule)
		}
		return s
	case remedy.Skill:
		s := th.Text.Render("new skill ") + th.Code.Render(p.SkillName)
		for i, st := range p.Steps {
			num := th.Faint.Render(fmt.Sprintf("%2d  ", i+1))
			s += "\n" + lipgloss.JoinHorizontal(lipgloss.Top, num, th.Text.Width(w-4).Render(st))
		}
		return s
	}
	return th.OK.Width(w).Render("+ " + p.Rule)
}

// fileKindBars charts fix-ups by file kind, one bar per kind.
func fileKindBars(th ui.Theme, kinds map[string]int, c color.Color) string {
	const shown = 5
	ks := cluster.SortKinds(kinds)
	labelW := 0
	for i, k := range ks {
		if i == shown {
			break
		}
		labelW = max(labelW, lipgloss.Width(k.Kind))
	}
	var out []string
	for i, k := range ks {
		if i == shown {
			rest := 0
			for _, r := range ks[shown:] {
				rest += r.Count
			}
			out = append(out, th.Faint.Render(fmt.Sprintf("+%d more kinds, %d edits", len(ks)-shown, rest)))
			break
		}
		out = append(out, th.Text.Width(labelW+1).Render(k.Kind)+" "+
			th.Bar(float64(k.Count), float64(ks[0].Count), 24, c)+" "+th.Muted.Render(fmt.Sprint(k.Count)))
	}
	return strings.Join(out, "\n")
}

func repoList(th ui.Theme, repos []string, w int) string {
	seen := map[string]bool{}
	var short []string
	for _, r := range repos {
		if s := report.ShortRepo(r); !seen[s] {
			seen[s] = true
			short = append(short, s)
		}
	}
	shown, more := short, 0
	if len(short) > 3 {
		shown, more = short[:3], len(short)-3
	}
	parts := make([]string, len(shown))
	for i, r := range shown {
		parts[i] = th.Text.Render(r)
	}
	if more > 0 {
		parts = append(parts, th.Faint.Render(fmt.Sprintf("+%d more", more)))
	}
	return joinWrapped(parts, th.Faint.Render(", "), w, "")
}

// reportNotes covers the sections that don't need a card: a line each.
func reportNotes(th ui.Theme, r report.Report) string {
	var notes []string
	if n := len(r.Unheeded); n > 0 {
		notes = append(notes, th.Warn.Render("⚑ ")+th.Text.Render(fmt.Sprintf("%d CLAUDE.md instruction(s) keep being ignored", n))+
			th.Muted.Render(" · sminer finding list --status all"))
	}
	if len(r.Guardrails) > 0 {
		total := 0
		for _, g := range r.Guardrails {
			total += g.Count
		}
		notes = append(notes, th.OK.Render("✓ ")+th.Text.Render(fmt.Sprintf("Existing hooks blocked %d action(s)", total)))
	}
	if n := len(r.Ignored); n > 0 {
		notes = append(notes, th.Faint.Render("· ")+th.Muted.Render(fmt.Sprintf("%d more ignored as one-off or already covered", n)))
	}
	return strings.Join(notes, "\n")
}

// helpBar lists next steps as an aligned command / what-it-does pair per
// line, like a Charm app's expanded key help.
func helpBar(th ui.Theme, items ...[2]string) string {
	cmdW := 0
	for _, it := range items {
		cmdW = max(cmdW, lipgloss.Width(it[0]))
	}
	var lines []string
	for _, it := range items {
		lines = append(lines, th.Code.Width(cmdW+2).Render(it[0])+th.Muted.Render(it[1]))
	}
	return strings.Join(lines, "\n")
}

// headline shortens a title to its first sentence and at most n characters,
// cutting at a word boundary. Titles taken from a pasted prompt run long.
func headline(s string, n int) string {
	s = strings.TrimSuffix(strings.TrimSpace(s), "...")
	if i := strings.Index(s, ". "); i > 0 {
		s = s[:i]
	}
	if len(s) <= n {
		return s
	}
	cut := strings.LastIndexByte(s[:n], ' ')
	if cut < n/2 {
		cut = n
	}
	return strings.TrimRight(s[:cut], " ,;:") + "…"
}

// joinWrapped joins styled parts with sep, breaking lines between parts so
// no part is split, and prefixes every line with lead.
func joinWrapped(parts []string, sep string, width int, lead string) string {
	var lines []string
	cur := ""
	for _, p := range parts {
		next := p
		if cur != "" {
			next = cur + sep + p
		}
		if cur != "" && lipgloss.Width(lead+next) > width {
			lines = append(lines, cur)
			next = p
		}
		cur = next
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return lead + strings.Join(lines, "\n"+lead)
}
