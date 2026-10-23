package cli

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/rossedman/sminer/internal/cluster"
	"github.com/rossedman/sminer/internal/remedy"
	"github.com/rossedman/sminer/internal/report"
	"github.com/rossedman/sminer/internal/ui"
)

// renderReport draws the report for a person at a terminal: an overview
// that fits on a screen. Evidence lives in 'finding show' and 'review';
// the Markdown form keeps it for files and pipes.
func renderReport(th ui.Theme, r report.Report, width int) string {
	width = min(width, 100)
	const indent = 4
	body := width - indent
	pad := strings.Repeat(" ", indent)
	wrap := func(st lipgloss.Style, s string, w int) string {
		return st.Width(w).Render(s)
	}
	indentAll := func(s string) string {
		return pad + strings.ReplaceAll(s, "\n", "\n"+pad)
	}

	var b strings.Builder
	scope := "all repos"
	if r.Repo != "" {
		scope = report.ShortRepo(r.Repo)
	}
	fmt.Fprintf(&b, "\n%s %s\n\n", th.Heading.Render("sminer"),
		th.Muted.Render(fmt.Sprintf("· %d sessions since %s · %s", r.Sessions, r.Since.Format("Jan 2"), scope)))

	if fr := report.Friction(r.SignalCounts); len(fr) > 0 {
		var parts []string
		for _, f := range fr {
			parts = append(parts, th.Title.Render(fmt.Sprint(f.Count))+" "+th.Muted.Render(f.Label))
		}
		b.WriteString(joinWrapped(parts, th.Faint.Render(" · "), width-2, "  ") + "\n\n")
	}

	switch n := len(r.Findings); n {
	case 0:
		b.WriteString(th.Text.Render("Nothing recurring at this threshold.") + " " +
			th.Muted.Render("Try a lower --min-count or a longer --since.") + "\n")
	case 1:
		b.WriteString(th.Title.Render("1 thing worth fixing") + "\n\n")
	default:
		b.WriteString(th.Title.Render(fmt.Sprintf("%d things worth fixing", n)) + "\n\n")
	}

	for i, f := range r.Findings {
		// Title, hanging under its number.
		num := th.ID.Render(fmt.Sprintf("%2d", i+1)) + "  "
		title := wrap(th.Title, headline(f.Title, min(76, body)), body)
		b.WriteString(num + strings.ReplaceAll(title, "\n", "\n"+pad) + "\n")

		// How often, with the ID pushed to the right edge.
		stats := fmt.Sprintf("%d× · %d sessions", f.Occurrences, f.Sessions)
		if len(f.Repos) > 1 {
			stats += fmt.Sprintf(" · %d repos", len(f.Repos))
		} else if len(f.Repos) == 1 {
			stats += " · " + report.ShortRepo(f.Repos[0])
		}
		if f.Status == "accepted" {
			stats += " · accepted"
		}
		left := th.Muted.Render(stats)
		gap := max(2, body-lipgloss.Width(left)-lipgloss.Width(f.ID))
		b.WriteString(pad + left + strings.Repeat(" ", gap) + th.Faint.Render(f.ID) + "\n")

		// The fix and where it goes.
		fix := lipgloss.NewStyle().Foreground(th.RemedyColor(f.Remedy)).Render(fixLabel(f.Remedy))
		if f.Target != "" {
			fix += th.Faint.Render(" → ") + th.Code.Render(ui.Tilde(f.Target))
		}
		b.WriteString(pad + fix + "\n")

		// The proposal itself, behind a bar in the remedy's color.
		bar := lipgloss.NewStyle().Border(lipgloss.ThickBorder(), false, false, false, true).
			BorderForeground(th.RemedyColor(f.Remedy)).PaddingLeft(1).Width(min(body, 84))
		b.WriteString(indentAll(bar.Render(overviewProposal(th, f))) + "\n")

		if len(f.FileKinds) > 0 {
			var parts []string
			for _, k := range cluster.SortKinds(f.FileKinds) {
				parts = append(parts, th.Text.Render(k.Kind)+" "+th.Muted.Render(fmt.Sprint(k.Count)))
			}
			if len(parts) > 5 {
				parts = append(parts[:5], th.Muted.Render(fmt.Sprintf("+%d more", len(parts)-5)))
			}
			b.WriteString(joinWrapped(parts, th.Faint.Render(" · "), body, pad) + "\n")
		}
		b.WriteString("\n")
	}

	// Everything else is a line each.
	var notes []string
	if n := len(r.Unheeded); n > 0 {
		notes = append(notes, th.Warn.Render(fmt.Sprintf("%d CLAUDE.md instruction(s) keep being ignored", n))+
			th.Muted.Render(" · see sminer report --json, section unheeded"))
	}
	if len(r.Guardrails) > 0 {
		total := 0
		for _, g := range r.Guardrails {
			total += g.Count
		}
		notes = append(notes, th.Muted.Render(fmt.Sprintf("Existing hooks blocked %d action(s) in this window", total)))
	}
	if n := len(r.Ignored); n > 0 {
		notes = append(notes, th.Muted.Render(fmt.Sprintf("%d more ignored as one-off or already covered", n)))
	}
	for _, n := range notes {
		b.WriteString(n + "\n")
	}
	if len(notes) > 0 {
		b.WriteString("\n")
	}

	if len(r.Findings) > 0 {
		hint := func(cmd, what string) {
			b.WriteString(th.Code.Render(fmt.Sprintf("%-24s", cmd)) + th.Muted.Render(what) + "\n")
		}
		hint("sminer review", "go through them one by one")
		hint("sminer finding show ID", "evidence and sessions")
	}
	return b.String()
}

// fixLabel returns the display name for a remedy type, or the type itself if
// it has no special name.
func fixLabel(rem string) string {
	switch rem {
	case remedy.Hook:
		return "New hook"
	case remedy.Skill:
		return "New skill"
	case remedy.ClaudeMD:
		return "CLAUDE.md line"
	}
	return rem
}

// overviewProposal is the change itself, short enough to read at a glance.
func overviewProposal(th ui.Theme, f report.Finding) string {
	p := f.Proposal
	switch f.Remedy {
	case remedy.Hook:
		s := th.Text.Render(fmt.Sprintf("Block %s when %s matches ", p.Matcher, p.Field)) + th.Code.Render(p.Pattern)
		if p.Rule != "" {
			s += "\n" + th.Muted.Render(p.Rule)
		}
		return s
	case remedy.Skill:
		if len(p.Steps) == 0 {
			return th.Text.Render(p.Description)
		}
		var steps []string
		for _, st := range p.Steps {
			steps = append(steps, th.Text.Render(lowerFirst(st)))
		}
		return strings.Join(steps, th.Faint.Render(" → "))
	}
	return th.OK.Render("+ " + p.Rule)
}

// lowerFirst lowercases the first letter of s so it can follow a lead-in.
// Words that start with two capitals, such as acronyms, are left alone.
func lowerFirst(s string) string {
	if s == "" || (len(s) > 1 && s[1] >= 'A' && s[1] <= 'Z') {
		return s // acronyms stay as they are
	}
	return strings.ToLower(s[:1]) + s[1:]
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
