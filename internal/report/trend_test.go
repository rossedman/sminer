package report

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rossedman/sminer/internal/cluster"
	"github.com/rossedman/sminer/internal/config"
	"github.com/rossedman/sminer/internal/embed"
	"github.com/rossedman/sminer/internal/ingest"
	"github.com/rossedman/sminer/internal/redact"
	"github.com/rossedman/sminer/internal/remedy"
	"github.com/rossedman/sminer/internal/signal"
	"github.com/rossedman/sminer/internal/store"
)

func TestWeekStart(t *testing.T) {
	t.Parallel()
	monday := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
	for _, in := range []time.Time{
		monday, monday.Add(13 * time.Hour), monday.AddDate(0, 0, 3), monday.AddDate(0, 0, 6).Add(23 * time.Hour),
	} {
		if got := WeekStart(in); !got.Equal(monday) {
			t.Errorf("WeekStart(%v) = %v, want %v", in, got, monday)
		}
	}
	if got := WeekStart(monday.Add(-time.Second)); !got.Equal(monday.AddDate(0, 0, -7)) {
		t.Errorf("previous Sunday belongs to the previous week, got %v", got)
	}
	// A non-UTC zone is converted first.
	la := time.FixedZone("x", -8*3600)
	if got := WeekStart(time.Date(2026, 10, 18, 20, 0, 0, 0, la)); !got.Equal(monday) {
		t.Errorf("zone handling: %v", got)
	}
}

func TestSparkline(t *testing.T) {
	t.Parallel()
	if got := Sparkline(nil); got != "" {
		t.Errorf("empty = %q", got)
	}
	if got := Sparkline([]Week{{Count: 0}, {Count: 0}}); got != "  " {
		t.Errorf("all zero = %q", got)
	}
	got := []rune(Sparkline([]Week{{Count: 0}, {Count: 1}, {Count: 4}, {Count: 8}}))
	if len(got) != 4 || got[0] != ' ' || got[3] != '█' || !(got[1] < got[2] && got[2] < got[3]) {
		t.Errorf("sparkline = %q", string(got))
	}
	// Any non-zero count is visible.
	if one := []rune(Sparkline([]Week{{Count: 1}, {Count: 1000}})); one[0] == ' ' {
		t.Error("a count of 1 must not render as blank")
	}
}

func seedTrend(t testing.TB, st *store.Store, adopted *time.Time) {
	t.Helper()
	if err := st.SaveFinding(store.Finding{ID: "T1", Title: "Trend me", Status: store.StatusOpen}); err != nil {
		t.Fatal(err)
	}
	// 3 signals in the week of Sep 21, 1 in the week of Oct 12.
	for i, d := range []time.Time{
		time.Date(2026, 9, 21, 10, 0, 0, 0, time.UTC), time.Date(2026, 9, 22, 10, 0, 0, 0, time.UTC),
		time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC), time.Date(2026, 10, 13, 10, 0, 0, 0, time.UTC),
	} {
		id := fmt.Sprintf("t%d", i)
		exec(t, st, `INSERT INTO signals(id, session_id, type, weight, ts) VALUES(?, 's', 'correction', 3, ?)`, id, store.Ms(d))
		exec(t, st, `INSERT INTO finding_signals VALUES('T1', ?)`, id)
	}
	if adopted != nil {
		exec(t, st, `INSERT INTO adoptions(finding_id, adopted_at) VALUES('T1', ?)`, store.Ms(*adopted))
	}
}

func TestComputeTrendUnadopted(t *testing.T) {
	t.Parallel()
	st := newStore(t)
	seedTrend(t, st, nil)
	tr, err := ComputeTrend(st, "T1", now)
	if err != nil {
		t.Fatal(err)
	}
	if tr.ID != "T1" || tr.Title != "Trend me" || tr.AdoptedAt != nil {
		t.Errorf("trend = %+v", tr)
	}
	// Sep 21 through Oct 19 is five weekly buckets.
	if len(tr.Weeks) != 5 {
		t.Fatalf("weeks = %d", len(tr.Weeks))
	}
	counts := []int{}
	for _, w := range tr.Weeks {
		counts = append(counts, w.Count)
	}
	if fmt.Sprint(counts) != "[3 0 0 1 0]" {
		t.Errorf("counts = %v", counts)
	}
	if !tr.Weeks[0].Start.Equal(time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("first week = %v", tr.Weeks[0].Start)
	}
	wantRate := 4 / (now.Sub(time.Date(2026, 9, 21, 10, 0, 0, 0, time.UTC)).Hours() / 168)
	if d := tr.BeforeRate - wantRate; d > 1e-9 || d < -1e-9 {
		t.Errorf("BeforeRate = %v, want %v", tr.BeforeRate, wantRate)
	}
	if tr.AfterRate != 0 || tr.AfterWeeks != 0 {
		t.Errorf("after = %v %v", tr.AfterRate, tr.AfterWeeks)
	}
}

func TestComputeTrendAdopted(t *testing.T) {
	t.Parallel()
	st := newStore(t)
	adopted := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	seedTrend(t, st, &adopted)
	tr, err := ComputeTrend(st, "T1", now)
	if err != nil {
		t.Fatal(err)
	}
	if tr.AdoptedAt == nil || !tr.AdoptedAt.Equal(adopted) {
		t.Fatalf("AdoptedAt = %v", tr.AdoptedAt)
	}
	first := time.Date(2026, 9, 21, 10, 0, 0, 0, time.UTC)
	wantBefore := 3 / (adopted.Sub(first).Hours() / 168)
	wantAfterWeeks := now.Sub(adopted).Hours() / 168
	if d := tr.BeforeRate - wantBefore; d > 1e-9 || d < -1e-9 {
		t.Errorf("BeforeRate = %v, want %v", tr.BeforeRate, wantBefore)
	}
	if d := tr.AfterWeeks - wantAfterWeeks; d > 1e-9 || d < -1e-9 {
		t.Errorf("AfterWeeks = %v, want %v", tr.AfterWeeks, wantAfterWeeks)
	}
	if d := tr.AfterRate - 1/wantAfterWeeks; d > 1e-9 || d < -1e-9 {
		t.Errorf("AfterRate = %v, want %v", tr.AfterRate, 1/wantAfterWeeks)
	}
}

func TestComputeTrendEdges(t *testing.T) {
	t.Parallel()
	st := newStore(t)
	if _, err := ComputeTrend(st, "missing", now); err == nil {
		t.Error("unknown finding should fail")
	}
	if err := st.SaveFinding(store.Finding{ID: "E", Title: "no signals"}); err != nil {
		t.Fatal(err)
	}
	tr, err := ComputeTrend(st, "E", now)
	if err != nil || len(tr.Weeks) != 0 || tr.BeforeRate != 0 {
		t.Errorf("empty trend = %+v %v", tr, err)
	}
	exec(t, st, `DROP TABLE finding_signals`)
	if _, err := ComputeTrend(st, "E", now); err == nil {
		t.Error("missing table should fail")
	}
	// weeksBetween never drops below one so rates stay finite.
	if weeksBetween(now, now) != 1 || weeksBetween(now, now.Add(14*24*time.Hour)) != 2 {
		t.Error("weeksBetween")
	}
}

func TestWriteTrendOutput(t *testing.T) {
	t.Parallel()
	st := newStore(t)
	adopted := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	seedTrend(t, st, &adopted)
	tr, _ := ComputeTrend(st, "T1", now)
	var b bytes.Buffer
	WriteTrend(&b, tr)
	out := b.String()
	for _, want := range []string{"T1  Trend me", "2026-09-21    3 ###", "2026-10-05    0   <- adopted 2026-10-05", "Before adoption: ", "weeks ("} {
		if !strings.Contains(out, want) {
			t.Errorf("WriteTrend missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "%)") {
		t.Errorf("percent change missing:\n%s", out)
	}

	un := tr
	un.AdoptedAt = nil
	b.Reset()
	WriteTrend(&b, un)
	if !strings.Contains(b.String(), "Not adopted. Rate: ") || strings.Contains(b.String(), "adopted 2026") {
		t.Errorf("unadopted:\n%s", b.String())
	}

	zero := tr
	zero.BeforeRate = 0
	b.Reset()
	WriteTrend(&b, zero)
	if strings.Contains(b.String(), "%)") {
		t.Errorf("no percent when there was no baseline:\n%s", b.String())
	}

	long := Trend{ID: "L", Title: "long", AdoptedAt: &adopted, Weeks: make([]Week, 30)}
	b.Reset()
	WriteTrendTable(&b, []Trend{tr, un, long})
	lines := strings.Split(strings.TrimSpace(b.String()), "\n")
	if len(lines) != 4 || !strings.HasPrefix(lines[0], "ID") {
		t.Fatalf("table:\n%s", b.String())
	}
	if !strings.Contains(lines[1], "T1") || strings.Fields(lines[2])[2] != "-" {
		t.Errorf("rows:\n%s", b.String())
	}
	if n := len([]rune(strings.Fields(lines[3])[3])); n > 24 {
		t.Errorf("sparkline not capped at 24 weeks: %d", n)
	}
}

// ---- corpus-backed store for benchmarks ----

func corpusStore(t testing.TB) *store.Store {
	t.Helper()
	st := newStore(t)
	red, err := redact.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	cfg := config.Default()
	dir := t.TempDir()
	cfg.Remedies = config.Remedies{HooksDir: filepath.Join(dir, "h"), GlobalClaudeMD: filepath.Join(dir, "CLAUDE.md"),
		SkillsDir: filepath.Join(dir, "s"), Settings: filepath.Join(dir, "settings.json")}
	if _, err := ingest.Run(ctx, st, ingest.Options{Roots: []string{filepath.Join("..", "..", "testdata", "projects")},
		Since: 3650 * 24 * time.Hour, Now: now, Config: cfg, Redact: red}); err != nil {
		t.Fatal(err)
	}
	if _, err := signal.Run(ctx, st, signal.Options{Embedder: embed.Hash{}, Now: now}); err != nil {
		t.Fatal(err)
	}
	cs, err := cluster.Run(st, cluster.Options{Embedder: embed.Hash{}, MinCount: 1, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := remedy.Run(ctx, st, cs.Findings, remedy.Options{Config: cfg, Existing: remedy.LoadExisting(cfg)}); err != nil {
		t.Fatal(err)
	}
	return st
}

func TestBuildOnCorpus(t *testing.T) {
	t.Parallel()
	st := corpusStore(t)
	r, err := Build(st, Options{Since: now.Add(-3650 * 24 * time.Hour), Now: now, MinCount: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Findings) == 0 || r.Sessions == 0 || len(r.SignalCounts) == 0 {
		t.Fatalf("report = %+v", r)
	}
	var b bytes.Buffer
	if err := WriteMarkdown(&b, r); err != nil || !strings.Contains(b.String(), "## Findings (") {
		t.Errorf("markdown: %v", err)
	}
}

func BenchmarkBuild(b *testing.B) {
	st := corpusStore(b)
	opt := Options{Since: now.Add(-3650 * 24 * time.Hour), Now: now, MinCount: 1}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := Build(st, opt); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkWriteMarkdown(b *testing.B) {
	st := corpusStore(b)
	r, err := Build(st, Options{Since: now.Add(-3650 * 24 * time.Hour), Now: now, MinCount: 1})
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		if err := WriteMarkdown(&bytes.Buffer{}, r); err != nil {
			b.Fatal(err)
		}
	}
}
