package embed

import (
	"math/rand"
	"runtime"
	"sync"
)

// Greedy assigns each vector, in order, to the most similar existing cluster
// whose centroid is at least thr away in cosine similarity, or starts a new
// cluster. It returns one cluster index per input and the final centroids.
//
// A vector joins only if it is also within leaderSlack of the cluster's
// first member. Without that guard a large cluster's centroid drifts toward
// whatever its members share (often just framing like "no, don't") and
// absorbs unrelated texts.
//
// Small inputs compare against every cluster. Large inputs (repeated
// instruction detection over months of prompts) narrow candidates with
// random-hyperplane LSH first; every candidate is still scored exactly.
func Greedy(vecs [][]float32, thr float64) ([]int, [][]float32) {
	assign := make([]int, len(vecs))
	var sums, cents, leaders [][]float32
	var lsh *lshIndex
	if len(vecs) > bruteForceMax {
		lsh = newLSH(len(vecs[0]))
	}
	var cand []int
	var stamp []int32 // stamp[c] == i+1 when cluster c is already a candidate for item i
	for i, v := range vecs {
		cand = cand[:0]
		if lsh != nil {
			for len(stamp) < len(cents) {
				stamp = append(stamp, 0)
			}
			cand = lsh.candidates(v, cand, stamp, int32(i+1))
		} else {
			for c := range cents {
				cand = append(cand, c)
			}
		}
		best := bestMatch(v, cand, cents, leaders, thr)
		if best < 0 {
			best = len(cents)
			sums = append(sums, append([]float32(nil), v...))
			cents = append(cents, append([]float32(nil), v...))
			leaders = append(leaders, v)
		} else {
			for k := range v {
				sums[best][k] += v[k]
			}
			cents[best] = Normalize(append(cents[best][:0], sums[best]...))
		}
		assign[i] = best
		if lsh != nil {
			lsh.add(v, best)
		}
	}
	return assign, cents
}

// bestMatch returns the candidate cluster most similar to v at or above
// thr, or -1. Large candidate sets are scored in parallel.
func bestMatch(v []float32, cand []int, cents, leaders [][]float32, thr float64) int {
	score := func(c int) float64 {
		s := Cosine(v, cents[c])
		if s >= thr && Cosine(v, leaders[c]) < thr-leaderSlack {
			return -1
		}
		return s
	}
	const chunk = 512
	if len(cand) <= chunk {
		best, bestSim := -1, thr
		for _, c := range cand {
			if s := score(c); s > bestSim || (s == bestSim && best < 0) {
				best, bestSim = c, s
			}
		}
		return best
	}
	workers := min(runtime.GOMAXPROCS(0), (len(cand)+chunk-1)/chunk)
	type res struct {
		c int
		s float64
	}
	out := make([]res, workers)
	var wg sync.WaitGroup
	per := (len(cand) + workers - 1) / workers
	for w := range workers {
		lo, hi := w*per, min((w+1)*per, len(cand))
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := res{-1, thr}
			for _, c := range cand[lo:hi] {
				if s := score(c); s > r.s || (s == r.s && r.c < 0) {
					r = res{c, s}
				}
			}
			out[w] = r
		}()
	}
	wg.Wait()
	best := res{-1, thr}
	for _, r := range out {
		// Ties go to the earliest cluster so results are deterministic.
		if r.c >= 0 && (r.s > best.s || (r.s == best.s && (best.c < 0 || r.c < best.c))) {
			best = r
		}
	}
	return best.c
}

const (
	leaderSlack   = 0.1
	bruteForceMax = 3000
	lshBands      = 16
	lshBits       = 8
)

type lshIndex struct {
	planes  [][]float32
	buckets []map[uint8][]int
}

// newLSH returns an empty LSH index for vectors of the given dimension. The
// random hyperplanes come from a fixed seed, so signatures are the same on
// every run.
func newLSH(dims int) *lshIndex {
	r := rand.New(rand.NewSource(42))
	l := &lshIndex{buckets: make([]map[uint8][]int, lshBands)}
	for range lshBands * lshBits {
		p := make([]float32, dims)
		for i := range p {
			p[i] = float32(r.NormFloat64())
		}
		l.planes = append(l.planes, p)
	}
	for b := range l.buckets {
		l.buckets[b] = map[uint8][]int{}
	}
	return l
}

// sig returns the signature of v: one byte per band, with a bit set for each
// hyperplane that v lies on the positive side of.
func (l *lshIndex) sig(v []float32) [lshBands]uint8 {
	var s [lshBands]uint8
	for i, p := range l.planes {
		var d float32
		for k := range v {
			d += v[k] * p[k]
		}
		if d >= 0 {
			s[i/lshBits] |= 1 << (i % lshBits)
		}
	}
	return s
}

// candidates appends to out the clusters sharing a bucket with v in any band.
// The stamp slice and mark keep each cluster from being added twice.
func (l *lshIndex) candidates(v []float32, out []int, stamp []int32, mark int32) []int {
	s := l.sig(v)
	for b, key := range s {
		for _, c := range l.buckets[b][key] {
			if stamp[c] != mark {
				stamp[c] = mark
				out = append(out, c)
			}
		}
	}
	return out
}

// add records cluster in the bucket for v in every band, skipping a repeat of
// the bucket's most recent entry.
func (l *lshIndex) add(v []float32, cluster int) {
	s := l.sig(v)
	for b, key := range s {
		ids := l.buckets[b][key]
		if len(ids) == 0 || ids[len(ids)-1] != cluster {
			l.buckets[b][key] = append(ids, cluster)
		}
	}
}
