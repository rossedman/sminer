// Package cli is sminer's command-line interface: a cobra command tree over
// viper-resolved configuration. Commands parse input, call into the
// pipeline and finding packages, and render results; they hold no domain
// logic of their own.
//
// Streams are typed: stdout carries only data (reports, JSON, patch paths),
// stderr carries progress, warnings and errors.
package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"

	"github.com/rossedman/sminer/internal/classify"
	"github.com/rossedman/sminer/internal/config"
	"github.com/rossedman/sminer/internal/finding"
	"github.com/rossedman/sminer/internal/pipeline"
	"github.com/rossedman/sminer/internal/store"
	"github.com/rossedman/sminer/internal/ui"
)

// Options inject I/O and dependencies; zero values use the real ones.
type Options struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	Now    func() time.Time
	// NewLLM builds the classifier client for a model.
	NewLLM func(model string) classify.LLM
	// ForceInteractive treats stdin and stderr as terminals (for tests).
	ForceInteractive bool
}

func (o Options) withDefaults() Options {
	if o.Stdin == nil {
		o.Stdin = os.Stdin
	}
	if o.Stdout == nil {
		o.Stdout = os.Stdout
	}
	if o.Stderr == nil {
		o.Stderr = os.Stderr
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.NewLLM == nil {
		o.NewLLM = func(model string) classify.LLM { return classify.NewAnthropic(model) }
	}
	return o
}

// globals are the persistent flags every command accepts.
type globals struct {
	configFile string
	json       bool
	quiet      bool
	verbose    bool
	noColor    bool
}

// app is the state of one invocation.
type app struct {
	opts    Options
	out     io.Writer
	err     io.Writer
	v       *viper.Viper
	g       globals
	cfg     config.Config
	cfgFile string // config file actually read, "" if none
	loaded  bool
	st      *store.Store
	now     time.Time
	th      *ui.Theme
	spin    *spinner
}

func newApp(opts Options) *app {
	opts = opts.withDefaults()
	v := viper.New()
	config.Setup(v)
	a := &app{opts: opts, out: opts.Stdout, err: opts.Stderr, v: v, now: opts.Now()}
	a.spin = &spinner{a: a}
	return a
}

// viperKey is the flag annotation naming the config key a flag sets.
const viperKey = "sminer/viper-key"

// bindFlag marks a flag as setting a config key, so the flag beats the
// environment, which beats the config file, which beats the default.
func bindFlag(fs *pflag.FlagSet, name, key string) {
	_ = fs.SetAnnotation(name, viperKey, []string{key})
}

// load binds the running command's flags to their config keys and resolves
// configuration. It is idempotent.
func (a *app) load(cmd *cobra.Command) error {
	if a.loaded {
		return nil
	}
	// Several flags may set one key (a deprecated alias and its
	// replacement); bind the ones the user set last so they win.
	var unset, set []*pflag.Flag
	collect := func(f *pflag.Flag) {
		if keys := f.Annotations[viperKey]; len(keys) == 1 {
			if f.Changed {
				set = append(set, f)
			} else {
				unset = append(unset, f)
			}
		}
	}
	cmd.Flags().VisitAll(collect)
	cmd.InheritedFlags().VisitAll(collect)
	for _, f := range append(unset, set...) {
		if err := a.v.BindPFlag(f.Annotations[viperKey][0], f); err != nil {
			return err
		}
	}
	cfg, file, err := config.Load(a.v, a.g.configFile)
	if err != nil {
		return err
	}
	a.cfg, a.cfgFile, a.loaded = cfg, file, true
	return nil
}

// source names the layer that set key: flag, env, file or default.
func (a *app) source(cmd *cobra.Command, key string) string {
	found := ""
	visit := func(f *pflag.Flag) {
		if keys := f.Annotations[viperKey]; len(keys) == 1 && keys[0] == key && f.Changed {
			found = "flag --" + f.Name
		}
	}
	cmd.Flags().VisitAll(visit)
	cmd.InheritedFlags().VisitAll(visit)
	switch {
	case found != "":
		return found
	case os.Getenv(config.EnvVar(key)) != "":
		return "env " + config.EnvVar(key)
	case a.v.InConfig(key):
		return "file " + a.cfgFile
	}
	return "default"
}

// store opens the database on first use.
func (a *app) store() (*store.Store, error) {
	if a.st != nil {
		return a.st, nil
	}
	st, err := store.Open(config.Expand(a.cfg.DB))
	if err != nil {
		return nil, fmt.Errorf("open database %s: %w", a.cfg.DB, err)
	}
	a.st = st
	return st, nil
}

func (a *app) close() {
	if a.st != nil {
		a.st.Close()
	}
}

// env is the pipeline environment for this invocation.
func (a *app) env() (pipeline.Env, error) {
	st, err := a.store()
	if err != nil {
		return pipeline.Env{}, err
	}
	env := pipeline.Env{Store: st, Config: a.cfg, Now: a.now, Progress: a.progress}
	if !a.cfg.Offline {
		env.NewLLM = a.opts.NewLLM
	}
	return env, nil
}

// findings is the finding service for this invocation.
func (a *app) findings() (finding.Service, error) {
	st, err := a.store()
	if err != nil {
		return finding.Service{}, err
	}
	return finding.Service{Store: st, Config: a.cfg, Now: a.now}, nil
}

// progress reports a milestone on stderr, one line each, unless --quiet.
func (a *app) progress(format string, args ...any) {
	if a.g.quiet {
		return
	}
	a.spin.step(fmt.Sprintf(format, args...))
}

// info prints a short human note on stderr unless --quiet.
func (a *app) info(format string, args ...any) {
	if !a.g.quiet {
		a.spin.pause()
		fmt.Fprintf(a.err, format+"\n", args...)
	}
}

// done reports a stage's outcome. On a terminal it replaces the spinner's
// line (title, then muted detail); elsewhere it prints plain, a line that
// scripts and logs can match.
func (a *app) done(plain, title, detail string) {
	if a.g.quiet {
		return
	}
	if a.fancyErr() && a.spin.outcome(title, detail) {
		return
	}
	a.info("%s", plain)
}

// debugf prints detail only with --verbose.
func (a *app) debugf(format string, args ...any) {
	if a.g.verbose {
		a.spin.pause()
		fmt.Fprintln(a.err, style(a.theme().Faint, fmt.Sprintf(format, args...)))
	}
}

// warn always prints, prefixed so it survives without color.
func (a *app) warn(format string, args ...any) {
	a.spin.pause()
	fmt.Fprintln(a.err, style(a.theme().Warn.Bold(true), "warning:")+" "+fmt.Sprintf(format, args...))
}

// printJSON writes v to stdout as indented JSON.
func (a *app) printJSON(v any) error {
	enc := json.NewEncoder(a.out)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
