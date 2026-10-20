package ui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
)

func TestSpark(t *testing.T) {
	th := New(true)
	got := plain(th.Spark([]int{0, 1, 4, 2}, 2, th.P.Ember, th.P.Gem))
	if got != "·▂█▄" {
		t.Errorf("spark = %q", got)
	}
}

func TestBar(t *testing.T) {
	th := New(true)
	for _, tt := range []struct {
		v, max float64
		heavy  int
	}{{0, 10, 0}, {10, 10, 8}, {5, 10, 4}, {0.1, 10, 1}} {
		b := plain(th.Bar(tt.v, tt.max, 8, th.P.Ore))
		if n := strings.Count(b, "━"); n != tt.heavy || lipgloss.Width(b) != 8 {
			t.Errorf("Bar(%v/%v) = %q", tt.v, tt.max, b)
		}
	}
}

func TestDiffKeepsText(t *testing.T) {
	d := "sminer F1: x\n\n--- a/CLAUDE.md\n+++ b/CLAUDE.md\n@@ -1,1 +1,2 @@\n a\n+- b\n"
	if got := plain(New(true).Diff(d)); got != d {
		t.Errorf("Diff changed the text:\n%q", got)
	}
}

func TestDark(t *testing.T) {
	for v, want := range map[string]bool{"": true, "15;0": true, "0;15": false, "0;default;7": false} {
		t.Setenv("COLORFGBG", v)
		if Dark() != want {
			t.Errorf("COLORFGBG=%q: dark = %v", v, !want)
		}
	}
}

func plain(s string) string {
	var b strings.Builder
	w := NewWriter(&b, true)
	w.Profile = colorprofile.NoTTY
	w.Write([]byte(s))
	return b.String()
}
