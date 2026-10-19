# Getting started

sminer reads the transcripts Claude Code already keeps on your machine, finds the corrections you make over and over, and proposes a fix for each one. This page takes you from install to your first accepted fix.

## Install

sminer is a single Go binary with no cgo and no runtime dependencies.

```bash
go install github.com/rossedman/sminer/cmd/sminer@latest
```

Check that it runs:

```bash
sminer version
```

For tab completion, including live finding IDs:

=== "zsh"

    ```bash
    sminer completion zsh > "${fpath[1]}/_sminer"
    ```

=== "bash"

    ```bash
    sminer completion bash > /etc/bash_completion.d/sminer
    ```

=== "fish"

    ```bash
    sminer completion fish > ~/.config/fish/completions/sminer.fish
    ```

## Your first run, offline

There's nothing to configure. sminer looks for transcripts in `~/.claude/projects`, where Claude Code writes them.

```bash
sminer run --offline -o report.md
```

`run` does three things in order:

1. **Ingest** reads new transcript lines into a local database, masking secrets on the way in.
2. **Analyze** finds signals, groups them into findings and picks a remedy for each.
3. **Report** writes the ranked report: here to `report.md`, with `findings.json` beside it. Without `-o` the report goes to stdout.

Progress goes to stderr, so `sminer run --offline > report.md` works too.

!!! tip "Why start offline"
    `--offline` keeps everything on your machine. Heuristics alone are tuned for precision, so the first report is already useful, and it shows you what the classifier would be asked about before you turn it on.

## Add the classifier

With an Anthropic API key, sminer asks a small model to confirm each candidate correction and to state the rule behind it. That gives cleaner clusters and better-worded proposals.

```bash
export ANTHROPIC_API_KEY=...
sminer run -o report.md
```

Only candidate turns are sent, about 2 KB each, and results are cached: a second run sends only what's new. [Privacy and safety](privacy.md) has the details.

## Read the report

Each finding in the report has:

- **A title** that names the rule, such as *"Never hand-edit package-lock.json"*.
- **Counts:** occurrences, sessions, repos, and when it was first and last seen.
- **Excerpts:** two or three redacted examples, each with the command that reopens the session (`claude --resume <id>`).
- **A remedy** (hook, CLAUDE.md line or skill) and the proposal itself.

Findings are ranked by `occurrences × weight × repos`, so a correction you make in every repo outranks one you make often in a single repo. Findings that are one-off, or already covered by your CLAUDE.md, hooks or skills, are listed in an appendix instead of ranked.

## Act on a finding

List what's open:

```bash
sminer finding list
```

```text
Fc72cc0  open  hook       9  7  27  Never hand-edit package-lock.json
Fcd5b9b  open  claude_md  4  4  24  Use table-driven tests, not testify
```

Look at every excerpt behind one:

```bash
sminer finding show Fc72cc0
```

Preview the patch. This changes nothing:

```bash
sminer finding accept Fc72cc0 --preview
```

Accept it. sminer writes the patch to its data directory, prints the path, and records the date so it can later tell whether the fix worked:

```bash
sminer finding accept Fc72cc0
# then apply it, using the command accept printed:
cd ~/src/claude-hooks && patch -p1 < ~/.local/share/sminer/patches/Fc72cc0.patch
```

Not worth fixing? Dismiss it, and it stays out of future reports:

```bash
sminer finding dismiss Fcd5b9b --reason "intentional in this repo"
```

Both are undone with `sminer finding reopen <id>`.

## See whether it worked

Run sminer again after a few weeks of work:

```bash
sminer run -o report.md
sminer finding trend
```

For accepted findings, `trend` shows the weekly rate before and after the day you accepted. If the fix worked, the rate after drops toward zero.

## Next steps

- [How it works](how-it-works.md): the pipeline, stage by stage.
- [Signals](signals.md): what counts as friction.
- [Configuration](configuration.md): windows, exclusions, redaction patterns and paths.
