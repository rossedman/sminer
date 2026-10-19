package remedy

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/rossedman/sminer/internal/config"
	"github.com/rossedman/sminer/internal/textutil"
)

// Existing is what is already in place, read-only: CLAUDE.md lines, hook
// configuration and scripts, and installed skills. Proposals already
// covered by it are routed to Ignore.
type Existing struct {
	cfg       config.Config
	mdCache   map[string][]mdLine
	hookTexts []hookText
	skills    map[string]string // slug -> description
}

type mdLine struct {
	path   string
	n      int
	text   string
	tokens []string
}

type hookText struct {
	where string
	text  string
}

// LoadExisting reads the global CLAUDE.md, settings and hooks directory.
// Per-repo files are read lazily as findings reference repos.
func LoadExisting(cfg config.Config) *Existing {
	e := &Existing{cfg: cfg, mdCache: map[string][]mdLine{}, skills: map[string]string{}}
	e.loadSettings(config.Expand(cfg.Remedies.Settings))
	e.loadSettings(filepath.Join(filepath.Dir(config.Expand(cfg.Remedies.Settings)), "settings.local.json"))
	if dir := config.Expand(cfg.Remedies.HooksDir); dir != "" {
		_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				if d != nil && d.IsDir() && (d.Name() == ".git" || d.Name() == "node_modules") {
					return filepath.SkipDir
				}
				return nil
			}
			if info, err := d.Info(); err == nil && info.Size() < 256<<10 {
				if b, err := os.ReadFile(p); err == nil {
					e.hookTexts = append(e.hookTexts, hookText{where: p, text: string(b)})
				}
			}
			return nil
		})
	}
	if dir := config.Expand(cfg.Remedies.SkillsDir); dir != "" {
		ents, _ := os.ReadDir(dir)
		for _, ent := range ents {
			if !ent.IsDir() {
				continue
			}
			desc := ""
			if b, err := os.ReadFile(filepath.Join(dir, ent.Name(), "SKILL.md")); err == nil {
				for _, l := range strings.Split(string(b), "\n") {
					if strings.HasPrefix(l, "description:") {
						desc = strings.Trim(strings.TrimSpace(strings.TrimPrefix(l, "description:")), `"'`)
						break
					}
				}
			}
			e.skills[ent.Name()] = desc
		}
	}
	return e
}

// LoadRepo adds a repo's project settings to the hook texts.
func (e *Existing) LoadRepo(repo string) {
	if repo == "" {
		return
	}
	e.loadSettings(filepath.Join(repo, ".claude", "settings.json"))
	e.loadSettings(filepath.Join(repo, ".claude", "settings.local.json"))
}

func (e *Existing) loadSettings(path string) {
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var s struct {
		Hooks map[string][]struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if json.Unmarshal(b, &s) != nil {
		return
	}
	for event, entries := range s.Hooks {
		for _, en := range entries {
			for _, h := range en.Hooks {
				e.hookTexts = append(e.hookTexts, hookText{
					where: fmt.Sprintf("%s (%s %s)", path, event, en.Matcher),
					text:  en.Matcher + "\n" + h.Command,
				})
				// Also read the script a hook runs, when it is a local file.
				if f := strings.Fields(h.Command); len(f) > 0 {
					script := config.Expand(f[len(f)-1])
					if st, err := os.Stat(script); err == nil && !st.IsDir() && st.Size() < 256<<10 {
						if b, err := os.ReadFile(script); err == nil {
							e.hookTexts = append(e.hookTexts, hookText{where: script, text: string(b)})
						}
					}
				}
			}
		}
	}
}

// ClaudeMDFiles lists the instruction files that apply to a repo.
func (e *Existing) ClaudeMDFiles(repos []string) []string {
	files := []string{config.Expand(e.cfg.Remedies.GlobalClaudeMD)}
	for _, r := range repos {
		if r == "" {
			continue
		}
		files = append(files, filepath.Join(r, "CLAUDE.md"), filepath.Join(r, ".claude", "CLAUDE.md"), filepath.Join(r, "CLAUDE.local.md"))
	}
	return files
}

func (e *Existing) lines(path string) []mdLine {
	if l, ok := e.mdCache[path]; ok {
		return l
	}
	var out []mdLine
	if b, err := os.ReadFile(path); err == nil {
		for i, l := range strings.Split(string(b), "\n") {
			if t := textutil.Tokens(l); len(t) >= 2 {
				out = append(out, mdLine{path: path, n: i + 1, text: l, tokens: t})
			}
		}
	}
	e.mdCache[path] = out
	return out
}

// CoveredByClaudeMD returns "path:line" when an existing instruction already
// says roughly the same thing as rule.
func (e *Existing) CoveredByClaudeMD(rule string, repos []string) string {
	rt := textutil.Tokens(rule)
	if len(rt) < 2 {
		return ""
	}
	rset := map[string]bool{}
	for _, w := range rt {
		rset[w] = true
	}
	for _, f := range e.ClaudeMDFiles(repos) {
		for _, l := range e.lines(f) {
			if textutil.Jaccard(rt, l.tokens) >= 0.5 {
				return fmt.Sprintf("%s:%d", l.path, l.n)
			}
			inter := 0
			seen := map[string]bool{}
			for _, w := range l.tokens {
				if rset[w] && !seen[w] {
					inter++
					seen[w] = true
				}
			}
			if len(rset) >= 3 && float64(inter)/float64(len(rset)) >= 0.8 {
				return fmt.Sprintf("%s:%d", l.path, l.n)
			}
		}
	}
	return ""
}

// CoveredByHook returns where an existing hook already mentions literal.
func (e *Existing) CoveredByHook(literal string) string {
	literal = strings.TrimSpace(literal)
	if len(literal) < 3 {
		return ""
	}
	for _, h := range e.hookTexts {
		if strings.Contains(h.text, literal) {
			return h.where
		}
	}
	return ""
}

// CoveredBySkill returns an installed skill with the same name or a similar
// description.
func (e *Existing) CoveredBySkill(slug, description string) string {
	if _, ok := e.skills[slug]; ok {
		return filepath.Join(config.Expand(e.cfg.Remedies.SkillsDir), slug)
	}
	dt := textutil.Tokens(description)
	for name, d := range e.skills {
		if textutil.Jaccard(dt, textutil.Tokens(d)) >= 0.5 {
			return filepath.Join(config.Expand(e.cfg.Remedies.SkillsDir), name)
		}
	}
	return ""
}
