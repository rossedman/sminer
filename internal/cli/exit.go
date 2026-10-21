package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/spf13/cobra"

	"github.com/rossedman/sminer/internal/config"
	"github.com/rossedman/sminer/internal/remedy"
	"github.com/rossedman/sminer/internal/store"
)

// Exit codes are part of the interface: documented, never renumbered.
const (
	ExitOK          = 0   // success, including empty results
	ExitFailure     = 1   // runtime error
	ExitUsage       = 2   // bad flags, arguments or config values
	ExitNotFound    = 4   // the named finding does not exist
	ExitConflict    = 5   // a precondition failed (nothing to patch, target exists)
	ExitInterrupted = 130 // Ctrl-C
)

const issuesURL = "https://github.com/rossedman/sminer/issues"

// cliError carries an exit code, a stable machine code and a next step.
type cliError struct {
	exit int
	code string
	err  error
	hint string
}

// Error returns the message of the wrapped error.
func (e *cliError) Error() string { return e.err.Error() }

// Unwrap returns the wrapped error so errors.Is and errors.As see through
// cliError.
func (e *cliError) Unwrap() error { return e.err }

// usageError reports a mistake in how the command was invoked.
func usageError(cmd *cobra.Command, err error) error {
	return &cliError{exit: ExitUsage, code: "usage", err: err,
		hint: fmt.Sprintf("run '%s --help' for usage", cmd.CommandPath())}
}

// withHint attaches a next step to err.
func withHint(err error, hint string) error {
	var ce *cliError
	if errors.As(err, &ce) {
		ce.hint = hint
		return ce
	}
	return &cliError{err: err, hint: hint}
}

// args wraps a cobra argument validator so its failures are usage errors.
func args(v cobra.PositionalArgs) cobra.PositionalArgs {
	return func(cmd *cobra.Command, a []string) error {
		if err := v(cmd, a); err != nil {
			return usageError(cmd, err)
		}
		return nil
	}
}

// resolve maps any error to its exit code, machine code and hint.
func resolve(err error) (exit int, code, hint string) {
	exit, code = ExitFailure, "failure"
	var ce *cliError
	if errors.As(err, &ce) {
		hint = ce.hint
		if ce.exit != 0 {
			return ce.exit, ce.code, hint
		}
	}
	switch {
	case errors.Is(err, context.Canceled):
		return ExitInterrupted, "interrupted", ""
	case errors.Is(err, store.ErrNotFound):
		return ExitNotFound, "not_found", or(hint, "list findings with 'sminer finding list --status all'")
	case errors.Is(err, remedy.ErrNoRemedy):
		return ExitConflict, "no_remedy", or(hint, "run 'sminer analyze' first")
	case errors.Is(err, remedy.ErrNotPatchable):
		return ExitConflict, "not_patchable", or(hint, "see why with 'sminer finding show <id>'")
	case errors.Is(err, remedy.ErrTargetExists):
		return ExitConflict, "target_exists", or(hint, "move the existing file aside, or dismiss the finding")
	case errors.Is(err, config.ErrInvalid):
		return ExitUsage, "config", or(hint, "check the value; 'sminer config show' shows where each setting came from")
	case errors.Is(err, config.ErrNoFile):
		return ExitUsage, "config", or(hint, "create it, or drop --config to run on defaults (see 'sminer config path')")
	case isCobraUsage(err):
		return ExitUsage, "usage", or(hint, "run 'sminer --help' for usage")
	}
	return exit, code, hint
}

// isCobraUsage recognises usage errors cobra raises before our code runs.
func isCobraUsage(err error) bool {
	msg := err.Error()
	for _, p := range []string{"unknown command", "unknown flag", "unknown shorthand", "required flag", "if any flags in the group", "invalid argument"} {
		if strings.Contains(msg, p) {
			return true
		}
	}
	return false
}

// or returns a, or b when a is empty.
func or(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// printError prints err (one sentence, then the next step) and returns its
// exit code. Under --json the error is also a JSON object on stderr. On a
// terminal it's a badge and the hint; elsewhere "error:" and "hint:" lines,
// so scripts and logs can grep for them.
func (a *app) printError(err error) int {
	w := a.err
	exit, code, hint := resolve(err)
	if exit == ExitInterrupted {
		fmt.Fprintln(w, "interrupted")
		return exit
	}
	if a.g.json {
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{
			"code": code, "exit": exit, "message": err.Error(), "hint": hint,
		}})
		return exit
	}
	t := a.theme()
	if isTerminal(a.opts.Stderr) {
		badge := lipgloss.NewStyle().Bold(true).Padding(0, 1).Foreground(t.P.Ink).Background(t.P.Ember).Render("ERROR")
		fmt.Fprintf(w, "\n%s %s\n", badge, style(t.Text, err.Error()))
		if hint != "" {
			fmt.Fprintf(w, "%s %s\n", style(t.Faint, "      →"), style(t.Muted, hint))
		}
		fmt.Fprintln(w)
		return exit
	}
	fmt.Fprintf(w, "error: %s\n", err)
	if hint != "" {
		fmt.Fprintf(w, "hint: %s\n", hint)
	}
	return exit
}
