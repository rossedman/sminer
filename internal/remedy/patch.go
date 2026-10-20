package remedy

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/rossedman/sminer/internal/config"
	"github.com/rossedman/sminer/internal/store"
)

// Errors returned by BuildPatch. All are preconditions the user can fix.
var (
	ErrNotPatchable = errors.New("nothing to patch")
	ErrNoRemedy     = errors.New("no remedy yet")
	ErrTargetExists = errors.New("target already exists")
)

// Section heading sminer appends CLAUDE.md lines under.
const mdSection = "## Learned conventions"

// Patch renders a finding's proposal as a unified diff that `patch -p1`
// (or `git apply`) applies from Base. It reads the current target, never
// writes it.
type Patch struct {
	Base  string // directory to apply from
	Diff  string // full patch file contents, header included
	Extra string // follow-up the patch cannot express (settings.json entry)
}

// BuildPatch renders the patch for a finding.
func BuildPatch(f store.Finding, cfg config.Config) (Patch, error) {
	p := DecodeProposal(f.Proposal)
	switch f.RemedyType {
	case ClaudeMD:
		return claudeMDPatch(f, p)
	case Hook:
		return hookPatch(f, p, cfg)
	case Skill:
		return skillPatch(f, p)
	case Ignore:
		return Patch{}, fmt.Errorf("%w: finding %s is routed to ignore (%s)", ErrNotPatchable, f.ID, p.Why)
	}
	return Patch{}, fmt.Errorf("%w: finding %s has no remedy yet", ErrNoRemedy, f.ID)
}

func header(f store.Finding, base string, extra string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "sminer %s: %s\n", f.ID, f.Title)
	fmt.Fprintf(&sb, "Remedy: %s\n", f.RemedyType)
	fmt.Fprintf(&sb, "Apply:  cd %s && patch -p1 < THIS_FILE\n", base)
	if extra != "" {
		sb.WriteString("\n" + extra + "\n")
	}
	sb.WriteString("\n")
	return sb.String()
}

func claudeMDPatch(f store.Finding, p Proposal) (Patch, error) {
	return ClaudeMDPatch([]store.Finding{f})
}

// ClaudeMDPatch renders one patch that appends every finding's rule to
// their shared target file, so several CLAUDE.md lines for the same file can
// be applied together. All findings must be claude_md with the same target.
func ClaudeMDPatch(fs []store.Finding) (Patch, error) {
	if len(fs) == 0 {
		return Patch{}, ErrNotPatchable
	}
	path := fs[0].Target
	for _, f := range fs {
		if f.Target == "" {
			return Patch{}, fmt.Errorf("%w: finding %s has no target file", ErrNoRemedy, f.ID)
		}
		if f.RemedyType != ClaudeMD || f.Target != path {
			return Patch{}, fmt.Errorf("%w: finding %s is not a CLAUDE.md line for %s", ErrNotPatchable, f.ID, path)
		}
	}
	base := baseFor(path)
	rel, _ := filepath.Rel(base, path)
	old := ""
	if b, err := os.ReadFile(path); err == nil {
		old = string(b)
	} else if !errors.Is(err, os.ErrNotExist) {
		return Patch{}, err
	}
	var add []string
	if !strings.Contains(old, mdSection) {
		if strings.TrimSpace(old) != "" {
			add = append(add, "")
		}
		add = append(add, mdSection, "")
	}
	for _, f := range fs {
		add = append(add, "- "+DecodeProposal(f.Proposal).Rule)
	}
	diff := AppendDiff(rel, old, add, old != "" || fileExists(path))
	hdr := header(fs[0], base, "")
	if len(fs) > 1 {
		hdr = combinedHeader(fs, base)
	}
	return Patch{Base: base, Diff: hdr + diff}, nil
}

func combinedHeader(fs []store.Finding, base string) string {
	var sb strings.Builder
	ids := make([]string, len(fs))
	for i, f := range fs {
		ids[i] = f.ID
	}
	fmt.Fprintf(&sb, "sminer %s: %d CLAUDE.md lines\n", strings.Join(ids, ", "), len(fs))
	for _, f := range fs {
		fmt.Fprintf(&sb, "  %s %s\n", f.ID, f.Title)
	}
	fmt.Fprintf(&sb, "Remedy: %s\n", ClaudeMD)
	fmt.Fprintf(&sb, "Apply:  cd %s && patch -p1 < THIS_FILE\n\n", base)
	return sb.String()
}

func hookPatch(f store.Finding, p Proposal, cfg config.Config) (Patch, error) {
	path := f.Target
	base := filepath.Dir(path)
	if fileExists(path) {
		return Patch{}, fmt.Errorf("%w: %s", ErrTargetExists, path)
	}
	what := map[string]string{"file_path": "edits to files matching", "command": "shell commands matching"}[p.Field]
	script := strings.NewReplacer(
		"{{ID}}", f.ID,
		"{{TITLE}}", f.Title,
		"{{WHAT}}", what,
		"{{PATTERN}}", p.Pattern,
		"{{FIELD}}", p.Field,
		"{{QPATTERN}}", shellQuote(p.Pattern),
		"{{QMSG}}", shellQuote("Blocked by sminer hook: "+p.Rule),
	).Replace(hookTemplate)

	settings := fmt.Sprintf(`Then register it in %s (merge into "hooks"):
  "PreToolUse": [
    {"matcher": %q, "hooks": [{"type": "command", "command": %q}]}
  ]`, cfg.Remedies.Settings, p.Matcher, path)
	diff := NewFileDiff(filepath.Base(path), script, true)
	return Patch{Base: base, Diff: header(f, base, settings) + diff, Extra: settings}, nil
}

func skillPatch(f store.Finding, p Proposal) (Patch, error) {
	path := f.Target
	if fileExists(path) {
		return Patch{}, fmt.Errorf("%w: %s", ErrTargetExists, path)
	}
	base := filepath.Dir(filepath.Dir(path))
	var sb strings.Builder
	fmt.Fprintf(&sb, "---\nname: %s\ndescription: %q\n---\n\n# %s\n\n", p.SkillName, p.Description, p.Rule)
	sb.WriteString("Drafted by sminer from instructions you repeated across sessions. Review and edit before use.\n\n## Steps\n\n")
	for i, s := range p.Steps {
		fmt.Fprintf(&sb, "%d. %s\n", i+1, s)
	}
	rel := filepath.Join(p.SkillName, "SKILL.md")
	return Patch{Base: base, Diff: header(f, base, "") + NewFileDiff(rel, sb.String(), false)}, nil
}

// baseFor picks where a CLAUDE.md patch is applied from: the repo for a
// repo file, the home directory for the global one.
func baseFor(path string) string {
	home := config.Expand("~")
	dir := filepath.Dir(path)
	if filepath.Base(dir) == ".claude" {
		dir = filepath.Dir(dir)
	}
	if dir == home || strings.HasPrefix(path, filepath.Join(home, ".claude")+string(filepath.Separator)) {
		return home
	}
	return dir
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// AppendDiff is a unified diff that appends lines to the end of old.
func AppendDiff(rel, old string, add []string, exists bool) string {
	if !exists {
		return NewFileDiff(rel, strings.Join(add, "\n")+"\n", false)
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "--- a/%s\n+++ b/%s\n", rel, rel)
	if old == "" {
		fmt.Fprintf(&sb, "@@ -0,0 +1,%d @@\n", len(add))
		for _, l := range add {
			sb.WriteString("+" + l + "\n")
		}
		return sb.String()
	}
	trailing := strings.HasSuffix(old, "\n")
	lines := strings.Split(strings.TrimSuffix(old, "\n"), "\n")
	ctx := lines[max(0, len(lines)-3):]
	start := len(lines) - len(ctx) + 1
	newCount := len(ctx) + len(add)
	fmt.Fprintf(&sb, "@@ -%d,%d +%d,%d @@\n", start, len(ctx), start, newCount)
	for i, l := range ctx {
		if i == len(ctx)-1 && !trailing {
			sb.WriteString("-" + l + "\n\\ No newline at end of file\n+" + l + "\n")
			continue
		}
		sb.WriteString(" " + l + "\n")
	}
	for _, l := range add {
		sb.WriteString("+" + l + "\n")
	}
	return sb.String()
}

// NewFileDiff is a git-style diff creating rel with content.
func NewFileDiff(rel, content string, executable bool) string {
	mode := "100644"
	if executable {
		mode = "100755"
	}
	lines := strings.Split(strings.TrimSuffix(content, "\n"), "\n")
	var sb strings.Builder
	fmt.Fprintf(&sb, "diff --git a/%s b/%s\nnew file mode %s\n--- /dev/null\n+++ b/%s\n@@ -0,0 +1,%d @@\n", rel, rel, mode, rel, len(lines))
	for _, l := range lines {
		sb.WriteString("+" + l + "\n")
	}
	return sb.String()
}

const hookTemplate = `#!/usr/bin/env bash
# Generated by sminer for finding {{ID}}:
#   {{TITLE}}
# Claude Code PreToolUse hook. Blocks {{WHAT}}:
#   {{PATTERN}}
# Exit code 2 blocks the tool call and shows stderr to Claude.
set -uo pipefail

input="$(cat)"
value="$(printf '%s' "$input" | { jq -r '.tool_input.{{FIELD}} // empty' 2>/dev/null ||
  python3 -c 'import json,sys; print(json.load(sys.stdin).get("tool_input",{}).get("{{FIELD}}",""))'; })"

if printf '%s' "$value" | grep -Eq -- {{QPATTERN}}; then
  echo {{QMSG}} >&2
  exit 2
fi
exit 0
`
