# sminer

`sminer` (session miner) is a local Go CLI that reads your Claude Code session transcripts, finds the corrections and friction you keep repeating across sessions, and proposes concrete fixes: a hook, a CLAUDE.md line, or a skill. It turns your guardrails from hand-curated into evidence-driven.

It never edits CLAUDE.md, settings or hooks itself. Its output is a ranked report plus a ready-to-apply patch for each finding you accept.

## Install

```sh
go install github.com/rossedman/sminer/cmd/sminer@latest
```

It is a single binary with no cgo. The SQLite driver is pure Go (`modernc.org/sqlite`).

## Quick start

```sh
sminer run --no-llm      # offline first pass: ingest, analyze, report
open sminer-report.md

export ANTHROPIC_API_KEY=...
sminer run               # LLM confirms candidates and writes better proposals

sminer show F1a2b3c      # every excerpt behind one finding
sminer accept F1a2b3c    # writes ~/.local/share/sminer/patches/F1a2b3c.patch
sminer dismiss F9d8e7f --reason "intentional in this repo"
sminer trend             # weekly rate per finding, before vs after adoption
```

## Commands

```
sminer ingest   [--since 90d] [--root ~/.claude/projects]
sminer analyze  [--repo <path>] [--min-count 3] [--no-llm]
sminer report   [--format md|json] [--out ./sminer-report.md]
sminer show     <finding-id>        # all excerpts for one finding
sminer accept   <finding-id>        # write the patch file, mark adopted
sminer dismiss  <finding-id> [--reason ...]
sminer trend    [<finding-id>]      # rate per week, before vs after adoption
sminer run                          # ingest + analyze + report
sminer eval     [--labels file] [--live]
```

Every command also accepts `--config` and `--db`.

- **ingest** is incremental. Each transcript is re-read only from the byte offset where the last run stopped. A partially written last line is left for the next run.
- **analyze** recomputes signals for sessions with new turns, confirms candidates with the classifier (unless you pass `--no-llm`), clusters the signals into findings, and routes each finding to a remedy.
- **report** writes the Markdown report, with `findings.json` beside it. Findings are ranked by `count × weight × repos`.
- **accept** renders the finding's patch against the target as it is right now and records the adoption date, so `trend` can show whether the fix worked. Apply one patch before you accept the next one for the same file.

## What it looks for

| Signal | How it is detected | Weight |
|---|---|---|
| Correction | User turn after a tool action matching pushback cues ("no,", "don't", "instead", "I said", "revert", ...), then confirmed by the classifier | High |
| Interrupt | User interrupt marker, or a tool call rejected at the permission prompt (with the feedback typed there) | High |
| Rewind / branch | A `parentUuid` with more than one user-message child | Medium |
| Repeated instruction | Near-duplicate guidance-like prompts in 3 or more sessions | High |
| Hook block | Hook output in a tool result or system event showing a guardrail rejected an action | Medium |
| Tool failure loop | The same Bash command failing 2 or more times in a row | Medium |
| Manual fix-up | A later full `Read` of a file shows content Claude did not write (whitespace-only changes ignored), or Claude Code's "file was modified" notice | Low |

Heuristics run first and are cheap. Only candidate turns go to the classifier.

## Remedies

| Remedy | When | Patch |
|---|---|---|
| Hook | The rule is mechanically checkable (a file path or a command) | A `PreToolUse` script for `hooks_dir`, plus the `settings.json` entry to register it |
| CLAUDE.md line | Judgment or style the model must apply | A diff appending to the repo's `CLAUDE.md` (single-repo findings) or the global one |
| Skill | A multi-step procedure you keep re-explaining | A draft `SKILL.md` |
| Ignore | One-off (single session) or already covered by an existing CLAUDE.md line, hook or skill | Listed in the report's appendix |

When both a hook and a CLAUDE.md line would fit, the hook wins: deterministic beats instructed. Hook-block findings become CLAUDE.md lines, because the guardrail already exists and Claude keeps tripping it.

## Configuration

`~/.config/sminer/config.yaml` (see [`config.example.yaml`](config.example.yaml)):

```yaml
roots: [~/.claude/projects]
window: 90d
exclude_repos: ["~/personal/*"]
classifier:
  model: claude-haiku-5-5
  max_calls_per_run: 500
redact:
  patterns: ["(?i)aws_secret", "ghp_[A-Za-z0-9]+"]
remedies:
  hooks_dir: ~/src/claude-hooks
  global_claude_md: ~/.claude/CLAUDE.md
embeddings:
  provider: auto            # ollama if it answers locally, else the built-in hashed model
  model: nomic-embed-text
```

State lives in `~/.local/share/sminer/sminer.db`, created with mode `0600`.

## Privacy and safety

Transcripts contain work code, secrets pasted by accident, and internal hostnames.

- **Redaction on ingest.** Built-in patterns cover AWS keys, GitHub and GitLab tokens, JWTs, private keys, Anthropic/OpenAI-style keys, Slack tokens and `password=`-style assignments. Your configured patterns are added on top. Only redacted, truncated extracts are stored, and output from successful tool calls is not kept at all. Raw transcripts are never copied into the database; rows point back to a file path and byte offset.
- **Classifier calls** send only the candidate user turn and a truncated summary of the preceding assistant actions (tool name, path or command, a short snippet), capped at about 2 KB. They never include file contents or tool output bodies. When the model is enabled, `analyze` also sends up to 8 redacted excerpts per correction cluster in one call to draft a title and proposal. Hook, failure-loop and fix-up findings are never sent.
- **`--no-llm`** keeps everything on the machine. Repos matching `exclude_repos` are not ingested at all.
- **Embeddings are local.** They come from the built-in hashed model or a local ollama server.
- **Read-only against `~/.claude`.** `accept` writes only patch files.
- Before you run the classifier on work repos, confirm that your employer's AI data policy allows it.

## How it works

A linear batch pipeline in which every stage reads from and writes to SQLite, so any stage can be re-run on its own:

```
transcripts ─► ingest ─► signal ─► classify ─► cluster ─► remedy ─► report
 (JSONL)      turns     signals   (LLM, opt)   findings   proposals   md + json
                                                  ▲          ▲
                       local embeddings ──────────┘          └── existing CLAUDE.md, hooks, skills
```

Packages map one-to-one to stages: `transcript`, `ingest`, `signal`, `classify`, `cluster`, `remedy` and `report`, with `store` wrapping SQLite. Findings keep a stable ID across runs because each new cluster is matched to an existing finding (by centroid similarity for corrections, by exact key for hooks, failures and fix-ups). That way accept/dismiss state and trends survive re-analysis.

The transcript schema is undocumented and changes between releases. The decoder ignores unknown fields, accepts content as a string or as an array of blocks, and counts unknown event types (they appear in the report) instead of failing.

## Evaluation

```sh
make test       # unit, golden and end-to-end tests; classifier mocked
make eval       # heuristic precision/recall on testdata/labels
sminer eval --live   # also scores the live classifier (makes API calls)
```

- `testdata/projects` is a synthetic, redacted fixture corpus covering both transcript shapes (string content in 1.0.x, block content in 2.0.x). Regenerate it with `python3 testdata/gen_fixtures.py`.
- `testdata/labels/candidates.jsonl` holds hand-labelled candidate turns, and `pairs.jsonl` holds same-rule/different-rule pairs for tuning clustering. Both are starter sets. Grow them from your own transcripts; the spec targets about 200 candidates and at least 85% precision for corrections.

## Development

```sh
make check      # gofmt, go vet, tests
make fuzz       # fuzz the line decoder
```
