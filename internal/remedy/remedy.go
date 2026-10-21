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
	"unicode/utf8"

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
	// Unheeded is set when the rule is already in a CLAUDE.md that Claude
	// keeps not following.
	Unheeded *Unheeded `json:"unheeded,omitempty"`
}

// Unheeded records an instruction that exists but isn't being followed: the
// correction kept happening after the CLAUDE.md holding it last changed.
type Unheeded struct {
	Where string `json:"where"` // path:line
	Line  string `json:"line"`  // the existing instruction
	Since int    `json:"since"` // occurrences after the file last changed
}

// unheededMin is how many corrections after the instruction's file last
// changed it takes to call the instruction unheeded.
const unheededMin = 2

// DecodeProposal parses Finding.Proposal.
func DecodeProposal(s string) Proposal {
	var p Proposal
	if json.Unmarshal([]byte(s), &p) != nil {
		p.Rule = s
	}
	return p
}

// encode returns p as a JSON string. The error from Marshal is ignored because
// Proposal holds only plain fields.
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

// reposOf returns the distinct non-empty repos of sigs, sorted.
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
		var kinds []string
		for i, k := range cluster.FixupKinds(sigs) {
			if i == 3 {
				break
			}
			kinds = append(kinds, k.Kind)
		}
		what := "files"
		switch n := len(kinds); {
		case n == 1:
			what = kinds[0] + " files"
		case n > 1:
			what = strings.Join(kinds[:n-1], ", ") + " and " + kinds[n-1] + " files"
		}
		rule := fmt.Sprintf("Edits to %s are often hand-corrected afterwards: match the surrounding code's conventions and run the project's formatter after editing.", what)
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

// representativeText returns the text that best stands for a finding: the most
// common classifier rule, with ties going to the alphabetically first,
// otherwise the text of the signal closest to the finding's centroid. It
// returns an empty string when there are no signals.
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

// majorityHint returns the remedy hint that appears most often on sigs, with
// ties going to the alphabetically first, or an empty string if none has one.
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
			// A pattern must be valid UTF-8 to compile.
			if f := strings.Fields(m[1]); len(f) > 0 && knownCLIs[f[0]] && utf8.ValidString(m[1]) {
				return Proposal{Rule: rule, Matcher: "Bash", Field: "command", Pattern: regexp.QuoteMeta(m[1])}, true
			}
		}
	}
	return Proposal{}, false
}

// globToRegex converts the file glob g to a regular expression. A star matches
// anything but a slash or quote, and a glob without a slash matches only a
// final path element. The expression is anchored at the end.
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

// looksProcedural reports whether text reads like a multi-step procedure:
// three or more steps, or two or more when at least half the signals are
// repeated instructions and the text is long.
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

// skillFrom builds a skill proposal from text. Steps come from its lines with
// list markers removed or, if that gives one step or fewer, from splitting on
// step separators. The skill name and description are derived from the text.
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
			s = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), " and"))
			if len(s) > 3 {
				steps = append(steps, strings.ToUpper(s[:1])+s[1:])
			}
		}
	}
	title := textutil.Truncate(textutil.OneLine(text), 80)
	return Proposal{
		Rule:        title,
		SkillName:   skillName(text),
		Description: fmt.Sprintf("Use when asked to: %s", textutil.Truncate(textutil.OneLine(text), 160)),
		Steps:       steps,
	}
}

// skillFiller are words too generic to name a skill by.
var skillFiller = map[string]bool{
	"create": true, "run": true, "make": true, "then": true, "repo": true, "repository": true,
	"branch": true, "skill": true, "determine": true, "how": true, "many": true, "much": true,
	"please": true, "can": true, "you": true, "this": true, "that": true, "these": true, "it": true,
	"up": true, "all": true, "new": true, "get": true, "do": true, "use": true, "using": true,
}

// skillName builds a short slug from the first few distinctive words, so a
// prompt like "Create a branch and run the jfrog-cli skill to audit..."
// becomes "jfrog-cli-audit-fixable" rather than the prompt cut mid-word.
func skillName(text string) string {
	var words []string
	for _, w := range textutil.Tokens(text) {
		w = strings.Trim(slugChars.ReplaceAllString(w, "-"), "-")
		if w == "" || skillFiller[w] {
			continue
		}
		words = append(words, w)
		if len(words) == 3 || len(strings.Join(words, "-")) >= 24 {
			break
		}
	}
	return textutil.Slug(strings.Join(words, "-"), 40)
}

var slugChars = regexp.MustCompile(`[^a-z0-9-]+`)

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

// dedupe checks a proposed remedy against what already exists. It returns
// ignore for a one-off in a single session, or when an existing hook or skill
// covers it, together with where. An existing CLAUDE.md line leads to
// escalation. Otherwise the remedy is returned unchanged.
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
		if c, ok := ex.ClaudeMDCoverage(p.Rule, repos); ok {
			return escalate(p, f, sigs, repos, ex, c)
		}
	case Skill:
		if where := ex.CoveredBySkill(p.SkillName, p.Description); where != "" {
			p.Why = "an existing skill covers this"
			return Ignore, p, where
		}
	}
	return remedy, p, ""
}

// escalate handles a rule that CLAUDE.md already states. If the correction
// kept happening after that file last changed, the instruction isn't
// working: propose a hook when a mechanical check can be found, and
// otherwise report it as unheeded rather than quietly ignoring it. The file's
// modification time is a lower bound on when the line was added, so the
// count is conservative.
func escalate(p Proposal, f store.Finding, sigs []store.Signal, repos []string, ex *Existing, c Coverage) (string, Proposal, string) {
	since := 0
	if f.Family == cluster.Behavior {
		mt := modTime(c.Path)
		for _, s := range sigs {
			if s.TS.After(mt) {
				since++
			}
		}
	}
	if since < unheededMin {
		p.Why = "already in " + c.String()
		return Ignore, p, c.String()
	}
	u := &Unheeded{Where: c.String(), Line: c.Text, Since: since}
	candidates := []string{strings.TrimLeft(c.Text, "-*0123456789.) "), p.Rule}
	for _, s := range sigs {
		candidates = append(candidates, s.Rule, cluster.SignalText(s))
	}
	for _, text := range candidates {
		if hp, ok := ExtractHook(text); ok {
			hp.Rule = p.Rule
			hp.Unheeded = u
			// An existing hook that already blocks it wins.
			return dedupe(Hook, hp, f, sigs, repos, ex)
		}
	}
	p.Unheeded = u
	p.Why = fmt.Sprintf("already in %s, but corrected %d times since that file last changed; no mechanical check could be derived, so reword the line or write a hook by hand", c.String(), since)
	return Ignore, p, c.String()
}

// target returns the file the remedy would write: CLAUDE.md in the single repo
// or the global one, a hook script, or a skill's SKILL.md. Other remedies have
// no target and return an empty string.
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
