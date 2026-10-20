// Package ingest incrementally reads transcript files into the turns table.
//
// Each file is re-read only from files.last_offset. Every stored string is
// redacted first, and long text is truncated: the database holds extracts and
// pointers back into the transcript, never a copy of it.
package ingest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rossedman/sminer/internal/config"
	"github.com/rossedman/sminer/internal/redact"
	"github.com/rossedman/sminer/internal/store"
	"github.com/rossedman/sminer/internal/textutil"
	"github.com/rossedman/sminer/internal/transcript"
)

// Turn kinds.
const (
	KindUserText      = "user_text"
	KindInterrupt     = "interrupt"
	KindRejected      = "rejected"
	KindToolResult    = "tool_result"
	KindAssistantText = "assistant_text"
	KindToolUse       = "tool_use"
	KindHook          = "hook"
	KindFixup         = "fixup"
	KindCommand       = "command"
)

const (
	maxUserText   = 2000
	maxOtherText  = 600
	maxToolKey    = 300
	rejectPrefix  = "The user doesn't want to proceed with this tool use"
	interruptText = "[Request interrupted by user"
)

var (
	sysReminderRe = regexp.MustCompile(`(?s)<system-reminder>.*?</system-reminder>`)
	modifiedRe    = regexp.MustCompile(`Note: (\S+) was modified, either by the user or by a linter`)
	commandNameRe = regexp.MustCompile(`<command-name>([^<]*)</command-name>`)
	// HookRe recognises guardrail output in tool results and system events.
	// Claude Code names the hook event ("PreToolUse:Bash hook error: ...")
	// on the first line; matching only there keeps file contents and command
	// output that merely mention hooks from counting as blocks.
	HookRe = regexp.MustCompile(`(?i)(\b(PreToolUse|PostToolUse|UserPromptSubmit|PreCompact)(:\w+)?\b[^\n]{0,60}\bhook\b|\bblocked by (a |the )?[\w-]* ?hook\b)`)
	// harnessRe matches messages the app injects into the user role, such as
	// <task-notification> or <ci-monitor-event>. They start with a
	// hyphenated tag, which hand-typed prompts practically never do.
	harnessRe   = regexp.MustCompile(`^<[a-z]+(-[a-z]+)+[\s>]`)
	interruptRe = regexp.MustCompile(`^\[Request interrupted by user[^\]]*\]\s*`)
)

// IsHarnessText reports whether a user-role message was injected by the app
// rather than typed by the user.
func IsHarnessText(s string) bool {
	return harnessRe.MatchString(strings.TrimSpace(s))
}

// StripInterrupt removes a leading "[Request interrupted by user...]"
// marker, leaving whatever the user typed after it.
func StripInterrupt(s string) string {
	return strings.TrimSpace(interruptRe.ReplaceAllString(strings.TrimSpace(s), ""))
}

// IsHook checks the first line of s for guardrail output.
func IsHook(s string) bool {
	if !mentionsHook(s) {
		return false
	}
	return HookRe.MatchString(textutil.FirstLine(strings.TrimSpace(s)))
}

// Options control one ingest run.
type Options struct {
	Roots  []string
	Since  time.Duration
	Now    time.Time
	Config config.Config
	Redact *redact.Redactor
	// Workers parse files in parallel; writes stay single-threaded.
	Workers int
}

// Stats summarises one ingest run.
type Stats struct {
	Files        int
	Unchanged    int
	Skipped      int // older than the window
	Lines        int
	Malformed    int
	Turns        int
	Sessions     int
	UnknownTypes map[string]int
	Versions     map[string]int
}

type fileJob struct {
	path   string
	info   fs.FileInfo
	id     int64
	offset int64
	reset  bool
}

type fileResult struct {
	job      fileJob
	end      int64
	version  string
	turns    []store.Turn
	sessions map[string]*sessionAgg
	types    map[string]int
	stats    transcript.ScanStats
	err      error
}

type sessionAgg struct {
	repo, branch, cwd string
	start, end        time.Time
	turns             int
}

// Run ingests every *.jsonl under the roots. Each file is committed in its
// own transaction, so cancelling ctx leaves every finished file recorded and
// the rest to be picked up by the next run.
func Run(ctx context.Context, st *store.Store, opt Options) (Stats, error) {
	stats := Stats{UnknownTypes: map[string]int{}, Versions: map[string]int{}}
	if opt.Now.IsZero() {
		opt.Now = time.Now()
	}
	if opt.Workers <= 0 {
		opt.Workers = runtime.NumCPU()
	}
	var cutoff time.Time
	if opt.Since > 0 {
		cutoff = opt.Now.Add(-opt.Since)
	}

	known, err := knownFiles(st)
	if err != nil {
		return stats, err
	}
	var jobs []fileJob
	for _, root := range opt.Roots {
		root = config.Expand(root)
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				if p == root {
					return err
				}
				return nil
			}
			if d.IsDir() || !strings.HasSuffix(p, ".jsonl") {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return nil
			}
			stats.Files++
			if !cutoff.IsZero() && info.ModTime().Before(cutoff) {
				stats.Skipped++
				return nil
			}
			k, ok := known[p]
			job := fileJob{path: p, info: info}
			if ok {
				job.id = k.id
				if k.size == info.Size() && k.mtime == info.ModTime().UnixNano() {
					stats.Unchanged++
					return nil
				}
				job.offset = k.offset
				if info.Size() < k.offset {
					job.offset, job.reset = 0, true // truncated or rewritten
				}
			}
			jobs = append(jobs, job)
			return nil
		})
		if err != nil {
			return stats, fmt.Errorf("walk %s: %w", root, err)
		}
	}
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].path < jobs[j].path })

	repos := newRepoResolver()
	in := make(chan fileJob)
	out := make(chan fileResult)
	var wg sync.WaitGroup
	for range opt.Workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range in {
				out <- parseFile(j, opt, cutoff, repos)
			}
		}()
	}
	go func() {
	feed:
		for _, j := range jobs {
			select {
			case in <- j:
			case <-ctx.Done():
				break feed
			}
		}
		close(in)
		wg.Wait()
		close(out)
	}()

	sessions := map[string]bool{}
	var firstErr error
	for r := range out {
		if r.err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("%s: %w", r.job.path, r.err)
			}
			continue
		}
		if firstErr == nil && ctx.Err() != nil {
			firstErr = ctx.Err()
		}
		if firstErr != nil {
			continue
		}
		n, err := write(st, r)
		if err != nil {
			firstErr = fmt.Errorf("%s: %w", r.job.path, err)
			continue
		}
		stats.Lines += r.stats.Lines
		stats.Malformed += r.stats.Malformed
		stats.Turns += n
		for t, c := range r.types {
			if !transcript.IsKnownType(t) {
				stats.UnknownTypes[t] += c
			}
		}
		if r.version != "" {
			stats.Versions[r.version]++
		}
		for id := range r.sessions {
			sessions[id] = true
		}
	}
	stats.Sessions = len(sessions)
	if firstErr == nil {
		firstErr = ctx.Err()
	}
	return stats, firstErr
}

type knownFile struct {
	id, size, mtime, offset int64
}

func knownFiles(st *store.Store) (map[string]knownFile, error) {
	rows, err := st.DB.Query(`SELECT id, path, size, mtime, last_offset FROM files`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]knownFile{}
	for rows.Next() {
		var k knownFile
		var p string
		if err := rows.Scan(&k.id, &p, &k.size, &k.mtime, &k.offset); err != nil {
			return nil, err
		}
		out[p] = k
	}
	return out, rows.Err()
}

func parseFile(j fileJob, opt Options, cutoff time.Time, repos *repoResolver) fileResult {
	res := fileResult{job: j, sessions: map[string]*sessionAgg{}, types: map[string]int{}}
	f, err := os.Open(j.path)
	if err != nil {
		res.err = err
		return res
	}
	defer f.Close()
	if _, err := f.Seek(j.offset, io.SeekStart); err != nil {
		res.err = err
		return res
	}
	x := &extractor{
		opt:     opt,
		red:     opt.Redact,
		repos:   repos,
		fixups:  map[string]*fileState{},
		pending: map[string]pendingWrite{},
		seq:     0,
	}
	end, stats, err := transcript.Scan(f, j.offset, func(ev transcript.Event) error {
		res.types[ev.Type]++
		if ev.Version != "" {
			res.version = ev.Version
		}
		if !cutoff.IsZero() && !ev.Timestamp.IsZero() && ev.Timestamp.Before(cutoff) {
			return nil
		}
		if ev.SessionID == "" {
			return nil
		}
		repo := ""
		if ev.Cwd != "" {
			repo = repos.resolve(ev.Cwd)
			if opt.Config.RepoExcluded(repo) {
				return nil
			}
		}
		turns := x.extract(ev)
		agg := res.sessions[ev.SessionID]
		if agg == nil {
			agg = &sessionAgg{}
			res.sessions[ev.SessionID] = agg
		}
		if agg.cwd == "" && ev.Cwd != "" {
			agg.cwd, agg.repo, agg.branch = ev.Cwd, repo, ev.GitBranch
		}
		if !ev.Timestamp.IsZero() {
			if agg.start.IsZero() || ev.Timestamp.Before(agg.start) {
				agg.start = ev.Timestamp
			}
			if ev.Timestamp.After(agg.end) {
				agg.end = ev.Timestamp
			}
		}
		agg.turns += len(turns)
		res.turns = append(res.turns, turns...)
		return nil
	})
	res.end, res.stats, res.err = end, stats, err
	return res
}

func write(st *store.Store, r fileResult) (int, error) {
	tx, err := st.DB.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	id := r.job.id
	if id == 0 {
		if err := tx.QueryRow(`INSERT INTO files(path) VALUES(?) RETURNING id`, r.job.path).Scan(&id); err != nil {
			return 0, err
		}
	} else if r.job.reset {
		if _, err := tx.Exec(`DELETE FROM turns WHERE file_id=?`, id); err != nil {
			return 0, err
		}
	}
	var seqBase int64
	_ = tx.QueryRow(`SELECT lines FROM files WHERE id=?`, id).Scan(&seqBase)
	if r.job.reset {
		seqBase = 0
	}
	ins, err := tx.Prepare(`INSERT OR IGNORE INTO turns(id, session_id, event_uuid, parent_uuid, seq, ts, role, kind, text,
 tool_name, tool_key, tool_use_id, is_error, sidechain, file_id, file_offset) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return 0, err
	}
	defer ins.Close()
	n := 0
	for _, t := range r.turns {
		res, err := ins.Exec(t.ID, t.SessionID, t.EventUUID, t.ParentUUID, seqBase+t.Seq, store.Ms(t.TS), t.Role, t.Kind,
			t.Text, t.ToolName, t.ToolKey, t.ToolUseID, b2i(t.IsError), b2i(t.Sidechain), id, t.FileOffset)
		if err != nil {
			return 0, err
		}
		if c, _ := res.RowsAffected(); c > 0 {
			n++
		}
	}
	for sid, a := range r.sessions {
		if a.turns == 0 && a.cwd == "" {
			continue
		}
		_, err := tx.Exec(`INSERT INTO sessions(session_id, repo, branch, cwd, started_at, ended_at, turns, dirty)
VALUES(?,?,?,?,?,?,?,1)
ON CONFLICT(session_id) DO UPDATE SET
 repo=CASE WHEN sessions.repo='' THEN excluded.repo ELSE sessions.repo END,
 branch=CASE WHEN sessions.branch='' THEN excluded.branch ELSE sessions.branch END,
 cwd=CASE WHEN sessions.cwd='' THEN excluded.cwd ELSE sessions.cwd END,
 started_at=CASE WHEN sessions.started_at=0 OR (excluded.started_at>0 AND excluded.started_at<sessions.started_at) THEN excluded.started_at ELSE sessions.started_at END,
 ended_at=MAX(sessions.ended_at, excluded.ended_at),
 turns=sessions.turns+excluded.turns,
 dirty=1`, sid, a.repo, a.branch, a.cwd, store.Ms(a.start), store.Ms(a.end), a.turns)
		if err != nil {
			return 0, err
		}
	}
	for t, c := range r.types {
		if _, err := tx.Exec(`INSERT INTO event_types(type, count, known) VALUES(?,?,?)
ON CONFLICT(type) DO UPDATE SET count=count+excluded.count`, t, c, b2i(transcript.IsKnownType(t))); err != nil {
			return 0, err
		}
	}
	_, err = tx.Exec(`UPDATE files SET size=?, mtime=?, last_offset=?, schema_version=CASE WHEN ?='' THEN schema_version ELSE ? END,
 lines=?, malformed=malformed+? WHERE id=?`,
		r.job.info.Size(), r.job.info.ModTime().UnixNano(), r.end, r.version, r.version,
		seqBase+int64(r.stats.Lines), r.stats.Malformed, id)
	if err != nil {
		return 0, err
	}
	return n, tx.Commit()
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// extractor turns events into stored turns, tracking per-file state for
// manual fix-up detection.
type extractor struct {
	opt     Options
	red     *redact.Redactor
	repos   *repoResolver
	fixups  map[string]*fileState
	pending map[string]pendingWrite
	seq     int64
}

type fileState struct {
	content  string
	known    bool
	expected []string
}

type pendingWrite struct {
	tool  string
	input json.RawMessage
}

func (x *extractor) turn(ev transcript.Event, idx int, kind string) store.Turn {
	id := ev.UUID
	if id == "" {
		id = textutil.Hash(16, ev.SessionID, fmt.Sprint(ev.Offset))
	}
	return store.Turn{
		ID:         fmt.Sprintf("%s#%d", id, idx),
		SessionID:  ev.SessionID,
		EventUUID:  ev.UUID,
		ParentUUID: ev.ParentUUID,
		Seq:        x.seq,
		TS:         ev.Timestamp,
		Role:       ev.Role,
		Kind:       kind,
		Sidechain:  ev.IsSidechain,
		FileOffset: ev.Offset,
	}
}

// clean redacts and truncates. Only a prefix a little longer than the kept
// text is redacted, so a secret straddling the cut is still masked without
// scanning megabytes of tool output.
func (x *extractor) clean(s string, n int) string {
	if len(s) > n+redactMargin {
		s = textutil.Truncate(s, n+redactMargin)
	}
	return textutil.Truncate(x.red.String(s), n)
}

// redactMargin exceeds the longest single-token secret the patterns match.
const redactMargin = 512

// mentionsHook is a cheap pre-check before running HookRe.
func mentionsHook(s string) bool {
	return strings.Contains(s, "hook") || strings.Contains(s, "Hook") || strings.Contains(s, "HOOK")
}

// head bounds how much of a tool result pattern checks look at.
func head(s string) string {
	if len(s) > 4096 {
		return s[:4096]
	}
	return s
}

func (x *extractor) extract(ev transcript.Event) []store.Turn {
	x.seq++
	var out []store.Turn
	switch ev.Type {
	case "user":
		for i, b := range ev.Blocks {
			switch b.Type {
			case "text":
				if m := modifiedRe.FindStringSubmatch(head(b.Text)); m != nil {
					t := x.turn(ev, 100+i, KindFixup)
					t.ToolKey = x.clean(m[1], maxToolKey)
					t.Text = "file modified outside Claude (user or linter)"
					out = append(out, t)
				}
				if ev.IsMeta {
					continue
				}
				text := strings.TrimSpace(sysReminderRe.ReplaceAllString(b.Text, ""))
				if text == "" || strings.Contains(text, "<local-command-stdout>") || strings.HasPrefix(text, "Caveat: ") || IsHarnessText(text) {
					continue
				}
				if m := commandNameRe.FindStringSubmatch(text); m != nil {
					t := x.turn(ev, i, KindCommand)
					t.ToolKey = x.clean(m[1], maxToolKey)
					t.Role = "user"
					out = append(out, t)
					continue
				}
				kind := KindUserText
				if strings.HasPrefix(text, interruptText) {
					kind = KindInterrupt
					text = StripInterrupt(text)
				}
				t := x.turn(ev, i, kind)
				t.Role = "user"
				t.Text = x.clean(text, maxUserText)
				out = append(out, t)
			case "tool_result":
				out = append(out, x.toolResult(ev, i, b)...)
			}
		}
	case "assistant":
		for i, b := range ev.Blocks {
			switch b.Type {
			case "text":
				if strings.TrimSpace(b.Text) == "" {
					continue
				}
				t := x.turn(ev, i, KindAssistantText)
				t.Role = "assistant"
				t.Text = x.clean(b.Text, maxOtherText)
				out = append(out, t)
			case "tool_use":
				t := x.turn(ev, i, KindToolUse)
				t.Role = "assistant"
				t.ToolName = b.ToolName
				t.ToolUseID = b.ToolUseID
				key, summary := toolInputSummary(b.ToolName, b.Input)
				t.ToolKey = x.clean(key, maxToolKey)
				t.Text = x.clean(summary, maxOtherText)
				out = append(out, t)
				if (isWriteTool(b.ToolName) || b.ToolName == "Bash") && b.ToolUseID != "" {
					x.pending[b.ToolUseID] = pendingWrite{tool: b.ToolName, input: b.Input}
				}
			}
		}
	case "system":
		if IsHook(head(ev.Text)) {
			t := x.turn(ev, 0, KindHook)
			t.Role = "system"
			t.Text = x.clean(ev.Text, maxOtherText)
			t.IsError = true
			out = append(out, t)
		}
	case "attachment":
		var a struct {
			Type     string `json:"type"`
			Filename string `json:"filename"`
		}
		if json.Unmarshal(ev.Attachment, &a) == nil && a.Type == "edited_text_file" && a.Filename != "" {
			t := x.turn(ev, 0, KindFixup)
			t.ToolKey = x.clean(a.Filename, maxToolKey)
			t.Text = "file modified outside Claude (user or linter)"
			out = append(out, t)
		}
	}
	return out
}

func (x *extractor) toolResult(ev transcript.Event, i int, b transcript.Block) []store.Turn {
	text := b.Text
	t := x.turn(ev, i, KindToolResult)
	t.Role = "user"
	t.ToolUseID = b.ToolUseID
	t.IsError = b.IsError
	switch {
	case strings.HasPrefix(text, rejectPrefix):
		t.Kind = KindRejected
		if k := strings.Index(text, "the user said:"); k >= 0 {
			text = strings.TrimSpace(text[k+len("the user said:"):])
		} else {
			text = ""
		}
		t.Text = x.clean(text, maxUserText)
	case strings.HasPrefix(strings.TrimSpace(text), interruptText):
		t.Kind = KindInterrupt
		t.Text = ""
	case IsHook(head(text)):
		t.Kind = KindHook
		t.IsError = true
		t.Text = x.clean(text, maxOtherText)
	case b.IsError:
		t.Text = x.clean(text, maxOtherText)
	default:
		// Successful tool output is never used downstream; don't keep it.
	}
	out := []store.Turn{t}
	if fx, ok := x.trackFixup(ev, b); ok {
		f := x.turn(ev, 200+i, KindFixup)
		f.ToolKey = x.clean(fx, maxToolKey)
		f.Text = "file content changed after Claude wrote it"
		out = append(out, f)
	}
	return out
}

func isWriteTool(name string) bool {
	return name == "Write" || name == "Edit" || name == "MultiEdit"
}

type editInput struct {
	FilePath   string `json:"file_path"`
	Content    string `json:"content"`
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all"`
	Edits      []struct {
		OldString  string `json:"old_string"`
		NewString  string `json:"new_string"`
		ReplaceAll bool   `json:"replace_all"`
	} `json:"edits"`
}

// trackFixup applies Claude's successful writes to a model of each file and
// reports a path when a later full Read shows content Claude did not write.
func (x *extractor) trackFixup(ev transcript.Event, b transcript.Block) (string, bool) {
	if pw, ok := x.pending[b.ToolUseID]; ok {
		delete(x.pending, b.ToolUseID)
		if pw.tool == "Bash" {
			// A failed command may still have written before it failed.
			var in struct {
				Command string `json:"command"`
			}
			if json.Unmarshal(pw.input, &in) == nil {
				for p := range x.fixups {
					if BashMayWrite(in.Command, p) {
						delete(x.fixups, p)
					}
				}
			}
			return "", false
		}
		if b.IsError {
			return "", false
		}
		var in editInput
		if json.Unmarshal(pw.input, &in) != nil || in.FilePath == "" {
			return "", false
		}
		fsx := x.fixups[in.FilePath]
		if fsx == nil {
			fsx = &fileState{}
			x.fixups[in.FilePath] = fsx
		}
		switch pw.tool {
		case "Write":
			fsx.content, fsx.known, fsx.expected = in.Content, true, nil
		case "Edit":
			fsx.apply(in.OldString, in.NewString, in.ReplaceAll)
		case "MultiEdit":
			for _, e := range in.Edits {
				fsx.apply(e.OldString, e.NewString, e.ReplaceAll)
			}
		}
		return "", false
	}
	if len(ev.ToolUseResult) == 0 || ev.ToolUseResult[0] != '{' {
		return "", false
	}
	var r struct {
		File *struct {
			FilePath   string `json:"filePath"`
			Content    string `json:"content"`
			NumLines   int    `json:"numLines"`
			StartLine  int    `json:"startLine"`
			TotalLines int    `json:"totalLines"`
		} `json:"file"`
	}
	if json.Unmarshal(ev.ToolUseResult, &r) != nil || r.File == nil || r.File.FilePath == "" {
		return "", false
	}
	full := r.File.StartLine <= 1 && r.File.NumLines >= r.File.TotalLines
	fsx := x.fixups[r.File.FilePath]
	changed := false
	if fsx != nil && full {
		got := squash(r.File.Content)
		if fsx.known {
			changed = got != squash(fsx.content)
		} else {
			for _, e := range fsx.expected {
				if !strings.Contains(got, squash(e)) {
					changed = true
					break
				}
			}
		}
	}
	if full {
		x.fixups[r.File.FilePath] = &fileState{content: r.File.Content, known: true}
	}
	if changed {
		return r.File.FilePath, true
	}
	return "", false
}

// rewriteRe matches commands that rewrite files they may not name: in-place
// formatters and fixers, and git operations that touch the working tree.
var rewriteRe = regexp.MustCompile(`(?:^|[\s;&|(` + "`" + `])(?:` +
	`(?:gofmt|goimports|gofumpt|shfmt)\s[^;&|]*-[a-z]*w|go\s+fmt|golangci-lint\s[^;&|]*--fix|` +
	`prettier\s[^;&|]*(?:--write|-w\b)|eslint\s[^;&|]*--fix|biome\s+(?:format|check)\s[^;&|]*--write|` +
	`(?:sed|perl)\s[^;&|]*-[a-zA-Z]*i|clang-format\s[^;&|]*-i|autopep8\s[^;&|]*-i|` +
	`black|isort|ruff\s+(?:format|check\s[^;&|]*--fix)|rustfmt|cargo\s+(?:fmt|clippy\s[^;&|]*--fix)|` +
	`terraform\s+fmt|swiftformat|(?:npm|pnpm|yarn)\s+(?:run\s+)?(?:format|fmt|lint:fix)|make\s+(?:fmt|format)|` +
	`git\s+(?:checkout|restore|reset\s[^;&|]*--hard|stash|apply|am|merge|rebase|pull|cherry-pick|revert)|patch)\b`)

// BashMayWrite reports whether a shell command plausibly rewrote path: it
// names the file, or runs a rewriting tool over a directory holding it. It
// errs towards yes, since missing a fix-up is cheaper than blaming the user
// for Claude's own edit.
func BashMayWrite(cmd, path string) bool {
	if cmd == "" || path == "" {
		return false
	}
	if strings.Contains(cmd, path) {
		return true
	}
	if base := filepath.Base(path); base != "." && base != "/" && strings.Contains(cmd, base) {
		return true
	}
	if !rewriteRe.MatchString(cmd) {
		return false
	}
	dir := "/" + strings.Trim(filepath.ToSlash(filepath.Dir(path)), "/") + "/"
	targets := 0
	for _, f := range strings.Fields(cmd) {
		f = strings.Trim(f, `"'();`)
		if strings.HasPrefix(f, "-") || strings.ContainsAny(f, "<>=$") ||
			!(strings.Contains(f, "/") || strings.HasPrefix(f, ".") || strings.ContainsAny(f, "*?[")) {
			continue
		}
		targets++
		if k := strings.IndexAny(f, "*?["); k >= 0 {
			f = f[:k]
		}
		f = strings.TrimSuffix(f, "...")
		f = strings.Trim(strings.TrimPrefix(f, "./"), "/")
		if f == "" || f == "." || strings.HasPrefix(f, "..") || strings.Contains(dir, "/"+f+"/") {
			return true
		}
	}
	// No path arguments: the tool runs over the working directory.
	return targets == 0
}

func (f *fileState) apply(old, new string, all bool) {
	if f.known {
		n := 1
		if all {
			n = -1
		}
		f.content = strings.Replace(f.content, old, new, n)
		return
	}
	if len(strings.TrimSpace(new)) >= 16 {
		f.expected = append(f.expected, new)
	}
}

// squash removes all whitespace so formatter-only changes do not count as a
// manual fix-up.
func squash(s string) string {
	return strings.Join(strings.Fields(s), "")
}

// toolInputSummary returns a key (command, path, pattern) and a short
// description of a tool call's input.
func toolInputSummary(name string, raw json.RawMessage) (string, string) {
	var in map[string]any
	if json.Unmarshal(raw, &in) != nil {
		return "", ""
	}
	str := func(k string) string {
		v, _ := in[k].(string)
		return v
	}
	switch name {
	case "Bash":
		return textutil.OneLine(str("command")), str("description")
	case "Edit", "MultiEdit":
		return str("file_path"), textutil.Truncate(str("new_string"), 200)
	case "Write":
		return str("file_path"), fmt.Sprintf("write %d bytes", len(str("content")))
	case "Read", "NotebookEdit":
		if p := str("file_path"); p != "" {
			return p, ""
		}
		return str("notebook_path"), ""
	case "Grep", "Glob":
		return str("pattern"), str("path")
	case "WebFetch":
		return str("url"), ""
	case "Task", "Agent":
		return str("description"), textutil.Truncate(str("prompt"), 200)
	}
	for _, k := range []string{"file_path", "path", "command", "query", "url"} {
		if v := str(k); v != "" {
			return v, ""
		}
	}
	return "", ""
}

// repoResolver maps a cwd to its git toplevel when that still exists on disk.
type repoResolver struct {
	mu    sync.Mutex
	cache map[string]string
}

func newRepoResolver() *repoResolver { return &repoResolver{cache: map[string]string{}} }

func (r *repoResolver) resolve(cwd string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if v, ok := r.cache[cwd]; ok {
		return v
	}
	repo := cwd
	for d := filepath.Clean(cwd); ; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			repo = d
			break
		}
		if d == filepath.Dir(d) {
			break
		}
	}
	r.cache[cwd] = repo
	return repo
}
