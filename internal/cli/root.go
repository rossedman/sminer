package cli

import (
	"context"
	"fmt"
	"image/color"
	"io"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"

	"charm.land/fang/v2"
	"charm.land/lipgloss/v2"
	"github.com/spf13/cobra"

	"github.com/rossedman/sminer/internal/ui"
	"github.com/rossedman/sminer/internal/version"
)

// Execute runs sminer with the process's arguments and returns the exit code.
func Execute() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return Run(ctx, os.Args[1:], Options{})
}

// Run executes one invocation. It never calls os.Exit, so tests can drive it.
func Run(ctx context.Context, argv []string, opts Options) (code int) {
	a := newApp(opts)
	if noColorArg(argv) {
		// Before anything renders, so help and usage errors honor it too.
		os.Setenv("NO_COLOR", "1")
	}
	a.out = ui.NewWriter(a.opts.Stdout, false)
	a.err = ui.NewWriter(a.opts.Stderr, false)
	defer a.close()
	defer func() {
		if r := recover(); r != nil {
			a.spin.finish(false)
			fmt.Fprintf(a.err, "%s sminer crashed: %v\n\n%s\nThis is a bug in sminer, not something you did. Please report it at %s\nwith the command you ran and this output (version %s).\n",
				style(a.theme().Err.Bold(true), "error:"), r, debug.Stack(), issuesURL, version.Get().Version)
			code = ExitFailure
		}
	}()
	root := newRootCmd(a)
	root.SetArgs(argv)
	// Help is rendered by fang, which does its own color detection on the
	// real streams.
	root.SetOut(a.opts.Stdout)
	root.SetErr(a.opts.Stderr)
	err := fang.Execute(ctx, root,
		fang.WithColorSchemeFunc(helpColors),
		fang.WithoutVersion(),
		fang.WithErrorHandler(func(io.Writer, fang.Styles, error) {}), // printed below, with exit codes
	)
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	a.spin.finish(err == nil)
	if err == nil {
		return ExitOK
	}
	return a.printError(err)
}

// helpColors is fang's help color scheme in the brand palette.
func helpColors(c lipgloss.LightDarkFunc) fang.ColorScheme {
	p := ui.NewPalette(c(nil, lipgloss.Color("#000")) != nil)
	return fang.ColorScheme{
		Base:           p.Text,
		Title:          p.Ore,
		Description:    p.Text,
		Codeblock:      p.Surface,
		Program:        p.Ore,
		DimmedArgument: p.Muted,
		Comment:        p.Faint,
		Flag:           p.Gem,
		FlagDefault:    p.Muted,
		Command:        p.Violet,
		QuotedString:   p.Ember,
		Argument:       p.Text,
		Help:           p.Text,
		Dash:           p.Muted,
		ErrorHeader:    [2]color.Color{p.Ink, p.Ember},
		ErrorDetails:   p.Ember,
	}
}

const rootLong = `sminer reads your Claude Code session transcripts, finds the corrections
you keep repeating, and proposes fixes: a hook, a CLAUDE.md line, or a skill.

It never edits your CLAUDE.md, settings or hooks. Accepting a finding writes a
patch file you review and apply yourself.

Network: only classification and proposal drafting call the Anthropic API,
and only candidate turns (about 2 KB each) are sent. --offline or
SMINER_OFFLINE=1 keeps everything on this machine.` + "\n" + exitCodesHelp + linksHelp

const exitCodesHelp = `
Exit codes: 0 ok, 1 failure, 2 usage, 4 not found, 5 precondition failed, 130 interrupted.
`

const linksHelp = `
Docs: https://github.com/rossedman/sminer#readme
Report a bug: ` + issuesURL + "\n"

// newRootCmd builds the sminer root command with its global flags and every
// subcommand. Commands are listed in workflow order, and the persistent pre-
// run warns about deprecated commands and loads the config unless the command
// opts out.
func newRootCmd(a *app) *cobra.Command {
	cobra.EnableCommandSorting = false // list commands in workflow order
	root := &cobra.Command{
		Use:     "sminer",
		Short:   "Find the corrections you keep making to Claude, and fix them for good",
		Long:    rootLong,
		Version: version.Get().Version,
		Example: `  # Offline first pass over the last 90 days, report to the terminal
  sminer run --offline

  # Full run with the classifier, report saved to a file
  sminer run --output report.md

  # Review and act on findings
  sminer finding list
  sminer finding accept F1a2b3c --preview
  sminer finding accept F1a2b3c`,
		SilenceErrors:     true,
		SilenceUsage:      true,
		DisableAutoGenTag: true,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			a.warnDeprecated(cmd)
			if cmd.Annotations["config"] == "skip" {
				return nil
			}
			return a.load(cmd)
		},
	}
	root.SetVersionTemplate("sminer {{.Version}}\n")
	root.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error { return usageError(cmd, err) })

	pf := root.PersistentFlags()
	pf.StringVar(&a.g.configFile, "config", "", "config file (default $XDG_CONFIG_HOME/sminer/config.yaml)")
	pf.String("db", "", "database file (default $XDG_DATA_HOME/sminer/sminer.db)")
	bindFlag(pf, "db", "db")
	pf.Bool("offline", false, "never call the network; heuristics and local clustering only")
	bindFlag(pf, "offline", "offline")
	pf.BoolVar(&a.g.json, "json", false, "print machine-readable JSON on stdout")
	bindFlag(pf, "json", "json")
	pf.BoolVarP(&a.g.quiet, "quiet", "q", false, "print nothing on stderr except errors")
	bindFlag(pf, "quiet", "quiet")
	pf.BoolVarP(&a.g.verbose, "verbose", "v", false, "print detail on stderr")
	bindFlag(pf, "verbose", "verbose")
	pf.BoolVar(&a.g.noColor, "no-color", false, "disable color (also NO_COLOR=1)")
	bindFlag(pf, "no-color", "no_color")
	root.MarkFlagsMutuallyExclusive("quiet", "verbose")

	root.AddGroup(
		&cobra.Group{ID: "pipeline", Title: "Mining"},
		&cobra.Group{ID: "findings", Title: "Acting on findings"},
		&cobra.Group{ID: "other", Title: "Other"},
	)
	for _, c := range []*cobra.Command{newRunCmd(a), newIngestCmd(a), newAnalyzeCmd(a), newReportCmd(a)} {
		c.GroupID = "pipeline"
		root.AddCommand(c)
	}
	for _, c := range []*cobra.Command{newReviewCmd(a), newFindingCmd(a)} {
		c.GroupID = "findings"
		root.AddCommand(c)
	}
	for _, c := range []*cobra.Command{newConfigCmd(a), newLabelCmd(a), newEvalCmd(a), newVersionCmd(a)} {
		c.GroupID = "other"
		root.AddCommand(c)
	}
	root.SetHelpCommandGroupID("other")
	root.SetCompletionCommandGroupID("other")
	addCompat(root, a)
	return root
}
