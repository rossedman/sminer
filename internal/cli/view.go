package cli

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/term"

	"github.com/rossedman/sminer/internal/finding"
	"github.com/rossedman/sminer/internal/remedy"
	"github.com/rossedman/sminer/internal/report"
	"github.com/rossedman/sminer/internal/textutil"
	"github.com/rossedman/sminer/internal/ui"
)

// Terminal renderings of findings, trends and settings. Each has a plain
// counterpart used when stdout is not a terminal.

// width is the terminal's width, kept to a readable measure.
func (a *app) width() int {
	w := 100
	if f, ok := a.opts.Stdout.(interface{ Fd() uintptr }); ok {
		if tw, _, err := term.GetSize(f.Fd()); err == nil && tw > 0 {
			w = tw
		}
	}
	return min(w, 120)
}

func (a *app) findingTable(fs []report.Finding) error {
	th := a.theme()
	top := 0.0
	for _, f := range fs {
		top = max(top, f.Score)
	}
	// Everything but the title is fixed width; the title gets the rest.
	titleW := max(24, a.width()-58)
	rows := make([][]string, len(fs))
	for i, f := range fs {
		rows[i] = []string{
			f.ID,
			th.Status(f.Status),
			th.Remedy(f.Remedy),
			th.Bar(f.Score, top, 8, th.RemedyColor(f.Remedy)) + " " + fmt.Sprintf("%-4s", strconv.FormatFloat(f.Score, 'f', 0, 64)),
			fmt.Sprintf("%d×", f.Occurrences),
			textutil.Truncate(textutil.OneLine(f.Title), titleW),
		}
	}
	t := th.Table([]string{"ID", "STATUS", "REMEDY", "SCORE", "SEEN", "TITLE"}, rows, func(row, col int) lipgloss.Style {
		switch col {
		case 0:
			return th.ID
		case 4:
			return th.Muted
		case 5:
			return th.Text
		}
		return lipgloss.NewStyle()
	})
	_, err := fmt.Fprintln(a.out, a.fit(t))
	if err == nil && len(fs) > 0 {
		a.info("\n%s", style(th.Faint, fmt.Sprintf("%d finding(s) · inspect one with 'sminer finding show %s'", len(fs), fs[0].ID)))
	}
	return err
}

func (a *app) findingCard(f report.Finding) string {
	return "\n" + renderCard(a.theme(), f, a.width(), true)
}

// renderCard draws a finding as a card at most width cells wide; hint adds
// the next-step line for open findings.
func renderCard(th ui.Theme, f report.Finding, width int, hint bool) string {
	var b strings.Builder

	b.WriteString(th.Badge(f.Remedy) + "  " + style(th.ID, f.ID) + "  " + style(th.Title, f.Title) + "\n")
	facts := []string{th.Status(f.Status), fmt.Sprintf("score %.0f", f.Score),
		fmt.Sprintf("%d occurrences in %d sessions", f.Occurrences, f.Sessions)}
	if f.AdoptedAt != nil {
		facts = append(facts, "adopted "+f.AdoptedAt.Format("2006-01-02"))
	}
	b.WriteString(strings.Join(facts, style(th.Faint, "  ·  ")) + "\n")
	kv := func(k, v string) {
		if v != "" {
			b.WriteString(style(th.Key, fmt.Sprintf("%-7s", k)) + " " + v + "\n")
		}
	}
	b.WriteString("\n")
	kv("repos", strings.Join(tildeAll(f.Repos), ", "))
	kv("target", style(th.Code, ui.Tilde(f.Target)))
	kv("first", f.FirstSeen.Format("2006-01-02"))
	kv("last", f.LastSeen.Format("2006-01-02"))

	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(th.RemedyColor(f.Remedy)).
		Padding(0, 1).Width(min(width, 96))
	b.WriteString("\n" + box.Render(renderProposal(th, f)) + "\n")

	if len(f.Excerpts) > 0 {
		b.WriteString("\n" + th.Rule(fmt.Sprintf("Evidence (%d)", len(f.Excerpts)), min(width, 96)) + "\n\n")
		quote := lipgloss.NewStyle().Border(lipgloss.ThickBorder(), false, false, false, true).
			BorderForeground(th.P.Ember).PaddingLeft(1).Width(min(width, 96) - 2)
		for _, e := range f.Excerpts {
			head := style(th.Muted, e.TS.Format("2006-01-02 15:04")) + "  " + style(th.Accent, e.Type)
			if e.Repo != "" {
				head += "  " + style(th.Faint, e.Repo)
			}
			b.WriteString(head + "\n")
			b.WriteString(quote.Render(style(th.Text, e.Text)) + "\n")
			b.WriteString(style(th.Faint, "  ↳ "+e.Resume) + "\n\n")
		}
	}
	if hint && f.Status == "open" && f.Remedy != remedy.Ignore {
		b.WriteString(style(th.Faint, fmt.Sprintf("Preview the patch with 'sminer finding accept %s --preview'.", f.ID)) + "\n")
	}
	return b.String()
}

func renderProposal(th ui.Theme, f report.Finding) string {
	p := f.Proposal
	label := style(th.Heading, "Proposal")
	switch f.Remedy {
	case remedy.Hook:
		s := label + "  " + style(th.Muted, "PreToolUse hook") + "\n\n"
		if u := p.Unheeded; u != nil {
			s += style(th.Warn, fmt.Sprintf("⚑ Escalated: %s was ignored %d times since that file changed.", u.Where, u.Since)) + "\n\n"
		}
		s += style(th.Key, "on     ") + " " + style(th.Code, p.Matcher) + "\n"
		s += style(th.Key, "block  ") + " " + style(th.Code, p.Field) + " matching " + style(th.Code, p.Pattern) + "\n"
		s += style(th.Key, "tell   ") + " " + style(th.Text, p.Rule)
		return s
	case remedy.Skill:
		s := label + "  " + style(th.Muted, "skill ") + style(th.Code, p.SkillName) + "\n\n" + style(th.Text, p.Description)
		for i, st := range p.Steps {
			s += fmt.Sprintf("\n%s %s", style(th.Muted, fmt.Sprintf("%2d.", i+1)), st)
		}
		return s
	case remedy.Ignore:
		return style(th.Muted, "Ignored") + "\n\n" + style(th.Text, p.Why)
	}
	return label + "  " + style(th.Muted, "CLAUDE.md line") + "\n\n" + style(th.OK, "+ - "+p.Rule)
}

// acceptedNote reports one written patch on stderr.
func (a *app) acceptedNote(group []string, res finding.AcceptResult) {
	if a.g.quiet {
		return
	}
	a.spin.pause()
	th := a.theme()
	fmt.Fprintf(a.err, "%s accepted %s\n", style(th.OK, "✓"), style(th.ID, strings.Join(group, ", ")))
	fmt.Fprintf(a.err, "  %s %s\n", style(th.Key, "apply with:"), style(th.Code, res.Apply))
	if res.FollowUp != "" {
		for _, l := range strings.Split(res.FollowUp, "\n") {
			fmt.Fprintf(a.err, "  %s\n", style(th.Muted, l))
		}
	}
	fmt.Fprintf(a.err, "  %s %s\n", style(th.Key, "undo with:"), style(th.Faint, "sminer finding reopen "+strings.Join(group, " ")))
}

func (a *app) trendChart(tr report.Trend) string {
	th := a.theme()
	var b strings.Builder
	b.WriteString("\n" + style(th.ID, tr.ID) + "  " + style(th.Title, tr.Title) + "\n\n")
	hi := 0
	for _, w := range tr.Weeks {
		hi = max(hi, w.Count)
	}
	adoptWeek := time.Time{}
	if tr.AdoptedAt != nil {
		adoptWeek = report.WeekStart(*tr.AdoptedAt)
	}
	for _, w := range tr.Weeks {
		c := th.P.Ember
		if tr.AdoptedAt != nil && !w.Start.Before(adoptWeek) {
			c = th.P.Gem
		}
		mark := ""
		if tr.AdoptedAt != nil && w.Start.Equal(adoptWeek) {
			mark = style(th.OK, "  ◀ adopted "+tr.AdoptedAt.Format("Jan 2"))
		}
		fmt.Fprintf(&b, "  %s %s %s%s\n", style(th.Muted, w.Start.Format("Jan 02")),
			th.Bar(float64(w.Count), float64(hi), 30, c), style(th.Text, fmt.Sprintf("%2d", w.Count)), mark)
	}
	b.WriteString("\n")
	if tr.AdoptedAt == nil {
		b.WriteString(style(th.Muted, fmt.Sprintf("Not adopted · %.2f a week", tr.BeforeRate)) + "\n")
		return b.String()
	}
	change := ""
	if tr.BeforeRate > 0 {
		pct := (tr.AfterRate - tr.BeforeRate) / tr.BeforeRate * 100
		st := th.OK
		if pct >= 0 {
			st = th.Err
		}
		change = "  " + style(st.Bold(true), fmt.Sprintf("%+.0f%%", pct))
	}
	fmt.Fprintf(&b, "%s %s  →  %s %s over %.1f weeks%s\n",
		style(th.Muted, "before"), style(th.Err, fmt.Sprintf("%.2f/wk", tr.BeforeRate)),
		style(th.Muted, "after"), style(th.OK, fmt.Sprintf("%.2f/wk", tr.AfterRate)), tr.AfterWeeks, change)
	return b.String()
}

func (a *app) trendTable(trs []report.Trend) error {
	th := a.theme()
	rows := make([][]string, len(trs))
	for i, tr := range trs {
		ws := tr.Weeks
		if len(ws) > 24 {
			ws = ws[len(ws)-24:]
		}
		counts := make([]int, len(ws))
		split := -1
		for j, w := range ws {
			counts[j] = w.Count
			if split < 0 && tr.AdoptedAt != nil && !w.Start.Before(report.WeekStart(*tr.AdoptedAt)) {
				split = j
			}
		}
		after := style(th.Faint, "—")
		if tr.AdoptedAt != nil {
			after = style(th.OK, strconv.FormatFloat(tr.AfterRate, 'f', 2, 64))
		}
		rows[i] = []string{tr.ID, style(th.Err, strconv.FormatFloat(tr.BeforeRate, 'f', 2, 64)), after,
			th.Spark(counts, split, th.P.Ember, th.P.Gem), textutil.Truncate(textutil.OneLine(tr.Title), max(24, a.width()-60))}
	}
	t := th.Table([]string{"ID", "BEFORE/WK", "AFTER/WK", "WEEKLY", "TITLE"}, rows, func(row, col int) lipgloss.Style {
		if col == 0 {
			return th.ID
		}
		return lipgloss.NewStyle()
	})
	_, err := fmt.Fprintln(a.out, a.fit(t))
	return err
}

// sourceStyle colors where a setting came from.
func sourceStyle(th ui.Theme, source string) lipgloss.Style {
	switch {
	case strings.HasPrefix(source, "flag"):
		return th.Accent
	case strings.HasPrefix(source, "env"):
		return th.OK
	case strings.HasPrefix(source, "file"):
		return th.Warn
	}
	return th.Faint
}

func tildeAll(ps []string) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = ui.Tilde(p)
	}
	return out
}

// tildeIn shortens every home-directory path in s.
func tildeIn(s string) string {
	home, err := os.UserHomeDir()
	if err != nil || len(home) < 2 {
		return s
	}
	return strings.ReplaceAll(s, home+string(os.PathSeparator), "~"+string(os.PathSeparator))
}
