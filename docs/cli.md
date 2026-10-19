# CLI reference

sminer's commands read like sentences: `sminer <noun> <verb> <target> --modifier`. Every command has `--help` with examples. Bare `sminer` prints help.

```text
Mining
  sminer run        ingest, analyze and print the report
  sminer ingest     read new transcript lines into the database
  sminer analyze    find signals, cluster them, propose remedies
  sminer report     print the ranked report

Acting on findings
  sminer finding list | show | accept | dismiss | reopen | trend

Other
  sminer config show | path
  sminer eval
  sminer version
  sminer completion bash | zsh | fish | powershell
```

## Global flags

| Flag | Meaning |
|---|---|
| `--config FILE` | Config file to read. Default: `$XDG_CONFIG_HOME/sminer/config.yaml`. |
| `--db FILE` | Database file. Default: `$XDG_DATA_HOME/sminer/sminer.db`. |
| `--offline` | Never call the network. Same as `SMINER_OFFLINE=1`. |
| `--json` | Print machine-readable JSON on stdout. |
| `-q`, `--quiet` | Print nothing on stderr except errors. |
| `-v`, `--verbose` | Print detail on stderr. |
| `--no-color` | Disable color. Same as `NO_COLOR=1`. |

## Mining

### `sminer run`

Ingest, analyze and report in one go. The report goes to stdout as Markdown, or JSON with `--json`. With `--output FILE`, it's written to that file and `findings.json` is written beside it.

```bash
sminer run --offline
sminer run --output ~/notes/sminer.md
sminer run --json | jq '.findings[].title'
```

| Flag | Default | Meaning |
|---|---|---|
| `--since` | `90d` | How far back to look: `90d`, `2w`, `12h` |
| `--root DIR` | `~/.claude/projects` | Transcript directory; repeatable |
| `--repo PATH` | | Only consider sessions in this repository |
| `--min-count N` | `3` | Occurrences a finding needs to be reported |
| `-o`, `--output FILE` | stdout | Write the report to a file |

### `sminer ingest`

Read new transcript lines into the database. Incremental: each file is re-read only from where the last run stopped.

| Flag | Default | Meaning |
|---|---|---|
| `--since` | `90d` | Only read transcripts modified within this window |
| `--root DIR` | `~/.claude/projects` | Transcript directory; repeatable |

### `sminer analyze`

Find signals in sessions with new turns, confirm candidates with the classifier (unless offline), cluster them into findings and route each to a remedy. Takes `--since`, `--repo` and `--min-count`, like `run`.

### `sminer report`

Print the ranked report. Takes `--since`, `--repo`, `--min-count` and `--output`, like `run`.

## Acting on findings

### `sminer finding list`

One finding per line, highest score first:

```text
ID       STATUS  REMEDY     OCCURRENCES  SESSIONS  SCORE  TITLE
Fc72cc0  open    hook       9            7         27     Never hand-edit package-lock.json
```

The header is printed only on a terminal. No findings prints nothing and exits 0. `--status` filters by `open` (the default), `accepted`, `dismissed` or `all`.

### `sminer finding show <id>`

The proposal, the target file and every excerpt behind the finding, each with the command that resumes its session.

### `sminer finding accept <id>...`

Render each finding's patch against its target as it is now, write it to the patch directory, print its path on stdout, and record the adoption date. The apply command, and the `settings.json` entry for a hook, go to stderr.

| Flag | Meaning |
|---|---|
| `--preview` | Print the patch; write nothing and leave the finding unchanged |
| `--patch-dir DIR` | Where to write patches (default `$XDG_DATA_HOME/sminer/patches`) |

```bash
patch -d ~ -p1 < "$(sminer finding accept F1a2b3c)"
```

### `sminer finding dismiss <id>...`

Hide findings that aren't worth fixing. `--reason` records why.

### `sminer finding reopen <id>...`

Undo accept or dismiss. For an accepted finding this also forgets the adoption date. The patch file is left where it is.

### `sminer finding trend [<id>]`

Weekly occurrence rates. Without an id: one line per finding, with before and after rates and a sparkline of recent weeks. With an id: week-by-week counts and the before/after comparison.

## Other

### `sminer config show`

Every setting, its effective value, and the layer that set it: `flag --…`, `env SMINER_…`, `file …` or `default`. See [Configuration](configuration.md).

### `sminer config path`

The config file's path, whether or not it exists. `--json` adds `exists` and the data directory.

```bash
$EDITOR "$(sminer config path)"
```

### `sminer eval`

Score correction detection against a labelled set. See [Evaluation](evaluation.md).

### `sminer version`

The version, commit, Go version and platform. `--json` adds the full command tree, for tools that introspect.

## Output

sminer treats its output as an interface.

- **stdout is data:** the report, JSON, patch paths, lists. **stderr is everything else:** progress, warnings, hints and errors. `sminer run > report.md` captures only the report.
- **`--json` works on every command that returns data.** Fields are only ever added, never renamed or removed.
- **Plain lists are line-oriented:** one record per line, stable field order, a header only on a terminal.
- **Empty is not an error.** No findings exits 0, prints nothing in plain mode, and prints `[]` with `--json`.
- **Color only decorates.** It's off when the output isn't a terminal, with `NO_COLOR`, with `TERM=dumb`, and with `--no-color`.

## Errors and exit codes

An error is one line saying what failed, then a hint with the command that fixes it:

```text
error: finding F000000: not found
hint: list findings with 'sminer finding list --status all'
```

With `--json`, the error is a JSON object on stderr:

```json
{"error": {"code": "not_found", "exit": 4, "message": "finding F000000: not found", "hint": "list findings with 'sminer finding list --status all'"}}
```

| Exit | Code | Meaning |
|---|---|---|
| 0 | | Success, including empty results |
| 1 | `failure` | Runtime error |
| 2 | `usage`, `config` | Bad flag, argument or config value |
| 4 | `not_found` | The named finding doesn't exist |
| 5 | `no_remedy`, `not_patchable`, `target_exists` | A precondition failed |
| 130 | `interrupted` | Ctrl-C |

A mistyped command gets a suggestion (`Did you mean analyze?`).

## Renamed commands

These 0.1 names still work until **2027-04-30**, each with a notice on stderr:

| Old | New |
|---|---|
| `sminer show`, `accept`, `dismiss`, `trend` | `sminer finding show`, `accept`, `dismiss`, `trend` |
| `--no-llm` | `--offline` |
| `report --format json` | `report --json` |
| `report --out FILE` | `report --output FILE` |
