package cli

import (
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/rossedman/sminer/internal/config"
	"github.com/rossedman/sminer/internal/fsutil"
	"github.com/rossedman/sminer/internal/pipeline"
	"github.com/rossedman/sminer/internal/remedy"
	"github.com/rossedman/sminer/internal/report"
	"github.com/rossedman/sminer/internal/ui"
)

// scopeFlags are shared by run, analyze and report.
type scopeFlags struct {
	repo string
}

func (s *scopeFlags) register(cmd *cobra.Command) {
	f := cmd.Flags()
	f.String("since", "90d", "how far back to look: 90d, 2w, 12h")
	bindFlag(f, "since", "window")
	f.StringVar(&s.repo, "repo", "", "only consider sessions in this repository")
	f.Int("min-count", 3, "occurrences a finding needs to be reported")
	bindFlag(f, "min-count", "min_count")
}

func (a *app) scope(s scopeFlags) pipeline.Scope {
	repo := s.repo
	if repo != "" {
		if abs, err := filepath.Abs(config.Expand(repo)); err == nil {
			repo = abs
		}
	}
	return pipeline.Scope{Window: a.cfg.WindowDuration(), Repo: repo, MinCount: a.cfg.MinCount}
}

func registerRoots(cmd *cobra.Command) {
	cmd.Flags().StringSlice("root", nil, "transcript directory, repeatable (default ~/.claude/projects)")
	bindFlag(cmd.Flags(), "root", "roots")
}

// emitReport prints the report to stdout, or writes it (and findings.json
// beside a Markdown report) when output is set.
func (a *app) emitReport(r report.Report, output string) error {
	render := func(w io.Writer) error {
		if a.g.json {
			return report.WriteJSON(w, r)
		}
		return report.WriteMarkdown(w, r)
	}
	if output == "" || output == "-" {
		if a.fancyOut() && !a.g.json {
			// A person is reading: draw the overview. Piped, it stays
			// Markdown.
			a.spin.pause()
			_, err := io.WriteString(a.out, renderReport(a.theme(), r, a.width()))
			return err
		}
		return render(a.out)
	}
	var buf strings.Builder
	if err := render(&buf); err != nil {
		return err
	}
	if err := fsutil.WriteFileAtomic(output, []byte(buf.String()), 0o644); err != nil {
		return err
	}
	written := []string{output}
	if !a.g.json {
		var js strings.Builder
		if err := report.WriteJSON(&js, r); err != nil {
			return err
		}
		jsonPath := filepath.Join(filepath.Dir(output), "findings.json")
		if err := fsutil.WriteFileAtomic(jsonPath, []byte(js.String()), 0o644); err != nil {
			return err
		}
		written = append(written, jsonPath)
	}
	unheeded := ""
	if n := len(r.Unheeded); n > 0 {
		unheeded = fmt.Sprintf(", %d instructed but not followed", n)
	}
	if a.fancyErr() {
		th := a.theme()
		a.info("\n%s %s  %s", style(th.Heading, "→"), style(th.Title, "Wrote "+strings.Join(tildeAll(written), " and ")),
			style(th.Faint, fmt.Sprintf("%d findings · %d ignored%s", len(r.Findings), len(r.Ignored), unheeded)))
		return nil
	}
	a.info("%d finding(s), %d ignored%s; wrote %s", len(r.Findings), len(r.Ignored), unheeded, strings.Join(written, " and "))
	return nil
}

func (a *app) summarizeIngest(r pipeline.IngestResult) {
	a.done(fmt.Sprintf("ingested %d new line(s) from %d file(s) (%d unchanged, %d outside window), %d session(s) touched, in %s",
		r.Lines, r.Files-r.Unchanged-r.OutsideWin, r.Unchanged, r.OutsideWin, r.Sessions, r.Elapsed),
		fmt.Sprintf("Ingested %d new lines from %d files", r.Lines, r.Files-r.Unchanged-r.OutsideWin),
		fmt.Sprintf("%d unchanged · %d sessions touched", r.Unchanged, r.Sessions))
	if r.Malformed > 0 {
		a.warn("%d malformed transcript line(s) skipped", r.Malformed)
	}
	if len(r.UnknownTypes) > 0 {
		a.debugf("unknown event types (counted, skipped): %s", counts(r.UnknownTypes))
	}
	if len(r.Versions) > 0 {
		a.debugf("Claude Code versions seen: %s", counts(r.Versions))
	}
}

func (a *app) summarizeAnalyze(r pipeline.AnalyzeResult) {
	for _, w := range r.Warnings {
		a.warn("%s", w)
	}
	a.debugf("signals: %s", counts(r.Signals))
	if c := r.Classifier; c != nil {
		a.debugf("classifier %s: %d pending, %d calls, %d confirmed, %d rejected, %d errors", c.Model, c.Pending, c.Calls, c.Confirmed, c.Rejected, c.Errors)
	}
	a.debugf("embedder %s: %d clusters", r.Embedder, r.Clusters)
	llm := ""
	if r.LLMCalls > 0 {
		llm = fmt.Sprintf(", %d LLM call(s)", r.LLMCalls)
	}
	a.done(fmt.Sprintf("analyzed: %d finding(s) (%d new), remedies %s%s, in %s", len(r.Findings), r.NewFindings, counts(r.Remedies), llm, r.Elapsed),
		fmt.Sprintf("Analyzed %d findings, %d new", len(r.Findings), r.NewFindings),
		strings.TrimPrefix(remedyCounts(r.Remedies)+llm, ", "))
}

// counts renders a map as "a=1 b=2" in key order.
func counts(m map[string]int) string {
	if len(m) == 0 {
		return "none"
	}
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	parts := make([]string, len(ks))
	for i, k := range ks {
		parts[i] = fmt.Sprintf("%s=%d", k, m[k])
	}
	return strings.Join(parts, " ")
}

// remedyCounts renders remedy counts for people: "5 CLAUDE.md · 1 hook".
func remedyCounts(m map[string]int) string {
	var parts []string
	for _, r := range []string{remedy.Hook, remedy.ClaudeMD, remedy.Skill, remedy.Ignore} {
		if n := m[r]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, ui.RemedyLabel(r)))
		}
	}
	return strings.Join(parts, " · ")
}
