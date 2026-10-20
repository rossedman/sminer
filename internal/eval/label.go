package eval

import (
	"encoding/json"
	"errors"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"

	"github.com/rossedman/sminer/internal/signal"
)

// Unlabelled is a candidate written for labelling by hand: Correction is
// null until someone fills it in, and Load skips it until then.
type Unlabelled struct {
	Action     string `json:"action"`
	Message    string `json:"message"`
	Correction *bool  `json:"correction"`
	Turn       string `json:"turn,omitempty"`
}

// Sample picks up to n candidates to label, skipping messages already in
// done (keyed by Key). It balances heuristic positives and negatives as far
// as the pool allows, since real positives are rare and an unbalanced set
// says little about precision. The same seed picks the same sample.
func Sample(cands []signal.Candidate, done map[string]bool, n int, seed uint64) []signal.Candidate {
	var pos, neg []signal.Candidate
	seen := map[string]bool{}
	for _, c := range cands {
		k := Key(c.Message)
		if done[k] || seen[k] {
			continue
		}
		seen[k] = true
		if c.Heuristic {
			pos = append(pos, c)
		} else {
			neg = append(neg, c)
		}
	}
	r := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	r.Shuffle(len(pos), func(i, j int) { pos[i], pos[j] = pos[j], pos[i] })
	r.Shuffle(len(neg), func(i, j int) { neg[i], neg[j] = neg[j], neg[i] })
	var out []signal.Candidate
	for len(out) < n && (len(pos) > 0 || len(neg) > 0) {
		if len(pos) > 0 {
			out, pos = append(out, pos[0]), pos[1:]
		}
		if len(out) < n && len(neg) > 0 {
			out, neg = append(out, neg[0]), neg[1:]
		}
	}
	r.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
	return out
}

// Key identifies a message across label files, which may lack turn ids.
func Key(message string) string {
	return strings.ToLower(strings.Join(strings.Fields(message), " "))
}

// Labelled returns the keys of every message already in the label files,
// labelled or not. Missing files are empty.
func Labelled(paths ...string) (map[string]bool, error) {
	done := map[string]bool{}
	for _, p := range paths {
		items, err := load(p, true)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, it := range items {
			done[Key(it.Message)] = true
		}
	}
	return done, nil
}

// Append adds one label to a JSONL file, creating it (0600) if needed.
// Each answer is written as it is given, so quitting loses nothing.
func Append(path string, it Item) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	b, err := json.Marshal(it)
	if err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
