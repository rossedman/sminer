package version

import (
	"runtime"
	"testing"
)

func TestGet(t *testing.T) {
	// Not parallel: mutates package variables.
	oldV, oldC := Version, Commit
	t.Cleanup(func() { Version, Commit = oldV, oldC })

	Version, Commit = "v1.2.3", "abcdef"
	got := Get()
	want := Info{Version: "v1.2.3", Commit: "abcdef", Go: runtime.Version(), OS: runtime.GOOS, Arch: runtime.GOARCH}
	if got != want {
		t.Errorf("Get() = %+v, want %+v", got, want)
	}

	// Without ldflags the version is never empty (module data or "dev").
	Version, Commit = "", ""
	got = Get()
	if got.Version == "" || got.Version == "(devel)" {
		t.Errorf("fallback version = %q", got.Version)
	}
	if got.Commit != "" && len(got.Commit) != 12 {
		t.Errorf("fallback commit %q should be 12 chars", got.Commit)
	}
}
