package report

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/rossedman/sminer/internal/store"
)

// Week is one bucket of a trend.
type Week struct {
	Start time.Time `json:"start"`
	Count int       `json:"count"`
}

// Trend is a finding's weekly rate, split at adoption when there is one.
type Trend struct {
	ID         string     `json:"id"`
	Title      string     `json:"title"`
	Weeks      []Week     `json:"weeks"`
	AdoptedAt  *time.Time `json:"adopted_at,omitempty"`
	BeforeRate float64    `json:"before_per_week"` // occurrences per week before adoption (or overall)
	AfterRate  float64    `json:"after_per_week"`
	AfterWeeks float64    `json:"after_weeks"`
}

func WeekStart(t time.Time) time.Time {
	t = t.UTC().Truncate(24 * time.Hour)
	wd := (int(t.Weekday()) + 6) % 7 // Monday = 0
	return t.AddDate(0, 0, -wd)
}

// ComputeTrend buckets all of a finding's signals by week, through now.
func ComputeTrend(st *store.Store, id string, now time.Time) (Trend, error) {
	f, err := st.GetFinding(id)
	if err != nil {
		return Trend{}, err
	}
	sigs, err := st.FindingSignals(id)
	if err != nil {
		return Trend{}, err
	}
	tr := Trend{ID: f.ID, Title: f.Title}
	if at, ok := adoptedAt(st, id); ok {
		tr.AdoptedAt = &at
	}
	if len(sigs) == 0 {
		return tr, nil
	}
	start := WeekStart(sigs[0].TS)
	end := WeekStart(now)
	idx := map[time.Time]int{}
	for w := start; !w.After(end); w = w.AddDate(0, 0, 7) {
		idx[w] = len(tr.Weeks)
		tr.Weeks = append(tr.Weeks, Week{Start: w})
	}
	var before, after int
	for _, s := range sigs {
		if i, ok := idx[WeekStart(s.TS)]; ok {
			tr.Weeks[i].Count++
		}
		if tr.AdoptedAt != nil && !s.TS.Before(*tr.AdoptedAt) {
			after++
		} else {
			before++
		}
	}
	first := sigs[0].TS
	if tr.AdoptedAt == nil {
		tr.BeforeRate = float64(before) / weeksBetween(first, now)
		return tr, nil
	}
	tr.BeforeRate = float64(before) / weeksBetween(first, *tr.AdoptedAt)
	tr.AfterWeeks = weeksBetween(*tr.AdoptedAt, now)
	tr.AfterRate = float64(after) / tr.AfterWeeks
	return tr, nil
}

func weeksBetween(a, b time.Time) float64 {
	w := b.Sub(a).Hours() / (24 * 7)
	if w < 1 {
		return 1
	}
	return w
}

var bars = []rune(" ▁▂▃▄▅▆▇█")

// Sparkline renders weekly counts as block characters.
func Sparkline(ws []Week) string {
	mx := 0
	for _, w := range ws {
		mx = max(mx, w.Count)
	}
	var sb strings.Builder
	for _, w := range ws {
		if mx == 0 {
			sb.WriteRune(bars[0])
			continue
		}
		sb.WriteRune(bars[(w.Count*(len(bars)-1)+mx-1)/mx])
	}
	return sb.String()
}

// WriteTrend prints one finding's trend in detail.
func WriteTrend(w io.Writer, tr Trend) {
	fmt.Fprintf(w, "%s  %s\n\n", tr.ID, tr.Title)
	for _, wk := range tr.Weeks {
		mark := ""
		if tr.AdoptedAt != nil && WeekStart(*tr.AdoptedAt).Equal(wk.Start) {
			mark = "  <- adopted " + tr.AdoptedAt.Format("2006-01-02")
		}
		fmt.Fprintf(w, "  %s  %3d %s%s\n", wk.Start.Format("2006-01-02"), wk.Count, strings.Repeat("#", wk.Count), mark)
	}
	fmt.Fprintln(w)
	if tr.AdoptedAt == nil {
		fmt.Fprintf(w, "Not adopted. Rate: %.2f/week.\n", tr.BeforeRate)
		return
	}
	change := ""
	if tr.BeforeRate > 0 {
		change = fmt.Sprintf(" (%+.0f%%)", (tr.AfterRate-tr.BeforeRate)/tr.BeforeRate*100)
	}
	fmt.Fprintf(w, "Before adoption: %.2f/week. After: %.2f/week over %.1f weeks%s.\n", tr.BeforeRate, tr.AfterRate, tr.AfterWeeks, change)
}

// WriteTrendTable prints a one-line trend per finding.
func WriteTrendTable(w io.Writer, trs []Trend) {
	fmt.Fprintf(w, "%-10s %-9s %-9s %-24s %s\n", "ID", "before/wk", "after/wk", "weekly", "title")
	for _, tr := range trs {
		after := "-"
		if tr.AdoptedAt != nil {
			after = fmt.Sprintf("%.2f", tr.AfterRate)
		}
		ws := tr.Weeks
		if len(ws) > 24 {
			ws = ws[len(ws)-24:]
		}
		fmt.Fprintf(w, "%-10s %-9.2f %-9s %-24s %s\n", tr.ID, tr.BeforeRate, after, Sparkline(ws), tr.Title)
	}
}
