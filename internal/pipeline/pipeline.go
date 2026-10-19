// Package pipeline runs sminer's stages in order: ingest, then analyze
// (signal, classify, cluster, remedy), then report. It owns orchestration so
// the CLI only parses input and renders output.
//
// Result types carry JSON tags and are the machine-readable contract of
// `--json`: add fields freely, never rename or remove them.
package pipeline

import (
	"context"
	"fmt"
	"time"

	"github.com/rossedman/sminer/internal/classify"
	"github.com/rossedman/sminer/internal/cluster"
	"github.com/rossedman/sminer/internal/config"
	"github.com/rossedman/sminer/internal/embed"
	"github.com/rossedman/sminer/internal/ingest"
	"github.com/rossedman/sminer/internal/redact"
	"github.com/rossedman/sminer/internal/remedy"
	"github.com/rossedman/sminer/internal/report"
	"github.com/rossedman/sminer/internal/signal"
	"github.com/rossedman/sminer/internal/store"
)

// Env is what every stage needs.
type Env struct {
	Store  *store.Store
	Config config.Config
	Now    time.Time
	// NewLLM builds the classifier client. Nil means offline.
	NewLLM func(model string) classify.LLM
	// Progress receives one line per milestone. May be nil.
	Progress func(format string, args ...any)
}

func (e Env) progress(format string, args ...any) {
	if e.Progress != nil {
		e.Progress(format, args...)
	}
}

// Scope narrows analysis and reporting.
type Scope struct {
	Window   time.Duration
	Repo     string
	MinCount int
}

func (s Scope) since(now time.Time) time.Time { return now.Add(-s.Window) }

// IngestResult summarises an ingest run.
type IngestResult struct {
	Files        int            `json:"files"`
	Unchanged    int            `json:"unchanged"`
	OutsideWin   int            `json:"outside_window"`
	Lines        int            `json:"lines"`
	Malformed    int            `json:"malformed"`
	Turns        int            `json:"turns"`
	Sessions     int            `json:"sessions"`
	UnknownTypes map[string]int `json:"unknown_event_types"`
	Versions     map[string]int `json:"claude_code_versions"`
	Elapsed      Duration       `json:"elapsed"`
}

// Ingest reads new transcript lines from roots.
func Ingest(ctx context.Context, env Env, roots []string, window time.Duration) (IngestResult, error) {
	red, err := redact.New(env.Config.Redact.Patterns)
	if err != nil {
		return IngestResult{}, err
	}
	start := time.Now()
	env.progress("ingesting transcripts from %d root(s)", len(roots))
	st, err := ingest.Run(ctx, env.Store, ingest.Options{Roots: roots, Since: window, Now: env.Now, Config: env.Config, Redact: red})
	res := IngestResult{
		Files: st.Files, Unchanged: st.Unchanged, OutsideWin: st.Skipped, Lines: st.Lines,
		Malformed: st.Malformed, Turns: st.Turns, Sessions: st.Sessions,
		UnknownTypes: nonNil(st.UnknownTypes), Versions: nonNil(st.Versions),
		Elapsed: Duration(time.Since(start)),
	}
	return res, err
}

// AnalyzeResult summarises an analyze run.
type AnalyzeResult struct {
	SessionsRescanned int            `json:"sessions_rescanned"`
	Signals           map[string]int `json:"signals"`
	Classifier        *ClassifyInfo  `json:"classifier,omitempty"`
	Embedder          string         `json:"embedder"`
	Clusters          int            `json:"clusters"`
	Findings          []string       `json:"findings"`
	NewFindings       int            `json:"new_findings"`
	Remedies          map[string]int `json:"remedies"`
	Synthesized       int            `json:"synthesized"`
	LLMCalls          int            `json:"llm_calls"`
	Warnings          []string       `json:"warnings"`
	Elapsed           Duration       `json:"elapsed"`
}

// ClassifyInfo summarises the classifier step.
type ClassifyInfo struct {
	Model     string `json:"model"`
	Pending   int    `json:"pending"`
	Calls     int    `json:"calls"`
	Confirmed int    `json:"confirmed"`
	Rejected  int    `json:"rejected"`
	Errors    int    `json:"errors"`
	Aborted   bool   `json:"aborted"`
}

// Analyze turns ingested turns into findings with remedies.
func Analyze(ctx context.Context, env Env, scope Scope) (AnalyzeResult, error) {
	start := time.Now()
	res := AnalyzeResult{Warnings: []string{}, Findings: []string{}}
	cfg := env.Config
	since := scope.since(env.Now)
	emb, err := embed.New(cfg.Embeddings)
	if err != nil {
		return res, err
	}
	res.Embedder = emb.Name()

	env.progress("finding signals")
	sst, err := signal.Run(ctx, env.Store, signal.Options{Embedder: emb, Now: env.Now, Window: scope.Window})
	if err != nil {
		return res, err
	}
	res.SessionsRescanned, res.Signals = sst.Sessions, nonNil(sst.ByType)

	var llm classify.LLM
	var budget *classify.Budget
	if env.NewLLM != nil && !cfg.Offline && !cfg.RepoExcluded(scope.Repo) {
		llm = env.NewLLM(cfg.Classifier.Model)
		budget = classify.NewBudget(cfg.Classifier.MaxCallsPerRun)
		env.progress("classifying candidates with %s", cfg.Classifier.Model)
		cst, err := classify.Run(ctx, env.Store, classify.Options{
			LLM: llm, Budget: budget, Concurrency: cfg.Classifier.Concurrency, Since: since, Repo: scope.Repo,
		})
		if err != nil {
			return res, err
		}
		res.Classifier = &ClassifyInfo{Model: cfg.Classifier.Model, Pending: cst.Pending, Calls: cst.Calls,
			Confirmed: cst.Confirmed, Rejected: cst.Rejected, Errors: cst.Errors, Aborted: cst.Aborted}
		switch {
		case cst.Aborted:
			res.Warnings = append(res.Warnings, fmt.Sprintf(
				"classifier stopped after repeated errors (%v); continued with heuristics only. Check ANTHROPIC_API_KEY, or use --offline", cst.LastError))
			llm = nil
		case cst.Errors > 0:
			res.Warnings = append(res.Warnings, fmt.Sprintf("classifier: %d errors, last: %v", cst.Errors, cst.LastError))
		}
		if err := ctx.Err(); err != nil {
			return res, err
		}
	}

	env.progress("clustering signals")
	cs, err := cluster.Run(env.Store, cluster.Options{
		Embedder: emb, Threshold: cfg.Cluster.Threshold, MinCount: scope.MinCount, Since: since, Repo: scope.Repo, Now: env.Now,
	})
	if err != nil {
		return res, err
	}
	res.Clusters, res.NewFindings = cs.Clusters, cs.New
	if cs.Findings != nil {
		res.Findings = cs.Findings
	}

	env.progress("routing %d finding(s) to remedies", len(cs.Findings))
	rs, err := remedy.Run(ctx, env.Store, cs.Findings, remedy.Options{
		Config: cfg, Existing: remedy.LoadExisting(cfg), LLM: llm, Budget: budget,
	})
	if err != nil {
		return res, err
	}
	res.Remedies, res.Synthesized = nonNil(rs.ByType), rs.Synthesized
	if rs.Errors > 0 {
		res.Warnings = append(res.Warnings, fmt.Sprintf("proposal drafting: %d errors, last: %v", rs.Errors, rs.LastError))
	}
	res.LLMCalls = budget.Used()
	res.Elapsed = Duration(time.Since(start))
	return res, nil
}

// Report builds the report for scope.
func Report(env Env, scope Scope) (report.Report, error) {
	return report.Build(env.Store, report.Options{
		Since: scope.since(env.Now), Repo: scope.Repo, MinCount: scope.MinCount, Now: env.Now,
	})
}

// Duration marshals as a string like "1.25s".
type Duration time.Duration

func (d Duration) String() string { return time.Duration(d).Round(time.Millisecond).String() }

// MarshalText implements encoding.TextMarshaler.
func (d Duration) MarshalText() ([]byte, error) { return []byte(d.String()), nil }

func nonNil(m map[string]int) map[string]int {
	if m == nil {
		return map[string]int{}
	}
	return m
}
