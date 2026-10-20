package report

import (
	"strings"
	"testing"

	"github.com/rossedman/sminer/internal/remedy"
	"github.com/rossedman/sminer/internal/store"
)

func TestWriteMarkdownUnheeded(t *testing.T) {
	u := &remedy.Unheeded{Where: "/r/CLAUDE.md:4", Line: "- Use the standard library for tests", Since: 3}
	r := Report{
		Findings: []Finding{{ID: "F1", Title: "Never hand-edit go.sum", Remedy: remedy.Hook, Status: store.StatusAccepted,
			Proposal: remedy.Proposal{Matcher: "Edit", Field: "file_path", Pattern: "go\\.sum$", Unheeded: &remedy.Unheeded{Where: "/r/CLAUDE.md:2", Since: 2}}}},
		Unheeded: []Finding{{ID: "F2", Title: "No testify", Remedy: remedy.Ignore, Proposal: remedy.Proposal{Unheeded: u}}},
	}
	var b strings.Builder
	if err := WriteMarkdown(&b, r); err != nil {
		t.Fatal(err)
	}
	md := b.String()
	for _, want := range []string{
		"## Instructed but not followed (1)",
		"**F2** No testify — corrected 3 times since `/r/CLAUDE.md:4` changed",
		"Already instructed at `/r/CLAUDE.md:2`, but corrected 2 times",
		"sminer finding reopen F1",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("report missing %q:\n%s", want, md)
		}
	}
	if strings.Contains(md, "Appendix: ignored") {
		t.Error("unheeded findings belong in their own section, not the appendix")
	}
}

func TestShortRepo(t *testing.T) {
	for in, want := range map[string]string{
		"/Users/me/Code/github.com/acme/api":                              "acme/api",
		"/Users/me/Code/github.com/acme/api/.claude/worktrees/api-84a4c7": "acme/api",
		"/Users/me/Code/code.hq.example.com/library/base-debian/":         "library/base-debian",
	} {
		if got := ShortRepo(in); got != want {
			t.Errorf("ShortRepo(%q) = %q, want %q", in, got, want)
		}
	}
	if got := listSome([]string{"a", "b", "c", "d", "e"}, 3); got != "a, b, c and 2 more" {
		t.Errorf("listSome = %q", got)
	}
}
