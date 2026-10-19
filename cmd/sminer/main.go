// Command sminer mines Claude Code session transcripts for corrections you
// keep repeating and proposes hooks, CLAUDE.md lines or skills that fix them.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/pprof"
	"sort"
	"strings"
	"time"

	"github.com/rossedman/sminer/internal/classify"
	"github.com/rossedman/sminer/internal/cluster"
	"github.com/rossedman/sminer/internal/config"
	"github.com/rossedman/sminer/internal/embed"
	"github.com/rossedman/sminer/internal/eval"
	"github.com/rossedman/sminer/internal/ingest"
	"github.com/rossedman/sminer/internal/redact"
	"github.com/rossedman/sminer/internal/remedy"
	"github.com/rossedman/sminer/internal/report"
	"github.com/rossedman/sminer/internal/signal"
	"github.com/rossedman/sminer/internal/store"
)

const usage = `sminer: find the corrections you keep making to Claude and propose fixes.

Usage:
  sminer ingest   [--since 90d] [--root ~/.claude/projects]
  sminer analyze  [--repo <path>] [--min-count 3] [--no-llm]
  sminer report   [--format md|json] [--out ./sminer-report.md]
  sminer show     <finding-id>        # all excerpts for one finding
  sminer accept   <finding-id>        # write the patch file, mark adopted
  sminer dismiss  <finding-id> [--reason ...]
  sminer trend    [<finding-id>]      # rate per week, before vs after adoption
  sminer run                          # ingest + analyze + report
  sminer eval     [--labels file] [--live]

Every command accepts --config <path> (default ~/.config/sminer/config.yaml)
and --db <path>.
`

func main() {
	if path := os.Getenv("SMINER_CPUPROFILE"); path != "" {
		if f, err := os.Create(path); err == nil {
			pprof.StartCPUProfile(f)
			defer pprof.StopCPUProfile()
		}
	}
	if err := run(context.Background(), os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "sminer:", err)
		pprof.StopCPUProfile()
		os.Exit(1)
	}
}

// LLMFactory builds the classifier client; tests replace it.
var LLMFactory = func(model string) classify.LLM { return classify.NewAnthropic(model) }

// nowFunc is the clock; tests pin it.
var nowFunc = time.Now

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprint(stdout, usage)
		return nil
	}
	cmd, rest := args[0], args[1:]
	a := &app{out: stdout, err: stderr, now: nowFunc()}
	switch cmd {
	case "ingest":
		return a.cmdIngest(rest)
	case "analyze":
		return a.cmdAnalyze(ctx, rest)
	case "report":
		return a.cmdReport(rest)
	case "show":
		return a.cmdShow(rest)
	case "accept":
		return a.cmdAccept(rest)
	case "dismiss":
		return a.cmdDismiss(rest)
	case "trend":
		return a.cmdTrend(rest)
	case "run":
		return a.cmdRun(ctx, rest)
	case "eval":
		return a.cmdEval(ctx, rest)
	}
	return fmt.Errorf("unknown command %q\n\n%s", cmd, usage)
}

type app struct {
	out, err io.Writer
	now      time.Time
	cfg      config.Config
	st       *store.Store
	cfgPath  string
	dbPath   string
}

func (a *app) flags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(a.err)
	fs.StringVar(&a.cfgPath, "config", "", "config file")
	fs.StringVar(&a.dbPath, "db", "", "database path")
	return fs
}

// parse accepts flags before or after positional arguments.
func parse(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return pos, nil
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
}

func (a *app) open() error {
	cfg, err := config.Load(a.cfgPath)
	if err != nil {
		return err
	}
	a.cfg = cfg
	db := cfg.DB
	if a.dbPath != "" {
		db = a.dbPath
	}
	st, err := store.Open(config.Expand(db))
	if err != nil {
		return err
	}
	a.st = st
	return nil
}

func (a *app) close() {
	if a.st != nil {
		a.st.Close()
	}
}

func (a *app) window(override string) (time.Duration, error) {
	w := a.cfg.Window
	if override != "" {
		w = override
	}
	return config.ParseDuration(w)
}

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

// ---- ingest ----

type ingestFlags struct {
	since string
	roots multiFlag
}

func (f *ingestFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&f.since, "since", "", "only read transcripts from this window (default: config window)")
	fs.Var(&f.roots, "root", "transcript root (repeatable; default: config roots)")
}

func (a *app) cmdIngest(args []string) error {
	fs := a.flags("ingest")
	var f ingestFlags
	f.register(fs)
	if _, err := parse(fs, args); err != nil {
		return err
	}
	if err := a.open(); err != nil {
		return err
	}
	defer a.close()
	return a.ingest(f)
}

func (a *app) ingest(f ingestFlags) error {
	win, err := a.window(f.since)
	if err != nil {
		return err
	}
	red, err := redact.New(a.cfg.Redact.Patterns)
	if err != nil {
		return err
	}
	roots := []string(f.roots)
	if len(roots) == 0 {
		roots = a.cfg.Roots
	}
	start := time.Now()
	stats, err := ingest.Run(a.st, ingest.Options{Roots: roots, Since: win, Now: a.now, Config: a.cfg, Redact: red})
	if err != nil {
		return err
	}
	fmt.Fprintf(a.out, "ingest: %d files (%d unchanged, %d outside window), %d new lines, %d turns, %d sessions touched in %s\n",
		stats.Files, stats.Unchanged, stats.Skipped, stats.Lines, stats.Turns, stats.Sessions, time.Since(start).Round(time.Millisecond))
	if stats.Malformed > 0 {
		fmt.Fprintf(a.out, "ingest: %d malformed lines skipped\n", stats.Malformed)
	}
	if len(stats.UnknownTypes) > 0 {
		fmt.Fprintf(a.out, "ingest: unknown event types (counted, skipped): %s\n", fmtCounts(stats.UnknownTypes))
	}
	if len(stats.Versions) > 0 {
		fmt.Fprintf(a.out, "ingest: Claude Code versions seen: %s\n", fmtCounts(stats.Versions))
	}
	return nil
}

func fmtCounts(m map[string]int) string {
	var ks []string
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

// ---- analyze ----

type analyzeFlags struct {
	repo     string
	minCount int
	noLLM    bool
	since    string
}

func (f *analyzeFlags) register(fs *flag.FlagSet, withSince bool) {
	fs.StringVar(&f.repo, "repo", "", "only analyze sessions in this repo")
	fs.IntVar(&f.minCount, "min-count", 3, "minimum occurrences for a finding")
	fs.BoolVar(&f.noLLM, "no-llm", false, "heuristics and clustering only; nothing leaves the machine")
	if withSince {
		fs.StringVar(&f.since, "since", "", "analysis window (default: config window)")
	}
}

func (a *app) cmdAnalyze(ctx context.Context, args []string) error {
	fs := a.flags("analyze")
	var f analyzeFlags
	f.register(fs, true)
	if _, err := parse(fs, args); err != nil {
		return err
	}
	if err := a.open(); err != nil {
		return err
	}
	defer a.close()
	return a.analyze(ctx, f)
}

func repoPath(r string) string {
	if r == "" {
		return ""
	}
	p, err := filepath.Abs(config.Expand(r))
	if err != nil {
		return r
	}
	return p
}

func (a *app) analyze(ctx context.Context, f analyzeFlags) error {
	win, err := a.window(f.since)
	if err != nil {
		return err
	}
	since := a.now.Add(-win)
	repo := repoPath(f.repo)
	emb, err := embed.New(a.cfg.Embeddings)
	if err != nil {
		return err
	}

	sst, err := signal.Run(a.st, signal.Options{Embedder: emb, Now: a.now, Window: win})
	if err != nil {
		return err
	}
	fmt.Fprintf(a.out, "signals: %d sessions rescanned; totals %s\n", sst.Sessions, fmtCounts(sst.ByType))

	var llm classify.LLM
	var budget *classify.Budget
	if !f.noLLM && a.cfg.RepoExcluded(repo) {
		f.noLLM = true
	}
	if !f.noLLM {
		llm = LLMFactory(a.cfg.Classifier.Model)
		budget = classify.NewBudget(a.cfg.Classifier.MaxCallsPerRun)
		cst, err := classify.Run(ctx, a.st, classify.Options{
			LLM: llm, Budget: budget, Concurrency: a.cfg.Classifier.Concurrency, Since: since, Repo: repo,
		})
		if err != nil {
			return err
		}
		fmt.Fprintf(a.out, "classify: %d pending, %d calls, %d confirmed, %d rejected, %d errors (model %s)\n",
			cst.Pending, cst.Calls, cst.Confirmed, cst.Rejected, cst.Errors, a.cfg.Classifier.Model)
		if cst.Aborted {
			fmt.Fprintf(a.err, "classify: stopped after repeated errors (%v); continuing with heuristics only. Check ANTHROPIC_API_KEY, or pass --no-llm.\n", cst.LastError)
			llm = nil
		} else if cst.Errors > 0 {
			fmt.Fprintf(a.err, "classify: last error: %v\n", cst.LastError)
		}
	}

	cs, err := cluster.Run(a.st, cluster.Options{
		Embedder: emb, Threshold: a.cfg.Cluster.Threshold, MinCount: f.minCount, Since: since, Repo: repo, Now: a.now,
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(a.out, "cluster: %d signals -> %d clusters, %d findings (%d new) using %s\n",
		cs.Signals, cs.Clusters, len(cs.Findings), cs.New, emb.Name())

	rs, err := remedy.Run(ctx, a.st, cs.Findings, remedy.Options{
		Config: a.cfg, Existing: remedy.LoadExisting(a.cfg), LLM: llm, Budget: budget,
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(a.out, "remedy: %s", fmtCounts(rs.ByType))
	if rs.Synthesized > 0 {
		fmt.Fprintf(a.out, " (%d synthesized by the model)", rs.Synthesized)
	}
	fmt.Fprintln(a.out)
	if rs.Errors > 0 {
		fmt.Fprintf(a.err, "remedy: %d synthesis errors, last: %v\n", rs.Errors, rs.LastError)
	}
	if budget != nil {
		fmt.Fprintf(a.out, "llm calls this run: %d of %d\n", budget.Used(), a.cfg.Classifier.MaxCallsPerRun)
	}
	return nil
}

// ---- report ----

type reportFlags struct {
	format   string
	out      string
	repo     string
	minCount int
	since    string
}

func (f *reportFlags) register(fs *flag.FlagSet, full bool) {
	fs.StringVar(&f.format, "format", "md", "md or json")
	fs.StringVar(&f.out, "out", "./sminer-report.md", "output file ('-' for stdout)")
	if full {
		fs.StringVar(&f.repo, "repo", "", "only report this repo")
		fs.IntVar(&f.minCount, "min-count", 3, "minimum occurrences for a finding")
		fs.StringVar(&f.since, "since", "", "report window (default: config window)")
	}
}

func (a *app) cmdReport(args []string) error {
	fs := a.flags("report")
	var f reportFlags
	f.register(fs, true)
	if _, err := parse(fs, args); err != nil {
		return err
	}
	if err := a.open(); err != nil {
		return err
	}
	defer a.close()
	return a.report(f)
}

func (a *app) report(f reportFlags) error {
	win, err := a.window(f.since)
	if err != nil {
		return err
	}
	r, err := report.Build(a.st, report.Options{Since: a.now.Add(-win), Repo: repoPath(f.repo), MinCount: f.minCount, Now: a.now})
	if err != nil {
		return err
	}
	switch f.format {
	case "md", "json":
	default:
		return fmt.Errorf("unknown format %q", f.format)
	}
	if f.out == "-" {
		if f.format == "json" {
			return report.WriteJSON(a.out, r)
		}
		return report.WriteMarkdown(a.out, r)
	}
	out := f.out
	if f.format == "json" && strings.HasSuffix(out, ".md") {
		out = filepath.Join(filepath.Dir(out), "findings.json")
	}
	write := func(path string, fn func(io.Writer) error) error {
		fh, err := os.Create(path)
		if err != nil {
			return err
		}
		if err := fn(fh); err != nil {
			fh.Close()
			return err
		}
		return fh.Close()
	}
	jsonPath := filepath.Join(filepath.Dir(out), "findings.json")
	if f.format == "md" {
		if err := write(out, func(w io.Writer) error { return report.WriteMarkdown(w, r) }); err != nil {
			return err
		}
	}
	// findings.json always sits beside the report.
	if err := write(jsonPath, func(w io.Writer) error { return report.WriteJSON(w, r) }); err != nil {
		return err
	}
	if f.format == "md" {
		fmt.Fprintf(a.out, "report: %d findings, %d ignored -> %s (+ %s)\n", len(r.Findings), len(r.Ignored), out, jsonPath)
	} else {
		fmt.Fprintf(a.out, "report: %d findings, %d ignored -> %s\n", len(r.Findings), len(r.Ignored), jsonPath)
	}
	return nil
}

// ---- show / accept / dismiss ----

func (a *app) oneID(name string, args []string, extra func(*flag.FlagSet)) (string, error) {
	fs := a.flags(name)
	if extra != nil {
		extra(fs)
	}
	pos, err := parse(fs, args)
	if err != nil {
		return "", err
	}
	if len(pos) != 1 {
		return "", fmt.Errorf("usage: sminer %s <finding-id>", name)
	}
	return pos[0], a.open()
}

func (a *app) cmdShow(args []string) error {
	id, err := a.oneID("show", args, nil)
	if err != nil {
		return err
	}
	defer a.close()
	f, err := a.st.GetFinding(id)
	if err != nil {
		return err
	}
	sigs, err := a.st.FindingSignals(id)
	if err != nil {
		return err
	}
	sessions, err := a.st.Sessions()
	if err != nil {
		return err
	}
	v := report.View(f, sigs, sessions, len(sigs))
	p := v.Proposal
	fmt.Fprintf(a.out, "%s  %s\n", v.ID, v.Title)
	fmt.Fprintf(a.out, "status %s · remedy %s · %d occurrences · %d sessions · repos: %s\n", v.Status, v.Remedy, v.Occurrences, v.Sessions, strings.Join(v.Repos, ", "))
	if v.Target != "" {
		fmt.Fprintf(a.out, "target %s\n", v.Target)
	}
	if p.Rule != "" {
		fmt.Fprintf(a.out, "proposal: %s\n", p.Rule)
	}
	if p.Pattern != "" {
		fmt.Fprintf(a.out, "hook: %s on %s matches %s\n", p.Matcher, p.Field, p.Pattern)
	}
	if p.Why != "" {
		fmt.Fprintf(a.out, "ignored: %s\n", p.Why)
	}
	fmt.Fprintln(a.out)
	for i := len(sigs) - 1; i >= 0; i-- {
		s := sigs[i]
		fmt.Fprintf(a.out, "- %s  %-20s %s\n", s.TS.Format("2006-01-02 15:04"), s.Type, s.Repo)
		fmt.Fprintf(a.out, "    %s\n", strings.ReplaceAll(strings.TrimSpace(s.Excerpt), "\n", "\n    "))
		if s.Rule != "" {
			fmt.Fprintf(a.out, "    rule: %s\n", s.Rule)
		}
		cwd := sessions[s.SessionID].Cwd
		fmt.Fprintf(a.out, "    (cd %s && claude --resume %s)\n", cwd, s.SessionID)
	}
	return nil
}

func (a *app) cmdAccept(args []string) error {
	var outDir string
	id, err := a.oneID("accept", args, func(fs *flag.FlagSet) {
		fs.StringVar(&outDir, "out", "", "directory for the patch file (default: config patch_dir)")
	})
	if err != nil {
		return err
	}
	defer a.close()
	f, err := a.st.GetFinding(id)
	if err != nil {
		return err
	}
	p, err := remedy.BuildPatch(f, a.cfg)
	if err != nil {
		return err
	}
	if outDir == "" {
		outDir = config.Expand(a.cfg.PatchDir)
	}
	if err := os.MkdirAll(outDir, 0o700); err != nil {
		return err
	}
	path := filepath.Join(outDir, f.ID+".patch")
	if err := os.WriteFile(path, []byte(p.Diff), 0o600); err != nil {
		return err
	}
	f.Status, f.Reason = store.StatusAccepted, ""
	if err := a.st.SaveFinding(f); err != nil {
		return err
	}
	if _, err := a.st.DB.Exec(`INSERT INTO adoptions(finding_id, adopted_at, patch_path) VALUES(?,?,?)
ON CONFLICT(finding_id) DO UPDATE SET adopted_at=excluded.adopted_at, patch_path=excluded.patch_path`, f.ID, store.Ms(a.now), path); err != nil {
		return err
	}
	fmt.Fprintf(a.out, "accepted %s; patch written to %s\napply with: cd %s && patch -p1 < %s\n", f.ID, path, p.Base, path)
	if p.Extra != "" {
		fmt.Fprintln(a.out, p.Extra)
	}
	return nil
}

func (a *app) cmdDismiss(args []string) error {
	var reason string
	id, err := a.oneID("dismiss", args, func(fs *flag.FlagSet) {
		fs.StringVar(&reason, "reason", "", "why this is not worth fixing")
	})
	if err != nil {
		return err
	}
	defer a.close()
	f, err := a.st.GetFinding(id)
	if err != nil {
		return err
	}
	f.Status, f.Reason = store.StatusDismissed, reason
	if err := a.st.SaveFinding(f); err != nil {
		return err
	}
	fmt.Fprintf(a.out, "dismissed %s\n", f.ID)
	return nil
}

// ---- trend ----

func (a *app) cmdTrend(args []string) error {
	fs := a.flags("trend")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := a.open(); err != nil {
		return err
	}
	defer a.close()
	if len(pos) == 1 {
		tr, err := report.ComputeTrend(a.st, pos[0], a.now)
		if err != nil {
			return err
		}
		report.WriteTrend(a.out, tr)
		return nil
	}
	fsx, err := a.st.QueryFindings(`status <> ?`, store.StatusDismissed)
	if err != nil {
		return err
	}
	var trs []report.Trend
	for _, f := range fsx {
		tr, err := report.ComputeTrend(a.st, f.ID, a.now)
		if err != nil {
			return err
		}
		if len(tr.Weeks) > 0 {
			trs = append(trs, tr)
		}
	}
	sort.SliceStable(trs, func(i, j int) bool { return trs[i].BeforeRate > trs[j].BeforeRate })
	report.WriteTrendTable(a.out, trs)
	return nil
}

// ---- run ----

func (a *app) cmdRun(ctx context.Context, args []string) error {
	fs := a.flags("run")
	var inf ingestFlags
	var af analyzeFlags
	var rf reportFlags
	inf.register(fs)
	af.register(fs, false)
	rf.register(fs, false)
	if _, err := parse(fs, args); err != nil {
		return err
	}
	if err := a.open(); err != nil {
		return err
	}
	defer a.close()
	start := time.Now()
	if err := a.ingest(inf); err != nil {
		return err
	}
	af.since = inf.since
	if err := a.analyze(ctx, af); err != nil {
		return err
	}
	rf.repo, rf.minCount, rf.since = af.repo, af.minCount, inf.since
	if err := a.report(rf); err != nil {
		return err
	}
	fmt.Fprintf(a.out, "done in %s\n", time.Since(start).Round(time.Millisecond))
	return nil
}

// ---- eval ----

func (a *app) cmdEval(ctx context.Context, args []string) error {
	fs := a.flags("eval")
	labels := fs.String("labels", "testdata/labels/candidates.jsonl", "labelled candidate turns (JSONL)")
	live := fs.Bool("live", false, "also score the live classifier (makes API calls)")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	cfg, err := config.Load(a.cfgPath)
	if err != nil {
		return err
	}
	set, err := eval.Load(*labels)
	if err != nil {
		return err
	}
	h := eval.Heuristic(set)
	fmt.Fprintf(a.out, "heuristic   %s\n", h)
	if *live {
		c, err := eval.Classifier(ctx, set, LLMFactory(cfg.Classifier.Model))
		if err != nil {
			return err
		}
		fmt.Fprintf(a.out, "classifier  %s\n", c)
		p := eval.Pipeline(set, c.Verdicts)
		fmt.Fprintf(a.out, "pipeline    %s\n", p)
	}
	return nil
}
