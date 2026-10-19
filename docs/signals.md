# Signals

A signal is one piece of evidence that something went wrong in a session. The strongest is a user turn that follows an assistant action and pushes back on it. sminer looks for seven kinds.

| Signal | How it's detected | Weight | Classifier |
|---|---|---|---|
| [Correction](#correction) | A user turn after a tool action that matches pushback cues | High (3) | Confirms |
| [Interrupt](#interrupt) | An interrupt marker, or a tool call rejected at the permission prompt | High (3) | Confirms if you typed an explanation |
| [Rewind](#rewind) | Two user messages branching from the same point | Medium (2) | Confirms |
| [Repeated instruction](#repeated-instruction) | Near-identical guidance in 3 or more sessions | High (3) | Confirms |
| [Hook block](#hook-block) | Hook output showing a guardrail rejected an action | Medium (2) | Never sent |
| [Tool failure loop](#tool-failure-loop) | The same command failing twice or more in a row | Medium (2) | Never sent |
| [Manual fix-up](#manual-fix-up) | A file you changed right after Claude wrote it | Low (1) | Never sent |

Heuristics run first and are cheap. Only candidates go to the classifier, which keeps API use to a few hundred calls for 90 days of transcripts rather than one per turn. With `--offline`, heuristic candidates count as they are.

## Correction

A user message counts as a correction candidate when the assistant used a tool since your previous message and your reply matches at least one strong cue:

- starting with *no*, *nope*, *stop* or *wait*
- *don't*, *never*, *instead*, *revert*, *undo*, *wrong*
- *I said*, *I told you*, *why did you*, *that's not*, *you forgot*
- *we use*, *we always*, *use X, not Y*

Two weak cues also qualify: *actually*, *please use*, *prefer*, *should be*, *remember*, *always*. Phrases like *no problem* or *don't worry* are ignored. In long messages, which are usually new tasks, only the opening is checked.

> No, don't use testify/assert. We use table-driven tests with the standard library.

The classifier then decides whether the message really pushes back, and writes down the general rule it implies: *"Write table-driven tests with the standard library, not testify/assert."*

## Interrupt

Pressing Esc mid-action leaves an interrupt marker in the transcript. Rejecting a tool call at the permission prompt leaves a rejection, often with feedback you typed. Either one is a strong sign the assistant was about to do something you didn't want.

The excerpt is whatever you said next: the rejection feedback, or your following message.

> never force push to main, open a PR instead

If you interrupted without saying anything, the excerpt is the action that was interrupted (`(interrupted) Bash rm -rf ./vendor`). Those cluster by command and aren't sent to the classifier.

## Rewind

When you rewind a conversation and send a different message, the transcript keeps both branches: two user messages share one parent. The later message is the signal, and the earlier attempt is kept as its context.

## Repeated instruction

Some rules never show up as corrections, because you state them up front every time:

> Before committing, run make lint and make test, then write a conventional commit message.

sminer embeds every user prompt of a useful length that contains guidance wording (*always*, *never*, *before*, *after*, *make sure*, *remember*, *prefer*, *instead*, …), clusters near-duplicates, and flags any cluster that spans three or more sessions. One signal is kept per session. Plain task requests are left out, because they repeat for unrelated reasons.

Repeated multi-step instructions are the usual source of [skill](findings.md#skill) proposals.

## Hook block

When an existing hook rejects an action, the tool result or a system event says so:

```text
PreToolUse:Edit hook error: [~/src/claude-hooks/no-lockfiles.sh]: Editing lockfiles is not allowed
```

sminer groups these by event and script (`PreToolUse:Edit no-lockfiles.sh`), or by the message with variable parts masked. They feed two things: the report's *Guardrail activity* table, which shows how often each hook fires, and a finding proposing a CLAUDE.md line. The guardrail already exists; the fix is telling Claude the rule so it stops tripping over it.

## Tool failure loop

The same Bash command failing twice or more in a row, without a success in between, usually means Claude is retrying instead of reading the error. Loops group by program and subcommand (`go test`, `npm run`), and the excerpt includes the first line of the error.

## Manual fix-up

A file that changes right after Claude wrote it suggests you corrected the result by hand. sminer detects this two ways:

- Claude wrote or edited a file, and a later full `Read` shows content it didn't write. Whitespace-only changes are ignored, so formatters don't count.
- Claude Code's own notice that a file was modified outside the session.

Fix-ups group by file kind (`*.go`, `go.mod`). They're the weakest signal, since a linter can look like a person, so they carry the lowest weight.
