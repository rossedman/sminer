// Package version reports the build's version.
package version

import (
	"runtime"
	"runtime/debug"
)

// Set at build time:
//
//	go build -ldflags "-X github.com/rossedman/sminer/internal/version.Version=v0.2.0 -X github.com/rossedman/sminer/internal/version.Commit=abc123"
var (
	Version = ""
	Commit  = ""
)

// Info describes this binary.
type Info struct {
	Version string `json:"version"`
	Commit  string `json:"commit,omitempty"`
	Go      string `json:"go"`
	OS      string `json:"os"`
	Arch    string `json:"arch"`
}

// Get returns build information, falling back to module data that `go
// install` records when no ldflags were set.
func Get() Info {
	info := Info{Version: Version, Commit: Commit, Go: runtime.Version(), OS: runtime.GOOS, Arch: runtime.GOARCH}
	if bi, ok := debug.ReadBuildInfo(); ok {
		if info.Version == "" && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
			info.Version = bi.Main.Version
		}
		if info.Commit == "" {
			for _, s := range bi.Settings {
				if s.Key == "vcs.revision" && len(s.Value) >= 12 {
					info.Commit = s.Value[:12]
				}
			}
		}
	}
	if info.Version == "" {
		info.Version = "dev"
	}
	return info
}
