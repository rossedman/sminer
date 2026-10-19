package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"

	"github.com/spf13/cobra"

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
	defer a.close()
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(a.err, "%s sminer crashed: %v\n\n%s\nThis is a bug in sminer, not something you did. Please report it at %s\nwith the command you ran and this output (version %s).\n",
				a.style(red, "error:"), r, debug.Stack(), issuesURL, version.Get().Version)
			code = ExitFailure
		}
	}()
	root := newRootCmd(a)
	root.SetArgs(argv)
	root.SetOut(a.out)
	root.SetErr(a.err)
	err := root.ExecuteContext(ctx)
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	if err == nil {
		return ExitOK
	}
	return printError(a.err, err, a.g.json, a.style)
}

const rootLong = `sminer reads your Claude Code session transcripts, finds the corrections
you keep repeating, and proposes fixes: a hook, a CLAUDE.md line, or a skill.

It never edits your CLAUDE.md, settings or hooks. Accepting a finding writes a
patch file you review and apply yourself.

Network: only classification and proposal drafting call the Anthropic API,
and only candidate turns (about 2 KB each) are sent. --offline or
SMINER_OFFLINE=1 keeps everything on this machine.`

const exitCodesHelp = `
Exit codes: 0 ok, 1 failure, 2 usage, 4 not found, 5 precondition failed, 130 interrupted.
`

const linksHelp = `
Docs: https://github.com/rossedman/sminer#readme
Report a bug: ` + issuesURL + "\n"

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
	root.SetHelpTemplate(root.HelpTemplate() + `{{if not .HasParent}}` + exitCodesHelp + `{{end}}` + linksHelp)

	pf := root.PersistentFlags()
	pf.StringVar(&a.g.configFile, "config", "", "config file (default $XDG_CONFIG_HOME/sminer/config.yaml)")
	pf.String("db", "", "database file (default $XDG_DATA_HOME/sminer/sminer.db)")
	bindFlag(pf, "db", "db")
	pf.Bool("offline", false, "never call the network; heuristics and local clustering only")
	bindFlag(pf, "offline", "offline")
	pf.BoolVar(&a.g.json, "json", false, "print machine-readable JSON on stdout")
	pf.BoolVarP(&a.g.quiet, "quiet", "q", false, "print nothing on stderr except errors")
	pf.BoolVarP(&a.g.verbose, "verbose", "v", false, "print detail on stderr")
	pf.BoolVar(&a.g.noColor, "no-color", false, "disable color (also NO_COLOR=1)")
	root.MarkFlagsMutuallyExclusive("quiet", "verbose")

	root.AddGroup(
		&cobra.Group{ID: "pipeline", Title: "Mining:"},
		&cobra.Group{ID: "findings", Title: "Acting on findings:"},
		&cobra.Group{ID: "other", Title: "Other:"},
	)
	for _, c := range []*cobra.Command{newRunCmd(a), newIngestCmd(a), newAnalyzeCmd(a), newReportCmd(a)} {
		c.GroupID = "pipeline"
		root.AddCommand(c)
	}
	f := newFindingCmd(a)
	f.GroupID = "findings"
	root.AddCommand(f)
	for _, c := range []*cobra.Command{newConfigCmd(a), newEvalCmd(a), newVersionCmd(a)} {
		c.GroupID = "other"
		root.AddCommand(c)
	}
	root.SetHelpCommandGroupID("other")
	root.SetCompletionCommandGroupID("other")
	addCompat(root, a)
	return root
}
