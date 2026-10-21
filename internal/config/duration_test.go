package config

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/spf13/viper"
)

func TestParseDuration(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in      string
		want    time.Duration
		wantErr bool
	}{
		{"90d", 90 * 24 * time.Hour, false},
		{"2w", 14 * 24 * time.Hour, false},
		{"0d", 0, false},
		{"12h", 12 * time.Hour, false},
		{"1h30m", 90 * time.Minute, false},
		{"  7d  ", 7 * 24 * time.Hour, false},
		{"1500ms", 1500 * time.Millisecond, false},
		{"", 0, true},
		{"   ", 0, true},
		{"d", 0, true},
		{"w", 0, true},
		{"-3d", 0, true},
		{"1.5d", 0, true},
		{"abc", 0, true},
		{"10", 0, true},
		{"3x", 0, true},
		{"d3", 0, true},
		// Counts that would overflow time.Duration are rejected, not wrapped.
		{"106751d", 106751 * 24 * time.Hour, false},
		{"106752d", 0, true},
		{"999999999999999d", 0, true},
		{"9223372036854775807w", 0, true},
	}
	for _, tt := range tests {
		got, err := ParseDuration(tt.in)
		if (err != nil) != tt.wantErr {
			t.Errorf("ParseDuration(%q) err = %v, wantErr %v", tt.in, err, tt.wantErr)
			continue
		}
		if got != tt.want {
			t.Errorf("ParseDuration(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func FuzzParseDuration(f *testing.F) {
	for _, s := range []string{"90d", "2w", "12h", "1h30m", "0d", "", "-5h", "1.5d", " 7d ", "999999999999999d", "9223372036854775807w"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		d, err := ParseDuration(s)
		d2, err2 := ParseDuration(s)
		if d != d2 || (err == nil) != (err2 == nil) {
			t.Fatal("non-deterministic")
		}
		if err != nil {
			if d != 0 {
				t.Fatalf("error with non-zero duration %v", d)
			}
			return
		}
		trimmed := strings.TrimSpace(s)
		if d3, err3 := ParseDuration(" " + trimmed + "\n"); err3 != nil || d3 != d {
			t.Fatalf("whitespace changed result for %q: %v %v", s, d3, err3)
		}
		// Day and week counts are validated as non-negative, so the result
		// must be exactly count * unit and never wrap around.
		if last := trimmed[len(trimmed)-1]; last == 'd' || last == 'w' {
			n, perr := strconv.Atoi(trimmed[:len(trimmed)-1])
			if perr != nil {
				t.Fatalf("accepted %q but count does not parse: %v", s, perr)
			}
			unit := 24 * time.Hour
			if last == 'w' {
				unit *= 7
			}
			if d < 0 || d != time.Duration(n)*unit || (n > 0 && d/time.Duration(n) != unit) {
				t.Fatalf("ParseDuration(%q) = %v overflowed", s, d)
			}
		}
	})
}

func TestLoadErrors(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "xdg"))
	t.Setenv("HOME", dir)
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	tests := []struct {
		name    string
		path    string
		wantErr error
		wantMsg string
	}{
		{"missing named file", filepath.Join(dir, "nope.yaml"), ErrNoFile, "nope.yaml"},
		{"bad window", write("w.yaml", "window: soon\n"), ErrInvalid, "window"},
		{"bad min_count", write("m.yaml", "min_count: 0\n"), ErrInvalid, "min_count"},
		{"bad provider", write("p.yaml", "embeddings:\n  provider: magic\n"), ErrInvalid, "magic"},
		{"unparseable yaml", write("y.yaml", "window: [unclosed\n"), nil, "read"},
		{"type mismatch", write("t.yaml", "min_count: [1, 2]\n"), nil, "parse config"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := viper.New()
			Setup(v)
			_, used, err := Load(v, tt.path)
			if err == nil {
				t.Fatal("want error")
			}
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Errorf("err = %v, want %v", err, tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantMsg) {
				t.Errorf("err = %q, want it to mention %q", err, tt.wantMsg)
			}
			if used != "" {
				t.Errorf("used = %q on error", used)
			}
		})
	}
}

func TestLoadSearchesWhenPathEmpty(t *testing.T) {
	dir := t.TempDir()
	xdg := filepath.Join(dir, "xdg")
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("HOME", dir)
	if err := os.MkdirAll(filepath.Join(xdg, "sminer"), 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(xdg, "sminer", "config.yaml")
	if err := os.WriteFile(file, []byte("min_count: 7\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	v := viper.New()
	Setup(v)
	c, used, err := Load(v, "")
	if err != nil || used != file || c.MinCount != 7 {
		t.Errorf("c.MinCount=%d used=%q err=%v", c.MinCount, used, err)
	}
}

func TestExpand(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	tests := map[string]string{
		"~":        home,
		"~/x/y":    filepath.Join(home, "x", "y"),
		"~other/x": "~other/x",
		"/abs/~":   "/abs/~",
		"rel/path": "rel/path",
		"":         "",
	}
	for in, want := range tests {
		if got := Expand(in); got != want {
			t.Errorf("Expand(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestKeysMatchDefaults(t *testing.T) {
	t.Parallel()
	d := defaults()
	keys := Keys()
	if len(keys) != len(d) {
		t.Errorf("Keys has %d entries, defaults %d", len(keys), len(d))
	}
	for _, k := range keys {
		if _, ok := d[k]; !ok {
			t.Errorf("key %q has no default", k)
		}
	}
	if slices.Contains(keys, "") {
		t.Error("empty key")
	}
}

func TestDefaultAndWindow(t *testing.T) {
	t.Parallel()
	c := Default()
	if err := c.Validate(); err != nil {
		t.Fatalf("defaults must validate: %v", err)
	}
	if c.WindowDuration() != 90*24*time.Hour {
		t.Errorf("WindowDuration = %v", c.WindowDuration())
	}
	c.Window = "garbage"
	if c.WindowDuration() != 0 {
		t.Errorf("invalid window should give 0, got %v", c.WindowDuration())
	}
	if c.Classifier.MaxCallsPerRun != 500 || len(c.Roots) != 1 {
		t.Errorf("defaults = %+v", c)
	}
}

func TestSearchPathsAndDefaultFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	sp := SearchPaths()
	want := []string{filepath.Join(home, ".config", "sminer", "config.yaml"), filepath.Join(home, ".sminer.yaml"), ".sminer.yaml"}
	if !slices.Equal(sp, want) {
		t.Errorf("SearchPaths = %v, want %v", sp, want)
	}
	if DefaultFile() != want[0] {
		t.Errorf("DefaultFile = %q", DefaultFile())
	}
	if Find() != "" {
		t.Errorf("Find = %q in an empty home", Find())
	}
}

func TestXDGRejectsRelative(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", "relative/dir")
	if got, want := DataDir(), filepath.Join(home, ".local", "share", "sminer"); got != want {
		t.Errorf("DataDir = %q, want %q", got, want)
	}
}

func BenchmarkLoad(b *testing.B) {
	dir := b.TempDir()
	file := filepath.Join(dir, "config.yaml")
	body := "window: 30d\nmin_count: 4\nroots:\n  - ~/.claude/projects\nclassifier:\n  model: claude-haiku-5-5\n  concurrency: 8\nredact:\n  patterns:\n    - internal-[a-z]+-id\n"
	if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		v := viper.New()
		Setup(v)
		if _, _, err := Load(v, file); err != nil {
			b.Fatal(err)
		}
	}
}
