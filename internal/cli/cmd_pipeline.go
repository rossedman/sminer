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
	"github.com/rossedman/sminer/internal/report"
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

func newRunCmd(a *app) *cobra.Command {
	var scope scopeFlags
	var output string
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Ingest new transcripts, analyze them, and print the report",
		Long: `Run the whole pipeline: ingest, analyze, report.

The report goes to stdout as Markdown (or JSON with --json); progress goes to
stderr. With --output, the report is written to that file and findings.json
is written beside it.`,
		Example: `  sminer run --offline
  sminer run --output ~/notes/sminer.md
  sminer run --json | jq '.findings[].title'`,
		Args: args(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			env, err := a.env()
			if err != nil {
				return err
			}
			ir, err := pipeline.Ingest(cmd.Context(), env, a.cfg.Roots, a.cfg.WindowDuration())
			if err != nil {
				return err
			}
			a.summarizeIngest(ir)
			ar, err := pipeline.Analyze(cmd.Context(), env, a.scope(scope))
			if err != nil {
				return err
			}
			a.summarizeAnalyze(ar)
			r, err := pipeline.Report(env, a.scope(scope))
			if err != nil {
				return err
			}
			return a.emitReport(r, output)
		},
	}
	scope.register(cmd)
	registerRoots(cmd)
	cmd.Flags().StringVarP(&output, "output", "o", "", "write the report to this file instead of stdout")
	return cmd
}

func newIngestCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ingest",
		Short: "Read new transcript lines into the database",
		Long: `Read new transcript lines into the database.

Ingest is incremental: each transcript is re-read only from where the last run
stopped, so repeat runs are cheap. Text is redacted before it is stored, and
raw transcripts are never copied.`,
		Example: `  sminer ingest
  sminer ingest --since 30d --root ~/.claude/projects --root /mnt/old/projects`,
		Args: args(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			env, err := a.env()
			if err != nil {
				return err
			}
			res, err := pipeline.Ingest(cmd.Context(), env, a.cfg.Roots, a.cfg.WindowDuration())
			if err != nil {
				return err
			}
			if a.g.json {
				return a.printJSON(res)
			}
			a.summarizeIngest(res)
			return nil
		},
	}
	cmd.Flags().String("since", "90d", "only read transcripts modified within this window")
	bindFlag(cmd.Flags(), "since", "window")
	registerRoots(cmd)
	return cmd
}

func newAnalyzeCmd(a *app) *cobra.Command {
	var scope scopeFlags
	cmd := &cobra.Command{
		Use:   "analyze",
		Short: "Find signals, cluster them into findings, and propose remedies",
		Long: `Find signals in ingested sessions, confirm them with the classifier, cluster
them into findings, and route each finding to a remedy.

Classifier results are cached, so re-running only classifies new candidates.
--offline skips the classifier and proposal drafting entirely.`,
		Example: `  sminer analyze
  sminer analyze --offline --repo ~/src/api --min-count 2`,
		Args: args(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			env, err := a.env()
			if err != nil {
				return err
			}
			res, err := pipeline.Analyze(cmd.Context(), env, a.scope(scope))
			if err != nil {
				return err
			}
			if a.g.json {
				return a.printJSON(res)
			}
			a.summarizeAnalyze(res)
			return nil
		},
	}
	scope.register(cmd)
	return cmd
}

func newReportCmd(a *app) *cobra.Command {
	var scope scopeFlags
	var output, format string
	cmd := &cobra.Command{
		Use:   "report",
		Short: "Print the ranked findings report",
		Long: `Print the ranked findings report as Markdown, or JSON with --json.

Findings are ranked by occurrences × weight × repos. With --output, the report
is written to that file (atomically) and findings.json is written beside it.`,
		Example: `  sminer report | less
  sminer report --output report.md
  sminer report --json > findings.json`,
		Args: args(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if format == "json" {
				a.g.json = true
			}
			env, err := a.env()
			if err != nil {
				return err
			}
			r, err := pipeline.Report(env, a.scope(scope))
			if err != nil {
				return err
			}
			return a.emitReport(r, output)
		},
	}
	scope.register(cmd)
	cmd.Flags().StringVarP(&output, "output", "o", "", "write the report to this file instead of stdout")
	cmd.Flags().StringVar(&format, "format", "", "")
	deprecateFlag(cmd, "format", "--json")
	cmd.Flags().StringVar(&output, "out", "", "")
	deprecateFlag(cmd, "out", "--output")
	return cmd
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
	a.info("%d finding(s), %d ignored%s; wrote %s", len(r.Findings), len(r.Ignored), unheeded, strings.Join(written, " and "))
	return nil
}

func (a *app) summarizeIngest(r pipeline.IngestResult) {
	a.info("ingested %d new line(s) from %d file(s) (%d unchanged, %d outside window), %d session(s) touched, in %s",
		r.Lines, r.Files-r.Unchanged-r.OutsideWin, r.Unchanged, r.OutsideWin, r.Sessions, r.Elapsed)
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
	a.info("analyzed: %d finding(s) (%d new), remedies %s%s, in %s", len(r.Findings), r.NewFindings, counts(r.Remedies), llm, r.Elapsed)
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
