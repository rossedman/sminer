# How it works

sminer is a linear batch pipeline. Each stage reads from and writes to one SQLite file, so you can re-run any stage on its own, and a repeat run does only the new work.

<figure class="diagram" markdown>
[![Transcripts flow through ingest, signal, classify, cluster, remedy and report on your machine, around a SQLite store. Only classify calls the Anthropic API.](assets/how-it-works.png)](assets/how-it-works.png)
</figure>

| Stage | Command | Reads | Writes |
|---|---|---|---|
| Ingest | `sminer ingest` | `~/.claude/projects/**/*.jsonl` | turns, sessions, files |
| Signal | `sminer analyze` | turns | signals |
| Classify | `sminer analyze` | candidate signals | verdicts and rules on signals |
| Cluster | `sminer analyze` | signals | findings |
| Remedy | `sminer analyze` | findings, your CLAUDE.md, hooks and skills | proposals on findings |
| Report | `sminer report` | findings | Markdown, `findings.json` |

`sminer run` does all of them in order.

## Ingest

Claude Code writes one JSONL file per session under `~/.claude/projects/<encoded-cwd>/<session-id>.jsonl`, one event per line. The format is undocumented and changes between releases, so the parser is deliberately tolerant:

- Unknown fields are ignored.
- Message content can be a plain string (1.0.x) or a list of blocks (2.0.x).
- Unknown event types are counted and skipped. They appear in the report's *Parser notes*, so you notice when the format changes.
- A malformed line is skipped and counted. It never stops a run.

Ingest is **incremental**. For each file, sminer remembers the byte offset where it stopped and reads on from there next time. A last line that's still being written is left for the next run.

Before anything is stored, text is **redacted** and truncated. Output from successful tool calls is dropped entirely. Each stored turn points back to its file and byte offset rather than copying the transcript. See [Privacy and safety](privacy.md).

Repositories are attributed by the session's working directory, resolved to its git root when that still exists on disk.

## Signal

Seven detectors look for friction in each session that has new turns: corrections, interrupts, rewinds, repeated instructions, hook blocks, tool failure loops and manual fix-ups. Each emits a *signal* with a type, a weight and a redacted excerpt. [Signals](signals.md) describes each one.

Signal IDs are derived from the turn they came from, so re-running detection never duplicates them, and classifier results attached to them survive.

## Classify

Corrections, rewinds, interrupts with a typed explanation, and repeated instructions are candidates. The classifier (`claude-haiku-5-5` by default) answers three questions about each one:

- Is it really a correction, or a standing convention worth writing down?
- What is the general rule behind it, as one imperative sentence?
- Which remedy fits it: a hook, a CLAUDE.md line, a skill, or nothing?

Each call sends the candidate message and a short summary of what the assistant had just done (tool name, path or command, a snippet), capped near 2 KB. Calls are capped per run (`classifier.max_calls_per_run`) and cached on the signal. If calls keep failing, for example with a bad key, sminer stops early and continues with heuristics alone.

Hook blocks, failure loops and fix-ups are deterministic and never sent.

## Cluster

Signals are grouped into **findings**, one per underlying rule:

- **Corrections, interrupts, rewinds and repeated instructions** cluster on text similarity: the classifier's rule when there is one, otherwise the excerpt. Embeddings are always computed locally, by a built-in hashed bag-of-words model or by [ollama](https://ollama.com) when it's running.
- **Hook blocks** group by hook and script, **failure loops** by command (`go test`), and **fix-ups** by file kind (`*.go`, `go.mod`).

Clusters below `--min-count` occurrences (3 by default) are dropped.

Findings keep a **stable ID** across runs: each new cluster is matched to an existing finding by centroid similarity, or by key for the exact-match families. That's what lets accept and dismiss survive re-analysis, and what makes trends possible.

## Remedy

Each finding is routed to a hook, a CLAUDE.md line, a skill or nothing, then checked against what you already have. [Findings and remedies](findings.md#remedy-routing) covers the rules.

## Report

The report ranks findings by `occurrences × weight × repos`. Weights are 3 for high-signal types (corrections, interrupts, repeated instructions), 2 for medium (rewinds, hook blocks, failure loops) and 1 for fix-ups. It also lists how often each existing hook fired (*Guardrail activity*), the ignored findings, and parser notes.

## Storage

Everything lives in `$XDG_DATA_HOME/sminer` (by default `~/.local/share/sminer`):

| Path | What |
|---|---|
| `sminer.db` | The SQLite database, created with mode `0600`. Pure-Go driver, no cgo. |
| `patches/` | Patches written by `sminer finding accept`. |

Deleting the directory resets sminer completely. The next run starts from scratch.
