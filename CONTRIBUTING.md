# Contributing to sminer

Thanks for helping. sminer mines Claude Code transcripts for corrections people keep repeating; the [docs](https://rossedman.io/sminer/) explain how it works, and [design.md](docs/design.md) covers the architecture.

## Set up

You need Go (the version in `go.mod`; the `toolchain` line downloads it if yours is older), [golangci-lint](https://golangci-lint.run/welcome/install/) v2, [GoReleaser](https://goreleaser.com/install/) v2, and Python 3 for the docs site.

```bash
git clone https://github.com/rossedman/sminer.git
cd sminer
pip install -r docs/requirements.txt
make build && ./sminer --help
```

Tests never touch the network or your real transcripts: they run against the synthetic corpus in `testdata/projects` with a fake classifier.

## Check your work

One command runs everything CI runs:

```bash
make check
```

That is gofmt, `go vet`, golangci-lint, race tests with the coverage gate, `check-cmds`, `doccheck`, govulncheck, a benchmark smoke run, every fuzz target (30s each; `make check FUZZTIME=5s` while iterating), the strict docs build and `goreleaser check`. The pieces run on their own too: `make test`, `make cover`, `make lint`, `make fuzz`, `make bench`, `make docs`.

## Quality bars

- **Coverage** stays at or above **90%** of hand-written statements, measured across packages (`make cover`; `tools/pkgcov.sh coverage.hand.out` shows each package). Generated files and `examples/` don't count. Test behavior, not getters.
- **Doc comments** on every function and method, exported or not, starting with its name, and a package comment on every package (`make doccheck`).
- **One cobra command per file** in `internal/cli`, named after the command: `finding_accept.go` for `sminer finding accept` (`make check-cmds`). Commands stay thin; logic lives in the other `internal/` packages.
- **Flags are bound to config** with `bindFlag`, so each one can also come from `SMINER_*` or the config file. Command or flag changes need `make docs` to refresh `docs/reference/commands`; CI fails if it's stale.
- **Benchmarks and fuzz targets** for the critical paths below.
- **Lint clean**: `.golangci.yml` explains every exclusion. Prefer fixing over `//nolint`; when you must, say why on the same line.

## Critical paths

These parse untrusted input, sit in hot loops or resolve configuration. A change to one needs a benchmark (`BenchmarkXxx` with `b.ReportAllocs()`), and the input-handling ones need a fuzz target that asserts properties, not just "didn't panic". Compare benchmark runs with `make bench` before and after, then `benchstat bench/<before>.txt bench/<after>.txt`.

| Path | Why | Benchmark | Fuzz |
| --- | --- | --- | --- |
| `transcript.Decode`, `transcript.Scan` | parse every transcript line, in an undocumented, changing format | yes | yes |
| `redact.(*Redactor).String` | the last line of defense before text is stored or sent to the API | yes | yes |
| `ingest.BashMayWrite` | parses shell commands from transcripts | | yes |
| `signal.Detect`, `signal.IsCorrection`, `signal.HookKey`, `signal.CommandKey` | run over every turn; read free text | yes | yes |
| `classify.ParseVerdict` | parses model output | | yes |
| `remedy.ExtractHook`, `remedy.DecodeProposal` | parse model output into patches | | yes |
| `embed.Hash.Embed`, `embed.Cosine`, `embed.Greedy` | hot loops in clustering | yes | |
| `cluster.Run`, `report.Build`, `report.WriteMarkdown` | whole-corpus passes | yes | |
| `config.Load`, `config.ParseDuration` | configuration resolution and user-typed durations | yes | yes |
| `textutil.Slug` | turns free text into file names | | yes |

## Commits and pull requests

Commit messages and PR titles follow [Conventional Commits](https://www.conventionalcommits.org/); the release notes are grouped from them.

```text
feat: add --since to finding list
fix(ingest): keep the offset when a line is cut mid-write
docs: explain hook routing
test(redact): fuzz configured patterns
build: bump golang.org/x/text
ci: pin golangci-lint-action
feat!: rename --db to --database
```

Use `!` for a change that breaks the CLI, config keys or `--json` output, and say how to migrate in the body.

Pull requests need green CI and a review from a code owner (`.github/CODEOWNERS`), and are squash-merged, so the PR title becomes the commit message. Keep each PR to one change; the PR template has the checklist.

## Questions

Open an [issue](https://github.com/rossedman/sminer/issues/new/choose) for bugs and ideas. Report security problems privately, as described in [SECURITY.md](SECURITY.md). Everyone taking part follows the [code of conduct](CODE_OF_CONDUCT.md).
