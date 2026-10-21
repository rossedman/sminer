package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/spf13/cobra"

	"github.com/rossedman/sminer/internal/config"
	"github.com/rossedman/sminer/internal/eval"
	"github.com/rossedman/sminer/internal/signal"
	"github.com/rossedman/sminer/internal/textutil"
)

// defaultLabels is where 'sminer label' writes and 'sminer eval' reads.
func defaultLabels() string { return filepath.Join(config.DataDir(), "labels.jsonl") }

// newLabelCmd builds the label command, which samples candidate turns for the
// user to mark as corrections or not, or exports the sample with --export.
func newLabelCmd(a *app) *cobra.Command {
	var out string
	var limit int
	var seed uint64
	var export bool
	cmd := &cobra.Command{
		Use:   "label",
		Short: "Label your own candidate turns to measure correction detection",
		Long: `Label real turns from your transcripts, so 'sminer eval' measures detection on
your sessions rather than the bundled examples.

A candidate is a message you sent right after Claude used a tool: the turns
sminer decides "correction or not" on. label samples ones you haven't
labelled yet, half that the heuristic flags and half it doesn't, shows each
with what Claude had just done, and asks:

  y  a correction: it pushes back on what Claude did or restates a rule
  n  not a correction
  s  skip
  q  quit (answers so far are kept)

Each answer is appended to --out as soon as it is given. Turn text was
redacted at ingest, but read the file before sharing it.

--export writes the sample to stdout instead, with "correction": null, for
labelling in an editor; 'sminer eval' skips lines still null.`,
		Example: `  sminer label
  sminer label --limit 20 --seed 7
  sminer label --export > batch.jsonl && $EDITOR batch.jsonl && sminer eval --labels batch.jsonl`,
		Args: args(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if limit <= 0 {
				return usageError(cmd, fmt.Errorf("--limit must be positive"))
			}
			if out == "" {
				out = defaultLabels()
			}
			out = config.Expand(out)
			if !export && !a.opts.ForceInteractive && (!isTerminal(a.opts.Stdin) || !isTerminal(a.opts.Stderr)) {
				return withHint(usageError(cmd, errors.New("labelling is interactive and needs a terminal")),
					"use 'sminer label --export > batch.jsonl' to label in an editor instead")
			}
			st, err := a.store()
			if err != nil {
				return err
			}
			sessions, err := st.Sessions()
			if err != nil {
				return err
			}
			ids := make([]string, 0, len(sessions))
			for id := range sessions {
				ids = append(ids, id)
			}
			sort.Strings(ids)
			var cands []signal.Candidate
			for _, id := range ids {
				turns, err := st.SessionTurns(id)
				if err != nil {
					return err
				}
				cands = append(cands, signal.Candidates(turns)...)
			}
			if len(cands) == 0 {
				return withHint(errors.New("no candidate turns in the database"), "run 'sminer ingest' first")
			}
			done, err := eval.Labelled(out)
			if err != nil {
				return err
			}
			if !cmd.Flags().Changed("seed") {
				seed = uint64(a.now.UnixNano())
			}
			sample := eval.Sample(cands, done, limit, seed)
			if len(sample) == 0 {
				a.info("every candidate turn is already labelled in %s", out)
				return nil
			}
			if export {
				for _, c := range sample {
					if err := a.printLine(eval.Unlabelled{Action: c.Action, Message: c.Message, Turn: c.Turn}); err != nil {
						return err
					}
				}
				a.info("exported %d of %d candidate turns; set each \"correction\" to true or false", len(sample), len(cands))
				return nil
			}
			return a.labelInteractive(cmd, sample, out)
		},
	}
	cmd.Flags().StringVar(&out, "out", "", "labels file to append to (default $XDG_DATA_HOME/sminer/labels.jsonl)")
	cmd.Flags().IntVar(&limit, "limit", 50, "turns to label in this session")
	cmd.Flags().Uint64Var(&seed, "seed", 0, "sampling seed, for a repeatable sample (default random)")
	cmd.Flags().BoolVar(&export, "export", false, "write the sample to stdout as JSONL to label by hand")
	return cmd
}

// printLine writes v as one line of JSON on stdout.
func (a *app) printLine(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(a.out, "%s\n", b)
	return err
}

// tally counts one labelling session's answers.
type tally struct{ yes, no, skipped int }

// labelInteractive asks about each candidate in sample and appends every
// answer to out as it is given. It uses a plain line prompt when interactive
// input is forced or stderr is not a terminal, and the full-screen TUI
// otherwise. It prints a tally even if the session ended with an error.
func (a *app) labelInteractive(cmd *cobra.Command, sample []signal.Candidate, out string) error {
	var t tally
	save := func(c signal.Candidate, correction bool) error {
		if err := eval.Append(out, eval.Item{Action: c.Action, Message: c.Message, Correction: correction, Turn: c.Turn}); err != nil {
			return err
		}
		if correction {
			t.yes++
		} else {
			t.no++
		}
		return nil
	}
	var err error
	if a.opts.ForceInteractive || !a.fancyErr() {
		err = a.labelLines(cmd, sample, save, &t)
	} else {
		err = a.labelTUI(cmd, sample, save, &t)
	}
	th := a.theme()
	a.info("%s labelled %d %s, skipped %d %s %s", style(th.OK, "✓"), t.yes+t.no,
		style(th.Faint, fmt.Sprintf("(%d corrections, %d not)", t.yes, t.no)), t.skipped, style(th.Faint, "·"), out)
	if err != nil {
		return err
	}
	if t.yes+t.no == 0 {
		return nil
	}
	a.info("%s", style(th.Faint, "score detection with: sminer eval"))
	if a.g.json {
		return a.printJSON(map[string]any{"out": out, "labelled": t.yes + t.no, "corrections": t.yes, "not_corrections": t.no, "skipped": t.skipped})
	}
	fmt.Fprintln(a.out, out)
	return nil
}

// labelLines asks one line at a time: for tests, and terminals too plain
// for the full-screen labeller.
func (a *app) labelLines(cmd *cobra.Command, sample []signal.Candidate, save func(signal.Candidate, bool) error, t *tally) error {
	lines := make(chan string)
	go func() {
		sc := bufio.NewScanner(a.opts.Stdin)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	th := a.theme()
	w := a.err
	for i, c := range sample {
		fmt.Fprintf(w, "\n%s %s\n", style(th.Faint, fmt.Sprintf("[%d/%d]", i+1, len(sample))), style(th.Muted, "Claude had just done:"))
		for _, l := range strings.Split(c.Action, "\n") {
			fmt.Fprintf(w, "  %s\n", style(th.Muted, l))
		}
		fmt.Fprintf(w, "%s\n", style(th.Muted, "You said:"))
		for _, l := range strings.Split(textutil.Truncate(c.Message, 600), "\n") {
			fmt.Fprintf(w, "  %s\n", style(th.Title, l))
		}
	ask:
		for {
			fmt.Fprint(w, "correction? [y]es [n]o [s]kip [q]uit: ")
			var line string
			var ok bool
			select {
			case <-cmd.Context().Done():
				fmt.Fprintln(w)
				return cmd.Context().Err()
			case line, ok = <-lines:
			}
			if !ok {
				fmt.Fprintln(w)
				return nil
			}
			switch strings.ToLower(strings.TrimSpace(line)) {
			case "y", "yes":
				if err := save(c, true); err != nil {
					return err
				}
				break ask
			case "n", "no":
				if err := save(c, false); err != nil {
					return err
				}
				break ask
			case "s", "skip", "":
				t.skipped++
				break ask
			case "q", "quit":
				return nil
			}
		}
	}
	return nil
}

// labelTUI runs the full-screen labeller.
func (a *app) labelTUI(cmd *cobra.Command, sample []signal.Candidate, save func(signal.Candidate, bool) error, t *tally) error {
	m := &labelModel{th: a.theme(), items: sample, save: save, t: t, width: a.width()}
	p := tea.NewProgram(m, tea.WithContext(cmd.Context()), tea.WithInput(a.opts.Stdin), tea.WithOutput(a.opts.Stderr))
	_, err := p.Run()
	if ctxErr := cmd.Context().Err(); ctxErr != nil {
		return ctxErr
	}
	if m.err != nil {
		return m.err
	}
	if m.interrupted {
		return context.Canceled
	}
	if err != nil && !errors.Is(err, tea.ErrProgramKilled) {
		return err
	}
	return nil
}
