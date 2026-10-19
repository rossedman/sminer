package eval

import (
	"context"
	"strings"
	"testing"

	"github.com/rossedman/sminer/internal/embed"
)

const (
	candidates = "../../testdata/labels/candidates.jsonl"
	pairsFile  = "../../testdata/labels/pairs.jsonl"
)

// The heuristic pass favours precision; the classifier then confirms. These
// floors catch regressions in the regexes; raise them as the set grows.
func TestHeuristicPrecision(t *testing.T) {
	set, err := Load(candidates)
	if err != nil {
		t.Fatal(err)
	}
	s := Heuristic(set)
	t.Logf("heuristic on %d labelled turns: %s", len(set), s)
	if s.Precision() < 0.85 {
		t.Errorf("heuristic precision %.2f < 0.85", s.Precision())
	}
	if s.Recall() < 0.75 {
		t.Errorf("heuristic recall %.2f < 0.75", s.Recall())
	}
}

// TestPipelineWithMockedClassifier runs the labelled set through the
// heuristic-then-classifier pipeline with recorded classifier answers (here:
// the labels), so CI exercises the plumbing without API calls.
func TestPipelineWithMockedClassifier(t *testing.T) {
	set, err := Load(candidates)
	if err != nil {
		t.Fatal(err)
	}
	r, err := Classifier(context.Background(), set, labelEcho(set))
	if err != nil {
		t.Fatal(err)
	}
	p := Pipeline(set, r.Verdicts)
	if p.FP != 0 || p.Precision() != 1 {
		t.Errorf("pipeline with a perfect classifier should have no false positives: %s", p)
	}
}

type echo struct {
	labels map[string]bool
}

func labelEcho(set []Item) *echo {
	e := &echo{labels: map[string]bool{}}
	for _, it := range set {
		e.labels[it.Message] = it.Correction
	}
	return e
}

func (e *echo) Complete(_ context.Context, _, user string, _ int) (string, error) {
	for msg, c := range e.labels {
		if containsLine(user, msg) {
			if c {
				return `{"correction": true, "rule": "x", "remedy": "claude_md"}`, nil
			}
			break
		}
	}
	return `{"correction": false}`, nil
}

func containsLine(s, line string) bool {
	return line != "" && strings.Contains(s, "\n"+line+"\n")
}

func TestClusterPairs(t *testing.T) {
	pairs, err := LoadPairs(pairsFile)
	if err != nil {
		t.Fatal(err)
	}
	h := embed.Hash{}
	s, err := Pairs(pairs, h, h.Threshold())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("hash embedder at %.2f on %d pairs: %s", h.Threshold(), len(pairs), s)
	// The hashed model only sees shared words: it must not merge different
	// rules, and catches reworded same-rule pairs only partly. Run with
	// ollama for paraphrases.
	if s.Precision() < 0.9 {
		t.Errorf("pair precision %.2f < 0.9", s.Precision())
	}
}
