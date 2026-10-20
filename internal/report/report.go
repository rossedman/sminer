// Package report renders findings as Markdown and JSON, and computes the
// per-finding trend used to show whether an adopted fix worked.
package report

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/rossedman/sminer/internal/cluster"
	"github.com/rossedman/sminer/internal/remedy"
	"github.com/rossedman/sminer/internal/store"
	"github.com/rossedman/sminer/internal/textutil"
)

// Options select what a report covers.
type Options struct {
	Since    time.Time
	Repo     string
	MinCount int
	Now      time.Time
	// MaxExcerpts per finding (default 2).
	MaxExcerpts int
}

// Excerpt is one redacted occurrence.
type Excerpt struct {
	Type      string    `json:"type"`
	Text      string    `json:"text"`
	Repo      string    `json:"repo"`
	SessionID string    `json:"session_id"`
	Resume    string    `json:"resume"`
	TS        time.Time `json:"ts"`
}

// Finding is the report view of one finding.
type Finding struct {
	ID          string          `json:"id"`
	Title       string          `json:"title"`
	Family      string          `json:"family"`
	Remedy      string          `json:"remedy"`
	Status      string          `json:"status"`
	Target      string          `json:"target,omitempty"`
	CoveredBy   string          `json:"covered_by,omitempty"`
	Proposal    remedy.Proposal `json:"proposal"`
	Score       float64         `json:"score"`
	Occurrences int             `json:"occurrences"`
	Sessions    int             `json:"sessions"`
	Repos       []string        `json:"repos"`
	FirstSeen   time.Time       `json:"first_seen"`
	LastSeen    time.Time       `json:"last_seen"`
	ByType      map[string]int  `json:"by_type"`
	FileKinds   map[string]int  `json:"file_kinds,omitempty"`
	AdoptedAt   *time.Time      `json:"adopted_at,omitempty"`
	Excerpts    []Excerpt       `json:"excerpts"`
}

// GuardrailCount is how often one hook fired.
type GuardrailCount struct {
	Hook  string `json:"hook"`
	Count int    `json:"count"`
}

// Report is everything one run outputs.
type Report struct {
	GeneratedAt  time.Time      `json:"generated_at"`
	Since        time.Time      `json:"since"`
	Repo         string         `json:"repo,omitempty"`
	Sessions     int            `json:"sessions"`
	SignalCounts map[string]int `json:"signal_counts"`
	Findings     []Finding      `json:"findings"`
	Ignored      []Finding      `json:"ignored"`
	// Unheeded are CLAUDE.md instructions Claude keeps not following and no
	// hook could be derived for. They are not in Ignored.
	Unheeded     []Finding        `json:"unheeded"`
	Guardrails   []GuardrailCount `json:"guardrails"`
	UnknownTypes map[string]int   `json:"unknown_event_types,omitempty"`
}

// Build assembles a report from the database.
func Build(st *store.Store, opt Options) (Report, error) {
	if opt.MinCount <= 0 {
		opt.MinCount = 3
	}
	if opt.MaxExcerpts <= 0 {
		opt.MaxExcerpts = 2
	}
	if opt.Now.IsZero() {
		opt.Now = time.Now()
	}
	r := Report{GeneratedAt: opt.Now.UTC(), Since: opt.Since, Repo: opt.Repo, SignalCounts: map[string]int{}, UnknownTypes: map[string]int{}}
	sessions, err := st.Sessions()
	if err != nil {
		return r, err
	}

	where, args := scope(opt)
	sigs, err := st.QuerySignals(where+` AND (confirmed IS NULL OR confirmed=1)`, args...)
	if err != nil {
		return r, err
	}
	seenSess := map[string]bool{}
	hooks := map[string]int{}
	for _, s := range sigs {
		r.SignalCounts[s.Type]++
		seenSess[s.SessionID] = true
		if s.Type == store.HookBlock {
			hooks[s.Key]++
		}
	}
	// Sessions in scope, not just those with signals.
	q := `SELECT COUNT(*) FROM sessions WHERE ended_at >= ?`
	qa := []any{store.Ms(opt.Since)}
	if opt.Repo != "" {
		q += ` AND repo=?`
		qa = append(qa, opt.Repo)
	}
	_ = st.DB.QueryRow(q, qa...).Scan(&r.Sessions)
	for h, c := range hooks {
		r.Guardrails = append(r.Guardrails, GuardrailCount{Hook: h, Count: c})
	}
	sort.Slice(r.Guardrails, func(i, j int) bool {
		if r.Guardrails[i].Count != r.Guardrails[j].Count {
			return r.Guardrails[i].Count > r.Guardrails[j].Count
		}
		return r.Guardrails[i].Hook < r.Guardrails[j].Hook
	})

	rows, err := st.DB.Query(`SELECT type, count FROM event_types WHERE known=0`)
	if err == nil {
		for rows.Next() {
			var t string
			var c int
			if rows.Scan(&t, &c) == nil {
				r.UnknownTypes[t] = c
			}
		}
		rows.Close()
	}

	inScope := map[string]store.Signal{}
	for _, s := range sigs {
		inScope[s.ID] = s
	}
	findings, err := st.QueryFindings(`status <> ?`, store.StatusDismissed)
	if err != nil {
		return r, err
	}
	for _, f := range findings {
		linked, err := st.FindingSignals(f.ID)
		if err != nil {
			return r, err
		}
		var ss []store.Signal
		for _, s := range linked {
			if _, ok := inScope[s.ID]; ok {
				ss = append(ss, s)
			}
		}
		if len(ss) < opt.MinCount {
			continue
		}
		v := View(f, ss, sessions, opt.MaxExcerpts)
		if at, ok := adoptedAt(st, f.ID); ok {
			v.AdoptedAt = &at
		}
		if f.RemedyType == remedy.Ignore && v.Proposal.Unheeded != nil {
			r.Unheeded = append(r.Unheeded, v)
		} else if f.RemedyType == remedy.Ignore {
			r.Ignored = append(r.Ignored, v)
		} else {
			r.Findings = append(r.Findings, v)
		}
	}
	rank := func(fs []Finding) {
		sort.SliceStable(fs, func(i, j int) bool {
			if fs[i].Score != fs[j].Score {
				return fs[i].Score > fs[j].Score
			}
			return fs[i].ID < fs[j].ID
		})
	}
	rank(r.Findings)
	rank(r.Ignored)
	rank(r.Unheeded)
	return r, nil
}

func scope(opt Options) (string, []any) {
	where := `ts >= ?`
	args := []any{store.Ms(opt.Since)}
	if opt.Repo != "" {
		where += ` AND repo = ?`
		args = append(args, opt.Repo)
	}
	return where, args
}

func adoptedAt(st *store.Store, id string) (time.Time, bool) {
	var ms int64
	if err := st.DB.QueryRow(`SELECT adopted_at FROM adoptions WHERE finding_id=?`, id).Scan(&ms); err != nil {
		return time.Time{}, false
	}
	return store.FromMs(ms), true
}

// View builds the report view of a finding from its in-scope signals.
// Score is count × weight × repos: summed weights times distinct repos.
func View(f store.Finding, sigs []store.Signal, sessions map[string]store.Session, maxExcerpts int) Finding {
	v := Finding{
		ID: f.ID, Title: f.Title, Family: f.Family, Remedy: f.RemedyType, Status: f.Status,
		Target: f.Target, CoveredBy: f.CoveredBy, Proposal: remedy.DecodeProposal(f.Proposal),
		ByType: map[string]int{}, Occurrences: len(sigs),
	}
	sess := map[string]bool{}
	repos := map[string]bool{}
	weight := 0
	for _, s := range sigs {
		weight += s.Weight
		sess[s.SessionID] = true
		if s.Repo != "" {
			repos[s.Repo] = true
		}
		v.ByType[s.Type]++
		if s.Type == store.ManualFixup && s.Key != "" {
			if v.FileKinds == nil {
				v.FileKinds = map[string]int{}
			}
			v.FileKinds[s.Key]++
		}
		if v.FirstSeen.IsZero() || s.TS.Before(v.FirstSeen) {
			v.FirstSeen = s.TS
		}
		if s.TS.After(v.LastSeen) {
			v.LastSeen = s.TS
		}
	}
	v.Sessions = len(sess)
	for rp := range repos {
		v.Repos = append(v.Repos, rp)
	}
	sort.Strings(v.Repos)
	v.Score = float64(weight * max(1, len(repos)))
	v.Excerpts = excerpts(sigs, sessions, maxExcerpts)
	return v
}

// excerpts picks the most recent occurrences, one per session first.
func excerpts(sigs []store.Signal, sessions map[string]store.Session, n int) []Excerpt {
	var out []Excerpt
	used := map[string]bool{}
	pick := func(s store.Signal) {
		cwd := sessions[s.SessionID].Cwd
		resume := "claude --resume " + s.SessionID
		if cwd != "" {
			resume = fmt.Sprintf("cd %s && %s", shellQuote(ShortPath(cwd)), resume)
		}
		text := s.Excerpt
		if s.Repo != "" {
			text = strings.ReplaceAll(text, strings.TrimRight(s.Repo, "/")+"/", "")
		}
		text = ShortPath(text)
		out = append(out, Excerpt{Type: s.Type, Text: textutil.Truncate(textutil.OneLine(text), 280), Repo: s.Repo,
			SessionID: s.SessionID, Resume: resume, TS: s.TS})
	}
	for pass := 0; pass < 2 && len(out) < n; pass++ {
		for i := len(sigs) - 1; i >= 0 && len(out) < n; i-- {
			s := sigs[i]
			if used[s.ID] || (pass == 0 && sessUsed(out, s.SessionID)) || textUsed(out, s.Excerpt) {
				continue
			}
			used[s.ID] = true
			pick(s)
		}
	}
	return out
}

// textUsed skips an occurrence that reads the same as one already picked,
// as a prompt pasted into several sessions does.
func textUsed(ex []Excerpt, text string) bool {
	t := textutil.OneLine(text)
	for _, e := range ex {
		if e.Text == textutil.Truncate(t, 280) {
			return true
		}
	}
	return false
}

func sessUsed(ex []Excerpt, sid string) bool {
	for _, e := range ex {
		if e.SessionID == sid {
			return true
		}
	}
	return false
}

// WriteJSON writes findings.json.
func WriteJSON(w io.Writer, r Report) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

var remedyLabel = map[string]string{
	remedy.Hook:     "New hook",
	remedy.ClaudeMD: "CLAUDE.md line",
	remedy.Skill:    "New skill",
	remedy.Ignore:   "Ignore",
}

// signalNames are the plain-language singular and plural for each signal
// type, in the order the summary lists them.
var signalNames = []struct{ typ, one, many string }{
	{store.Correction, "correction", "corrections"},
	{store.Interrupt, "interrupt", "interrupts"},
	{store.Rewind, "rewind", "rewinds"},
	{store.RepeatedInstruction, "repeated prompt", "repeated prompts"},
	{store.HookBlock, "hook block", "hook blocks"},
	{store.ToolFailure, "failure loop", "failure loops"},
	{store.ManualFixup, "hand-fixed edit", "hand-fixed edits"},
}

// FrictionCount is one signal type's count with its plain-language label.
type FrictionCount struct {
	Count int
	Label string
}

// Friction lists the non-zero signal counts in a fixed order, labelled for
// people.
func Friction(counts map[string]int) []FrictionCount {
	var out []FrictionCount
	for _, n := range signalNames {
		switch c := counts[n.typ]; {
		case c == 1:
			out = append(out, FrictionCount{1, n.one})
		case c > 1:
			out = append(out, FrictionCount{c, n.many})
		}
	}
	return out
}

func signalSummary(counts map[string]int) string {
	var parts []string
	for _, f := range Friction(counts) {
		parts = append(parts, fmt.Sprintf("%d %s", f.Count, f.Label))
	}
	return strings.Join(parts, " · ")
}

// WriteMarkdown writes the human report: a summary table first, then one
// short section per finding.
func WriteMarkdown(w io.Writer, r Report) error {
	var b strings.Builder
	fmt.Fprintf(&b, "# sminer report\n\n")
	scope := "all repos"
	if r.Repo != "" {
		scope = "`" + ShortRepo(r.Repo) + "`"
	}
	fmt.Fprintf(&b, "%d sessions since %s · %s · generated %s\n\n",
		r.Sessions, r.Since.Format("2006-01-02"), scope, r.GeneratedAt.Format("2006-01-02 15:04 MST"))
	if s := signalSummary(r.SignalCounts); s != "" {
		fmt.Fprintf(&b, "**Friction seen:** %s\n\n", s)
	}

	fmt.Fprintf(&b, "## Findings (%d)\n\n", len(r.Findings))
	if len(r.Findings) == 0 {
		b.WriteString("No recurring findings at this threshold. Try a lower `--min-count` or a longer `--since`.\n\n")
	} else {
		b.WriteString("| # | Finding | Fix | Seen | ID |\n|---:|---|---|---|---|\n")
		for i, f := range r.Findings {
			fmt.Fprintf(&b, "| %d | %s | %s | %d× in %d sessions | `%s` |\n", i+1,
				escapePipes(textutil.Truncate(f.Title, 70)), remedyLabel[f.Remedy], f.Occurrences, f.Sessions, f.ID)
		}
		b.WriteString("\nPreview a fix with `sminer finding accept <id> --preview`, apply it without `--preview`, or drop it with `sminer finding dismiss <id>`. `sminer review` walks through them one at a time.\n\n")
	}
	for i, f := range r.Findings {
		writeFinding(&b, i+1, f)
	}

	if len(r.Unheeded) > 0 {
		fmt.Fprintf(&b, "## Instructed but not followed (%d)\n\nAlready in a CLAUDE.md, yet corrected again after that file last changed. No mechanical check could be derived, so reword the line, move it somewhere more specific, or write a hook by hand.\n\n", len(r.Unheeded))
		for _, f := range r.Unheeded {
			u := f.Proposal.Unheeded
			fmt.Fprintf(&b, "- **%s** %s — corrected %d times since `%s` changed\n  > %s\n", f.ID, f.Title, u.Since, u.Where, u.Line)
		}
		b.WriteString("\n")
	}

	if len(r.Guardrails) > 0 {
		b.WriteString("## Guardrail activity\n\nHow often existing hooks blocked an action in this window.\n\n| Hook | Blocks |\n|---|---:|\n")
		for _, g := range r.Guardrails {
			fmt.Fprintf(&b, "| `%s` | %d |\n", escapePipes(g.Hook), g.Count)
		}
		b.WriteString("\n")
	}

	if len(r.Ignored) > 0 {
		b.WriteString("## Appendix: ignored\n\nOne-off or already covered. Not ranked.\n\n")
		for _, f := range r.Ignored {
			why := f.Proposal.Why
			if f.CoveredBy != "" && !strings.Contains(why, f.CoveredBy) {
				why += " (" + f.CoveredBy + ")"
			}
			fmt.Fprintf(&b, "- **%s** %s — %d occurrences; %s\n", f.ID, f.Title, f.Occurrences, why)
		}
		b.WriteString("\n")
	}
	if len(r.UnknownTypes) > 0 {
		var ks []string
		for k := range r.UnknownTypes {
			ks = append(ks, k)
		}
		sort.Strings(ks)
		fmt.Fprintf(&b, "<sub>Skipped %d transcript event types sminer doesn't read (%s). Counts are in `--json`.</sub>\n", len(ks), textutil.Truncate(strings.Join(ks, ", "), 160))
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func writeFinding(b *strings.Builder, n int, f Finding) {
	status := ""
	if f.Status == store.StatusAccepted {
		status = " · accepted"
		if f.AdoptedAt != nil {
			status += " " + f.AdoptedAt.Format("2006-01-02")
		}
	}
	fmt.Fprintf(b, "### %d. %s\n\n", n, f.Title)

	fix := remedyLabel[f.Remedy]
	if f.Target != "" {
		fix += " → `" + ShortPath(f.Target) + "`"
	}
	fmt.Fprintf(b, "- **Fix:** %s%s\n", fix, status)
	repos := ""
	if len(f.Repos) > 1 {
		repos = fmt.Sprintf(" across %d repos", len(f.Repos))
	}
	fmt.Fprintf(b, "- **Seen:** %d times in %d sessions%s, %s to %s\n", f.Occurrences, f.Sessions, repos,
		f.FirstSeen.Format("Jan 2"), f.LastSeen.Format("Jan 2"))
	if len(f.Repos) > 0 {
		fmt.Fprintf(b, "- **Repos:** %s\n", listSome(shortRepos(f.Repos), 3))
	}
	if len(f.FileKinds) > 0 {
		var parts []string
		for _, k := range cluster.SortKinds(f.FileKinds) {
			parts = append(parts, fmt.Sprintf("`%s` %d", k.Kind, k.Count))
		}
		fmt.Fprintf(b, "- **By file:** %s\n", listSome(parts, 6))
	}
	b.WriteString("\n")
	for _, e := range f.Excerpts {
		where := e.TS.Format("Jan 2")
		if e.Repo != "" {
			where += " · " + ShortRepo(e.Repo)
		}
		fmt.Fprintf(b, "> %s\n>\n> <sub>%s</sub>\n\n", e.Text, where)
	}

	p := f.Proposal
	switch f.Remedy {
	case remedy.Hook:
		if u := p.Unheeded; u != nil {
			fmt.Fprintf(b, "Already instructed at `%s`, but corrected %d times since that file changed. Escalating to a hook.\n\n", u.Where, u.Since)
		}
		fmt.Fprintf(b, "**Proposed hook:** on `%s`, block when `%s` matches `%s`.\n\n> %s\n\n", p.Matcher, p.Field, p.Pattern, p.Rule)
	case remedy.Skill:
		fmt.Fprintf(b, "**Proposed skill** `%s`: %s\n\n", p.SkillName, p.Description)
		for i, s := range p.Steps {
			fmt.Fprintf(b, "%d. %s\n", i+1, s)
		}
		b.WriteString("\n")
	default:
		fmt.Fprintf(b, "**Proposed line:**\n\n```diff\n+ - %s\n```\n\n", p.Rule)
	}
	switch {
	case f.Status == store.StatusOpen:
		fmt.Fprintf(b, "`sminer finding accept %s --preview` · `sminer finding dismiss %s`\n\n", f.ID, f.ID)
	case f.Status == store.StatusAccepted && p.Unheeded != nil:
		// The accepted CLAUDE.md line didn't stick; the hook is a new proposal.
		fmt.Fprintf(b, "To adopt the hook, `sminer finding reopen %s`, then accept it.\n\n", f.ID)
	}
	if len(f.Excerpts) > 0 {
		fmt.Fprintf(b, "<sub>Latest session: `%s`</sub>\n\n", f.Excerpts[0].Resume)
	}
}

// ShortRepo turns a repo path into something readable: owner/name for
// checkouts, with worktree suffixes dropped.
func ShortRepo(p string) string {
	if i := strings.Index(p, "/.claude/worktrees/"); i >= 0 {
		p = p[:i]
	}
	p = strings.TrimRight(p, "/")
	if home, err := os.UserHomeDir(); err == nil && strings.Contains(p, "/scratch-workspaces/") && strings.HasPrefix(p, home) {
		return "scratch/" + filepath.Base(p)
	}
	parts := strings.Split(p, "/")
	if len(parts) >= 2 {
		return strings.Join(parts[len(parts)-2:], "/")
	}
	return p
}

func shortRepos(rs []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, r := range rs {
		s := "`" + ShortRepo(r) + "`"
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// ShortPath replaces the home directory with ~.
func ShortPath(p string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" && strings.HasPrefix(p, home+"/") {
		return "~" + p[len(home):]
	}
	return p
}

// listSome joins the first n items and counts the rest.
func listSome(items []string, n int) string {
	if len(items) <= n {
		return strings.Join(items, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(items[:n], ", "), len(items)-n)
}

func quoteAll(ss []string) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = "`" + s + "`"
	}
	return out
}

func escapePipes(s string) string { return strings.ReplaceAll(s, "|", `\|`) }

// shellQuote quotes p for a copy-pasted cd when it has spaces; a leading ~
// stays outside the quotes so the shell still expands it.
func shellQuote(p string) string {
	if !strings.ContainsAny(p, " '\"$") {
		return p
	}
	rest, prefix := p, ""
	if strings.HasPrefix(p, "~/") {
		prefix, rest = "~/", p[2:]
	}
	return prefix + "'" + strings.ReplaceAll(rest, "'", `'\''`) + "'"
}
