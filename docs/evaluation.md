# Evaluation

A report full of false corrections gets ignored after one run, so precision is the number that matters most. sminer ships with labelled sets and an `eval` command to measure it.

## Correction detection

`testdata/labels/candidates.jsonl` holds hand-labelled candidate turns: the assistant's last action, your next message, and whether that message is a correction.

```json
{"action": "Edit internal/api/widgets_test.go", "message": "we don't use assert libraries in this repo", "correction": true}
{"action": "Bash go test ./...", "message": "run the integration tests next", "correction": false}
```

Score the heuristics, offline:

```bash
sminer eval --labels testdata/labels/candidates.jsonl
```

```text
heuristic  precision 95.6%  recall 95.6%  (tp 43 fp 2 fn 2 tn 48, n=95)
```

Add `--live` to also score the classifier and the full pipeline (heuristic, then classifier). It makes one API call per labelled turn.

```bash
sminer eval --labels testdata/labels/candidates.jsonl --live
```

!!! warning "The bundled set is a starter"
    The same person wrote the heuristics and the 95 bundled examples, so the score above is optimistic. The target is about 200 labelled turns taken from your own transcripts, with at least 85% precision for corrections. Recall matters less: a missed correction comes back next week, while a false one costs trust in every report.

## Label your own turns

`sminer label` builds that set from the transcripts you've already ingested. It samples messages you sent right after Claude used a tool, half that the heuristic flags as corrections and half it doesn't, and shows each one with what Claude had just done:

```text
[2/50] Claude had just done:
  Write /work/infra/s3.tf — write 45 bytes
You said:
  Use snake_case for terraform resource names, not camelCase.
correction? [y]es [n]o [s]kip [q]uit: y
```

Each answer is appended to `$XDG_DATA_HOME/sminer/labels.jsonl` (mode `0600`) as soon as you give it, so quitting or pressing Ctrl-C loses nothing. Turns you've labelled aren't offered again. Plain `sminer eval` then scores against your own labels:

```bash
sminer label            # 50 at a time; run it again for more
sminer eval             # reads labels.jsonl
sminer eval --live      # and the classifier
```

The heuristic's guess isn't shown while you label, so it can't bias your answer. The text was redacted at ingest, but read the file before sharing it.

To label in an editor instead, export a sample with `"correction": null` on every line and fill them in. `eval` skips lines that are still `null`:

```bash
sminer label --export --limit 100 > batch.jsonl
sminer eval --labels batch.jsonl
```

## Clustering

`testdata/labels/pairs.jsonl` holds pairs of rule texts labelled as the same rule or different rules. They're used to tune the similarity threshold:

```json
{"a": "Use pnpm, not npm", "b": "we use pnpm in this repo, not npm", "same": true}
{"a": "Never hand-edit go.sum", "b": "Never hand-edit package-lock.json", "same": false}
```

The built-in hashed embedder reaches 94% precision and 80% recall on the bundled pairs at its default threshold of 0.45. It's tuned not to merge different rules, at the cost of splitting some paraphrases. The classifier, which rewrites corrections as general rules first, or ollama embeddings close most of that gap.

## In CI

The labelled sets run on every push, with the classifier mocked:

- heuristic precision and recall must stay above their floors,
- a perfect mock classifier must leave the pipeline with zero false positives, and
- the hashed embedder must keep its pair precision.

The live classifier eval runs on demand only.

## Measuring a fix

The end-to-end check is the one in the spec: run sminer on your own 90 days, review the top findings by hand, and adopt the ones that are real. Then, after a few weeks, `sminer finding trend` shows whether each correction actually stopped. [Findings and remedies](findings.md#trends) explains how.
