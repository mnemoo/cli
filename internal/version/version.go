// Package version exposes build-time metadata about the stakecli binary.
//
// The variables below are overridden at build time via -ldflags, e.g.:
//
//	go build -ldflags "\
//	    -X github.com/mnemoo/cli/internal/version.Version=v1.2.3 \
//	    -X github.com/mnemoo/cli/internal/version.Commit=abcdef \
//	    -X github.com/mnemoo/cli/internal/version.Date=2026-04-09T12:00:00Z \
//	    -X github.com/mnemoo/cli/internal/version.BuiltBy=goreleaser" \
//	    ./cmd/stake
package version

import (
	"fmt"
	"runtime"
	"runtime/debug"
)

// These values are overridden at build time via -ldflags.
// When built with `go build` without ldflags, Version falls back to the
// module version embedded by the Go toolchain (if any), or "dev".
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
	BuiltBy = "source"
)

// Info is a machine-readable snapshot of build metadata.
type Info struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	Date      string `json:"date"`
	BuiltBy   string `json:"built_by"`
	GoVersion string `json:"go_version"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
}

// Get returns the current build metadata, resolving the Version from
// embedded debug info when ldflags were not applied.
func Get() Info {
	v := Version
	commit := Commit
	if v == "dev" {
		if bi, ok := debug.ReadBuildInfo(); ok {
			if bi.Main.Version != "" && bi.Main.Version != "(devel)" {
				v = bi.Main.Version
			}
			if commit == "none" {
				for _, s := range bi.Settings {
					if s.Key == "vcs.revision" && s.Value != "" {
						commit = s.Value
						break
					}
				}
			}
		}
	}
	return Info{
		Version:   v,
		Commit:    commit,
		Date:      Date,
		BuiltBy:   BuiltBy,
		GoVersion: runtime.Version(),
		OS:        runtime.GOOS,
		Arch:      runtime.GOARCH,
	}
}

// Short returns a one-line human version string like "v1.2.3 (abcdef)".
func Short() string {
	i := Get()
	if i.Commit != "none" && len(i.Commit) >= 7 {
		return fmt.Sprintf("%s (%s)", i.Version, i.Commit[:7])
	}
	return i.Version
}

// String returns a multi-line human-readable version report.
func String() string {
	i := Get()
	commit := i.Commit
	if len(commit) >= 12 {
		commit = commit[:12]
	}
	return fmt.Sprintf(
		"stakecli %s\n  commit:   %s\n  built:    %s\n  built by: %s\n  go:       %s\n  platform: %s/%s",
		i.Version, commit, i.Date, i.BuiltBy, i.GoVersion, i.OS, i.Arch,
	)
}
