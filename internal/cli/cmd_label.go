package cli

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/rossedman/sminer/internal/config"
	"github.com/rossedman/sminer/internal/eval"
	"github.com/rossedman/sminer/internal/signal"
	"github.com/rossedman/sminer/internal/textutil"
)

// defaultLabels is where 'sminer label' writes and 'sminer eval' reads.
func defaultLabels() string { return filepath.Join(config.DataDir(), "labels.jsonl") }

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
			if !export && !a.opts.ForceInteractive && (!isTerminal(a.opts.Stdin) || !isTerminal(a.err)) {
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

func (a *app) labelInteractive(cmd *cobra.Command, sample []signal.Candidate, out string) error {
	lines := make(chan string)
	go func() {
		sc := bufio.NewScanner(a.opts.Stdin)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	var yes, no, skipped int
	summary := func() {
		a.info("labelled %d (%d corrections, %d not), skipped %d; saved to %s", yes+no, yes, no, skipped, out)
		if yes+no > 0 {
			a.info("score detection with: sminer eval")
		}
	}
	finish := func() error {
		summary()
		if yes+no == 0 {
			return nil
		}
		if a.g.json {
			return a.printJSON(map[string]any{"out": out, "labelled": yes + no, "corrections": yes, "not_corrections": no, "skipped": skipped})
		}
		fmt.Fprintln(a.out, out)
		return nil
	}
	w := a.err
	for i, c := range sample {
		fmt.Fprintf(w, "\n%s %s\n", a.style(dim, fmt.Sprintf("[%d/%d]", i+1, len(sample))), a.style(dim, "Claude had just done:"))
		for _, l := range strings.Split(c.Action, "\n") {
			fmt.Fprintf(w, "  %s\n", a.style(dim, l))
		}
		fmt.Fprintf(w, "%s\n", a.style(dim, "You said:"))
		for _, l := range strings.Split(textutil.Truncate(c.Message, 600), "\n") {
			fmt.Fprintf(w, "  %s\n", a.style(bold, l))
		}
	ask:
		for {
			fmt.Fprint(w, "correction? [y]es [n]o [s]kip [q]uit: ")
			var line string
			var ok bool
			select {
			case <-cmd.Context().Done():
				fmt.Fprintln(w)
				summary()
				return cmd.Context().Err()
			case line, ok = <-lines:
			}
			if !ok {
				fmt.Fprintln(w)
				return finish()
			}
			switch strings.ToLower(strings.TrimSpace(line)) {
			case "y", "yes":
				if err := eval.Append(out, eval.Item{Action: c.Action, Message: c.Message, Correction: true, Turn: c.Turn}); err != nil {
					return err
				}
				yes++
				break ask
			case "n", "no":
				if err := eval.Append(out, eval.Item{Action: c.Action, Message: c.Message, Correction: false, Turn: c.Turn}); err != nil {
					return err
				}
				no++
				break ask
			case "s", "skip", "":
				skipped++
				break ask
			case "q", "quit":
				return finish()
			}
		}
	}
	return finish()
}
