// Package version exposes build metadata injected at link time.
package version

import (
	"runtime"
	"runtime/debug"
)

// These variables are overridden at build time via -ldflags, for example:
//
//	-X github.com/sudharsanomprakash/kubevirt-netdoctor/internal/version.Version=v0.1.0
var (
	Version = "dev"
	Commit  = "unknown"
	Date    = "unknown"
)

// Info is the structured form of the build metadata.
type Info struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	Date      string `json:"date"`
	GoVersion string `json:"goVersion"`
	Platform  string `json:"platform"`
}

// Get returns the build metadata of the running binary. When -ldflags were
// not used (for example "go build" or "go install"), the commit and date fall
// back to the VCS information the Go toolchain embeds in the binary.
func Get() Info {
	info := Info{
		Version:   Version,
		Commit:    Commit,
		Date:      Date,
		GoVersion: runtime.Version(),
		Platform:  runtime.GOOS + "/" + runtime.GOARCH,
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		applyBuildInfo(&info, bi)
	}
	return info
}

func applyBuildInfo(info *Info, bi *debug.BuildInfo) {
	if info.Version == "dev" && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		info.Version = bi.Main.Version // set by "go install module@version"
	}
	var revision, modified string
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			revision = s.Value
		case "vcs.time":
			if info.Date == "unknown" {
				info.Date = s.Value
			}
		case "vcs.modified":
			modified = s.Value
		}
	}
	if info.Commit == "unknown" && revision != "" {
		if len(revision) > 12 {
			revision = revision[:12]
		}
		if modified == "true" {
			revision += "-dirty"
		}
		info.Commit = revision
	}
}
