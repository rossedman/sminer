// Package cluster groups signals into findings. Behavioural signals
// (corrections, interrupts, rewinds, repeated instructions) cluster on
// embedding similarity; hook blocks, failure loops and fix-ups group on
// their exact key. Findings keep a stable ID across runs by matching new
// clusters to existing findings, so accept/dismiss state and trends survive
// re-analysis.
package cluster

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/rossedman/sminer/internal/embed"
	"github.com/rossedman/sminer/internal/store"
	"github.com/rossedman/sminer/internal/textutil"
)

// Families.
const (
	Behavior = "behavior"
	Hook     = "hook"
	Failure  = "failure"
	Fixup    = "fixup"
)

// FamilyOf maps a signal type to its family.
func FamilyOf(typ string) string {
	switch typ {
	case store.HookBlock:
		return Hook
	case store.ToolFailure:
		return Failure
	case store.ManualFixup:
		return Fixup
	}
	return Behavior
}

// Options control the cluster stage.
type Options struct {
	Embedder  embed.Embedder
	Threshold float64
	MinCount  int
	Since     time.Time
	Repo      string
	Now       time.Time
}

// Stats summarises a cluster run.
type Stats struct {
	Signals  int
	Clusters int
	Findings []string // ids touched this run, largest first
	New      int
}

type group struct {
	family  string
	key     string
	members []store.Signal
	vecs    [][]float32
	cent    []float32
}

// FixupKey groups every manual fix-up into one finding. Per-file-kind
// findings read as near-duplicates; FixupKinds gives the breakdown instead.
const FixupKey = "edits"

// KindCount is how many fix-ups touched one kind of file.
type KindCount struct {
	Kind  string
	Count int
}

// FixupKinds counts fix-up signals by file kind (their key), most first.
func FixupKinds(sigs []store.Signal) []KindCount {
	counts := map[string]int{}
	for _, s := range sigs {
		if s.Type == store.ManualFixup && s.Key != "" {
			counts[s.Key]++
		}
	}
	return SortKinds(counts)
}

// SortKinds orders per-kind counts, most first.
func SortKinds(counts map[string]int) []KindCount {
	var out []KindCount
	for k, c := range counts {
		out = append(out, KindCount{k, c})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Kind < out[j].Kind
	})
	return out
}

// SignalText is the text a behavioural signal clusters on: the classifier's
// generalised rule when there is one, else the excerpt.
func SignalText(s store.Signal) string {
	if s.Rule != "" {
		return s.Rule
	}
	return strings.TrimPrefix(s.Excerpt, "(interrupted) ")
}

// Run clusters the signals in scope and updates findings.
func Run(st *store.Store, opt Options) (Stats, error) {
	var stats Stats
	if opt.MinCount <= 0 {
		opt.MinCount = 3
	}
	if opt.Now.IsZero() {
		opt.Now = time.Now()
	}
	thr := opt.Threshold
	if thr <= 0 {
		thr = opt.Embedder.Threshold()
	}

	where := `(confirmed IS NULL OR confirmed=1)`
	args := []any{}
	if !opt.Since.IsZero() {
		where += ` AND ts >= ?`
		args = append(args, store.Ms(opt.Since))
	}
	if opt.Repo != "" {
		where += ` AND repo = ?`
		args = append(args, opt.Repo)
	}
	sigs, err := st.QuerySignals(where, args...)
	if err != nil {
		return stats, err
	}
	// A classifier "ignore" means one-off; keep it out of clustering. So is
	// an interrupt with no reason given: it is friction, but there is no
	// rule in it to propose.
	all := append([]store.Signal(nil), sigs...)
	kept := sigs[:0]
	for _, s := range sigs {
		if s.RemedyHint == "ignore" || (s.Type == store.Interrupt && strings.HasPrefix(s.Excerpt, "(interrupted) ")) {
			continue
		}
		kept = append(kept, s)
	}
	sigs = kept
	stats.Signals = len(sigs)

	if err := ensureEmbeddings(st, opt.Embedder, sigs); err != nil {
		return stats, err
	}

	var groups []*group
	byKey := map[string]*group{}
	var behavior []store.Signal
	for _, s := range sigs {
		fam := FamilyOf(s.Type)
		if fam == Behavior {
			behavior = append(behavior, s)
			continue
		}
		key := s.Key
		if fam == Fixup {
			key = FixupKey
		}
		k := fam + "\x00" + key
		g := byKey[k]
		if g == nil {
			g = &group{family: fam, key: key}
			byKey[k] = g
			groups = append(groups, g)
		}
		g.members = append(g.members, s)
	}
	if len(behavior) > 0 {
		vecs := make([][]float32, len(behavior))
		for i, s := range behavior {
			vecs[i] = s.Embedding
		}
		assign, cents := embed.Greedy(vecs, thr)
		bg := make([]*group, len(cents))
		for i, c := range assign {
			if bg[c] == nil {
				bg[c] = &group{family: Behavior, cent: cents[c]}
				groups = append(groups, bg[c])
			}
			bg[c].members = append(bg[c].members, behavior[i])
			bg[c].vecs = append(bg[c].vecs, vecs[i])
		}
	}
	stats.Clusters = len(groups)

	var big []*group
	for _, g := range groups {
		if len(g.members) >= opt.MinCount {
			big = append(big, g)
		}
	}
	sort.SliceStable(big, func(i, j int) bool { return len(big[i].members) > len(big[j].members) })

	existing, err := st.QueryFindings("")
	if err != nil {
		return stats, err
	}
	taken := map[string]bool{}
	ids := map[string]bool{}
	for _, f := range existing {
		ids[f.ID] = true
	}

	tx, err := st.DB.Begin()
	if err != nil {
		return stats, err
	}
	defer tx.Rollback()
	// Re-link every in-scope signal from scratch, so one that no longer
	// clusters (or is now filtered out) stops counting toward its old
	// finding.
	for _, s := range all {
		if _, err := tx.Exec(`DELETE FROM finding_signals WHERE signal_id=?`, s.ID); err != nil {
			return stats, err
		}
	}
	for _, g := range big {
		f, ok := match(g, existing, taken, thr)
		if !ok {
			f = store.Finding{
				ID:     newID(g, ids),
				Family: g.family,
				Status: store.StatusOpen,
			}
			ids[f.ID] = true
			stats.New++
		}
		taken[f.ID] = true
		f.Family = g.family
		if g.family == Behavior {
			f.Centroid = g.cent
			f.Key = ""
		} else {
			f.Key = g.key
		}
		if !f.Synthesized || f.Title == "" {
			f.Title = DefaultTitle(g.family, g.key, g.members, g.vecs, g.cent)
		}
		f.UpdatedAt = opt.Now
		for _, s := range g.members {
			if _, err := tx.Exec(`INSERT OR IGNORE INTO finding_signals(finding_id, signal_id) VALUES(?,?)`, f.ID, s.ID); err != nil {
				return stats, err
			}
		}
		var first, last int64
		if err := tx.QueryRow(`SELECT MIN(s.ts), MAX(s.ts) FROM signals s JOIN finding_signals fs ON fs.signal_id=s.id WHERE fs.finding_id=?`, f.ID).Scan(&first, &last); err != nil {
			return stats, err
		}
		f.FirstSeen, f.LastSeen = store.FromMs(first), store.FromMs(last)
		if err := store.SaveFinding(tx, f); err != nil {
			return stats, err
		}
		stats.Findings = append(stats.Findings, f.ID)
	}
	return stats, tx.Commit()
}

func match(g *group, existing []store.Finding, taken map[string]bool, thr float64) (store.Finding, bool) {
	best, bestSim := -1, thr
	for i, f := range existing {
		if taken[f.ID] || f.Family != g.family {
			continue
		}
		if g.family != Behavior {
			if f.Key == g.key {
				return f, true
			}
			continue
		}
		if s := embed.Cosine(g.cent, f.Centroid); s >= bestSim {
			best, bestSim = i, s
		}
	}
	if best < 0 {
		return store.Finding{}, false
	}
	return existing[best], true
}

func newID(g *group, ids map[string]bool) string {
	seed := g.key
	if g.family == Behavior {
		seed = g.members[0].ID
	}
	base := "F" + textutil.Hash(6, g.family, seed)
	id := base
	for n := 2; ids[id]; n++ {
		id = fmt.Sprintf("%s-%d", base, n)
	}
	return id
}

// DefaultTitle is the heuristic title used until (or unless) the remedy
// stage synthesises a better one.
func DefaultTitle(family, key string, members []store.Signal, vecs [][]float32, cent []float32) string {
	switch family {
	case Hook:
		msg := ""
		if len(members) > 0 {
			msg = members[len(members)-1].Excerpt
		}
		if msg != "" {
			return textutil.Truncate(fmt.Sprintf("Hook %s keeps blocking actions: %s", key, textutil.OneLine(msg)), 110)
		}
		return fmt.Sprintf("Hook %s keeps blocking actions", key)
	case Failure:
		return fmt.Sprintf("`%s` fails repeatedly in a row", key)
	case Fixup:
		return "You often hand-correct files right after Claude edits them"
	}
	rep := Representative(members, vecs, cent)
	return textutil.Truncate(textutil.OneLine(SignalText(rep)), 110)
}

// Representative is the member closest to the centroid.
func Representative(members []store.Signal, vecs [][]float32, cent []float32) store.Signal {
	best, bestSim := 0, -2.0
	for i := range members {
		if i < len(vecs) {
			if s := embed.Cosine(vecs[i], cent); s > bestSim {
				best, bestSim = i, s
			}
		}
	}
	return members[best]
}

func ensureEmbeddings(st *store.Store, e embed.Embedder, sigs []store.Signal) error {
	if st.Meta("signal_embedder") != e.Name() {
		if _, err := st.DB.Exec(`UPDATE signals SET embedding=NULL`); err != nil {
			return err
		}
		if err := st.SetMeta("signal_embedder", e.Name()); err != nil {
			return err
		}
		for i := range sigs {
			sigs[i].Embedding = nil
		}
	}
	var idx []int
	var texts []string
	for i, s := range sigs {
		if FamilyOf(s.Type) == Behavior && s.Embedding == nil {
			idx = append(idx, i)
			texts = append(texts, SignalText(s))
		}
	}
	if len(idx) == 0 {
		return nil
	}
	vecs, err := e.Embed(texts)
	if err != nil {
		return err
	}
	tx, err := st.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for k, i := range idx {
		sigs[i].Embedding = vecs[k]
		if _, err := tx.Exec(`UPDATE signals SET embedding=? WHERE id=?`, store.EncodeVec(vecs[k]), sigs[i].ID); err != nil {
			return err
		}
	}
	return tx.Commit()
}
