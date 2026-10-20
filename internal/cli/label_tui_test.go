package cli

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/rossedman/sminer/internal/signal"
	"github.com/rossedman/sminer/internal/ui"
)

func key(s string) tea.KeyPressMsg {
	switch s {
	case "ctrl+c":
		return tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	}
	return tea.KeyPressMsg{Code: rune(s[0]), Text: s}
}

func TestLabelModel(t *testing.T) {
	items := []signal.Candidate{
		{Turn: "t1", Action: "Edit go.sum", Message: "never hand-edit go.sum"},
		{Turn: "t2", Action: "Bash go test", Message: "now the integration tests"},
		{Turn: "t3", Action: "Write a.tf", Message: "use snake_case"},
		{Turn: "t4", Action: "Edit b.go", Message: "looks good"},
	}
	var saved []string
	var tl tally
	m := &labelModel{th: ui.New(true), items: items, t: &tl, width: 100, save: func(c signal.Candidate, yes bool) error {
		saved = append(saved, c.Turn+map[bool]string{true: "=y", false: "=n"}[yes])
		if yes {
			tl.yes++
		} else {
			tl.no++
		}
		return nil
	}}
	if v := m.render(); !strings.Contains(v, "never hand-edit go.sum") || !strings.Contains(v, "1 of 4") {
		t.Errorf("first screen:\n%s", v)
	}
	for _, k := range []string{"y", "x", "n", "s"} { // x is ignored
		m.Update(key(k))
	}
	if strings.Join(saved, " ") != "t1=y t2=n" || tl.skipped != 1 || m.i != 3 {
		t.Fatalf("saved %v, skipped %d, at %d", saved, tl.skipped, m.i)
	}
	if v := m.render(); !strings.Contains(v, "4 of 4") || !strings.Contains(v, "skipped") {
		t.Errorf("after answers:\n%s", v)
	}
	if _, cmd := m.Update(key("q")); cmd == nil || m.interrupted {
		t.Error("q should quit cleanly")
	}

	m = &labelModel{th: ui.New(true), items: items, t: &tally{}, save: func(signal.Candidate, bool) error { return nil }}
	if _, cmd := m.Update(key("ctrl+c")); cmd == nil || !m.interrupted {
		t.Error("ctrl+c should quit as interrupted")
	}
	m = &labelModel{th: ui.New(true), items: items[:1], t: &tally{}, save: func(signal.Candidate, bool) error { return nil }}
	if _, cmd := m.Update(key("n")); cmd == nil {
		t.Error("answering the last item should quit")
	}
}
