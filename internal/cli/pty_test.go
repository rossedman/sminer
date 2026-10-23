package cli

import (
	"strings"
	"testing"
)

func TestTerminalOutput(t *testing.T) {
	h := newHarness(t)
	code, out := h.runPTY("", "run", "--offline")
	if code != ExitOK {
		t.Fatalf("run exit %d:\n%s", code, out)
	}
	for _, want := range []string{"sminer", "worth fixing", "sminer review", "sminer finding show ID", "Ingested", "Analyzed"} {
		if !strings.Contains(out, want) {
			t.Errorf("run on a terminal missing %q:\n%s", want, out)
		}
	}
	fs := h.list("open")
	hook := byTitle(fs, "package-lock")
	if hook == nil {
		t.Fatal("no hook finding")
	}

	tests := []struct {
		argv []string
		want []string
	}{
		{[]string{"finding", "list"}, []string{"ID", "STATUS", "REMEDY", "SCORE", "SEEN", hook.ID, "○ open", "finding(s)"}},
		{[]string{"finding", "list", "--status", "dismissed"}, []string{"no dismissed findings"}},
		{[]string{"finding", "show", hook.ID}, []string{hook.ID, "Proposal", "PreToolUse hook", "Evidence", "--preview"}},
		{[]string{"finding", "trend"}, []string{"BEFORE/WK", "AFTER/WK", "WEEKLY", hook.ID}},
		{[]string{"finding", "trend", hook.ID}, []string{hook.ID, "Not adopted"}},
		{[]string{"config", "show"}, []string{"KEY", "VALUE", "SOURCE", "classifier.model", "file "}},
		{[]string{"config", "path"}, []string{"config.yaml"}},
		{[]string{"finding", "accept", hook.ID, "--preview"}, []string{"+++ "}},
		{[]string{"report"}, []string{"things worth fixing"}},
		{[]string{"--help"}, []string{"ACTING ON FINDINGS"}},
	}
	for _, tt := range tests {
		code, out := h.runPTY("", tt.argv...)
		if code != ExitOK {
			t.Errorf("%v: exit %d\n%s", tt.argv, code, out)
			continue
		}
		for _, w := range tt.want {
			if !strings.Contains(out, w) {
				t.Errorf("%v: missing %q in\n%s", tt.argv, w, out)
			}
		}
	}

	// Accepting on a terminal: patch path, note and trend after adoption.
	code, out = h.runPTY("", "finding", "accept", hook.ID)
	if code != ExitOK || !strings.Contains(out, "accepted "+hook.ID) || !strings.Contains(out, "apply with:") || !strings.Contains(out, "undo with:") {
		t.Errorf("accept: exit %d\n%s", code, out)
	}
	code, out = h.runPTY("", "finding", "trend", hook.ID)
	if code != ExitOK || !strings.Contains(out, "adopted") || !strings.Contains(out, "before") {
		t.Errorf("trend after adoption: exit %d\n%s", code, out)
	}
	code, out = h.runPTY("", "finding", "trend")
	if code != ExitOK || !strings.Contains(out, hook.ID) {
		t.Errorf("trend table after adoption: exit %d\n%s", code, out)
	}
	code, out = h.runPTY("", "finding", "show", hook.ID)
	if code != ExitOK || !strings.Contains(out, "adopted") {
		t.Errorf("show after adoption: exit %d\n%s", code, out)
	}

	// Errors on a terminal get a badge and an arrow hint.
	code, out = h.runPTY("", "finding", "show", "F000000")
	if code != ExitNotFound || !strings.Contains(out, "ERROR") || !strings.Contains(out, "→") {
		t.Errorf("not found: exit %d\n%s", code, out)
	}

	// --output on a terminal prints the "Wrote" line.
	path := h.dir + "/r/report.md"
	code, out = h.runPTY("", "report", "--output", path)
	if code != ExitOK || !strings.Contains(out, "Wrote") || !strings.Contains(out, "findings.json") {
		t.Errorf("report --output: exit %d\n%s", code, out)
	}
}

func TestReviewOnTerminal(t *testing.T) {
	h := newHarness(t)
	h.ok("run", "--offline", "--quiet")
	first := h.list("open")[0]

	code, out := h.runPTY("q", "review")
	if code != ExitOK || !strings.Contains(out, "no changes") {
		t.Errorf("review q: exit %d\n%s", code, out)
	}

	// Accept the selected finding, then quit: the patch path is the only
	// thing on stdout and the finding is adopted.
	code, out = h.runPTY("aq", "review")
	if code != ExitOK || !strings.Contains(out, "accepted 1") {
		t.Fatalf("review accept: exit %d\n%s", code, out)
	}
	if got := h.list("accepted"); len(got) != 1 || got[0].ID != first.ID {
		t.Errorf("accepted = %+v, want %s", got, first.ID)
	}

	code, out = h.runPTY("q", "review", "--json", "--status", "accepted")
	if code != ExitOK || !strings.Contains(out, `"patches": []`) {
		t.Errorf("review --json: exit %d\n%s", code, out)
	}
}

func TestLabelOnTerminal(t *testing.T) {
	h := newHarness(t)
	h.ok("ingest", "--quiet")
	code, out := h.runPTY("yq", "label", "--seed", "1", "--limit", "3")
	if code != ExitOK || !strings.Contains(out, "labelled 1") {
		t.Errorf("label TUI: exit %d\n%s", code, out)
	}
}
