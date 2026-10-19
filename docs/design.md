# Design and development

## Goals

- **Surface the top recurring corrections**, with counts, repos and example excerpts.
- **Classify each by its best remedy:** a CLAUDE.md instruction, a deterministic hook, or a skill.
- **Emit ready-to-apply patches,** never apply them.
- **Run in under 30 seconds** over 90 days of transcripts on a laptop.

And success means: at least five findings in the first run that are real and worth fixing, and a measurable drop in each adopted finding's rate afterwards.

## Non-goals

- **No automatic edits** to CLAUDE.md, settings or hooks.
- **No team-wide aggregation.** One user, one machine.
- **No real-time hook.** sminer is a batch analyzer you run weekly or on demand.
- **No dashboard.** Terminal, Markdown and JSON only.

## Principles

**Deterministic beats instructed.** A hook is a guarantee; a CLAUDE.md line is a request. When both fit, sminer proposes the hook.

**Cheap first, expensive only when needed.** Regex heuristics find candidates; the model only confirms them. That keeps API use to a few hundred calls for months of transcripts.

**Precision over recall.** A missed correction comes back next week. A false one makes you stop reading the report.

**Local by default.** Redaction before storage, local embeddings, one optional network stage with a hard per-run cap.

**Output is an interface.** The CLI follows the [cli.style](https://cli.style/) guidelines: noun-verb commands, data on stdout and logs on stderr, `--json` everywhere, documented exit codes, and flags over environment over config file over defaults.

## Code layout

```text
cmd/sminer          main: calls cli.Execute
internal/cli        cobra command tree; parses input, renders output
internal/pipeline   runs the stages in order; the --json result types
internal/finding    list, show, accept, dismiss, reopen, trend
internal/config     viper settings, XDG paths, validation
internal/transcript tolerant JSONL decoder
internal/ingest     incremental, redacted ingest into turns
internal/signal     the seven detectors, repeated-instruction pass
internal/classify   Anthropic SDK classifier, budget, caching
internal/embed      hashed and ollama embedders, greedy clustering
internal/cluster    findings with stable IDs
internal/remedy     routing, de-duplication, patches
internal/report     Markdown and JSON reports, trends
internal/store      SQLite schema and queries
internal/redact     secret patterns
```

Packages map one-to-one to pipeline stages. The CLI holds no domain logic: commands parse flags, call `pipeline` or `finding`, and render the result.

## Development

```bash
make check      # gofmt, go vet, tests
make eval       # heuristic precision on the labelled set
make fuzz       # fuzz the transcript decoder
make build      # binary with version and commit stamped in
make fixtures   # regenerate the synthetic transcript corpus
```

`testdata/projects` is a synthetic, redacted corpus that covers both transcript formats (string content in 1.0.x, block content in 2.0.x), with interrupts, rewinds, hook blocks, malformed lines and a partial trailing line. The end-to-end tests run the whole CLI against it.

## The docs site

This site is [MkDocs Material](https://squidfunk.github.io/mkdocs-material/) with a custom landing page (`overrides/home.html`) and brand styles in `docs/assets/site/`. `.github/workflows/pages.yml` builds it with `--strict` on every pull request and deploys it to GitHub Pages from `main`.

```bash
pip install -r docs/requirements.txt
mkdocs serve
```

The banner, social preview and diagrams are HTML in `docs/assets/src/`, rendered to 2x PNGs by headless Chrome. Fonts are vendored there, so renders are reproducible offline:

```bash
docs/assets/render.sh                 # all graphics
docs/assets/render.sh how-it-works    # one
```
