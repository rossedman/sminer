# Privacy and safety

Transcripts contain work code, secrets pasted by accident, and internal hostnames. sminer is built so that as little of that as possible is stored, almost nothing leaves your machine, and nothing of yours is changed without your say-so.

## What is stored

Everything sminer keeps lives in one SQLite file, `$XDG_DATA_HOME/sminer/sminer.db`, created with mode `0600`.

- **Redacted, truncated extracts.** Your messages are kept up to 2,000 characters; assistant text, tool inputs and failed tool output up to a few hundred. Every string is redacted before it's written.
- **Pointers, not copies.** Each stored turn records the transcript path and byte offset it came from. Raw transcripts are never copied in.
- **No successful tool output.** The output of tool calls that succeeded, which is where file contents and command output live, is never stored. Failed calls keep a few hundred redacted characters, enough to recognize failure loops and hook messages.
- **Derived data:** signals, classifier verdicts, findings, local embedding vectors and adoption dates.

### Redaction

Redaction runs on ingest, before anything is stored. The built-in patterns mask:

| Kind | Example shape |
|---|---|
| Private keys | `-----BEGIN … PRIVATE KEY-----` blocks |
| AWS | `AKIA…` / `ASIA…` access keys, `aws_secret_access_key = …` |
| GitHub, GitLab | `ghp_…`, `gho_…`, `github_pat_…`, `glpat-…` |
| JWTs | `eyJ….….…` |
| API keys | `sk-…`, `sk-ant-…` |
| Slack | `xoxb-…`, `xoxp-…` |
| Assignments | the value in `password=…`, `token: …`, `api_key=…`, `client_secret=…` |

Add your own in the config file. Each pattern also swallows a following `= value` or `: value`, so a pattern that names a key redacts its value too:

```yaml
redact:
  patterns: ["(?i)internal_token", "corp-[a-z0-9]{12}"]
```

## What leaves your machine

With `--offline`, or `SMINER_OFFLINE=1`, or `offline: true` in the config: **nothing**. Embeddings are always computed locally, by the built-in model or a local ollama server.

Otherwise, the network is used in exactly two places, both through the Anthropic API:

| Call | What's sent | When |
|---|---|---|
| Classification | One candidate user message (≤700 characters) and a summary of the assistant's preceding actions: tool names, a path or command, short snippets. Capped near 2 KB. | Once per new candidate; cached afterwards |
| Proposal drafting | Up to 8 redacted excerpts (≤300 characters each) and the classifier's rules for one cluster of corrections | Once per new correction cluster |

Never sent: file contents, tool output, hook blocks, failure loops, fix-ups, or anything from repos matched by `exclude_repos`. All calls share the per-run cap `classifier.max_calls_per_run` (500 by default).

!!! warning "Check your employer's policy"
    Before you run the classifier on work repositories, confirm that your employer's AI data policy allows sending excerpts to the Anthropic API. If it doesn't, use `--offline`, or exclude those repos.

## Excluding repositories

Repos matching a pattern in `exclude_repos` are never ingested, so they're never stored, analyzed or sent:

```yaml
exclude_repos: ["~/personal/*", "~/src/acme-secret"]
```

A pattern matches the repo or any parent directory, so `~/personal/*` also covers `~/personal/blog/drafts`.

## What sminer writes

- **Read-only against `~/.claude`.** Transcripts, settings and CLAUDE.md files are only ever read.
- **Patches, not edits.** `sminer finding accept` writes one patch file into the patch directory. It never applies it.
- **Only where you ask.** Reports go to stdout unless you pass `--output`. Patch files and report files are written atomically (to a temporary file, then renamed), so a crash never leaves half a file.

## Removing sminer

Delete the binary, `$XDG_DATA_HOME/sminer` (by default `~/.local/share/sminer`) and the config file, if you made one (`sminer config path` prints its location). Nothing else is left behind.
