package ui

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
)

func TestRemedyLabel(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"hook": "hook", "claude_md": "CLAUDE.md", "skill": "skill", "": "pending", "other": "other",
	} {
		if got := RemedyLabel(in); got != want {
			t.Errorf("RemedyLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRemedyColor(t *testing.T) {
	t.Parallel()
	th := New(true)
	for in, want := range map[string]any{
		"hook": th.P.Violet, "claude_md": th.P.Ore, "skill": th.P.Gem, "": th.P.Faint, "x": th.P.Faint,
	} {
		if got := th.RemedyColor(in); got != want {
			t.Errorf("RemedyColor(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestBadgeRemedyStatus(t *testing.T) {
	t.Parallel()
	th := New(false)
	if got := plain(th.Badge("hook")); got != " hook " {
		t.Errorf("Badge = %q", got)
	}
	if got := plain(th.Remedy("skill")); got != "● skill" {
		t.Errorf("Remedy = %q", got)
	}
	for in, want := range map[string]string{
		"open": "○ open", "accepted": "✓ accepted", "dismissed": "✗ dismissed", "weird": "weird",
	} {
		if got := plain(th.Status(in)); got != want {
			t.Errorf("Status(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBarEdges(t *testing.T) {
	t.Parallel()
	th := New(true)
	for _, tt := range []struct {
		v, total float64
		heavy    int
	}{{5, 0, 6}, {-3, 10, 0}, {99, 10, 6}} {
		b := plain(th.Bar(tt.v, tt.total, 6, th.P.Ore))
		if n := strings.Count(b, "━"); n != tt.heavy || lipgloss.Width(b) != 6 {
			t.Errorf("Bar(%v/%v) = %q, want %d heavy", tt.v, tt.total, b, tt.heavy)
		}
	}
}

func TestTable(t *testing.T) {
	t.Parallel()
	th := New(true)
	rows := [][]string{{"a", "1"}, {"bb", "22"}}
	for _, style := range []func(row, col int) lipgloss.Style{nil, func(int, int) lipgloss.Style { return th.OK }} {
		out := plain(th.Table([]string{"NAME", "N"}, rows, style).String())
		lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
		if len(lines) != 4 {
			t.Fatalf("want header, rule and 2 rows, got %q", out)
		}
		if !strings.HasPrefix(lines[0], "NAME") || !strings.Contains(lines[1], "─") ||
			!strings.HasPrefix(lines[2], "a ") || !strings.HasPrefix(lines[3], "bb") {
			t.Errorf("table = %q", out)
		}
	}
}

func TestRule(t *testing.T) {
	t.Parallel()
	th := New(true)
	if got := plain(th.Rule("", 5)); got != "─────" {
		t.Errorf("unlabelled rule = %q", got)
	}
	got := plain(th.Rule("hi", 12))
	if !strings.HasPrefix(got, "── hi ") || lipgloss.Width(got) != 12 {
		t.Errorf("labelled rule = %q", got)
	}
	if got := plain(th.Rule("long label", 3)); got != "── long label " {
		t.Errorf("overflowing rule = %q", got)
	}
}

func TestTilde(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for in, want := range map[string]string{
		home:                          "~",
		filepath.Join(home, "a", "b"): "~" + string(os.PathSeparator) + "a" + string(os.PathSeparator) + "b",
		home + "x":                    home + "x",
		"/elsewhere":                  "/elsewhere",
	} {
		if got := Tilde(in); got != want {
			t.Errorf("Tilde(%q) = %q, want %q", in, got, want)
		}
	}
	t.Setenv("HOME", "/")
	if got := Tilde("/usr"); got != "/usr" {
		t.Errorf("root home: Tilde = %q", got)
	}
}

func TestIsTerminal(t *testing.T) {
	t.Parallel()
	if IsTerminal(&bytes.Buffer{}) {
		t.Error("buffer is not a terminal")
	}
	f, err := os.CreateTemp(t.TempDir(), "x")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if IsTerminal(f) {
		t.Error("regular file is not a terminal")
	}
	if _, err := os.Stat(os.DevNull); err == nil {
		n, _ := os.Open(os.DevNull)
		defer n.Close()
		if !IsTerminal(n) {
			t.Error("/dev/null is a character device")
		}
	}
	f.Close()
	if IsTerminal(f) {
		t.Error("closed file must not report a terminal")
	}
}

func TestNewWriterNoColor(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("COLORTERM", "truecolor")
	t.Setenv("CLICOLOR_FORCE", "1")
	var b bytes.Buffer
	on := NewWriter(&b, false)
	off := NewWriter(&b, true)
	if on.Profile <= colorprofile.Ascii {
		t.Fatalf("test setup: forced profile = %v", on.Profile)
	}
	if off.Profile != colorprofile.Ascii {
		t.Errorf("noColor profile = %v, want Ascii", off.Profile)
	}
	_, _ = off.Write([]byte(New(true).Err.Render("x")))
	if out := b.String(); !strings.Contains(out, "x") || strings.Contains(out, "38;") {
		t.Errorf("noColor output has color: %q", out)
	}
}
