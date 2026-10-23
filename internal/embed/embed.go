// Package embed turns short texts into vectors for clustering. Everything
// here runs locally: either a hashed bag-of-words model built in, or an
// ollama server on this machine. Transcript text never leaves the machine
// through this package.
package embed

import (
	"bytes"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/rossedman/sminer/internal/config"
	"github.com/rossedman/sminer/internal/textutil"
)

// Embedder produces unit-length vectors.
type Embedder interface {
	// Name identifies the model; cached vectors are discarded when it changes.
	Name() string
	Embed(texts []string) ([][]float32, error)
	// Threshold is the default cosine similarity for "same rule".
	Threshold() float64
}

// New picks an embedder from config. "auto" uses ollama when it answers on
// its URL and has the model pulled, and falls back to the hashed model
// otherwise.
func New(cfg config.Embeddings) (Embedder, error) {
	switch cfg.Provider {
	case "", "auto":
		o := &Ollama{URL: cfg.URL, Model: cfg.Model}
		if o.Available() {
			return o, nil
		}
		return Hash{}, nil
	case "ollama":
		return &Ollama{URL: cfg.URL, Model: cfg.Model}, nil
	case "hash":
		return Hash{}, nil
	}
	return nil, fmt.Errorf("unknown embeddings provider %q", cfg.Provider)
}

// Hash is a feature-hashed bag of stemmed unigrams and bigrams. It needs no
// model and catches near-duplicate wording; paraphrases need ollama.
type Hash struct{}

const hashDims = 256

// Name identifies the embedder and its version, so stored vectors from an
// older scheme are recomputed.
func (Hash) Name() string { return "hash-v4" }

// Threshold is the cosine similarity at which two hashed texts cluster.
func (Hash) Threshold() float64 { return 0.45 }

// Embed returns one normalized feature-hashed vector per text.
func (h Hash) Embed(texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		out[i] = h.vec(t)
	}
	return out, nil
}

// vec returns the hashed feature vector for s: stemmed tokens, with framing
// words and one-letter tokens dropped and dotted or slashed tokens also split
// into parts.
func (Hash) vec(s string) []float32 {
	v := make([]float32, hashDims)
	all := textutil.Tokens(s)
	toks := make([]string, 0, len(all))
	var parts []string
	for _, t := range all {
		// "testify/assert" should share features with "testify" and "assert".
		if strings.ContainsAny(t, "/.") {
			parts = append(parts, strings.FieldsFunc(t, func(r rune) bool { return r == '/' || r == '.' })...)
			continue
		}
		parts = append(parts, t)
	}
	for _, t := range parts {
		if !framing[t] && len(t) > 1 {
			toks = append(toks, stem(t))
		}
	}
	add := func(feat string, w float32) {
		h := fnv.New32a()
		h.Write([]byte(feat))
		x := h.Sum32()
		sign := float32(1)
		if x&(1<<31) != 0 {
			sign = -1
		}
		v[x%hashDims] += sign * w
	}
	for i, t := range toks {
		add(t, 1)
		if i > 0 {
			add(toks[i-1]+" "+t, 0.7)
		}
	}
	return Normalize(v)
}

// framing words carry the "this is a correction" tone shared by unrelated
// corrections, not the rule itself; they are left out of hashed vectors.
var framing = map[string]bool{
	"no": true, "nope": true, "dont": true, "not": true, "please": true, "actually": true,
	"instead": true, "said": true, "told": true, "why": true, "did": true, "again": true,
	"stop": true, "wrong": true, "ok": true, "okay": true, "hmm": true, "use": true,
}

// stem strips a common English suffix from w, keeping at least three letters.
// Short words are returned unchanged.
func stem(w string) string {
	for _, suf := range []string{"ing", "ed", "es", "s"} {
		if len(w) > len(suf)+3 && strings.HasSuffix(w, suf) {
			return w[:len(w)-len(suf)]
		}
	}
	return w
}

// Ollama calls a local ollama server's /api/embed endpoint.
type Ollama struct {
	URL    string
	Model  string
	client *http.Client
}

// Name identifies the embedder by its model, so switching models re-embeds.
func (o *Ollama) Name() string { return "ollama:" + o.Model }

// Threshold is the cosine similarity at which two model embeddings cluster.
func (o *Ollama) Threshold() float64 { return 0.78 }

// http returns the HTTP client, creating one with a 60 second timeout on first
// use.
func (o *Ollama) http() *http.Client {
	if o.client == nil {
		o.client = &http.Client{Timeout: 60 * time.Second}
	}
	return o.client
}

// Available reports whether the server answers quickly and has the model
// pulled. A bare model name matches its ":latest" tag.
func (o *Ollama) Available() bool {
	c := &http.Client{Timeout: 400 * time.Millisecond}
	resp, err := c.Get(strings.TrimRight(o.URL, "/") + "/api/tags")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	var tags struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if json.NewDecoder(resp.Body).Decode(&tags) != nil {
		return false
	}
	want := o.Model
	if !strings.Contains(want, ":") {
		want += ":latest"
	}
	for _, m := range tags.Models {
		if m.Name == o.Model || m.Name == want {
			return true
		}
	}
	return false
}

// Embed asks the ollama server for one embedding per text.
func (o *Ollama) Embed(texts []string) ([][]float32, error) {
	out := make([][]float32, 0, len(texts))
	for start := 0; start < len(texts); start += 64 {
		end := min(start+64, len(texts))
		body, _ := json.Marshal(map[string]any{"model": o.Model, "input": texts[start:end]})
		resp, err := o.http().Post(strings.TrimRight(o.URL, "/")+"/api/embed", "application/json", bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("ollama embed: %w", err)
		}
		var r struct {
			Embeddings [][]float32 `json:"embeddings"`
			Error      string      `json:"error"`
		}
		err = json.NewDecoder(resp.Body).Decode(&r)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("ollama embed: %w", err)
		}
		if r.Error != "" || len(r.Embeddings) != end-start {
			return nil, fmt.Errorf("ollama embed: %s (got %d vectors for %d inputs)", r.Error, len(r.Embeddings), end-start)
		}
		for _, e := range r.Embeddings {
			out = append(out, Normalize(e))
		}
	}
	return out, nil
}

// Normalize scales v to unit length in place and returns it.
func Normalize(v []float32) []float32 {
	var n float64
	for _, x := range v {
		n += float64(x) * float64(x)
	}
	if n == 0 {
		return v
	}
	inv := float32(1 / math.Sqrt(n))
	for i := range v {
		v[i] *= inv
	}
	return v
}

// Cosine of two unit vectors (their dot product). Mismatched lengths score 0.
func Cosine(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var s float32
	for i := range a {
		s += a[i] * b[i]
	}
	return float64(s)
}

// Centroid is the normalised mean of vs.
func Centroid(vs [][]float32) []float32 {
	if len(vs) == 0 {
		return nil
	}
	c := make([]float32, len(vs[0]))
	for _, v := range vs {
		if len(v) != len(c) {
			continue
		}
		for i := range v {
			c[i] += v[i]
		}
	}
	return Normalize(c)
}
