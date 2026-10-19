package signal

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/rossedman/sminer/internal/embed"
	"github.com/rossedman/sminer/internal/store"
	"github.com/rossedman/sminer/internal/textutil"
)

// guidanceRe keeps prompts that state a convention or procedure ("always",
// "before committing", "make sure") and drops plain task requests, which
// repeat across sessions for unrelated reasons.
var guidanceRe = regexp.MustCompile(`(?i)\b(always|never|don'?t|do not|before|after|make sure|remember|prefer|should|must|every ?time|convention|instead|whenever|first|then|avoid)\b`)

// repeated finds near-duplicate user instructions across sessions. Every
// user prompt of a useful size is embedded once (vectors are cached on the
// turn), clustered, and any cluster spanning MinSessions sessions yields one
// signal per session.
func repeated(st *store.Store, opt Options, sessions map[string]store.Session) error {
	if st.Meta("turn_embedder") != opt.Embedder.Name() {
		if _, err := st.DB.Exec(`UPDATE turns SET embedding=NULL WHERE embedding IS NOT NULL`); err != nil {
			return err
		}
		if err := st.SetMeta("turn_embedder", opt.Embedder.Name()); err != nil {
			return err
		}
	}
	where := `kind='user_text' AND sidechain=0 AND length(text) BETWEEN 30 AND 600`
	args := []any{}
	if opt.Window > 0 && !opt.Now.IsZero() {
		where += ` AND ts >= ?`
		args = append(args, store.Ms(opt.Now.Add(-opt.Window)))
	}
	// Turns already explained by a correction-type signal are not
	// "instructions"; those cluster on their own.
	where += ` AND id NOT IN (SELECT key FROM signals WHERE type IN ('correction','interrupt','rewind'))`
	rows, err := st.DB.Query(`SELECT id, session_id, ts, text, file_id, file_offset, embedding FROM turns WHERE `+where+` ORDER BY ts, id`, args...)
	if err != nil {
		return err
	}
	type item struct {
		t   store.Turn
		vec []float32
	}
	var items []item
	for rows.Next() {
		var it item
		var ts int64
		var emb []byte
		if err := rows.Scan(&it.t.ID, &it.t.SessionID, &ts, &it.t.Text, &it.t.FileID, &it.t.FileOffset, &emb); err != nil {
			rows.Close()
			return err
		}
		it.t.TS = store.FromMs(ts)
		if len(textutil.Tokens(it.t.Text)) < 5 || !guidanceRe.MatchString(it.t.Text) {
			continue
		}
		it.vec = store.DecodeVec(emb)
		items = append(items, it)
	}
	rows.Close()

	var missing []int
	for i, it := range items {
		if it.vec == nil {
			missing = append(missing, i)
		}
	}
	if len(missing) > 0 {
		tx, err := st.DB.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()
		for start := 0; start < len(missing); start += 256 {
			batch := missing[start:min(start+256, len(missing))]
			texts := make([]string, len(batch))
			for k, i := range batch {
				texts[k] = items[i].t.Text
			}
			vecs, err := opt.Embedder.Embed(texts)
			if err != nil {
				return err
			}
			for k, i := range batch {
				items[i].vec = vecs[k]
				if _, err := tx.Exec(`UPDATE turns SET embedding=? WHERE id=?`, store.EncodeVec(vecs[k]), items[i].t.ID); err != nil {
					return err
				}
			}
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}

	vecs := make([][]float32, len(items))
	for i := range items {
		vecs[i] = items[i].vec
	}
	// Near-duplicate is stricter than "same rule".
	thr := min(opt.Embedder.Threshold()+0.15, 0.92)
	assign, _ := embed.Greedy(vecs, thr)
	groups := map[int][]int{}
	for i, c := range assign {
		groups[c] = append(groups[c], i)
	}

	var sigs []store.Signal
	for _, members := range groups {
		perSession := map[string]int{}
		for _, i := range members {
			if _, ok := perSession[items[i].t.SessionID]; !ok {
				perSession[items[i].t.SessionID] = i
			}
		}
		if len(perSession) < opt.MinSessions {
			continue
		}
		for sid, i := range perSession {
			t := items[i].t
			sigs = append(sigs, store.Signal{
				ID:         textutil.Hash(20, store.RepeatedInstruction, t.ID),
				SessionID:  sid,
				Repo:       sessions[sid].Repo,
				Type:       store.RepeatedInstruction,
				Weight:     store.High,
				TS:         t.TS,
				Excerpt:    t.Text,
				Context:    fmt.Sprintf("Similar instruction given in %d sessions", len(perSession)),
				Key:        t.ID,
				FileID:     t.FileID,
				FileOffset: t.FileOffset,
			})
		}
	}

	tx, err := st.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	keep := map[string]bool{}
	for _, s := range sigs {
		keep[s.ID] = true
		if err := upsertSignal(tx, s); err != nil {
			return err
		}
	}
	old, err := tx.Query(`SELECT id FROM signals WHERE type=?`, store.RepeatedInstruction)
	if err != nil {
		return err
	}
	var stale []string
	for old.Next() {
		var id string
		if err := old.Scan(&id); err != nil {
			old.Close()
			return err
		}
		if !keep[id] {
			stale = append(stale, id)
		}
	}
	old.Close()
	if len(stale) > 0 {
		q := `DELETE FROM signals WHERE id IN (?` + strings.Repeat(",?", len(stale)-1) + `)`
		a := make([]any, len(stale))
		for i, id := range stale {
			a[i] = id
		}
		if _, err := tx.Exec(q, a...); err != nil {
			return err
		}
	}
	return tx.Commit()
}
