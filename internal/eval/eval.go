// Package eval scores correction detection against a hand-labelled set of
// candidate turns. The heuristic score runs offline (and in CI); the
// classifier score makes live API calls and runs on demand.
package eval

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/rossedman/sminer/internal/classify"
	"github.com/rossedman/sminer/internal/embed"
	"github.com/rossedman/sminer/internal/signal"
	"github.com/rossedman/sminer/internal/store"
)

// Item is one labelled candidate.
type Item struct {
	Action     string `json:"action"`
	Message    string `json:"message"`
	Correction bool   `json:"correction"`
}

// Score is a confusion matrix.
type Score struct {
	TP, FP, FN, TN int
}

// Precision of positive predictions.
func (s Score) Precision() float64 {
	if s.TP+s.FP == 0 {
		return 0
	}
	return float64(s.TP) / float64(s.TP+s.FP)
}

// Recall of labelled positives.
func (s Score) Recall() float64 {
	if s.TP+s.FN == 0 {
		return 0
	}
	return float64(s.TP) / float64(s.TP+s.FN)
}

func (s Score) add(pred, truth bool) Score {
	switch {
	case pred && truth:
		s.TP++
	case pred && !truth:
		s.FP++
	case !pred && truth:
		s.FN++
	default:
		s.TN++
	}
	return s
}

func (s Score) String() string {
	return fmt.Sprintf("precision %.1f%%  recall %.1f%%  (tp %d fp %d fn %d tn %d, n=%d)",
		100*s.Precision(), 100*s.Recall(), s.TP, s.FP, s.FN, s.TN, s.TP+s.FP+s.FN+s.TN)
}

// Load reads a JSONL labelled set.
func Load(path string) ([]Item, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Item
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "//") {
			continue
		}
		var it Item
		if err := json.Unmarshal([]byte(line), &it); err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, n, err)
		}
		out = append(out, it)
	}
	return out, sc.Err()
}

// Heuristic scores signal.IsCorrection alone.
func Heuristic(items []Item) Score {
	var s Score
	for _, it := range items {
		s = s.add(signal.IsCorrection(it.Message), it.Correction)
	}
	return s
}

// ClassifierResult is the classifier's score plus its per-item verdicts.
type ClassifierResult struct {
	Score
	Verdicts []bool
}

// Classifier asks the LLM about every item, regardless of the heuristic.
func Classifier(ctx context.Context, items []Item, llm classify.LLM) (ClassifierResult, error) {
	var r ClassifierResult
	for _, it := range items {
		sig := store.Signal{Type: store.Correction, Excerpt: it.Message, Context: it.Action}
		reply, err := llm.Complete(ctx, classify.SystemPrompt, classify.UserPrompt(sig), 300)
		if err != nil {
			return r, err
		}
		v, err := classify.ParseVerdict(reply)
		if err != nil {
			return r, err
		}
		r.Verdicts = append(r.Verdicts, v.Correction)
		r.Score = r.Score.add(v.Correction, it.Correction)
	}
	return r, nil
}

// Pipeline scores what sminer actually does: heuristic first, then the
// classifier confirms.
func Pipeline(items []Item, verdicts []bool) Score {
	var s Score
	for i, it := range items {
		pred := signal.IsCorrection(it.Message) && i < len(verdicts) && verdicts[i]
		s = s.add(pred, it.Correction)
	}
	return s
}

// Pair is a labelled pair of rule texts.
type Pair struct {
	A    string `json:"a"`
	B    string `json:"b"`
	Same bool   `json:"same"`
}

// LoadPairs reads a JSONL pair set.
func LoadPairs(path string) ([]Pair, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []Pair
	for n, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "//") {
			continue
		}
		var p Pair
		if err := json.Unmarshal([]byte(line), &p); err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, n+1, err)
		}
		out = append(out, p)
	}
	return out, nil
}

// Pairs scores "same rule" predictions at a similarity threshold: a pair is
// predicted same when its cosine similarity reaches thr.
func Pairs(pairs []Pair, e embed.Embedder, thr float64) (Score, error) {
	var s Score
	for _, p := range pairs {
		v, err := e.Embed([]string{p.A, p.B})
		if err != nil {
			return s, err
		}
		s = s.add(embed.Cosine(v[0], v[1]) >= thr, p.Same)
	}
	return s, nil
}
