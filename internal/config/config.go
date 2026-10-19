// Package config loads ~/.config/sminer/config.yaml.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config mirrors config.yaml. Every field has a usable default.
type Config struct {
	Roots        []string   `yaml:"roots"`
	Window       string     `yaml:"window"`
	ExcludeRepos []string   `yaml:"exclude_repos"`
	Classifier   Classifier `yaml:"classifier"`
	Embeddings   Embeddings `yaml:"embeddings"`
	Cluster      Cluster    `yaml:"cluster"`
	Redact       Redact     `yaml:"redact"`
	Remedies     Remedies   `yaml:"remedies"`
	DB           string     `yaml:"db"`
	PatchDir     string     `yaml:"patch_dir"`
}

type Classifier struct {
	Model          string `yaml:"model"`
	MaxCallsPerRun int    `yaml:"max_calls_per_run"`
	Concurrency    int    `yaml:"concurrency"`
}

type Embeddings struct {
	// Provider is "auto" (ollama if reachable, else hash), "ollama" or "hash".
	Provider string `yaml:"provider"`
	Model    string `yaml:"model"`
	URL      string `yaml:"url"`
}

type Cluster struct {
	// Threshold is the cosine similarity needed to join a cluster.
	// Zero means the embedder's default.
	Threshold float64 `yaml:"threshold"`
}

type Redact struct {
	Patterns []string `yaml:"patterns"`
}

type Remedies struct {
	HooksDir       string `yaml:"hooks_dir"`
	GlobalClaudeMD string `yaml:"global_claude_md"`
	SkillsDir      string `yaml:"skills_dir"`
	Settings       string `yaml:"settings"`
}

// Default returns the built-in configuration.
func Default() Config {
	return Config{
		Roots:      []string{"~/.claude/projects"},
		Window:     "90d",
		Classifier: Classifier{Model: "claude-haiku-5-5", MaxCallsPerRun: 500, Concurrency: 4},
		Embeddings: Embeddings{Provider: "auto", Model: "nomic-embed-text", URL: "http://localhost:11434"},
		Remedies: Remedies{
			HooksDir:       "~/src/claude-hooks",
			GlobalClaudeMD: "~/.claude/CLAUDE.md",
			SkillsDir:      "~/.claude/skills",
			Settings:       "~/.claude/settings.json",
		},
		DB:       "~/.local/share/sminer/sminer.db",
		PatchDir: "~/.local/share/sminer/patches",
	}
}

// DefaultPath is where Load looks when no path is given.
func DefaultPath() string { return Expand("~/.config/sminer/config.yaml") }

// Load reads path over the defaults. A missing file at the default path is
// not an error; a missing explicit path is.
func Load(path string) (Config, error) {
	cfg := Default()
	explicit := path != ""
	if !explicit {
		path = DefaultPath()
	}
	b, err := os.ReadFile(Expand(path))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) && !explicit {
			return cfg, nil
		}
		return cfg, err
	}
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		return cfg, fmt.Errorf("parse %s: %w", path, err)
	}
	if _, err := ParseDuration(cfg.Window); err != nil {
		return cfg, fmt.Errorf("window: %w", err)
	}
	return cfg, nil
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
			return 0, fmt.Errorf("bad duration %q", s)
		}
		d := time.Duration(n) * 24 * time.Hour
		if unit == 'w' {
			d *= 7
		}
		return d, nil
	}
	return time.ParseDuration(s)
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
			if p == "/" || p == "." || p == filepath.Dir(p) {
				break
			}
		}
	}
	return false
}
