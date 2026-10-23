package embed

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/rossedman/sminer/internal/config"
)

func TestNewProviders(t *testing.T) {
	t.Parallel()
	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL
	dead.Close()

	tests := []struct {
		name     string
		cfg      config.Embeddings
		wantName string
		wantErr  bool
	}{
		{"hash", config.Embeddings{Provider: "hash"}, "hash-v4", false},
		{"ollama is used unchecked", config.Embeddings{Provider: "ollama", Model: "m", URL: deadURL}, "ollama:m", false},
		{"auto without server falls back", config.Embeddings{Provider: "auto", Model: "m", URL: deadURL}, "hash-v4", false},
		{"empty provider is auto", config.Embeddings{URL: deadURL, Model: "m"}, "hash-v4", false},
		{"unknown", config.Embeddings{Provider: "openai"}, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e, err := New(tt.cfg)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v", err)
			}
			if err == nil && e.Name() != tt.wantName {
				t.Errorf("Name = %q, want %q", e.Name(), tt.wantName)
			}
			if tt.wantErr && !strings.Contains(err.Error(), "openai") {
				t.Errorf("error should name the provider: %v", err)
			}
		})
	}
}

func TestThresholds(t *testing.T) {
	t.Parallel()
	if (Hash{}).Threshold() != 0.45 || (&Ollama{}).Threshold() != 0.78 {
		t.Error("unexpected default thresholds")
	}
	if (&Ollama{Model: "x"}).Name() != "ollama:x" {
		t.Error("ollama name")
	}
}

func TestOllamaAvailable(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		status int
		body   string
		model  string
		want   bool
	}{
		{"exact name", 200, `{"models":[{"name":"m:7b"}]}`, "m:7b", true},
		{"latest tag implied", 200, `{"models":[{"name":"m:latest"}]}`, "m", true},
		{"not pulled", 200, `{"models":[{"name":"other:latest"}]}`, "m", false},
		{"tag mismatch", 200, `{"models":[{"name":"m:latest"}]}`, "m:7b", false},
		{"no models", 200, `{"models":[]}`, "m", false},
		{"server error", 500, `{"models":[{"name":"m:latest"}]}`, "m", false},
		{"bad json", 200, `not json`, "m", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/tags" {
					http.NotFound(w, r)
					return
				}
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()
			// A trailing slash on the URL is tolerated.
			if got := (&Ollama{URL: srv.URL + "/", Model: tt.model}).Available(); got != tt.want {
				t.Errorf("Available = %v, want %v", got, tt.want)
			}
		})
	}
	dead := httptest.NewServer(http.NotFoundHandler())
	u := dead.URL
	dead.Close()
	if (&Ollama{URL: u, Model: "m"}).Available() {
		t.Error("unreachable server must not be available")
	}
}

type embedReq struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

func TestOllamaEmbedBatchesAndNormalizes(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	var sizes []int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/embed" || r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		var req embedReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Model != "nomic" {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		calls.Add(1)
		sizes = append(sizes, len(req.Input))
		out := make([][]float32, len(req.Input))
		for i := range out {
			out[i] = []float32{3, 4} // not unit length
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"embeddings": out})
	}))
	defer srv.Close()

	o := &Ollama{URL: srv.URL, Model: "nomic"}
	texts := make([]string, 130)
	for i := range texts {
		texts[i] = "t"
	}
	vecs, err := o.Embed(texts)
	if err != nil {
		t.Fatal(err)
	}
	if len(vecs) != 130 || calls.Load() != 3 || sizes[0] != 64 || sizes[1] != 64 || sizes[2] != 2 {
		t.Errorf("vecs=%d calls=%d sizes=%v", len(vecs), calls.Load(), sizes)
	}
	if math.Abs(float64(vecs[0][0])-0.6) > 1e-6 || math.Abs(float64(vecs[0][1])-0.8) > 1e-6 {
		t.Errorf("not normalized: %v", vecs[0])
	}
	if out, err := o.Embed(nil); err != nil || len(out) != 0 || calls.Load() != 3 {
		t.Errorf("empty input: %v %v calls=%d", out, err, calls.Load())
	}
}

func TestOllamaEmbedErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		handler http.HandlerFunc
		wantMsg string
	}{
		{"error field", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"error":"model not found"}`))
		}, "model not found"},
		{"wrong vector count", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"embeddings":[[1,0]]}`))
		}, "got 1 vectors for 2 inputs"},
		{"bad json", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`<html>`))
		}, "ollama embed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(tt.handler)
			defer srv.Close()
			_, err := (&Ollama{URL: srv.URL, Model: "m"}).Embed([]string{"a", "b"})
			if err == nil || !strings.Contains(err.Error(), tt.wantMsg) {
				t.Errorf("err = %v, want it to contain %q", err, tt.wantMsg)
			}
		})
	}
	dead := httptest.NewServer(http.NotFoundHandler())
	u := dead.URL
	dead.Close()
	if _, err := (&Ollama{URL: u, Model: "m"}).Embed([]string{"a"}); err == nil || !strings.Contains(err.Error(), "ollama embed") {
		t.Errorf("unreachable server err = %v", err)
	}
}

func TestOllamaReusesClient(t *testing.T) {
	t.Parallel()
	o := &Ollama{}
	c := o.http()
	if c == nil || c != o.http() || c.Timeout.Seconds() != 60 {
		t.Errorf("client = %+v", c)
	}
}

func TestNormalizeCosineCentroid(t *testing.T) {
	t.Parallel()
	v := Normalize([]float32{3, 4})
	if math.Abs(Cosine(v, v)-1) > 1e-6 {
		t.Errorf("unit vector self-cosine = %v", Cosine(v, v))
	}
	zero := []float32{0, 0}
	if got := Normalize(zero); got[0] != 0 || got[1] != 0 {
		t.Errorf("zero vector changed: %v", got)
	}
	if Cosine([]float32{1}, []float32{1, 0}) != 0 || Cosine(nil, nil) != 0 {
		t.Error("mismatched or empty vectors score 0")
	}
	if got := Cosine([]float32{1, 0}, []float32{0, 1}); got != 0 {
		t.Errorf("orthogonal = %v", got)
	}
	if got := Cosine([]float32{1, 0}, []float32{-1, 0}); got != -1 {
		t.Errorf("opposite = %v", got)
	}

	if Centroid(nil) != nil {
		t.Error("Centroid(nil) should be nil")
	}
	c := Centroid([][]float32{{1, 0}, {0, 1}, {9, 9, 9}}) // mismatched length skipped
	if math.Abs(float64(c[0])-math.Sqrt2/2) > 1e-6 || math.Abs(float64(c[1])-math.Sqrt2/2) > 1e-6 || len(c) != 2 {
		t.Errorf("centroid = %v", c)
	}
}

func TestHashEmbedProperties(t *testing.T) {
	t.Parallel()
	h := Hash{}
	if h.Name() != "hash-v4" {
		t.Errorf("Name = %q", h.Name())
	}
	vecs, err := h.Embed([]string{"Don't use testify/assert", "Don't use testify/assert", "", "ing"})
	if err != nil || len(vecs) != 4 {
		t.Fatalf("%v %d", err, len(vecs))
	}
	for i, v := range vecs {
		if len(v) != hashDims {
			t.Errorf("vec %d has %d dims", i, len(v))
		}
	}
	if Cosine(vecs[0], vecs[1]) < 0.999 {
		t.Error("identical texts must match")
	}
	if math.Abs(Cosine(vecs[0], vecs[0])-1) > 1e-5 {
		t.Error("vectors are unit length")
	}
	for _, x := range vecs[2] {
		if x != 0 {
			t.Fatal("empty text should embed to the zero vector")
		}
	}
	if out, _ := h.Embed(nil); len(out) != 0 {
		t.Error("no texts -> no vectors")
	}
	if stem("testing") != "test" || stem("tests") != "test" || stem("ed") != "ed" || stem("used") != "used" || stem("classes") != "class" {
		t.Errorf("stem: %q %q %q %q %q", stem("testing"), stem("tests"), stem("ed"), stem("used"), stem("classes"))
	}
}

func TestGreedyParallelScoringMatchesSerial(t *testing.T) {
	t.Parallel()
	// More clusters than one scoring chunk forces the parallel path.
	vecs, _ := randomVecs(1500, 64, 1400)
	assign, cents := Greedy(vecs, 0.95)
	if len(cents) < 600 {
		t.Fatalf("only %d clusters; test needs > 512 candidates", len(cents))
	}
	again, _ := Greedy(vecs, 0.95)
	for i := range assign {
		if assign[i] != again[i] {
			t.Fatalf("non-deterministic assignment at %d", i)
		}
	}
	// Every vector is at least as close to its own centroid as the threshold
	// allows for a join, or it started the cluster.
	first := map[int]int{}
	for i, c := range assign {
		if _, ok := first[c]; !ok {
			first[c] = i
		}
	}
	for c, i := range first {
		if assign[i] != c {
			t.Fatal("cluster ids are assigned in order of first appearance")
		}
	}
}

func TestGreedyEdgeCases(t *testing.T) {
	t.Parallel()
	assign, cents := Greedy(nil, 0.5)
	if len(assign) != 0 || len(cents) != 0 {
		t.Errorf("empty input: %v %v", assign, cents)
	}
	a := Normalize([]float32{1, 0})
	b := Normalize([]float32{0, 1})
	assign, cents = Greedy([][]float32{a, a, b, a}, 0.9)
	if len(cents) != 2 || assign[0] != 0 || assign[1] != 0 || assign[2] != 1 || assign[3] != 0 {
		t.Errorf("assign = %v cents = %d", assign, len(cents))
	}
}

func BenchmarkHashEmbed(b *testing.B) {
	texts := []string{
		"Don't use testify/assert; write table-driven tests with the standard library",
		"No, use pnpm instead of npm in this repo",
		"terraform resource names should be snake_case",
		"Always run go vet and go test before committing any change",
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := (Hash{}).Embed(texts); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkCosine(b *testing.B) {
	vecs, _ := Hash{}.Embed([]string{"use table-driven tests", "write table driven tests please"})
	b.ReportAllocs()
	var s float64
	for b.Loop() {
		s += Cosine(vecs[0], vecs[1])
	}
	if s == 0 {
		b.Fatal("zero similarity")
	}
}
