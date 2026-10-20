package cli

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/rossedman/sminer/internal/finding"
	"github.com/rossedman/sminer/internal/remedy"
	"github.com/rossedman/sminer/internal/report"
	"github.com/rossedman/sminer/internal/ui"
)

// fakeReviewer is an in-memory finding service.
type fakeReviewer struct {
	fs      []report.Finding
	calls   []string
	failFor string // AcceptMany fails when this id is included
}

func (r *fakeReviewer) List(status string) ([]report.Finding, error) {
	r.calls = append(r.calls, "list "+status)
	var out []report.Finding
	for _, f := range r.fs {
		if status == "all" || f.Status == status {
			out = append(out, f)
		}
	}
	return out, nil
}

func (r *fakeReviewer) Get(id string) (report.Finding, error) {
	for _, f := range r.fs {
		if f.ID == id {
			return f, nil
		}
	}
	return report.Finding{}, errors.New("not found")
}

func (r *fakeReviewer) set(id, status string) {
	for i := range r.fs {
		if r.fs[i].ID == id {
			r.fs[i].Status = status
		}
	}
}

func (r *fakeReviewer) AcceptMany(ids []string, dir string, preview bool) ([]finding.AcceptResult, error) {
	verb := "accept"
	if preview {
		verb = "preview"
	}
	r.calls = append(r.calls, verb+" "+strings.Join(ids, ","))
	var out []finding.AcceptResult
	for _, id := range ids {
		if id == r.failFor {
			return nil, errors.New("target already exists")
		}
	}
	path := "/p/" + strings.Join(ids, "+") + ".patch"
	for _, id := range ids {
		if !preview {
			r.set(id, "accepted")
		}
		out = append(out, finding.AcceptResult{ID: id, PatchPath: path, Apply: "cd ~ && patch -p1 < " + path,
			Diff: "--- a/CLAUDE.md\n+++ b/CLAUDE.md\n@@ -1,1 +1,2 @@\n+- rule " + strings.Join(ids, ",") + "\n"})
	}
	return out, nil
}

func (r *fakeReviewer) Dismiss(id, reason string) error {
	r.calls = append(r.calls, "dismiss "+id+" "+reason)
	r.set(id, "dismissed")
	return nil
}

func (r *fakeReviewer) Reopen(id string) error {
	r.calls = append(r.calls, "reopen "+id)
	r.set(id, "open")
	return nil
}

func newFake() *fakeReviewer {
	return &fakeReviewer{fs: []report.Finding{
		{ID: "F1", Title: "Never hand-edit go.sum", Remedy: remedy.Hook, Status: "open", Score: 30},
		{ID: "F2", Title: "Use table-driven tests", Remedy: remedy.ClaudeMD, Status: "open", Score: 20},
		{ID: "F3", Title: "Run make lint before commit", Remedy: remedy.ClaudeMD, Status: "open", Score: 10},
		{ID: "F4", Title: "Old one", Remedy: remedy.Skill, Status: "accepted", Score: 5},
	}}
}

func press(m *reviewModel, keys ...string) tea.Cmd {
	var cmd tea.Cmd
	for _, k := range keys {
		var msg tea.KeyPressMsg
		switch k {
		case "ctrl+c":
			msg = tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
		case "enter":
			msg = tea.KeyPressMsg{Code: tea.KeyEnter}
		case "esc":
			msg = tea.KeyPressMsg{Code: tea.KeyEscape}
		case "tab":
			msg = tea.KeyPressMsg{Code: tea.KeyTab}
		case "down":
			msg = tea.KeyPressMsg{Code: tea.KeyDown}
		case "space":
			msg = tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
		default:
			msg = tea.KeyPressMsg{Code: rune(k[0]), Text: k}
		}
		_, cmd = m.Update(msg)
	}
	return cmd
}

func TestReviewBrowseAndAccept(t *testing.T) {
	r := newFake()
	m := newReviewModel(ui.New(true), r, "", "open")
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	if len(m.items) != 3 || r.calls[0] != "list open" {
		t.Fatalf("items %d, calls %v", len(m.items), r.calls)
	}
	if v := m.render(); !strings.Contains(v, "Never hand-edit go.sum") || !strings.Contains(v, "3 findings") {
		t.Errorf("first screen:\n%s", v)
	}

	// Mark F2 and F3 (space moves down), preview them together, accept.
	press(m, "down", "space", "space")
	if len(m.marked) != 2 || !m.marked["F2"] || !m.marked["F3"] {
		t.Fatalf("marked = %v", m.marked)
	}
	press(m, "p")
	if m.mode != modePreview || r.calls[len(r.calls)-1] != "preview F2,F3" || !strings.Contains(m.vp.View(), "rule F2,F3") {
		t.Fatalf("preview: mode %v, calls %v\n%s", m.mode, r.calls, m.vp.View())
	}
	press(m, "a")
	if r.calls[len(r.calls)-1] != "accept F2,F3" || m.accepted != 2 || len(m.marked) != 0 || m.mode != modeCard {
		t.Fatalf("accept: calls %v accepted %d marked %v", r.calls, m.accepted, m.marked)
	}
	if strings.Join(m.written, " ") != "/p/F2+F3.patch" || !strings.Contains(m.note, "accepted F2, F3") {
		t.Errorf("written %v, note %q", m.written, m.note)
	}
	// Accepted findings stay listed, with their new status, until reload.
	if len(m.items) != 3 || m.items[1].Status != "accepted" {
		t.Errorf("items after accept: %+v", m.items)
	}

	// The cursor's finding when nothing is marked.
	press(m, "g", "a")
	if r.calls[len(r.calls)-1] != "accept F1" || m.accepted != 3 {
		t.Errorf("accept current: %v", r.calls)
	}

	// A failing batch reports the error and changes nothing.
	r.failFor = "F1"
	press(m, "a")
	if !m.noteErr || m.accepted != 3 {
		t.Errorf("failed accept: note %q err %v accepted %d", m.note, m.noteErr, m.accepted)
	}
}

func TestReviewDismissReopenFilter(t *testing.T) {
	r := newFake()
	m := newReviewModel(ui.New(true), r, "", "open")
	press(m, "d")
	if m.mode != modeDismiss {
		t.Fatal("d should ask for a reason")
	}
	press(m, "f", "l", "a", "k", "y", "enter")
	if r.calls[len(r.calls)-1] != "dismiss F1 flaky" || m.dismissed != 1 || m.mode != modeCard {
		t.Fatalf("dismiss: calls %v", r.calls)
	}
	press(m, "d", "n", "o", "esc")
	if m.dismissed != 1 || m.mode != modeCard {
		t.Error("esc should cancel a dismiss")
	}
	press(m, "r")
	if r.calls[len(r.calls)-1] != "reopen F1" || m.reopened != 1 {
		t.Errorf("reopen: %v", r.calls)
	}

	press(m, "tab")
	if reviewFilters[m.filter] != "accepted" || len(m.items) != 1 || m.items[0].ID != "F4" {
		t.Errorf("tab to accepted: %v %+v", reviewFilters[m.filter], m.items)
	}
	press(m, "tab", "tab")
	if reviewFilters[m.filter] != "all" || len(m.items) != 4 {
		t.Errorf("all: %d", len(m.items))
	}
	press(m, "?")
	if m.mode != modeHelp || !strings.Contains(m.render(), "mark a finding") {
		t.Error("? should show help")
	}
	press(m, "x")
	if m.mode != modeCard {
		t.Error("any key should leave help")
	}
	if cmd := press(m, "q"); cmd == nil || m.interrupted {
		t.Error("q should quit cleanly")
	}
	if cmd := press(m, "ctrl+c"); cmd == nil || !m.interrupted {
		t.Error("ctrl+c should quit as interrupted")
	}
}

func TestReviewEmpty(t *testing.T) {
	m := newReviewModel(ui.New(true), &fakeReviewer{}, "", "open")
	press(m, "a", "d", "space", "p", "down")
	if m.mode != modeCard || m.accepted != 0 || !strings.Contains(m.render(), "No open findings") {
		t.Errorf("empty review:\n%s", m.render())
	}
}
