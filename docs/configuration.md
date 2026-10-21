# Configuration

sminer runs with no configuration at all. When you do want to change something, every setting can come from a flag, an environment variable or the config file.

## Precedence

The first layer that sets a value wins:

1. **Command-line flags**, such as `--since 30d`
2. **Environment variables**: `SMINER_` plus the key in upper case, with dots as underscores. `classifier.model` is `SMINER_CLASSIFIER_MODEL`.
3. **The config file**: the file named by `--config`, or else the first of these that exists: `$XDG_CONFIG_HOME/sminer/config.yaml` (by default `~/.config/sminer/config.yaml`), `~/.sminer.yaml`, `./.sminer.yaml`
4. **Built-in defaults**

`sminer config show` prints every effective value and which layer set it:

```text
KEY               VALUE              SOURCE
window            30d                env SMINER_WINDOW
offline           true               flag --offline
classifier.model  claude-haiku-5-5   file /home/me/.config/sminer/config.yaml
min_count         3                  default
```

## The config file

Every key is optional. [`config.example.yaml`](https://github.com/rossedman/sminer/blob/main/config.example.yaml) in the repository is a commented starting point:

```yaml
roots: [~/.claude/projects]
window: 90d
min_count: 3
offline: false
exclude_repos: ["~/personal/*"]

classifier:
  model: claude-haiku-5-5
  max_calls_per_run: 500
  concurrency: 4

embeddings:
  provider: auto
  model: nomic-embed-text
  url: http://localhost:11434

cluster:
  threshold: 0

redact:
  patterns: ["(?i)aws_secret", "ghp_[A-Za-z0-9]+"]

remedies:
  hooks_dir: ~/src/claude-hooks
  global_claude_md: ~/.claude/CLAUDE.md
  skills_dir: ~/.claude/skills
  settings: ~/.claude/settings.json
```

## Keys

| Key | Default | Meaning |
|---|---|---|
| `roots` | `[~/.claude/projects]` | Directories searched for `*.jsonl` transcripts. Flag: `--root` |
| `window` | `90d` | How far back to ingest and analyze: `90d`, `2w`, `12h`. Flag: `--since` |
| `min_count` | `3` | Occurrences a finding needs to be reported. Flag: `--min-count` |
| `offline` | `false` | Never call the network. Flag: `--offline` |
| `exclude_repos` | `[]` | Globs of repos never ingested; a pattern also matches subdirectories |
| `classifier.model` | `claude-haiku-5-5` | Model for classification and proposal drafting |
| `classifier.max_calls_per_run` | `500` | Cap on API calls per run, across both uses |
| `classifier.concurrency` | `4` | Parallel classifier calls |
| `embeddings.provider` | `auto` | `auto` (ollama if it answers, else built-in), `ollama` or `hash` |
| `embeddings.model` | `nomic-embed-text` | ollama embedding model |
| `embeddings.url` | `http://localhost:11434` | ollama server |
| `cluster.threshold` | `0` | Cosine similarity to join a cluster; `0` uses the embedder's default (0.45 built-in, 0.78 ollama) |
| `redact.patterns` | `[]` | Extra regexes to redact, on top of the built-ins |
| `remedies.hooks_dir` | `~/src/claude-hooks` | Where hook proposals are written, and existing hooks are read |
| `remedies.global_claude_md` | `~/.claude/CLAUDE.md` | The CLAUDE.md for rules that span repos |
| `remedies.skills_dir` | `~/.claude/skills` | Where skill proposals go, and existing skills are read |
| `remedies.settings` | `~/.claude/settings.json` | Claude Code settings, read for existing hooks |
| `db` | `$XDG_DATA_HOME/sminer/sminer.db` | The database. Flag: `--db` |
| `patch_dir` | `$XDG_DATA_HOME/sminer/patches` | Where accepted patches are written |
| `json` | `false` | Print machine-readable JSON on stdout. Flag: `--json` |
| `quiet` | `false` | Print nothing on stderr except errors. Flag: `--quiet` |
| `verbose` | `false` | Print detail on stderr. Flag: `--verbose` |
| `no_color` | `false` | Disable color. Flag: `--no-color` |

Paths may start with `~`. Invalid values (a bad `window`, a `min_count` below 1, an unknown `embeddings.provider`, or `quiet` and `verbose` both set without either flag) are rejected up front with exit code 2.

## Credentials

The classifier uses the Anthropic SDK's standard credential lookup: `ANTHROPIC_API_KEY` most commonly. sminer never takes a key as a command-line argument.

## Locations

sminer follows the XDG base directory spec, on macOS too:

| What | Where |
|---|---|
| Config file | `$XDG_CONFIG_HOME/sminer/config.yaml`, default `~/.config/sminer/config.yaml`; then `~/.sminer.yaml` and `./.sminer.yaml` |
| Database and patches | `$XDG_DATA_HOME/sminer/`, default `~/.local/share/sminer/` |

## Better clustering with ollama

The built-in embedder hashes words, so it groups corrections that share wording but can miss paraphrases. A local embedding model groups by meaning, and nothing leaves your machine either way:

```bash
ollama pull nomic-embed-text
ollama serve   # sminer picks it up automatically with provider: auto
```

Changing the embedder recomputes all embeddings on the next run. Finding IDs are matched by similarity, so most survive the switch.
