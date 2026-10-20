package embed

import (
	"math/rand"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rossedman/sminer/internal/config"
)

func TestHashSimilarity(t *testing.T) {
	h := Hash{}
	v, _ := h.Embed([]string{
		"don't use testify, use table-driven tests",
		"no testify: table-driven tests please",
		"terraform resource names should be snake_case",
	})
	if s := Cosine(v[0], v[1]); s < h.Threshold() {
		t.Errorf("same rule scored %.2f", s)
	}
	if s := Cosine(v[0], v[2]); s >= h.Threshold() {
		t.Errorf("different rules scored %.2f", s)
	}
}

func randomVecs(n, dims int, groups int) ([][]float32, []int) {
	r := rand.New(rand.NewSource(7))
	centers := make([][]float32, groups)
	for g := range centers {
		c := make([]float32, dims)
		for i := range c {
			c[i] = float32(r.NormFloat64())
		}
		centers[g] = Normalize(c)
	}
	vecs := make([][]float32, n)
	truth := make([]int, n)
	for i := range vecs {
		g := r.Intn(groups)
		v := make([]float32, dims)
		for k := range v {
			v[k] = centers[g][k] + float32(r.NormFloat64())*0.02
		}
		vecs[i], truth[i] = Normalize(v), g
	}
	return vecs, truth
}

// Both the brute-force and the LSH path must recover well-separated groups.
func TestGreedyRecoversGroups(t *testing.T) {
	for _, n := range []int{500, bruteForceMax + 2000} {
		vecs, truth := randomVecs(n, 64, 40)
		assign, cents := Greedy(vecs, 0.8)
		if len(cents) != 40 {
			t.Errorf("n=%d: %d clusters, want 40", n, len(cents))
		}
		m := map[int]int{}
		for i, c := range assign {
			if g, ok := m[c]; ok && g != truth[i] {
				t.Fatalf("n=%d: cluster %d mixes groups %d and %d", n, c, g, truth[i])
			}
			m[c] = truth[i]
		}
	}
}

func BenchmarkGreedyDistinct(b *testing.B) {
	vecs, _ := randomVecs(20000, 256, 15000)
	b.ResetTimer()
	for range b.N {
		Greedy(vecs, 0.6)
	}
}

// Texts sharing only correction framing must not snowball into one cluster.
func TestGreedyNoDrift(t *testing.T) {
	r := rand.New(rand.NewSource(3))
	var texts []string
	for range 400 {
		s := "no, don't"
		for range 7 {
			s += " w" + string(rune('a'+r.Intn(26))) + string(rune('a'+r.Intn(26))) + string(rune('a'+r.Intn(26)))
		}
		texts = append(texts, s)
	}
	h := Hash{}
	vecs, _ := h.Embed(texts)
	assign, _ := Greedy(vecs, h.Threshold())
	sizes := map[int]int{}
	for _, c := range assign {
		sizes[c]++
	}
	for c, n := range sizes {
		if n > 3 {
			t.Errorf("cluster %d absorbed %d unrelated texts", c, n)
		}
	}
}

func TestAutoNeedsPulledModel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"models":[{"name":"llama3:latest"},{"name":"nomic-embed-text:latest"}]}`))
	}))
	defer srv.Close()
	for model, want := range map[string]string{
		"nomic-embed-text":        "ollama:nomic-embed-text",
		"nomic-embed-text:latest": "ollama:nomic-embed-text:latest",
		"mxbai-embed-large":       Hash{}.Name(),
	} {
		e, err := New(config.Embeddings{Provider: "auto", URL: srv.URL, Model: model})
		if err != nil {
			t.Fatal(err)
		}
		if e.Name() != want {
			t.Errorf("model %q: got %s, want %s", model, e.Name(), want)
		}
	}
}
