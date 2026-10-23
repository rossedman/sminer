package report

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rossedman/sminer/internal/remedy"
	"github.com/rossedman/sminer/internal/store"
)

var now = time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC) // a Monday

func newStore(t testing.TB) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func exec(t testing.TB, st *store.Store, q string, args ...any) {
	t.Helper()
	if _, err := st.DB.Exec(q, args...); err != nil {
		t.Fatal(err)
	}
}

// seed creates one open hook finding with n signals over n sessions, one
// ignored finding and one unheeded finding.
func seed(t testing.TB, st *store.Store, n int) {
	t.Helper()
	for i := range n {
		sid := fmt.Sprintf("sess-%d", i)
		ts := now.Add(-time.Duration(i+1) * 24 * time.Hour)
		exec(t, st, `INSERT INTO sessions(session_id, repo, cwd, started_at, ended_at, turns) VALUES(?,?,?,?,?,5)`, sid, "/home/me/code/acme/api", "/home/me/code/acme/api", store.Ms(ts), store.Ms(ts))
		exec(t, st, `INSERT INTO signals(id, session_id, repo, type, weight, ts, excerpt, key) VALUES(?,?,?,?,?,?,?,?)`,
			"sg"+sid, sid, "/home/me/code/acme/api", store.HookBlock, 2, store.Ms(ts), "no editing /home/me/code/acme/api/go.sum", "PreToolUse:Edit g.sh")
		exec(t, st, `INSERT INTO signals(id, session_id, repo, type, weight, ts, excerpt, key) VALUES(?,?,?,?,?,?,?,?)`,
			"fx"+sid, sid, "/home/me/code/acme/web", store.ManualFixup, 1, store.Ms(ts), "main.go changed", "*.go")
	}
	exec(t, st, `INSERT INTO signals(id, session_id, repo, type, weight, ts, excerpt, confirmed) VALUES('rej','sess-0','/home/me/code/acme/api','correction',3,?,'rejected one',0)`, store.Ms(now.Add(-time.Hour)))
	exec(t, st, `INSERT INTO event_types(type, count, known) VALUES('weird', 4, 0), ('user', 9, 1)`)

	save := func(f store.Finding, sigPrefix string) {
		t.Helper()
		if err := st.SaveFinding(f); err != nil {
			t.Fatal(err)
		}
		for i := range n {
			exec(t, st, `INSERT INTO finding_signals VALUES(?,?)`, f.ID, fmt.Sprintf("%ssess-%d", sigPrefix, i))
		}
	}
	hook := remedy.Proposal{Rule: "Never edit go.sum", Matcher: "Edit", Field: "file_path", Pattern: `go\.sum$`}
	save(store.Finding{ID: "F-hook", Title: "Hand edits to go.sum", Family: "hook", Key: "PreToolUse:Edit g.sh", RemedyType: remedy.Hook,
		Proposal: marshal(hook), Target: "/home/me/hooks/no-go-sum.sh", Status: store.StatusOpen}, "sg")
	skill := remedy.Proposal{SkillName: "release", Description: "Cut a release", Steps: []string{"tag", "push"}}
	save(store.Finding{ID: "F-skill", Title: "Release procedure", Family: "fixup", Key: "edits", RemedyType: remedy.Skill,
		Proposal: marshal(skill), Status: store.StatusAccepted}, "fx")
	exec(t, st, `INSERT INTO adoptions(finding_id, adopted_at) VALUES('F-skill', ?)`, store.Ms(now.Add(-48*time.Hour)))
	save(store.Finding{ID: "F-md", Title: "Pipe | in title", Family: "behavior", RemedyType: remedy.ClaudeMD,
		Proposal: marshal(remedy.Proposal{Rule: "Use pnpm"}), Status: store.StatusOpen}, "sg")
	save(store.Finding{ID: "F-ign", Title: "One-off", Family: "behavior", RemedyType: remedy.Ignore, CoveredBy: "/r/CLAUDE.md:3",
		Proposal: marshal(remedy.Proposal{Why: "already covered"}), Status: store.StatusOpen}, "sg")
	save(store.Finding{ID: "F-unh", Title: "Not followed", Family: "behavior", RemedyType: remedy.Ignore,
		Proposal: marshal(remedy.Proposal{Unheeded: &remedy.Unheeded{Where: "/r/CLAUDE.md:9", Line: "- be nice", Since: 4}}), Status: store.StatusOpen}, "sg")
	save(store.Finding{ID: "F-dis", Title: "Dismissed", Family: "behavior", RemedyType: remedy.ClaudeMD, Status: store.StatusDismissed}, "sg")
}

func marshal(p remedy.Proposal) string {
	b, _ := json.Marshal(p)
	return string(b)
}

func TestBuild(t *testing.T) {
	t.Parallel()
	st := newStore(t)
	seed(t, st, 4)
	r, err := Build(st, Options{Since: now.Add(-30 * 24 * time.Hour), Now: now, MinCount: 3})
	if err != nil {
		t.Fatal(err)
	}
	if !r.GeneratedAt.Equal(now) || r.Sessions != 4 {
		t.Errorf("header = %v sessions %d", r.GeneratedAt, r.Sessions)
	}
	if r.SignalCounts[store.HookBlock] != 4 || r.SignalCounts[store.ManualFixup] != 4 || r.SignalCounts[store.Correction] != 0 {
		t.Errorf("SignalCounts = %v (the rejected correction must not count)", r.SignalCounts)
	}
	if len(r.Guardrails) != 1 || r.Guardrails[0] != (GuardrailCount{"PreToolUse:Edit g.sh", 4}) {
		t.Errorf("Guardrails = %v", r.Guardrails)
	}
	if r.UnknownTypes["weird"] != 4 || len(r.UnknownTypes) != 1 {
		t.Errorf("UnknownTypes = %v", r.UnknownTypes)
	}
	// F-hook (score 4*2*1=8), F-skill (4*1*1... fixup repo web=4), F-md and the others link the same hook signals.
	ids := map[string]Finding{}
	for _, f := range r.Findings {
		ids[f.ID] = f
	}
	for _, want := range []string{"F-hook", "F-skill", "F-md"} {
		if _, ok := ids[want]; !ok {
			t.Errorf("missing finding %s in %v", want, ids)
		}
	}
	if _, dismissed := ids["F-dis"]; dismissed {
		t.Error("dismissed findings are excluded")
	}
	if len(r.Ignored) != 1 || r.Ignored[0].ID != "F-ign" || len(r.Unheeded) != 1 || r.Unheeded[0].ID != "F-unh" {
		t.Errorf("ignored = %v unheeded = %v", r.Ignored, r.Unheeded)
	}
	h := ids["F-hook"]
	if h.Occurrences != 4 || h.Sessions != 4 || h.Score != 8 || h.Remedy != remedy.Hook || h.Proposal.Pattern != `go\.sum$` ||
		h.Target != "/home/me/hooks/no-go-sum.sh" || len(h.Excerpts) != 2 || h.AdoptedAt != nil || len(h.Repos) != 1 {
		t.Errorf("hook finding = %+v", h)
	}
	if !h.FirstSeen.Before(h.LastSeen) {
		t.Errorf("first/last = %v / %v", h.FirstSeen, h.LastSeen)
	}
	s := ids["F-skill"]
	if s.AdoptedAt == nil || !s.AdoptedAt.Equal(now.Add(-48*time.Hour)) || s.FileKinds["*.go"] != 4 {
		t.Errorf("skill finding = %+v", s)
	}
	// Ranked by score, highest first, ties by id.
	for i := 1; i < len(r.Findings); i++ {
		a, b := r.Findings[i-1], r.Findings[i]
		if a.Score < b.Score || (a.Score == b.Score && a.ID > b.ID) {
			t.Errorf("not ranked: %s (%v) before %s (%v)", a.ID, a.Score, b.ID, b.Score)
		}
	}
}

func TestBuildFilters(t *testing.T) {
	t.Parallel()
	st := newStore(t)
	seed(t, st, 4)
	t.Run("min count", func(t *testing.T) {
		t.Parallel()
		r, err := Build(st, Options{Since: now.Add(-30 * 24 * time.Hour), Now: now, MinCount: 5})
		if err != nil || len(r.Findings) != 0 || len(r.Ignored) != 0 {
			t.Errorf("%v %+v", err, r)
		}
	})
	t.Run("window", func(t *testing.T) {
		t.Parallel()
		// Only the two most recent days of signals are in scope.
		r, err := Build(st, Options{Since: now.Add(-60 * time.Hour), Now: now, MinCount: 1})
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range r.Findings {
			if f.Occurrences > 2 {
				t.Errorf("%s has %d occurrences in a 2-day window", f.ID, f.Occurrences)
			}
		}
		if r.Sessions != 2 {
			t.Errorf("Sessions = %d, want 2", r.Sessions)
		}
	})
	t.Run("repo", func(t *testing.T) {
		t.Parallel()
		r, err := Build(st, Options{Since: now.Add(-30 * 24 * time.Hour), Now: now, MinCount: 1, Repo: "/home/me/code/acme/web"})
		if err != nil {
			t.Fatal(err)
		}
		if r.Repo != "/home/me/code/acme/web" || r.SignalCounts[store.HookBlock] != 0 || r.SignalCounts[store.ManualFixup] != 4 {
			t.Errorf("repo scope: %+v", r.SignalCounts)
		}
	})
	t.Run("defaults", func(t *testing.T) {
		t.Parallel()
		r, err := Build(st, Options{Since: now.Add(-30 * 24 * time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
		if time.Since(r.GeneratedAt) > time.Minute {
			t.Errorf("GeneratedAt = %v", r.GeneratedAt)
		}
	})
}

func TestBuildErrors(t *testing.T) {
	t.Parallel()
	for _, drop := range []string{"sessions", "signals", "findings", "finding_signals"} {
		t.Run(drop, func(t *testing.T) {
			t.Parallel()
			st := newStore(t)
			seed(t, st, 3)
			exec(t, st, `DROP TABLE `+drop)
			if _, err := Build(st, Options{Since: now.Add(-30 * 24 * time.Hour), Now: now, MinCount: 1}); err == nil {
				t.Error("want error")
			}
		})
	}
	t.Run("missing event_types is tolerated", func(t *testing.T) {
		t.Parallel()
		st := newStore(t)
		seed(t, st, 3)
		exec(t, st, `DROP TABLE event_types`)
		r, err := Build(st, Options{Since: now.Add(-30 * 24 * time.Hour), Now: now, MinCount: 1})
		if err != nil || len(r.UnknownTypes) != 0 {
			t.Errorf("%v %v", err, r.UnknownTypes)
		}
	})
}

func TestWriteMarkdownFull(t *testing.T) {
	t.Parallel()
	st := newStore(t)
	seed(t, st, 4)
	r, err := Build(st, Options{Since: now.Add(-30 * 24 * time.Hour), Now: now, MinCount: 3})
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	if err := WriteMarkdown(&b, r); err != nil {
		t.Fatal(err)
	}
	md := b.String()
	for _, want := range []string{
		"# sminer report",
		"4 sessions since 2026-09-19 · all repos · generated 2026-10-19 12:00 UTC",
		"**Friction seen:** 4 hook blocks · 4 hand-fixed edits",
		"## Findings (3)",
		"| 1 | Hand edits to go.sum | New hook | 4× in 4 sessions | `F-hook` |",
		"Pipe \\| in title",
		"### 1. Hand edits to go.sum",
		"- **Fix:** New hook → `/home/me/hooks/no-go-sum.sh`",
		"**Proposed hook:** on `Edit`, block when `file_path` matches `go\\.sum$`.",
		"**Proposed skill** `release`: Cut a release",
		"1. tag\n2. push",
		"- **Fix:** New skill · accepted 2026-10-17",
		"**Proposed line:**\n\n```diff\n+ - Use pnpm\n```",
		"`sminer finding accept F-hook --preview` · `sminer finding dismiss F-hook`",
		"## Instructed but not followed (1)",
		"## Guardrail activity",
		"| `PreToolUse:Edit g.sh` | 4 |",
		"## Appendix: ignored",
		"- **F-ign** One-off — 4 occurrences; already covered (/r/CLAUDE.md:3)",
		"Skipped 1 transcript event types sminer doesn't read (weird)",
		"<sub>Latest session: `cd /home/me/code/acme/api && claude --resume sess-",
		"**By file:** `*.go` 4",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown missing %q", want)
		}
	}
	if strings.Contains(md, "rejected one") || strings.Contains(md, "Dismissed") {
		t.Error("rejected signals and dismissed findings must not appear")
	}
}

func TestWriteMarkdownEmpty(t *testing.T) {
	t.Parallel()
	var b bytes.Buffer
	r := Report{Repo: "/home/me/code/acme/api", Since: now, GeneratedAt: now}
	if err := WriteMarkdown(&b, r); err != nil {
		t.Fatal(err)
	}
	md := b.String()
	for _, want := range []string{"`acme/api`", "## Findings (0)", "No recurring findings at this threshold"} {
		if !strings.Contains(md, want) {
			t.Errorf("missing %q in:\n%s", want, md)
		}
	}
	for _, unwanted := range []string{"Friction seen", "Guardrail", "Appendix", "Instructed"} {
		if strings.Contains(md, unwanted) {
			t.Errorf("empty report should not contain %q", unwanted)
		}
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, fmt.Errorf("disk full") }

func TestWriteErrors(t *testing.T) {
	t.Parallel()
	if err := WriteMarkdown(failWriter{}, Report{}); err == nil {
		t.Error("WriteMarkdown should surface write errors")
	}
	if err := WriteJSON(failWriter{}, Report{}); err == nil {
		t.Error("WriteJSON should surface write errors")
	}
}

func TestWriteJSONRoundTrip(t *testing.T) {
	t.Parallel()
	st := newStore(t)
	seed(t, st, 3)
	r, err := Build(st, Options{Since: now.Add(-30 * 24 * time.Hour), Now: now, MinCount: 3})
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	if err := WriteJSON(&b, r); err != nil {
		t.Fatal(err)
	}
	var back Report
	if err := json.Unmarshal(b.Bytes(), &back); err != nil {
		t.Fatal(err)
	}
	if len(back.Findings) != len(r.Findings) || back.Sessions != r.Sessions || back.Findings[0].ID != r.Findings[0].ID {
		t.Errorf("round trip lost data: %+v", back)
	}
	if !strings.Contains(b.String(), "\n  \"findings\"") {
		t.Error("JSON should be indented")
	}
}

func TestViewScoreAndExcerpts(t *testing.T) {
	t.Parallel()
	mk := func(id, sess, repo, text string, hrs int, w int) store.Signal {
		return store.Signal{ID: id, SessionID: sess, Repo: repo, Type: store.Correction, Weight: w, Excerpt: text,
			TS: now.Add(-time.Duration(hrs) * time.Hour)}
	}
	sigs := []store.Signal{
		mk("1", "s1", "/a/b", "first text in /a/b/file.go", 5, 3),
		mk("2", "s1", "/a/b", "second text same session", 4, 3),
		mk("3", "s2", "/c/d", "third text", 3, 2),
		mk("4", "s3", "/c/d", "third text", 2, 1), // duplicate wording
		mk("5", "s4", "", "last   text\nwith\twhitespace", 1, 1),
	}
	sessions := map[string]store.Session{"s1": {Cwd: "/a/b"}, "s2": {Cwd: "/it's here"}}
	f := store.Finding{ID: "F", Title: "T", RemedyType: remedy.ClaudeMD, Proposal: "plain text rule"}
	v := View(f, sigs, sessions, 3)
	// Score: weights 3+3+2+1+1=10 times 2 repos.
	if v.Score != 20 || v.Occurrences != 5 || v.Sessions != 4 || len(v.Repos) != 2 || v.Repos[0] != "/a/b" {
		t.Errorf("view = %+v", v)
	}
	if v.Proposal.Rule != "plain text rule" {
		t.Errorf("non-JSON proposal should become the rule: %+v", v.Proposal)
	}
	if v.FirstSeen != sigs[0].TS || v.LastSeen != sigs[4].TS || v.ByType[store.Correction] != 5 || v.FileKinds != nil {
		t.Errorf("times/types = %v %v %v", v.FirstSeen, v.LastSeen, v.ByType)
	}
	if len(v.Excerpts) != 3 {
		t.Fatalf("excerpts = %+v", v.Excerpts)
	}
	// Newest first, one per session, no repeated wording.
	if v.Excerpts[0].Text != "last text with whitespace" || v.Excerpts[0].Resume != "claude --resume s4" {
		t.Errorf("first excerpt = %+v", v.Excerpts[0])
	}
	if v.Excerpts[1].SessionID != "s3" || v.Excerpts[2].SessionID != "s1" {
		t.Errorf("excerpt order = %+v", v.Excerpts)
	}
	if v.Excerpts[2].Text != "second text same session" {
		t.Errorf("session s1 should be represented by its newest signal: %+v", v.Excerpts[2])
	}
	// Repo prefix is stripped from text.
	one := View(f, sigs[:1], sessions, 1)
	if one.Excerpts[0].Text != "first text in file.go" || one.Excerpts[0].Resume != "cd /a/b && claude --resume s1" {
		t.Errorf("excerpt = %+v", one.Excerpts[0])
	}
	// Second pass fills remaining slots with repeat sessions.
	many := View(f, sigs[:2], sessions, 2)
	if len(many.Excerpts) != 2 {
		t.Errorf("second pass should allow a repeat session, got %d", len(many.Excerpts))
	}
	quoted := View(f, []store.Signal{mk("9", "s2", "/c/d", "x", 1, 1)}, sessions, 1)
	if !strings.Contains(quoted.Excerpts[0].Resume, `'/it'\''s here'`) {
		t.Errorf("cwd not shell-quoted: %q", quoted.Excerpts[0].Resume)
	}
	empty := View(f, nil, nil, 2)
	if empty.Score != 0 || len(empty.Excerpts) != 0 || empty.Occurrences != 0 {
		t.Errorf("empty = %+v", empty)
	}
}

func TestFriction(t *testing.T) {
	t.Parallel()
	got := Friction(map[string]int{store.Correction: 1, store.Interrupt: 3, store.ManualFixup: 2, store.ToolFailure: 0})
	want := []FrictionCount{{1, "correction"}, {3, "interrupts"}, {2, "hand-fixed edits"}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("Friction = %v, want %v", got, want)
	}
	if len(Friction(nil)) != 0 {
		t.Error("no counts -> nothing")
	}
}

func TestPathHelpers(t *testing.T) {
	t.Setenv("HOME", "/home/me")
	if ShortPath("/home/me/code/x") != "~/code/x" || ShortPath("/home/meh/x") != "/home/meh/x" || ShortPath("/other") != "/other" || ShortPath("/home/me") != "/home/me" {
		t.Error("ShortPath")
	}
	if got := ShortRepo("/home/me/scratch-workspaces/exp1"); got != "scratch/exp1" {
		t.Errorf("ShortRepo scratch = %q", got)
	}
	if got := ShortRepo("single"); got != "single" {
		t.Errorf("ShortRepo single = %q", got)
	}
	if got := shortRepos([]string{"/a/b/c", "/x/b/c", "/q/r"}); strings.Join(got, ",") != "`b/c`,`q/r`" {
		t.Errorf("shortRepos = %v", got)
	}
	if listSome(nil, 3) != "" || listSome([]string{"a"}, 3) != "a" {
		t.Error("listSome")
	}
	if escapePipes("a|b") != `a\|b` {
		t.Error("escapePipes")
	}
}
