# Troubleshooting

## The report is empty

- **Nothing was ingested.** Check the ingest line on stderr. If it found 0 files, point sminer at your transcripts: `sminer run --root /path/to/projects`. Claude Code keeps them in `~/.claude/projects` by default.
- **Everything is outside the window.** Files not modified within `--since` (90 days by default) are skipped. Try `--since 180d`.
- **Nothing repeats often enough.** Findings need 3 occurrences by default. Try `--min-count 2` to see what's close.
- **Findings were dismissed.** `sminer finding list --status all` shows every finding, whatever its status.

## The classifier isn't running

If the classifier keeps failing, sminer warns and continues with heuristics alone:

```text
warning: classifier stopped after repeated errors (401 …); continued with heuristics only. Check ANTHROPIC_API_KEY, or use --offline
```

Check that `ANTHROPIC_API_KEY` is set in the environment sminer runs in. Also check that `offline` isn't set by a flag, `SMINER_OFFLINE` or the config file: `sminer config show` shows which layer set it.

## A finding mixes two different rules, or one rule is split

Clustering is a similarity threshold, and the built-in embedder only sees shared words.

- **Split rules** (the same correction worded differently): run with the classifier, which rewrites each correction as a general rule before clustering, or use [ollama embeddings](configuration.md#better-clustering-with-ollama). As a last resort, lower `cluster.threshold`.
- **Mixed rules:** raise `cluster.threshold` a little, such as `0.55` for the built-in embedder.

## `accept` fails with "target already exists"

Hook and skill proposals create new files, and sminer won't overwrite one. Move the existing file aside, or dismiss the finding if you already have an equivalent. Exit code 5.

## A patch doesn't apply

Patches are rendered against the target as it was when you accepted. If the file changed since, or you accepted two findings for the same CLAUDE.md, re-render it:

```bash
sminer finding accept Fcd5b9b   # writes a fresh patch against the current file
```

Accepting again is safe: it overwrites the patch and updates the adoption date.

## A transcript line is reported as malformed

The transcript format changes between Claude Code releases. A malformed line is skipped and counted, never fatal. Unknown event types are listed in the report's *Parser notes*. If counts jump after a Claude Code update, please [open an issue](https://github.com/rossedman/sminer/issues) with the Claude Code version (`sminer ingest -v` prints the versions it saw).

## Starting over

Delete the data directory. The next run re-ingests everything within the window:

```bash
rm -rf "${XDG_DATA_HOME:-$HOME/.local/share}/sminer"
```

This also forgets accepted and dismissed findings, and their trends.

## Reporting a bug

Include `sminer version`, the command you ran, and its output with `-v`. If sminer crashed, it prints a stack trace and the issue link: please paste both.
