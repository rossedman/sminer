// Package finding manages the lifecycle of findings after analysis: listing,
// inspecting, accepting (writing a patch), dismissing and reopening them.
package finding

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/rossedman/sminer/internal/config"
	"github.com/rossedman/sminer/internal/fsutil"
	"github.com/rossedman/sminer/internal/remedy"
	"github.com/rossedman/sminer/internal/report"
	"github.com/rossedman/sminer/internal/store"
)

// Service operates on findings in one database.
type Service struct {
	Store  *store.Store
	Config config.Config
	Now    time.Time
}

// Statuses accepted by List. "all" and "" mean every status.
var Statuses = []string{store.StatusOpen, store.StatusAccepted, store.StatusDismissed, "all"}

// List returns findings with the given status, ranked by score. Counts cover
// every linked signal, not just the analysis window. Open findings with no
// signals left are skipped.
func (s Service) List(status string) ([]report.Finding, error) {
	where, args := "", []any{}
	switch status {
	case "", "all":
	case store.StatusOpen, store.StatusAccepted, store.StatusDismissed:
		where, args = "status = ?", []any{status}
	default:
		return nil, fmt.Errorf("unknown status %q (want one of open, accepted, dismissed, all)", status)
	}
	fs, err := s.Store.QueryFindings(where, args...)
	if err != nil {
		return nil, err
	}
	sessions, err := s.Store.Sessions()
	if err != nil {
		return nil, err
	}
	out := make([]report.Finding, 0, len(fs))
	for _, f := range fs {
		v, err := s.view(f, sessions, 0)
		if err != nil {
			return nil, err
		}
		// An open finding whose signals were all re-filed or filtered out
		// by a later analysis has nothing left to act on.
		if v.Occurrences == 0 && f.Status == store.StatusOpen {
			continue
		}
		out = append(out, v)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// Get returns one finding with every excerpt.
func (s Service) Get(id string) (report.Finding, error) {
	f, err := s.Store.GetFinding(id)
	if err != nil {
		return report.Finding{}, err
	}
	sessions, err := s.Store.Sessions()
	if err != nil {
		return report.Finding{}, err
	}
	return s.view(f, sessions, -1)
}

// IDs returns every finding id, for shell completion.
func (s Service) IDs(status string) ([]string, error) {
	fs, err := s.List(status)
	if err != nil {
		return nil, err
	}
	ids := make([]string, len(fs))
	for i, f := range fs {
		ids[i] = f.ID + "\t" + f.Title
	}
	return ids, nil
}

// view builds the report view; maxExcerpts 0 means the default, -1 all.
func (s Service) view(f store.Finding, sessions map[string]store.Session, maxExcerpts int) (report.Finding, error) {
	sigs, err := s.Store.FindingSignals(f.ID)
	if err != nil {
		return report.Finding{}, err
	}
	n := maxExcerpts
	switch {
	case n < 0:
		n = len(sigs)
	case n == 0:
		n = 3
	}
	v := report.View(f, sigs, sessions, n)
	if at, ok := s.adoptedAt(f.ID); ok {
		v.AdoptedAt = &at
	}
	return v, nil
}

// adoptedAt returns when the finding was adopted, or false if it has no
// adoption record.
func (s Service) adoptedAt(id string) (time.Time, bool) {
	var ms int64
	if err := s.Store.DB.QueryRow(`SELECT adopted_at FROM adoptions WHERE finding_id=?`, id).Scan(&ms); err != nil {
		return time.Time{}, false
	}
	return store.FromMs(ms), true
}

// AcceptResult describes an accepted (or previewed) finding.
type AcceptResult struct {
	ID        string `json:"id"`
	Remedy    string `json:"remedy"`
	Target    string `json:"target"`
	PatchPath string `json:"patch_path,omitempty"`
	ApplyDir  string `json:"apply_dir"`
	Apply     string `json:"apply"`
	FollowUp  string `json:"follow_up,omitempty"`
	Diff      string `json:"diff"`
	Preview   bool   `json:"preview"`
	// Combined lists the other findings sharing this result's patch.
	Combined []string `json:"combined_with,omitempty"`
}

// Accept renders the finding's patch against its target as it is now. With
// preview it changes nothing; otherwise it writes the patch file atomically
// under dir (the configured patch_dir when empty) and records the adoption.
func (s Service) Accept(id, dir string, preview bool) (AcceptResult, error) {
	res, err := s.AcceptMany([]string{id}, dir, preview)
	if err != nil {
		return AcceptResult{}, err
	}
	return res[0], nil
}

// AcceptMany accepts several findings at once. CLAUDE.md lines for the same
// file share one patch, named after their ids joined by "+", so they apply
// together instead of conflicting. Every patch is built before anything is
// written: if one finding can't be patched, nothing changes.
func (s Service) AcceptMany(ids []string, dir string, preview bool) ([]AcceptResult, error) {
	var fs []store.Finding
	seen := map[string]bool{}
	for _, id := range ids {
		f, err := s.Store.GetFinding(id)
		if err != nil {
			return nil, err
		}
		if !seen[f.ID] {
			seen[f.ID] = true
			fs = append(fs, f)
		}
	}

	// Group CLAUDE.md lines by target, in the order given.
	var groups [][]store.Finding
	byTarget := map[string]int{}
	for _, f := range fs {
		if f.RemedyType == remedy.ClaudeMD && f.Target != "" {
			if i, ok := byTarget[f.Target]; ok {
				groups[i] = append(groups[i], f)
				continue
			}
			byTarget[f.Target] = len(groups)
		}
		groups = append(groups, []store.Finding{f})
	}

	if dir == "" {
		dir = config.Expand(s.Config.PatchDir)
	}
	type built struct {
		path string
		diff string
	}
	var results []AcceptResult
	var files []built
	for _, g := range groups {
		var p remedy.Patch
		var err error
		if len(g) > 1 {
			p, err = remedy.ClaudeMDPatch(g)
		} else {
			p, err = remedy.BuildPatch(g[0], s.Config)
		}
		if err != nil {
			return nil, err
		}
		gids := make([]string, len(g))
		for i, f := range g {
			gids[i] = f.ID
		}
		path := filepath.Join(dir, strings.Join(gids, "+")+".patch")
		apply := fmt.Sprintf("cd %s && patch -p1 < %s", p.Base, path)
		if preview {
			path, apply = "", fmt.Sprintf("cd %s && patch -p1", p.Base)
		} else {
			files = append(files, built{path, p.Diff})
		}
		for _, f := range g {
			res := AcceptResult{ID: f.ID, Remedy: f.RemedyType, Target: f.Target, PatchPath: path, ApplyDir: p.Base,
				Apply: apply, FollowUp: p.Extra, Diff: p.Diff, Preview: preview}
			for _, o := range gids {
				if o != f.ID {
					res.Combined = append(res.Combined, o)
				}
			}
			results = append(results, res)
		}
	}
	if preview {
		return results, nil
	}

	for _, b := range files {
		if err := fsutil.WriteFileAtomic(b.path, []byte(b.diff), 0o600); err != nil {
			return nil, err
		}
	}
	tx, err := s.Store.DB.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	for _, f := range fs {
		f.Status, f.Reason = store.StatusAccepted, ""
		if err := store.SaveFinding(tx, f); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(`INSERT INTO adoptions(finding_id, adopted_at, patch_path) VALUES(?,?,?)
ON CONFLICT(finding_id) DO UPDATE SET adopted_at=excluded.adopted_at, patch_path=excluded.patch_path`,
			f.ID, store.Ms(s.Now), patchFor(results, f.ID)); err != nil {
			return nil, err
		}
	}
	return results, tx.Commit()
}

// patchFor returns the patch path for the finding with the given id, or an
// empty string if results has none.
func patchFor(results []AcceptResult, id string) string {
	for _, r := range results {
		if r.ID == id {
			return r.PatchPath
		}
	}
	return ""
}

// Dismiss marks a finding as not worth fixing. It stays out of reports
// until reopened.
func (s Service) Dismiss(id, reason string) error {
	return s.setStatus(id, store.StatusDismissed, reason)
}

// Reopen undoes accept or dismiss. An accepted finding's adoption date is
// dropped, so its trend no longer splits at that date; the patch file is
// left where it is.
func (s Service) Reopen(id string) error {
	if err := s.setStatus(id, store.StatusOpen, ""); err != nil {
		return err
	}
	_, err := s.Store.DB.Exec(`DELETE FROM adoptions WHERE finding_id=?`, id)
	return err
}

// setStatus sets the status and reason of the stored finding id and saves it.
// It returns the error from the store if the finding does not exist.
func (s Service) setStatus(id, status, reason string) error {
	f, err := s.Store.GetFinding(id)
	if err != nil {
		return err
	}
	f.Status, f.Reason = status, reason
	return s.Store.SaveFinding(f)
}

// Trend returns one finding's weekly trend.
func (s Service) Trend(id string) (report.Trend, error) {
	return report.ComputeTrend(s.Store, id, s.Now)
}

// Trends returns trends for every finding that is not dismissed, highest
// rate first.
func (s Service) Trends() ([]report.Trend, error) {
	fs, err := s.Store.QueryFindings(`status <> ?`, store.StatusDismissed)
	if err != nil {
		return nil, err
	}
	trs := []report.Trend{}
	for _, f := range fs {
		tr, err := report.ComputeTrend(s.Store, f.ID, s.Now)
		if err != nil {
			return nil, err
		}
		if len(tr.Weeks) > 0 {
			trs = append(trs, tr)
		}
	}
	sort.SliceStable(trs, func(i, j int) bool { return trs[i].BeforeRate > trs[j].BeforeRate })
	return trs, nil
}
