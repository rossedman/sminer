// Package config resolves sminer's settings.
//
// Precedence, highest first: command-line flags, SMINER_* environment
// variables, the config file, built-in defaults. The file is the first of
// $XDG_CONFIG_HOME/sminer/config.yaml (default ~/.config/sminer/config.yaml),
// ~/.sminer.yaml and ./.sminer.yaml that exists, and is optional: sminer
// runs with no configuration at all.
package config

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/viper"
)

// EnvPrefix is the prefix of every environment variable sminer reads:
// classifier.model becomes SMINER_CLASSIFIER_MODEL.
const EnvPrefix = "SMINER"

// Config is the resolved configuration.
type Config struct {
	Roots        []string   `mapstructure:"roots" json:"roots"`
	Window       string     `mapstructure:"window" json:"window"`
	MinCount     int        `mapstructure:"min_count" json:"min_count"`
	Offline      bool       `mapstructure:"offline" json:"offline"`
	ExcludeRepos []string   `mapstructure:"exclude_repos" json:"exclude_repos"`
	Classifier   Classifier `mapstructure:"classifier" json:"classifier"`
	Embeddings   Embeddings `mapstructure:"embeddings" json:"embeddings"`
	Cluster      Cluster    `mapstructure:"cluster" json:"cluster"`
	Redact       Redact     `mapstructure:"redact" json:"redact"`
	Remedies     Remedies   `mapstructure:"remedies" json:"remedies"`
	DB           string     `mapstructure:"db" json:"db"`
	PatchDir     string     `mapstructure:"patch_dir" json:"patch_dir"`
	JSON         bool       `mapstructure:"json" json:"json"`
	Quiet        bool       `mapstructure:"quiet" json:"quiet"`
	Verbose      bool       `mapstructure:"verbose" json:"verbose"`
	NoColor      bool       `mapstructure:"no_color" json:"no_color"`
}

// Classifier configures the LLM pass that confirms candidate corrections.
type Classifier struct {
	Model          string `mapstructure:"model" json:"model"`
	MaxCallsPerRun int    `mapstructure:"max_calls_per_run" json:"max_calls_per_run"`
	Concurrency    int    `mapstructure:"concurrency" json:"concurrency"`
}

// Embeddings selects how signal text is embedded for clustering.
type Embeddings struct {
	// Provider is "auto" (ollama if reachable, else hash), "ollama" or "hash".
	Provider string `mapstructure:"provider" json:"provider"`
	Model    string `mapstructure:"model" json:"model"`
	URL      string `mapstructure:"url" json:"url"`
}

// Cluster tunes how embedded signals are grouped into findings.
type Cluster struct {
	// Threshold is the cosine similarity needed to join a cluster.
	// Zero means the embedder's default.
	Threshold float64 `mapstructure:"threshold" json:"threshold"`
}

// Redact lists extra regular expressions scrubbed from text before it is
// stored or sent anywhere.
type Redact struct {
	Patterns []string `mapstructure:"patterns" json:"patterns"`
}

// Remedies points at the existing hooks, CLAUDE.md, skills and settings that
// proposals are checked against and written for.
type Remedies struct {
	HooksDir       string `mapstructure:"hooks_dir" json:"hooks_dir"`
	GlobalClaudeMD string `mapstructure:"global_claude_md" json:"global_claude_md"`
	SkillsDir      string `mapstructure:"skills_dir" json:"skills_dir"`
	Settings       string `mapstructure:"settings" json:"settings"`
}

// defaults are the built-in values, keyed as in the config file.
func defaults() map[string]any {
	return map[string]any{
		"roots":                        []string{"~/.claude/projects"},
		"window":                       "90d",
		"min_count":                    3,
		"offline":                      false,
		"exclude_repos":                []string{},
		"classifier.model":             "claude-haiku-5-5",
		"classifier.max_calls_per_run": 500,
		"classifier.concurrency":       4,
		"embeddings.provider":          "auto",
		"embeddings.model":             "nomic-embed-text",
		"embeddings.url":               "http://localhost:11434",
		"cluster.threshold":            0.0,
		"redact.patterns":              []string{},
		"remedies.hooks_dir":           "~/src/claude-hooks",
		"remedies.global_claude_md":    "~/.claude/CLAUDE.md",
		"remedies.skills_dir":          "~/.claude/skills",
		"remedies.settings":            "~/.claude/settings.json",
		"db":                           filepath.Join(DataDir(), "sminer.db"),
		"patch_dir":                    filepath.Join(DataDir(), "patches"),
		"json":                         false,
		"quiet":                        false,
		"verbose":                      false,
		"no_color":                     false,
	}
}

// Keys lists every configuration key in a stable order.
func Keys() []string {
	return []string{
		"roots", "window", "min_count", "offline", "exclude_repos",
		"classifier.model", "classifier.max_calls_per_run", "classifier.concurrency",
		"embeddings.provider", "embeddings.model", "embeddings.url",
		"cluster.threshold", "redact.patterns",
		"remedies.hooks_dir", "remedies.global_claude_md", "remedies.skills_dir", "remedies.settings",
		"db", "patch_dir",
		"json", "quiet", "verbose", "no_color",
	}
}

// EnvVar is the environment variable that sets key.
func EnvVar(key string) string {
	return EnvPrefix + "_" + strings.ToUpper(strings.ReplaceAll(key, ".", "_"))
}

// Dir is $XDG_CONFIG_HOME/sminer, defaulting to ~/.config/sminer.
func Dir() string { return xdg("XDG_CONFIG_HOME", ".config") }

// DataDir is $XDG_DATA_HOME/sminer, defaulting to ~/.local/share/sminer.
func DataDir() string { return xdg("XDG_DATA_HOME", ".local/share") }

// xdg returns the sminer directory under the XDG base directory named by env,
// if that is set to an absolute path, otherwise under fallback in the home
// directory.
func xdg(env, fallback string) string {
	if d := os.Getenv(env); d != "" && filepath.IsAbs(d) {
		return filepath.Join(d, "sminer")
	}
	return filepath.Join(Expand("~"), fallback, "sminer")
}

// DefaultFile is the preferred config file location, and the one suggested
// when none exists yet.
func DefaultFile() string { return filepath.Join(Dir(), "config.yaml") }

// SearchPaths lists the config files tried, in order, when none is named.
func SearchPaths() []string {
	return []string{DefaultFile(), filepath.Join(Expand("~"), ".sminer.yaml"), ".sminer.yaml"}
}

// Find returns the first config file in SearchPaths that exists, or "" when
// there is none.
func Find() string {
	for _, f := range SearchPaths() {
		if _, err := os.Stat(f); err == nil {
			return f
		}
	}
	return ""
}

// Setup prepares v with defaults and environment binding. Call it before
// binding flags and before Load.
func Setup(v *viper.Viper) {
	for k, val := range defaults() {
		v.SetDefault(k, val)
	}
	v.SetEnvPrefix(EnvPrefix)
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()
}

// ErrNoFile is returned when an explicitly named config file is missing.
var ErrNoFile = errors.New("config file not found")

// ErrInvalid wraps every validation failure, so callers can treat a bad
// value as a usage error.
var ErrInvalid = errors.New("invalid configuration")

// Load reads the config file (path, or the first of SearchPaths when empty)
// into v and returns the resolved configuration and the file actually used
// ("" when none). Having no file is fine; a missing named one is not.
func Load(v *viper.Viper, path string) (Config, string, error) {
	file := path
	if file == "" {
		file = Find()
	}
	file = Expand(file)
	used := ""
	if _, err := os.Stat(file); file != "" && err == nil {
		v.SetConfigFile(file)
		if err := v.ReadInConfig(); err != nil {
			return Config{}, "", fmt.Errorf("read %s: %w", file, err)
		}
		used = file
	} else if path != "" {
		return Config{}, "", fmt.Errorf("%w: %s", ErrNoFile, file)
	}
	var c Config
	if err := v.Unmarshal(&c); err != nil {
		return Config{}, "", fmt.Errorf("parse config: %w", err)
	}
	if err := c.Validate(); err != nil {
		return Config{}, "", err
	}
	return c, used, nil
}

// Validate checks values that would otherwise fail deep in a run.
func (c Config) Validate() error {
	if _, err := ParseDuration(c.Window); err != nil {
		return fmt.Errorf("%w: window (--since): %v", ErrInvalid, err)
	}
	if c.MinCount < 1 {
		return fmt.Errorf("%w: min_count (--min-count) must be at least 1, got %d", ErrInvalid, c.MinCount)
	}
	switch c.Embeddings.Provider {
	case "auto", "ollama", "hash":
	default:
		return fmt.Errorf("%w: embeddings.provider must be auto, ollama or hash, got %q", ErrInvalid, c.Embeddings.Provider)
	}
	return nil
}

// Default is the configuration with nothing set.
func Default() Config {
	v := viper.New()
	for k, val := range defaults() {
		v.SetDefault(k, val)
	}
	var c Config
	_ = v.Unmarshal(&c)
	return c
}

// WindowDuration is Window parsed.
func (c Config) WindowDuration() time.Duration {
	d, _ := ParseDuration(c.Window)
	return d
}

// Expand replaces a leading ~ with the home directory.
func Expand(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
	}
	return p
}

// ParseDuration accepts Go durations plus d (days) and w (weeks).
func ParseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errors.New("empty duration")
	}
	unit := s[len(s)-1]
	if unit == 'd' || unit == 'w' {
		n, err := strconv.Atoi(s[:len(s)-1])
		if err != nil || n < 0 {
			return 0, fmt.Errorf("bad duration %q (want e.g. 90d, 2w, 12h)", s)
		}
		per := 24 * time.Hour
		if unit == 'w' {
			per *= 7
		}
		if int64(n) > int64(time.Duration(math.MaxInt64)/per) {
			return 0, fmt.Errorf("duration %q is too long", s)
		}
		return time.Duration(n) * per, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("bad duration %q (want e.g. 90d, 2w, 12h)", s)
	}
	return d, nil
}

// RepoExcluded reports whether repo matches any exclude_repos glob. A pattern
// matches the repo itself or any of its ancestors, so "~/personal/*" also
// excludes "~/personal/x/sub".
func (c Config) RepoExcluded(repo string) bool {
	for _, pat := range c.ExcludeRepos {
		pat = Expand(pat)
		for p := filepath.Clean(repo); ; p = filepath.Dir(p) {
			if ok, _ := filepath.Match(pat, p); ok {
				return true
			}
			if p == filepath.Dir(p) {
				break
			}
		}
	}
	return false
}
