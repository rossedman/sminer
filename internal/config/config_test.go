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
	if c.Validate() == nil {
		t.Error("bad window accepted")
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
