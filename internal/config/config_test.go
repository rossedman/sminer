package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/viper"
)

func TestLoadPrecedence(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "config.yaml")
	os.WriteFile(file, []byte("window: 30d\nclassifier:\n  model: from-file\n  concurrency: 9\n"), 0o600)
	t.Setenv("SMINER_CLASSIFIER_MODEL", "from-env")

	v := viper.New()
	Setup(v)
	c, used, err := Load(v, file)
	if err != nil {
		t.Fatal(err)
	}
	if used != file {
		t.Errorf("used = %q", used)
	}
	if c.Window != "30d" || c.Classifier.Concurrency != 9 {
		t.Errorf("file values not applied: %+v", c)
	}
	if c.Classifier.Model != "from-env" {
		t.Errorf("env should beat file: %q", c.Classifier.Model)
	}
	if c.MinCount != 3 || c.Embeddings.Provider != "auto" {
		t.Errorf("defaults not applied: %+v", c)
	}
}

func TestLoadWithoutFile(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Chdir(t.TempDir())
	v := viper.New()
	Setup(v)
	c, used, err := Load(v, "")
	if err != nil || used != "" {
		t.Fatalf("fresh install should need no config: %v %q", err, used)
	}
	if c.Window != "90d" {
		t.Errorf("window = %q", c.Window)
	}
	if _, _, err := Load(viper.New(), filepath.Join(t.TempDir(), "nope.yaml")); !errors.Is(err, ErrNoFile) {
		t.Errorf("named missing file: %v", err)
	}
}

func TestValidate(t *testing.T) {
	c := Default()
	c.Window = "soon"
	if err := c.Validate(); !errors.Is(err, ErrInvalid) {
		t.Errorf("bad window: %v", err)
	}
	c = Default()
	c.Embeddings.Provider = "openai"
	if c.Validate() == nil {
		t.Error("bad provider accepted")
	}
}

func TestXDG(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "/xdg/data")
	if DataDir() != "/xdg/data/sminer" {
		t.Errorf("DataDir = %q", DataDir())
	}
	t.Setenv("XDG_DATA_HOME", "relative/ignored")
	if filepath.Base(filepath.Dir(DataDir())) != "share" {
		t.Errorf("relative XDG path should be ignored: %q", DataDir())
	}
}

func TestEnvVar(t *testing.T) {
	if got := EnvVar("classifier.max_calls_per_run"); got != "SMINER_CLASSIFIER_MAX_CALLS_PER_RUN" {
		t.Error(got)
	}
}

func TestRepoExcluded(t *testing.T) {
	c := Config{ExcludeRepos: []string{"/home/me/personal/*"}}
	if !c.RepoExcluded("/home/me/personal/blog/sub") || c.RepoExcluded("/home/me/work/api") {
		t.Error("glob matching wrong")
	}
}

func TestFindSearchOrder(t *testing.T) {
	xdg, home, cwd := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("HOME", home)
	t.Chdir(cwd)
	write := func(path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	load := func() (Config, string) {
		t.Helper()
		v := viper.New()
		Setup(v)
		c, used, err := Load(v, "")
		if err != nil {
			t.Fatal(err)
		}
		return c, used
	}

	if f := Find(); f != "" {
		t.Fatalf("Find() with no files = %q", f)
	}
	write(filepath.Join(cwd, ".sminer.yaml"), "window: 7d\n")
	if c, used := load(); used != ".sminer.yaml" || c.Window != "7d" {
		t.Errorf("current dir: used %q, window %q", used, c.Window)
	}
	homeFile := filepath.Join(home, ".sminer.yaml")
	write(homeFile, "window: 14d\n")
	if c, used := load(); used != homeFile || c.Window != "14d" {
		t.Errorf("home should beat current dir: used %q, window %q", used, c.Window)
	}
	xdgFile := filepath.Join(xdg, "sminer", "config.yaml")
	write(xdgFile, "window: 21d\n")
	if c, used := load(); used != xdgFile || c.Window != "21d" {
		t.Errorf("XDG should beat home: used %q, window %q", used, c.Window)
	}
}

func TestOutputSettingsFromEnv(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Chdir(t.TempDir())
	t.Setenv("SMINER_JSON", "true")
	t.Setenv("SMINER_NO_COLOR", "1")
	v := viper.New()
	Setup(v)
	c, _, err := Load(v, "")
	if err != nil {
		t.Fatal(err)
	}
	if !c.JSON || !c.NoColor || c.Quiet || c.Verbose {
		t.Errorf("output settings = json %v no_color %v quiet %v verbose %v", c.JSON, c.NoColor, c.Quiet, c.Verbose)
	}
}
