package eval

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/rossedman/sminer/internal/embed"
	"github.com/rossedman/sminer/internal/signal"
)

func writeFile(t *testing.T, name, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestScore(t *testing.T) {
	t.Parallel()
	var zero Score
	if zero.Precision() != 0 || zero.Recall() != 0 {
		t.Error("empty score must be 0, not NaN")
	}
	s := Score{TP: 3, FP: 1, FN: 2, TN: 4}
	if s.Precision() != 0.75 || s.Recall() != 0.6 {
		t.Errorf("precision %v recall %v", s.Precision(), s.Recall())
	}
	want := "precision 75.0%  recall 60.0%  (tp 3 fp 1 fn 2 tn 4, n=10)"
	if s.String() != want {
		t.Errorf("String = %q, want %q", s.String(), want)
	}
	var a Score
	for _, c := range []struct{ pred, truth bool }{{true, true}, {true, false}, {false, true}, {false, false}, {true, true}} {
		a = a.add(c.pred, c.truth)
	}
	if a != (Score{TP: 2, FP: 1, FN: 1, TN: 1}) {
		t.Errorf("add = %+v", a)
	}
}

func TestLoad(t *testing.T) {
	t.Parallel()
	p := writeFile(t, "l.jsonl", strings.Join([]string{
		`// comment`,
		``,
		`{"action":"Edit a.go","message":"no, use pnpm","correction":true,"turn":"t1"}`,
		`{"message":"looks good","correction":false}`,
		`{"message":"unlabelled","correction":null}`,
		`  {"message":"indented","correction":true}  `,
	}, "\n"))
	got, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	want := []Item{
		{Action: "Edit a.go", Message: "no, use pnpm", Correction: true, Turn: "t1"},
		{Message: "looks good"},
		{Message: "indented", Correction: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Load = %+v, want %+v", got, want)
	}
	all, err := load(p, true)
	if err != nil || len(all) != 4 || all[2].Message != "unlabelled" || all[2].Correction {
		t.Errorf("load(unlabelled) = %+v %v", all, err)
	}
	if _, err := Load(filepath.Join(t.TempDir(), "nope")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing file err = %v", err)
	}
	bad := writeFile(t, "bad.jsonl", "{\"message\":\"ok\"}\n{nope\n")
	if _, err := Load(bad); err == nil || !strings.Contains(err.Error(), "bad.jsonl:2") {
		t.Errorf("malformed line err = %v", err)
	}
	huge := writeFile(t, "huge.jsonl", `{"message":"`+strings.Repeat("x", 2<<20)+`"}`+"\n")
	if _, err := Load(huge); err == nil {
		t.Error("a line over the scanner limit should fail, not be dropped")
	}
}

func TestLoadPairs(t *testing.T) {
	t.Parallel()
	p := writeFile(t, "p.jsonl", "// c\n\n{\"a\":\"x\",\"b\":\"y\",\"same\":true}\n{\"a\":\"p\",\"b\":\"q\"}\n")
	got, err := LoadPairs(p)
	if err != nil || !reflect.DeepEqual(got, []Pair{{"x", "y", true}, {"p", "q", false}}) {
		t.Errorf("LoadPairs = %+v %v", got, err)
	}
	if _, err := LoadPairs(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Error("missing file")
	}
	bad := writeFile(t, "bad.jsonl", "{}\n{oops\n")
	if _, err := LoadPairs(bad); err == nil || !strings.Contains(err.Error(), "bad.jsonl:2") {
		t.Errorf("err = %v", err)
	}
}

type scripted struct {
	replies []string
	err     error
	n       int
}

func (s *scripted) Complete(context.Context, string, string, int) (string, error) {
	if s.err != nil {
		return "", s.err
	}
	r := s.replies[s.n%len(s.replies)]
	s.n++
	return r, nil
}

func TestClassifierErrors(t *testing.T) {
	t.Parallel()
	items := []Item{{Message: "a", Correction: true}, {Message: "b"}}
	r, err := Classifier(context.Background(), items, &scripted{replies: []string{`{"correction":true}`, `{"correction":false}`}})
	if err != nil || r.TP != 1 || r.TN != 1 || !reflect.DeepEqual(r.Verdicts, []bool{true, false}) {
		t.Errorf("result = %+v %v", r, err)
	}
	boom := errors.New("network down")
	if _, err := Classifier(context.Background(), items, &scripted{err: boom}); !errors.Is(err, boom) {
		t.Errorf("err = %v", err)
	}
	r, err = Classifier(context.Background(), items, &scripted{replies: []string{`{"correction":true}`, "prose only"}})
	if err == nil || len(r.Verdicts) != 1 {
		t.Errorf("a bad reply stops the run with partial results: %+v %v", r, err)
	}
}

func TestPipelineNeedsBothStages(t *testing.T) {
	t.Parallel()
	items := []Item{
		{Message: "No, don't use testify.", Correction: true},
		{Message: "No, don't use testify.", Correction: false},
		{Message: "add a handler", Correction: true},
		{Message: "No, don't do that", Correction: true}, // no verdict for this one
	}
	got := Pipeline(items, []bool{true, true, true})
	want := Score{TP: 1, FP: 1, FN: 2}
	if got != want {
		t.Errorf("Pipeline = %+v, want %+v", got, want)
	}
	if got := Pipeline(items, nil); got.TP != 0 || got.FN != 3 {
		t.Errorf("no verdicts: %+v", got)
	}
}

type failingEmbedder struct{ embed.Hash }

func (failingEmbedder) Embed([]string) ([][]float32, error) { return nil, errors.New("down") }

func TestPairsErrorAndThreshold(t *testing.T) {
	t.Parallel()
	pairs := []Pair{{A: "use table-driven tests", B: "use table-driven tests", Same: true}, {A: "use table-driven tests", B: "terraform names snake_case", Same: false}}
	s, err := Pairs(pairs, embed.Hash{}, 0.9)
	if err != nil || s != (Score{TP: 1, TN: 1}) {
		t.Errorf("score = %+v %v", s, err)
	}
	s, _ = Pairs(pairs, embed.Hash{}, -1)
	if s.FP != 1 {
		t.Errorf("a threshold of -1 predicts everything the same: %+v", s)
	}
	if _, err := Pairs(pairs, failingEmbedder{}, 0.5); err == nil {
		t.Error("embedder failure must surface")
	}
	if s, err := Pairs(nil, embed.Hash{}, 0.5); err != nil || s != (Score{}) {
		t.Errorf("no pairs: %+v %v", s, err)
	}
}

func cand(msg string, heur bool) signal.Candidate {
	return signal.Candidate{Message: msg, Heuristic: heur}
}

func TestKeyAndSample(t *testing.T) {
	t.Parallel()
	if Key("  No,   USE\tpnpm ") != "no, use pnpm" {
		t.Errorf("Key = %q", Key("  No,   USE\tpnpm "))
	}
	var cands []signal.Candidate
	for _, m := range []string{"p1", "p2", "p3", "p4"} {
		cands = append(cands, cand(m, true))
	}
	for _, m := range []string{"n1", "n2", "n3", "n4", "n5", "n6"} {
		cands = append(cands, cand(m, false))
	}
	cands = append(cands, cand("  P1 ", true), cand("done-one", false))
	done := map[string]bool{Key("done-one"): true}

	a := Sample(cands, done, 6, 42)
	b := Sample(cands, done, 6, 42)
	if !reflect.DeepEqual(a, b) {
		t.Error("same seed must pick the same sample")
	}
	if reflect.DeepEqual(a, Sample(cands, done, 6, 43)) && reflect.DeepEqual(a, Sample(cands, done, 6, 44)) {
		t.Error("different seeds should (almost always) differ")
	}
	pos := 0
	seen := map[string]bool{}
	for _, c := range a {
		if c.Heuristic {
			pos++
		}
		if seen[Key(c.Message)] || c.Message == "done-one" {
			t.Errorf("duplicate or already-labelled message %q", c.Message)
		}
		seen[Key(c.Message)] = true
	}
	if len(a) != 6 || pos != 3 {
		t.Errorf("sample = %d items, %d positive; want 6 balanced 3/3", len(a), pos)
	}
	// Positives run out first: the rest fills with negatives.
	if got := Sample(cands, done, 9, 1); len(got) != 9 {
		t.Errorf("len = %d", len(got))
	} else {
		p := 0
		for _, c := range got {
			if c.Heuristic {
				p++
			}
		}
		if p != 4 {
			t.Errorf("all 4 positives expected, got %d", p)
		}
	}
	if got := Sample(cands, done, 100, 1); len(got) != 10 {
		t.Errorf("pool has 10 unique unlabelled messages, got %d", len(got))
	}
	if len(Sample(nil, nil, 5, 1)) != 0 || len(Sample(cands, nil, 0, 1)) != 0 {
		t.Error("empty pool or zero n")
	}
}

func TestLabelledAndAppend(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "labels.jsonl")
	if err := Append(path, Item{Action: "a", Message: "No, use pnpm", Correction: true, Turn: "t1"}); err != nil {
		t.Fatal(err)
	}
	if err := Append(path, Item{Message: "fine", Correction: false}); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("file mode: %v %v", fi, err)
	}
	if fi, _ := os.Stat(filepath.Dir(path)); fi.Mode().Perm() != 0o700 {
		t.Errorf("dir mode = %v", fi.Mode().Perm())
	}
	items, err := Load(path)
	if err != nil || len(items) != 2 || !items[0].Correction || items[0].Turn != "t1" || items[1].Message != "fine" {
		t.Errorf("round trip = %+v %v", items, err)
	}
	// Unlabelled files count too, and missing files are fine.
	unl := writeFile(t, "u.jsonl", `{"message":"Pending  one","correction":null}`+"\n")
	done, err := Labelled(path, unl, filepath.Join(dir, "missing.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"no, use pnpm", "fine", "pending one"} {
		if !done[k] {
			t.Errorf("Labelled missing %q: %v", k, done)
		}
	}
	bad := writeFile(t, "bad.jsonl", "{nope\n")
	if _, err := Labelled(bad); err == nil {
		t.Error("malformed file must fail")
	}
	// Append fails when the parent is a file or the target is a directory.
	if err := Append(filepath.Join(unl, "x", "y.jsonl"), Item{}); err == nil {
		t.Error("parent is a file")
	}
	if err := Append(dir, Item{}); err == nil {
		t.Error("target is a directory")
	}
}

func TestShippedLabels(t *testing.T) {
	t.Parallel()
	items, err := Load(filepath.Join("..", "..", "testdata", "labels", "candidates.jsonl"))
	if err != nil || len(items) == 0 {
		t.Fatalf("labels: %d %v", len(items), err)
	}
	pairs, err := LoadPairs(filepath.Join("..", "..", "testdata", "labels", "pairs.jsonl"))
	if err != nil || len(pairs) == 0 {
		t.Fatalf("pairs: %d %v", len(pairs), err)
	}
}

func BenchmarkHeuristic(b *testing.B) {
	items, err := Load(filepath.Join("..", "..", "testdata", "labels", "candidates.jsonl"))
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		Heuristic(items)
	}
}
