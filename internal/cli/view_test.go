package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/rossedman/sminer/internal/remedy"
	"github.com/rossedman/sminer/internal/report"
	"github.com/rossedman/sminer/internal/ui"
)

var (
	t0      = time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC) // a Monday
	adopted = time.Date(2026, 9, 21, 10, 0, 0, 0, time.UTC)
)

func weeks(counts ...int) []report.Week {
	ws := make([]report.Week, len(counts))
	for i, c := range counts {
		ws[i] = report.Week{Start: t0.AddDate(0, 0, 7*i), Count: c}
	}
	return ws
}

func sampleFinding(rem string) report.Finding {
	f := report.Finding{
		ID: "F1", Title: "Never hand-edit go.sum", Remedy: rem, Status: "open", Score: 12,
		Occurrences: 5, Sessions: 3, Repos: []string{"/x/acme/web"}, Target: "/x/CLAUDE.md",
		FirstSeen: t0, LastSeen: adopted,
		Excerpts: []report.Excerpt{{Type: "correction", Text: "no, don't", Repo: "/x/acme/web", Resume: "claude --resume abc", TS: t0}},
	}
	switch rem {
	case remedy.Hook:
		f.Proposal = remedy.Proposal{Rule: "Do not edit go.sum", Matcher: "Edit", Field: "file_path", Pattern: `go\.sum$`}
	case remedy.Skill:
		f.Proposal = remedy.Proposal{SkillName: "release", Description: "Cut a release", Steps: []string{"Tag it", "Push the tag"}}
	case remedy.Ignore:
		f.Proposal = remedy.Proposal{Why: "one-off"}
	default:
		f.Proposal = remedy.Proposal{Rule: "Use pnpm."}
	}
	return f
}

func TestProposalText(t *testing.T) {
	t.Parallel()
	unheeded := remedy.Proposal{Matcher: "Edit", Field: "file_path", Pattern: "x", Rule: "r", Unheeded: &remedy.Unheeded{Where: "CLAUDE.md:3", Since: 4}}
	tests := []struct {
		name string
		r    string
		p    remedy.Proposal
		want string
	}{
		{"hook", remedy.Hook, remedy.Proposal{Matcher: "Bash", Field: "command", Pattern: "rm", Rule: "no rm"},
			"proposal: PreToolUse hook on Bash, block when command matches rm\n  no rm"},
		{"escalated hook", remedy.Hook, unheeded,
			"escalated: CLAUDE.md:3 is ignored (corrected 4 times since)\nproposal: PreToolUse hook on Edit, block when file_path matches x\n  r"},
		{"skill", remedy.Skill, remedy.Proposal{SkillName: "s", Description: "d", Steps: []string{"a", "b"}},
			"proposal: skill s (d)\n  1. a\n  2. b"},
		{"ignore", remedy.Ignore, remedy.Proposal{Why: "covered"}, "ignored: covered"},
		{"claude_md", remedy.ClaudeMD, remedy.Proposal{Rule: "Use pnpm."}, "proposal: Use pnpm."},
	}
	for _, tt := range tests {
		if got := proposalText(tt.r, tt.p); got != tt.want {
			t.Errorf("%s: got %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestRenderCard(t *testing.T) {
	t.Parallel()
	th := ui.New(true)
	tests := []struct {
		name   string
		mutate func(*report.Finding)
		rem    string
		want   []string
		not    []string
	}{
		{"hook", nil, remedy.Hook, []string{"hook", "F1", "Never hand-edit go.sum", "PreToolUse hook", "go\\.sum$", "Evidence (1)", "claude --resume abc", "5 occurrences in 3 sessions", "--preview"}, nil},
		{"escalated hook", func(f *report.Finding) {
			f.Proposal.Unheeded = &remedy.Unheeded{Where: "CLAUDE.md:3", Since: 2}
		}, remedy.Hook, []string{"Escalated: CLAUDE.md:3 was ignored 2 times"}, nil},
		{"skill", nil, remedy.Skill, []string{"skill release", "Cut a release", " 1. Tag it", " 2. Push the tag"}, nil},
		{"ignore has no hint", nil, remedy.Ignore, []string{"Ignored", "one-off"}, []string{"--preview"}},
		{"claude_md", nil, remedy.ClaudeMD, []string{"CLAUDE.md line", "+ - Use pnpm."}, nil},
		{"accepted shows adoption", func(f *report.Finding) {
			f.Status = "accepted"
			f.AdoptedAt = &adopted
		}, remedy.ClaudeMD, []string{"adopted 2026-09-21", "accepted"}, []string{"--preview"}},
		{"no excerpts, no repo", func(f *report.Finding) {
			f.Excerpts = nil
			f.Repos = nil
			f.Target = ""
		}, remedy.ClaudeMD, nil, []string{"Evidence", "repos "}},
		{"excerpt without repo", func(f *report.Finding) { f.Excerpts[0].Repo = "" }, remedy.ClaudeMD, []string{"no, don't"}, nil},
	}
	for _, tt := range tests {
		f := sampleFinding(tt.rem)
		if tt.mutate != nil {
			tt.mutate(&f)
		}
		got := ansi.Strip(renderCard(th, f, 100, true))
		for _, w := range tt.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s: missing %q in\n%s", tt.name, w, got)
			}
		}
		for _, w := range tt.not {
			if strings.Contains(got, w) {
				t.Errorf("%s: unexpected %q in\n%s", tt.name, w, got)
			}
		}
	}
}

func TestTrendChart(t *testing.T) {
	t.Parallel()
	a := newApp(Options{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}})
	tests := []struct {
		name string
		tr   report.Trend
		want []string
	}{
		{"not adopted", report.Trend{ID: "F1", Title: "T", Weeks: weeks(2, 4), BeforeRate: 3},
			[]string{"F1", "Sep 07", "Sep 14", "Not adopted · 3.00 a week"}},
		{"improved", report.Trend{ID: "F1", Title: "T", Weeks: weeks(4, 4, 2, 1), AdoptedAt: &adopted, BeforeRate: 4, AfterRate: 1.5, AfterWeeks: 2},
			[]string{"◀ adopted Sep 21", "before 4.00/wk", "after 1.50/wk", "over 2.0 weeks", "-62%"}},
		{"worse", report.Trend{ID: "F1", Title: "T", Weeks: weeks(1, 1, 3), AdoptedAt: &adopted, BeforeRate: 1, AfterRate: 3, AfterWeeks: 1},
			[]string{"+200%"}},
		{"no baseline", report.Trend{ID: "F1", Title: "T", Weeks: weeks(0, 0, 3), AdoptedAt: &adopted, AfterRate: 3, AfterWeeks: 1},
			[]string{"before 0.00/wk", "after 3.00/wk"}},
	}
	for _, tt := range tests {
		got := ansi.Strip(a.trendChart(tt.tr))
		for _, w := range tt.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s: missing %q in\n%s", tt.name, w, got)
			}
		}
	}
	if got := ansi.Strip(a.trendChart(report.Trend{ID: "F1", Weeks: weeks(1), AdoptedAt: &adopted, BeforeRate: 0})); strings.Contains(got, "%") {
		t.Errorf("no baseline should show no percentage:\n%s", got)
	}
}

func TestTrendTable(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	a := newApp(Options{Stdout: &out, Stderr: &bytes.Buffer{}})
	many := make([]int, 30)
	for i := range many {
		many[i] = i % 5
	}
	trs := []report.Trend{
		{ID: "F1", Title: "adopted one", Weeks: weeks(3, 3, 1, 0), AdoptedAt: &adopted, BeforeRate: 3, AfterRate: 0.5},
		{ID: "F2", Title: "not adopted\nmultiline title", Weeks: weeks(many...), BeforeRate: 2},
	}
	if err := a.trendTable(trs); err != nil {
		t.Fatal(err)
	}
	got := ansi.Strip(out.String())
	for _, w := range []string{"ID", "BEFORE/WK", "AFTER/WK", "WEEKLY", "F1", "3.00", "0.50", "F2", "—", "not adopted multiline title"} {
		if !strings.Contains(got, w) {
			t.Errorf("missing %q in\n%s", w, got)
		}
	}
}

func TestFindingTable(t *testing.T) {
	t.Parallel()
	var out, errb bytes.Buffer
	a := newApp(Options{Stdout: &out, Stderr: &errb})
	fs := []report.Finding{sampleFinding(remedy.Hook), sampleFinding(remedy.Skill)}
	fs[1].ID, fs[1].Status, fs[1].Score = "F2", "accepted", 3
	if err := a.findingTable(fs); err != nil {
		t.Fatal(err)
	}
	got := ansi.Strip(out.String())
	for _, w := range []string{"STATUS", "F1", "F2", "○ open", "✓ accepted", "5×", "Never hand-edit go.sum"} {
		if !strings.Contains(got, w) {
			t.Errorf("missing %q in\n%s", w, got)
		}
	}
	if !strings.Contains(errb.String(), "2 finding(s)") || !strings.Contains(errb.String(), "finding show F1") {
		t.Errorf("footer = %q", errb.String())
	}
}

func TestRenderReportVariants(t *testing.T) {
	t.Parallel()
	th := ui.New(false)
	if got := ansi.Strip(renderReport(th, report.Report{Sessions: 2, Since: t0}, 80)); !strings.Contains(got, "Nothing recurring") || strings.Contains(got, "sminer review") {
		t.Errorf("empty report:\n%s", got)
	}
	hook, skill, md := sampleFinding(remedy.Hook), sampleFinding(remedy.Skill), sampleFinding(remedy.ClaudeMD)
	hook.Repos = []string{"/a/b", "/c/d"}
	skill.Repos = nil
	skill.ID, skill.Status = "F2", "accepted"
	md.ID = "F3"
	md.FileKinds = map[string]int{"go": 5, "tf": 4, "md": 3, "yaml": 2, "json": 1, "toml": 1, "sh": 1}
	skill.Proposal.Steps = nil
	r := report.Report{
		Sessions: 9, Since: t0, Repo: "/x/acme/web",
		SignalCounts: map[string]int{"correction": 1},
		Findings:     []report.Finding{hook, skill, md},
		Ignored:      []report.Finding{sampleFinding(remedy.Ignore)},
		Unheeded:     []report.Finding{hook},
		Guardrails:   []report.GuardrailCount{{Hook: "a", Count: 2}, {Hook: "b", Count: 3}},
	}
	got := ansi.Strip(renderReport(th, r, 120))
	for _, w := range []string{
		"acme/web", "1 correction", "3 things worth fixing", "2 repos", "accepted", "New hook", "New skill", "CLAUDE.md line",
		"Block Edit when file_path matches", "Cut a release", "+ Use pnpm.", "+2 more",
		"1 CLAUDE.md instruction(s) keep being ignored", "Existing hooks blocked 5 action(s)", "1 more ignored",
	} {
		if !strings.Contains(got, w) {
			t.Errorf("missing %q in\n%s", w, got)
		}
	}
	// Skill with steps joins them with arrows, lowercasing the first letter.
	skill.Proposal.Steps = []string{"Tag it", "Push the tag", "CI runs"}
	r.Findings = []report.Finding{skill}
	if got := ansi.Strip(renderReport(th, r, 80)); !strings.Contains(got, "tag it → push the tag → CI runs") {
		t.Errorf("skill steps:\n%s", got)
	}
	// Hook without a rule shows only the match line.
	hook.Proposal.Rule = ""
	r.Findings = []report.Finding{hook}
	if got := ansi.Strip(renderReport(th, r, 80)); !strings.Contains(got, "Block Edit when file_path matches") {
		t.Errorf("hook:\n%s", got)
	}
}

func TestSmallRenderHelpers(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		remedy.Hook: "New hook", remedy.Skill: "New skill", remedy.ClaudeMD: "CLAUDE.md line", "other": "other",
	} {
		if got := fixLabel(in); got != want {
			t.Errorf("fixLabel(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{
		"": "", "Run tests": "run tests", "CI is red": "CI is red", "A": "a", "ab": "ab",
	} {
		if got := lowerFirst(in); got != want {
			t.Errorf("lowerFirst(%q) = %q, want %q", in, got, want)
		}
	}
	for _, tt := range []struct{ in, want string }{
		{"", ""},
		{"capitalize", "Capitalize"},
		{"Already", "Already"},
	} {
		if got := capitalize(tt.in); got != tt.want {
			t.Errorf("capitalize(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
	long := strings.Repeat("x", 100)
	for _, tt := range []struct {
		in   string
		n    int
		want string
	}{
		{"First sentence. Second sentence.", 60, "First sentence"},
		{"  padded...  ", 60, "padded"},
		{long, 10, strings.Repeat("x", 10) + "…"},
		{"aaa bbb ccc ddd eee fff", 12, "aaa bbb ccc…"},
		{"exactly ten", 11, "exactly ten"},
	} {
		if got := headline(tt.in, tt.n); got != tt.want {
			t.Errorf("headline(%q, %d) = %q, want %q", tt.in, tt.n, got, tt.want)
		}
	}
	if got := joinWrapped([]string{"aaaa", "bbbb", "cccc"}, "|", 11, "> "); got != "> aaaa|bbbb\n> cccc" {
		t.Errorf("joinWrapped = %q", got)
	}
	if got := joinWrapped(nil, "|", 10, "> "); got != "> " {
		t.Errorf("joinWrapped(nil) = %q", got)
	}
	if got := counts(nil); got != "none" {
		t.Errorf("counts(nil) = %q", got)
	}
	if got := counts(map[string]int{"b": 2, "a": 1}); got != "a=1 b=2" {
		t.Errorf("counts = %q", got)
	}
	if got := remedyCounts(map[string]int{remedy.Hook: 1, remedy.ClaudeMD: 5, remedy.Ignore: 0}); got != "1 hook · 5 CLAUDE.md" {
		t.Errorf("remedyCounts = %q", got)
	}
	if got := orEmpty(nil); got == nil || len(got) != 0 {
		t.Errorf("orEmpty(nil) = %#v", got)
	}
	if got := orEmpty([]string{"a"}); len(got) != 1 {
		t.Errorf("orEmpty = %#v", got)
	}
	if !validStatus("all") || validStatus("nope") {
		t.Error("validStatus")
	}
	for in, want := range map[string]string{"show <id>": "<id>", "list": "", "accept <id>...": "<id>..."} {
		if got := trimUse(in); got != want {
			t.Errorf("trimUse(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSourceStyle(t *testing.T) {
	t.Parallel()
	th := ui.New(true)
	for src, want := range map[string]lipgloss.Style{
		"flag --db": th.Accent, "env SMINER_X": th.OK, "file /c.yaml": th.Warn, "default": th.Faint,
	} {
		got := sourceStyle(th, src)
		if got.Render("x") != want.Render("x") {
			t.Errorf("sourceStyle(%q) rendered differently", src)
		}
	}
}

func TestTildeIn(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	sep := "/"
	if got := tildeIn("a " + home + sep + "b " + home + sep + "c"); got != "a ~/b ~/c" {
		t.Errorf("tildeIn = %q", got)
	}
	if got := tildeIn("/elsewhere/x"); got != "/elsewhere/x" {
		t.Errorf("tildeIn = %q", got)
	}
	if got := tildeAll([]string{home + "/p", "/q"}); got[0] != "~/p" || got[1] != "/q" {
		t.Errorf("tildeAll = %v", got)
	}
	t.Setenv("HOME", "/")
	if got := tildeIn("/usr/x"); got != "/usr/x" {
		t.Errorf("tildeIn with root home = %q", got)
	}
}

func TestWidth(t *testing.T) {
	t.Parallel()
	a := newApp(Options{Stdout: &bytes.Buffer{}})
	if got := a.width(); got != 100 {
		t.Errorf("non-terminal width = %d, want 100", got)
	}
}
