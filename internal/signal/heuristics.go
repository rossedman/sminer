package signal

import (
	"path/filepath"
	"regexp"
	"strings"

	"github.com/rossedman/sminer/internal/textutil"
)

var (
	strongRe = []*regexp.Regexp{
		regexp.MustCompile(`(?i)^\s*(no|nope|nah|stop|wait|hold on)\b[\s,.!-]`),
		regexp.MustCompile(`(?i)\b(don'?t|do not|never|shouldn'?t|should not|stop)\b`),
		regexp.MustCompile(`(?i)\binstead\b`),
		regexp.MustCompile(`(?i)\bi (said|told you|asked( you)?|meant|wanted)\b`),
		regexp.MustCompile(`(?i)\b(revert|undo|roll ?back|put (it|that) back)\b`),
		regexp.MustCompile(`(?i)\b(wrong|incorrect|not what i)\b`),
		regexp.MustCompile(`(?i)\bwhy (did|are|would|do) you\b`),
		regexp.MustCompile(`(?i)\bthat'?s not\b`),
		regexp.MustCompile(`(?i)\byou (forgot|missed|broke|didn'?t|did not|ignored|removed|deleted|changed)\b`),
		regexp.MustCompile(`(?i)\bwe (use|always|never|don'?t|prefer)\b`),
		regexp.MustCompile(`(?i)\buse\b[^.?!\n]{0,60}[, ](not|instead of|rather than) \S+`),
		regexp.MustCompile(`(?i)\b(again|still)\b.{0,25}\b(broken|failing|fails|wrong|not working)\b`),
	}
	weakRe = []*regexp.Regexp{
		regexp.MustCompile(`(?i)\bactually\b`),
		regexp.MustCompile(`(?i)\bplease (use|keep|remove|fix)\b`),
		regexp.MustCompile(`(?i)\bprefer\b`),
		regexp.MustCompile(`(?i)\bshould (be|use|have)\b`),
		regexp.MustCompile(`(?i)\bremember\b`),
		regexp.MustCompile(`(?i)\balways\b`),
	}
	// Phrases that match a pattern above but are not pushback.
	benignRe = regexp.MustCompile(`(?i)^\s*(no (problem|worries|need|rush)|don'?t (worry|forget to)|never ?mind)\b|\b(don'?t|do not) (know|think so|have|mind|see why not)\b`)
)

// IsCorrection is the cheap first-pass test for a user turn that pushes back
// on what the assistant just did. It favours precision: one strong cue, or
// two weak ones. Long prompts are usually new tasks, so only their opening
// is considered.
func IsCorrection(text string) bool {
	t := strings.TrimSpace(text)
	if t == "" || strings.HasPrefix(t, "/") {
		return false
	}
	if len(t) > 800 {
		t = t[:300]
	}
	if benignRe.MatchString(t) {
		// Still allow an explicit strong cue later in the message.
		t = benignRe.ReplaceAllString(t, "")
	}
	for _, re := range strongRe {
		if re.MatchString(t) {
			return true
		}
	}
	weak := 0
	for _, re := range weakRe {
		if re.MatchString(t) {
			weak++
		}
	}
	return weak >= 2
}

var (
	hookLabelRe  = regexp.MustCompile(`\b(PreToolUse|PostToolUse|UserPromptSubmit|PreCompact)(:\w+)?`)
	hookScriptRe = regexp.MustCompile(`\[([^\]\n]+)\]`)
	numRe        = regexp.MustCompile(`\d+`)
	quotedRe     = regexp.MustCompile("[\"'`][^\"'`\n]{1,200}[\"'`]")
	pathRe       = regexp.MustCompile(`(~|\.{0,2})?/[\w./-]+`)
)

// HookKey reduces hook output to a stable identity: the event label plus the
// hook script name when present, else the message with variable parts masked.
func HookKey(text string) (key, message string) {
	label := hookLabelRe.FindString(text)
	script := ""
	if m := hookScriptRe.FindStringSubmatch(text); m != nil {
		f := strings.Fields(m[1])
		if len(f) > 0 {
			script = filepath.Base(f[0])
			if len(f) > 1 && (script == "bash" || script == "sh" || script == "python3" || script == "node" || script == "go") {
				script = filepath.Base(f[len(f)-1])
			}
		}
	}
	msg := text
	if i := strings.LastIndex(msg, "]: "); i >= 0 {
		msg = msg[i+3:]
	} else if i := strings.Index(strings.ToLower(msg), "hook error:"); i >= 0 {
		msg = msg[i+len("hook error:"):]
	}
	message = textutil.Truncate(textutil.FirstLine(msg), 200)
	if label == "" {
		label = "hook"
	}
	if script != "" {
		return label + " " + script, message
	}
	norm := strings.ToLower(message)
	norm = quotedRe.ReplaceAllString(norm, "<s>")
	norm = pathRe.ReplaceAllString(norm, "<path>")
	norm = numRe.ReplaceAllString(norm, "#")
	return label + " " + textutil.Truncate(norm, 80), message
}

// CommandKey reduces a shell command to its program and subcommand, e.g.
// "cd x && FOO=1 go test ./..." becomes "go test".
func CommandKey(cmd string) string {
	segs := regexp.MustCompile(`&&|\|\||;|\|`).Split(cmd, -1)
	seg := ""
	for _, s := range segs {
		s = strings.TrimSpace(s)
		if s != "" && !strings.HasPrefix(s, "cd ") && s != "cd" {
			seg = s
			break
		}
	}
	f := strings.Fields(seg)
	for len(f) > 0 && strings.Contains(f[0], "=") && !strings.HasPrefix(f[0], "-") {
		f = f[1:]
	}
	if len(f) == 0 {
		return ""
	}
	prog := filepath.Base(f[0])
	if len(f) > 1 && !strings.HasPrefix(f[1], "-") && !strings.ContainsAny(f[1], "/.=\"'") {
		return prog + " " + f[1]
	}
	return prog
}

// FileKey groups fix-ups by file kind: well-known basenames as-is, others by
// extension.
func FileKey(path string) string {
	base := filepath.Base(path)
	ext := filepath.Ext(base)
	if ext == "" || ext == base || len(ext) > 8 {
		return base
	}
	switch base {
	case "go.mod", "go.sum", "package.json", "package-lock.json", "yarn.lock", "pnpm-lock.yaml", "Cargo.toml", "Cargo.lock", "CLAUDE.md", "README.md":
		return base
	}
	return "*" + ext
}
