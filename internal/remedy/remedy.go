// Package remedy decides how each finding is best fixed (a hook, a CLAUDE.md
// line, a skill, or nothing) and renders the fix as a patch. It never writes
// to CLAUDE.md, settings or hooks itself: output is proposals and patch files.
package remedy

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/rossedman/sminer/internal/classify"
	"github.com/rossedman/sminer/internal/cluster"
	"github.com/rossedman/sminer/internal/config"
	"github.com/rossedman/sminer/internal/store"
	"github.com/rossedman/sminer/internal/textutil"
)

// Remedy types.
const (
	Hook     = "hook"
	ClaudeMD = "claude_md"
	Skill    = "skill"
	Ignore   = "ignore"
)

// Proposal is the concrete fix, stored as JSON on the finding.
type Proposal struct {
	Rule string `json:"rule"`
	// Hook: a PreToolUse guard that blocks when Field of a Matcher tool
	// call matches Pattern.
	Matcher string `json:"matcher,omitempty"`
	Field   string `json:"field,omitempty"`
	Pattern string `json:"pattern,omitempty"`
	// Skill.
	SkillName   string   `json:"skill_name,omitempty"`
	Description string   `json:"description,omitempty"`
	Steps       []string `json:"steps,omitempty"`
	// Why the remedy was ignored, when it was.
	Why string `json:"why,omitempty"`
}

// DecodeProposal parses Finding.Proposal.
func DecodeProposal(s string) Proposal {
	var p Proposal
	if json.Unmarshal([]byte(s), &p) != nil {
		p.Rule = s
	}
	return p
}

func (p Proposal) encode() string {
	b, _ := json.Marshal(p)
	return string(b)
}

// Options control the remedy stage.
type Options struct {
	Config   config.Config
	Existing *Existing
	LLM      classify.LLM
	Budget   *classify.Budget
}

// Stats summarises a remedy run.
type Stats struct {
	ByType      map[string]int
	Synthesized int
	Errors      int
	LastError   error
}

// Run routes every finding the cluster stage touched.
func Run(ctx context.Context, st *store.Store, ids []string, opt Options) (Stats, error) {
	stats := Stats{ByType: map[string]int{}}
	for _, id := range ids {
		f, err := st.GetFinding(id)
		if err != nil {
			return stats, err
		}
		sigs, err := st.FindingSignals(id)
		if err != nil {
			return stats, err
		}
		repos := reposOf(sigs)
		for _, r := range repos {
			opt.Existing.LoadRepo(r)
		}
		var p Proposal
		var remedy string
		if f.Synthesized && f.Status != store.StatusOpen {
			// Accepted or dismissed: keep what the user saw.
			stats.ByType[f.RemedyType]++
			continue
		}
		if opt.LLM != nil && f.Family == cluster.Behavior && !f.Synthesized && opt.Budget.Take() {
			syn, err := synthesize(ctx, opt.LLM, f, sigs)
			if err != nil {
				stats.Errors++
				stats.LastError = err
			} else {
				stats.Synthesized++
				f.Synthesized = true
				if syn.Title != "" {
					f.Title = textutil.Truncate(textutil.OneLine(syn.Title), 110)
				}
				remedy, p = syn.Remedy, syn.Proposal
			}
		}
		if remedy == "" {
			remedy, p = Route(f, sigs)
		}
		remedy, p, f.CoveredBy = dedupe(remedy, p, f, sigs, repos, opt.Existing)
		f.Target = target(remedy, p, repos, opt.Config)
		f.RemedyType = remedy
		f.Proposal = p.encode()
		if err := st.SaveFinding(f); err != nil {
			return stats, err
		}
		stats.ByType[remedy]++
	}
	return stats, nil
}

func reposOf(sigs []store.Signal) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range sigs {
		if s.Repo != "" && !seen[s.Repo] {
			seen[s.Repo] = true
			out = append(out, s.Repo)
		}
	}
	sort.Strings(out)
	return out
}

// Route picks a remedy heuristically. A hook wins over a CLAUDE.md line
// whenever a mechanical check can be extracted: deterministic beats
// instructed.
func Route(f store.Finding, sigs []store.Signal) (string, Proposal) {
	switch f.Family {
	case cluster.Hook:
		msg := ""
		if len(sigs) > 0 {
			msg = sigs[len(sigs)-1].Excerpt
		}
		rule := fmt.Sprintf("The %s hook enforces this; follow it up front instead of retrying: %s", f.Key, textutil.OneLine(msg))
		return ClaudeMD, Proposal{Rule: textutil.Truncate(rule, 300)}
	case cluster.Failure:
		errLine := ""
		if len(sigs) > 0 {
			if i := strings.Index(sigs[len(sigs)-1].Excerpt, ": "); i >= 0 {
				errLine = sigs[len(sigs)-1].Excerpt[i+2:]
			}
		}
		rule := fmt.Sprintf("When `%s` fails, read the error and fix the cause before re-running the same command (it repeatedly failed with: %s). Document the correct invocation here.", f.Key, textutil.Truncate(textutil.OneLine(errLine), 160))
		return ClaudeMD, Proposal{Rule: rule}
	case cluster.Fixup:
		rule := fmt.Sprintf("Edits to %s files are regularly hand-corrected afterwards: match the surrounding code's conventions and run the project's formatter after editing them.", f.Key)
		return ClaudeMD, Proposal{Rule: rule}
	}

	rep := representativeText(f, sigs)
	hint := majorityHint(sigs)
	if p, ok := ExtractHook(rep); ok {
		return Hook, p
	}
	if hint == Skill || looksProcedural(rep, sigs) {
		return Skill, skillFrom(rep, sigs)
	}
	return ClaudeMD, Proposal{Rule: asRule(rep)}
}

func representativeText(f store.Finding, sigs []store.Signal) string {
	// Prefer the most common classifier rule, else the excerpt closest to
	// the cluster centroid.
	counts := map[string]int{}
	for _, s := range sigs {
		if s.Rule != "" {
			counts[s.Rule]++
		}
	}
	best, n := "", 0
	for r, c := range counts {
		if c > n || (c == n && r < best) {
			best, n = r, c
		}
	}
	if best != "" {
		return best
	}
	if len(sigs) == 0 {
		return ""
	}
	vecs := make([][]float32, len(sigs))
	for i, s := range sigs {
		vecs[i] = s.Embedding
	}
	return cluster.SignalText(cluster.Representative(sigs, vecs, f.Centroid))
}

func majorityHint(sigs []store.Signal) string {
	counts := map[string]int{}
	for _, s := range sigs {
		if s.RemedyHint != "" {
			counts[s.RemedyHint]++
		}
	}
	best, n := "", 0
	for h, c := range counts {
		if c > n || (c == n && h < best) {
			best, n = h, c
		}
	}
	return best
}

var (
	negRe     = regexp.MustCompile(`(?i)\b(don'?t|do not|never|stop|no more|avoid|must not|shouldn'?t)\b`)
	editVerb  = regexp.MustCompile(`(?i)\b(edit|edits|editing|touch|modify|change|write|update|regenerate|hand-?edit)\b`)
	runVerb   = regexp.MustCompile(`(?i)\b(run|running|use|using|call|execute|invoke)\b`)
	backtick  = regexp.MustCompile("`([^`\n]{2,80})`")
	fileish   = regexp.MustCompile(`(?i)(?:^|[\s"'(])((?:[\w*.-]+/)*[\w*-]+\.(?:lock|lockb|sum|mod|json|ya?ml|toml|tf|tfvars|pb\.go|gen\.go|min\.js|snap|env))\b`)
	lockNames = regexp.MustCompile(`(?i)\b(package-lock\.json|yarn\.lock|pnpm-lock\.yaml|go\.sum|cargo\.lock|poetry\.lock|gemfile\.lock|composer\.lock|uv\.lock)\b`)
	knownCLIs = map[string]bool{
		"git": true, "npm": true, "npx": true, "yarn": true, "pnpm": true, "go": true, "make": true,
		"terraform": true, "kubectl": true, "helm": true, "docker": true, "pip": true, "pip3": true,
		"python": true, "python3": true, "pytest": true, "cargo": true, "rm": true, "curl": true,
		"brew": true, "apt": true, "sudo": true, "gh": true, "aws": true, "gcloud": true,
	}
	riskyCmds = []string{
		"git push --force", "git push -f", "git reset --hard", "git commit --amend", "git rebase",
		"rm -rf", "terraform apply", "terraform destroy", "kubectl apply", "kubectl delete",
		"npm install", "yarn add", "pip install", "go get", "go mod tidy", "helm upgrade",
		"docker system prune", "sudo",
	}
)

// ExtractHook finds a mechanically checkable rule in text: "don't edit
// <file>" becomes an Edit/Write guard, "never run <command>" a Bash guard.
func ExtractHook(text string) (Proposal, bool) {
	if !negRe.MatchString(text) {
		return Proposal{}, false
	}
	rule := asRule(text)
	if editVerb.MatchString(text) || lockNames.MatchString(text) {
		name := ""
		if m := lockNames.FindStringSubmatch(text); m != nil {
			name = m[1]
		} else if m := fileish.FindStringSubmatch(text); m != nil {
			name = m[1]
		}
		if name != "" {
			return Proposal{Rule: rule, Matcher: "Edit|Write|MultiEdit", Field: "file_path", Pattern: globToRegex(name)}, true
		}
	}
	if runVerb.MatchString(text) {
		lower := strings.ToLower(text)
		for _, c := range riskyCmds {
			if strings.Contains(lower, c) {
				return Proposal{Rule: rule, Matcher: "Bash", Field: "command", Pattern: regexp.QuoteMeta(c)}, true
			}
		}
		for _, m := range backtick.FindAllStringSubmatch(text, -1) {
			if f := strings.Fields(m[1]); len(f) > 0 && knownCLIs[f[0]] {
				return Proposal{Rule: rule, Matcher: "Bash", Field: "command", Pattern: regexp.QuoteMeta(m[1])}, true
			}
		}
	}
	return Proposal{}, false
}

func globToRegex(g string) string {
	parts := strings.Split(g, "*")
	for i := range parts {
		parts[i] = regexp.QuoteMeta(parts[i])
	}
	re := strings.Join(parts, `[^/"]*`)
	if !strings.Contains(g, "/") {
		re = `(^|/)` + re
	}
	return re + `$`
}

var stepSplit = regexp.MustCompile(`(?i)[.;]\s+|[.;]$|,\s*(and\s+)?then\s+|,\s+|\s+then\s+`)

var stepRe = regexp.MustCompile(`(?im)(^\s*(\d+[.)]|-|\*)\s+\S|\b(first|then|before|after|next|finally|step)\b)`)

func looksProcedural(text string, sigs []store.Signal) bool {
	steps := len(stepRe.FindAllString(text, -1))
	repeated := 0
	for _, s := range sigs {
		if s.Type == store.RepeatedInstruction {
			repeated++
		}
	}
	return steps >= 3 || (repeated*2 >= len(sigs) && len(text) > 80 && steps >= 2)
}

func skillFrom(text string, sigs []store.Signal) Proposal {
	var steps []string
	for _, l := range strings.Split(text, "\n") {
		l = strings.TrimSpace(l)
		l = strings.TrimLeft(l, "-*0123456789.) ")
		if len(l) > 3 {
			steps = append(steps, l)
		}
	}
	if len(steps) <= 1 {
		steps = nil
		for _, s := range stepSplit.Split(text, -1) {
			if s = strings.TrimSpace(s); len(s) > 3 {
				steps = append(steps, strings.ToUpper(s[:1])+s[1:])
			}
		}
	}
	title := textutil.Truncate(textutil.OneLine(text), 80)
	return Proposal{
		Rule:        title,
		SkillName:   textutil.Slug(title, 40),
		Description: fmt.Sprintf("Use when asked to: %s", textutil.Truncate(textutil.OneLine(text), 160)),
		Steps:       steps,
	}
}

// asRule turns an excerpt into an instruction-shaped line.
func asRule(text string) string {
	t := textutil.OneLine(text)
	t = regexp.MustCompile(`(?i)^(no|nope|nah|wait|hmm|ugh|ok|okay)[,.!]+\s*`).ReplaceAllString(t, "")
	t = strings.TrimSpace(t)
	if t != "" {
		t = strings.ToUpper(t[:1]) + t[1:]
	}
	return textutil.Truncate(t, 240)
}

func dedupe(remedy string, p Proposal, f store.Finding, sigs []store.Signal, repos []string, ex *Existing) (string, Proposal, string) {
	sess := map[string]bool{}
	for _, s := range sigs {
		sess[s.SessionID] = true
	}
	if len(sess) == 1 && f.Family == cluster.Behavior {
		p.Why = "one-off: every occurrence is in a single session"
		return Ignore, p, ""
	}
	switch remedy {
	case Hook:
		lit := strings.ReplaceAll(strings.Trim(strings.ReplaceAll(p.Pattern, `\`, ""), "^$()|/"), `[^/"]*`, "")
		if where := ex.CoveredByHook(lit); where != "" {
			p.Why = "an existing hook already mentions " + lit
			return Ignore, p, where
		}
	case ClaudeMD:
		if where := ex.CoveredByClaudeMD(p.Rule, repos); where != "" {
			p.Why = "already in " + where
			return Ignore, p, where
		}
	case Skill:
		if where := ex.CoveredBySkill(p.SkillName, p.Description); where != "" {
			p.Why = "an existing skill covers this"
			return Ignore, p, where
		}
	}
	return remedy, p, ""
}

func target(remedy string, p Proposal, repos []string, cfg config.Config) string {
	switch remedy {
	case ClaudeMD:
		if len(repos) == 1 {
			return filepath.Join(repos[0], "CLAUDE.md")
		}
		return config.Expand(cfg.Remedies.GlobalClaudeMD)
	case Hook:
		return filepath.Join(config.Expand(cfg.Remedies.HooksDir), "sminer-"+textutil.Slug(p.Rule, 40)+".sh")
	case Skill:
		return filepath.Join(config.Expand(cfg.Remedies.SkillsDir), p.SkillName, "SKILL.md")
	}
	return ""
}
