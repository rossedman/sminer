package cli

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/rossedman/sminer/internal/remedy"
	"github.com/rossedman/sminer/internal/report"
	"github.com/rossedman/sminer/internal/ui"
)

// flakyReviewer fails the operations named in its fields.
type flakyReviewer struct {
	*fakeReviewer
	listErr, getErr, dismissErr, reopenErr error
}

func (r *flakyReviewer) List(status string) ([]report.Finding, error) {
	if r.listErr != nil {
		return nil, r.listErr
	}
	return r.fakeReviewer.List(status)
}

func (r *flakyReviewer) Get(id string) (report.Finding, error) {
	if r.getErr != nil {
		return report.Finding{}, r.getErr
	}
	return r.fakeReviewer.Get(id)
}

func (r *flakyReviewer) Dismiss(id, reason string) error {
	if r.dismissErr != nil {
		return r.dismissErr
	}
	return r.fakeReviewer.Dismiss(id, reason)
}

func (r *flakyReviewer) Reopen(id string) error {
	if r.reopenErr != nil {
		return r.reopenErr
	}
	return r.fakeReviewer.Reopen(id)
}

func pressMsg(m *reviewModel, msgs ...tea.KeyPressMsg) {
	for _, k := range msgs {
		m.Update(k)
	}
}

func TestReviewLoadFailure(t *testing.T) {
	boom := errors.New("database is locked")
	r := &flakyReviewer{fakeReviewer: newFake(), listErr: boom}
	m := newReviewModel(ui.New(true), r, "", "open")
	if !errors.Is(m.err, boom) {
		t.Fatalf("m.err = %v", m.err)
	}
	a := newApp(Options{})
	if err := a.review(context.Background(), r, "", "open"); !errors.Is(err, boom) {
		t.Errorf("review returned %v, want the load error", err)
	}
}

func TestReviewActionFailures(t *testing.T) {
	r := &flakyReviewer{fakeReviewer: newFake()}
	m := newReviewModel(ui.New(true), r, "", "open")

	r.dismissErr = errors.New("read-only database")
	press(m, "d", "x", "enter")
	if !m.noteErr || !strings.Contains(m.note, "dismiss F1: read-only database") || m.dismissed != 0 {
		t.Errorf("dismiss failure: note %q err %v dismissed %d", m.note, m.noteErr, m.dismissed)
	}
	r.dismissErr = nil

	r.reopenErr = errors.New("nope")
	press(m, "r")
	if !m.noteErr || !strings.Contains(m.note, "reopen F1: nope") || m.reopened != 0 {
		t.Errorf("reopen failure: note %q reopened %d", m.note, m.reopened)
	}
	r.reopenErr = nil

	// A failed preview shows why in the pane instead of a patch.
	r.failFor = "F1"
	press(m, "p")
	if m.mode != modePreview || !strings.Contains(ansi.Strip(m.vp.View()), "Can't build a patch: target already exists") {
		t.Errorf("preview failure:\n%s", ansi.Strip(m.vp.View()))
	}
	// Esc leaves the preview without quitting.
	if cmd := press(m, "esc"); cmd != nil || m.mode != modeCard {
		t.Errorf("esc in preview: mode %v", m.mode)
	}
	if cmd := press(m, "esc"); cmd == nil {
		t.Error("esc in browse mode should quit")
	}

	// A finding that can't be fetched still renders from the list row.
	r.getErr = errors.New("gone")
	m2 := newReviewModel(ui.New(true), r, "", "open")
	if !strings.Contains(ansi.Strip(m2.vp.View()), "Never hand-edit go.sum") {
		t.Errorf("fallback card:\n%s", ansi.Strip(m2.vp.View()))
	}
}

func TestReviewNavigation(t *testing.T) {
	r := newFake()
	m := newReviewModel(ui.New(true), r, "", "open")
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	cursor := func() string { f, _ := m.current(); return f.ID }

	steps := []struct {
		keys []string
		want string
	}{
		{[]string{"j"}, "F2"},
		{[]string{"down"}, "F3"},
		{[]string{"j"}, "F3"}, // clamped at the end
		{[]string{"k"}, "F2"},
		{[]string{"g"}, "F1"},
		{[]string{"k"}, "F1"}, // clamped at the start
		{[]string{"G"}, "F3"},
		{[]string{"g"}, "F1"},
	}
	for _, s := range steps {
		press(m, s.keys...)
		if got := cursor(); got != s.want {
			t.Fatalf("after %v: cursor %s, want %s", s.keys, got, s.want)
		}
	}
	pressMsg(m, tea.KeyPressMsg{Code: tea.KeyUp}, tea.KeyPressMsg{Code: tea.KeyEnd})
	if cursor() != "F3" {
		t.Errorf("end key: %s", cursor())
	}
	pressMsg(m, tea.KeyPressMsg{Code: tea.KeyHome})
	if cursor() != "F1" {
		t.Errorf("home key: %s", cursor())
	}
	// Scrolling the detail pane never moves the cursor.
	press(m, "J", "K")
	pressMsg(m, tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl}, tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl},
		tea.KeyPressMsg{Code: tea.KeyPgDown}, tea.KeyPressMsg{Code: tea.KeyPgUp})
	if cursor() != "F1" {
		t.Errorf("scrolling moved the cursor to %s", cursor())
	}

	// Unmark by pressing space twice on the same row.
	press(m, "x", "k", "space")
	if len(m.marked) != 0 {
		t.Errorf("marked = %v", m.marked)
	}
	press(m, "g", "x")
	if !m.marked["F1"] || cursor() != "F2" {
		t.Errorf("x marks and moves: %v at %s", m.marked, cursor())
	}
	// Enter toggles the patch preview, and moving while previewing marked
	// findings keeps it.
	press(m, "enter")
	if m.mode != modePreview {
		t.Fatal("enter should preview")
	}
	press(m, "j")
	if m.mode != modePreview {
		t.Error("moving should keep the preview")
	}
	press(m, "space")
	if !m.marked["F3"] {
		t.Errorf("space in preview marks: %v", m.marked)
	}
	press(m, "enter")
	if m.mode != modeCard {
		t.Error("enter again should close the preview")
	}

	// Reverse tab order wraps around.
	press(m, "F")
	if reviewFilters[m.filter] != "all" {
		t.Errorf("F from open: %s", reviewFilters[m.filter])
	}
	pressMsg(m, tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	if reviewFilters[m.filter] != "dismissed" {
		t.Errorf("shift+tab: %s", reviewFilters[m.filter])
	}
	if !strings.Contains(m.render(), "No dismissed findings") {
		t.Errorf("empty filter:\n%s", m.render())
	}
	// Reload drops marks for findings no longer listed.
	if len(m.marked) != 0 {
		t.Errorf("marks survived a filter change: %v", m.marked)
	}
}

func TestReviewHintsAndFooter(t *testing.T) {
	r := &fakeReviewer{fs: []report.Finding{
		{ID: "F1", Title: "ignored", Remedy: remedy.Ignore, Status: "open", Proposal: remedy.Proposal{Why: "covered by a hook"}},
		{ID: "F2", Title: "done", Remedy: remedy.ClaudeMD, Status: "accepted"},
		{ID: "F3", Title: "hidden", Remedy: remedy.ClaudeMD, Status: "dismissed"},
		{ID: "F4", Title: "todo", Remedy: remedy.ClaudeMD, Status: "open"},
	}}
	m := newReviewModel(ui.New(true), r, "", "all")
	m.Update(tea.WindowSizeMsg{Width: 140, Height: 30})
	want := []string{"Ignored by routing: covered by a hook", "Accepted. Apply its patch", "Dismissed. Press r", "Press p to see the patch"}
	for i, w := range want {
		if i > 0 {
			press(m, "j")
		}
		if got := ansi.Strip(m.render()); !strings.Contains(got, w) {
			t.Errorf("row %d: missing hint %q in\n%s", i, w, got)
		}
	}

	press(m, "d")
	if got := ansi.Strip(m.renderFooter()); !strings.Contains(got, "Dismiss F4") || !strings.Contains(got, "enter dismiss") || !strings.Contains(got, "esc cancel") {
		t.Errorf("dismiss footer = %q", got)
	}
	press(m, "esc")
	m.note = "" // notes outrank the preview line
	press(m, "p")
	if got := ansi.Strip(m.renderFooter()); !strings.Contains(got, "Previewing the patch for F4") {
		t.Errorf("preview footer = %q", got)
	}
	press(m, "p", "a")
	if got := ansi.Strip(m.renderFooter()); !strings.Contains(got, "✓ accepted F4") {
		t.Errorf("note footer = %q", got)
	}
	m.Update(tea.WindowSizeMsg{Width: 30, Height: 12})
	for _, l := range strings.Split(ansi.Strip(m.renderFooter()), "\n") {
		if len(l) > 0 && ansi.StringWidth(l) > 29 {
			t.Errorf("footer line wider than the screen: %q", l)
		}
	}

	v := m.View()
	if !v.AltScreen || !strings.Contains(ansi.Strip(v.Content), "todo") {
		t.Errorf("View: alt=%v", v.AltScreen)
	}
	if m.Init() != nil {
		t.Error("Init should not start a command")
	}
	if _, cmd := m.Update(struct{}{}); cmd != nil {
		t.Error("unknown messages are ignored")
	}
	empty := newReviewModel(ui.New(true), &fakeReviewer{}, "", "open")
	if empty.hint() != "" {
		t.Errorf("hint with no findings = %q", empty.hint())
	}
}
