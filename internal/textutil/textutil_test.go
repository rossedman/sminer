package textutil

import (
	"reflect"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTruncate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		n    int
		want string
	}{
		{"shorter", "abc", 10, "abc"},
		{"exact", "abcdef", 6, "abcdef"},
		{"cut with ellipsis", "abcdefghij", 8, "abcde..."},
		{"tiny budget has no ellipsis", "abcdefghij", 3, "abc"},
		{"zero budget", "abc", 0, ""},
		{"rune boundary backs up", "abéééé", 6, "ab..."},
		{"empty", "", 5, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := Truncate(tt.in, tt.n)
			if got != tt.want {
				t.Errorf("Truncate(%q, %d) = %q, want %q", tt.in, tt.n, got, tt.want)
			}
			if len(got) > tt.n && len(tt.in) > tt.n {
				t.Errorf("result longer than %d", tt.n)
			}
			if !utf8.ValidString(got) {
				t.Errorf("invalid utf8 %q", got)
			}
		})
	}
}

func TestOneLine(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"":                 "",
		"  a \t b\n\nc  ":  "a b c",
		"single":           "single",
		"\n\n":             "",
		"tabs\tand\r\nnls": "tabs and nls",
	}
	for in, want := range tests {
		if got := OneLine(in); got != want {
			t.Errorf("OneLine(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFirstLine(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"":               "",
		"\n  \n":         "",
		"first\nsecond":  "first",
		"\n\n  hi  \nyo": "hi",
		"only":           "only",
		"  \t\nx\ny":     "x",
	}
	for in, want := range tests {
		if got := FirstLine(in); got != want {
			t.Errorf("FirstLine(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHash(t *testing.T) {
	t.Parallel()
	full := Hash(0, "a", "b")
	if len(full) != 64 {
		t.Fatalf("full digest length = %d", len(full))
	}
	if Hash(0, "a", "b") != full {
		t.Error("not deterministic")
	}
	if got := Hash(12, "a", "b"); got != full[:12] {
		t.Errorf("Hash(12) = %q, want prefix of %q", got, full)
	}
	if got := Hash(64, "a", "b"); got != full {
		t.Errorf("n == len should return everything, got %q", got)
	}
	if got := Hash(100, "a", "b"); got != full {
		t.Errorf("n > len should return everything, got %q", got)
	}
	if got := Hash(-1, "a", "b"); got != full {
		t.Errorf("negative n should return everything, got %q", got)
	}
	// The separator keeps part boundaries significant.
	if Hash(0, "ab", "c") == Hash(0, "a", "bc") {
		t.Error("part boundaries collide")
	}
	if Hash(0, "a") == Hash(0, "a", "") {
		t.Error("trailing empty part ignored")
	}
}

func TestTokens(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"The quick Brown fox", []string{"quick", "brown", "fox"}},
		{"Don't use git add -A", []string{"dont", "use", "git", "add"}},
		{"edit ./internal/foo_bar.go now", []string{"edit", "internal/foo_bar.go"}},
		{"a b c", nil},
		{"--flag -x ---", []string{"flag"}},
		{"snake_case and 42 numbers", []string{"snake_case", "42", "numbers"}},
		{"trailing... dots.", []string{"trailing", "dots"}},
	}
	for _, tt := range tests {
		got := Tokens(tt.in)
		if len(got) == 0 && len(tt.want) == 0 {
			continue
		}
		if !reflect.DeepEqual(got, tt.want) {
			t.Errorf("Tokens(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestJaccard(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		a, b []string
		want float64
	}{
		{"both empty", nil, nil, 0},
		{"a empty", nil, []string{"x"}, 0},
		{"b empty", []string{"x"}, nil, 0},
		{"identical", []string{"a", "b"}, []string{"b", "a"}, 1},
		{"disjoint", []string{"a"}, []string{"b"}, 0},
		{"half", []string{"a", "b"}, []string{"b", "c"}, 1.0 / 3.0},
		{"duplicates ignored", []string{"a", "a", "b"}, []string{"a", "b", "b"}, 1},
		{"subset", []string{"a", "b", "c", "d"}, []string{"a", "b"}, 0.5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := Jaccard(tt.a, tt.b)
			if d := got - tt.want; d > 1e-9 || d < -1e-9 {
				t.Errorf("Jaccard = %v, want %v", got, tt.want)
			}
			if rev := Jaccard(tt.b, tt.a); rev != got {
				t.Errorf("not symmetric: %v vs %v", got, rev)
			}
		})
	}
}

func TestSlug(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in     string
		maxLen int
		want   string
	}{
		{"Hello, World!", 40, "hello-world"},
		{"  --Already--Slugged--  ", 40, "already-slugged"},
		{"", 10, "finding"},
		{"!!!", 10, "finding"},
		{"héllo wörld", 40, "h-llo-w-rld"},
		{"abcdefghij", 5, "abcde"},
		{"ab-cd-ef", 3, "ab"},
		{"ab-cd-ef", 5, "ab-cd"},
		{"exactly", 7, "exactly"},
	}
	for _, tt := range tests {
		if got := Slug(tt.in, tt.maxLen); got != tt.want {
			t.Errorf("Slug(%q, %d) = %q, want %q", tt.in, tt.maxLen, got, tt.want)
		}
	}
}

var slugShape = regexp.MustCompile(`^[a-z0-9-]*$`)

func FuzzSlug(f *testing.F) {
	for _, s := range []string{
		"Add a handler for GET /widgets with tests",
		"Don't use git add -A; stage files explicitly",
		"never run `rm -rf` in the repo root",
		"héllo wörld", "", "---", "A", "\x00\xff", strings.Repeat("long-name ", 40),
	} {
		f.Add(s, 40)
		f.Add(s, 1)
	}
	f.Fuzz(func(t *testing.T, s string, maxLen int) {
		// Slug slices with maxLen, so non-positive values are outside its contract.
		if maxLen < 0 {
			maxLen = -maxLen
		}
		maxLen = 1 + maxLen%500
		got := Slug(s, maxLen)
		if got == "" {
			t.Fatalf("empty slug for %q", s)
		}
		if !slugShape.MatchString(got) {
			t.Fatalf("Slug(%q, %d) = %q has characters outside [a-z0-9-]", s, maxLen, got)
		}
		if len(got) > maxLen && got != "finding" {
			t.Fatalf("Slug(%q, %d) = %q exceeds maxLen", s, maxLen, got)
		}
		if strings.HasPrefix(got, "-") || strings.HasSuffix(got, "-") {
			t.Fatalf("Slug(%q, %d) = %q has an edge dash", s, maxLen, got)
		}
		if Slug(s, maxLen) != got {
			t.Fatal("not deterministic")
		}
	})
}

func BenchmarkTokens(b *testing.B) {
	s := "Don't use git add -A; please stage files explicitly in ./internal/foo_bar.go"
	b.ReportAllocs()
	for b.Loop() {
		_ = Tokens(s)
	}
}
