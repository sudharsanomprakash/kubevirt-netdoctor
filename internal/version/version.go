// Package version exposes build metadata injected at link time.
package version

import "runtime"

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

// Get returns the build metadata of the running binary.
func Get() Info {
	return Info{
		Version:   Version,
		Commit:    Commit,
		Date:      Date,
		GoVersion: runtime.Version(),
		Platform:  runtime.GOOS + "/" + runtime.GOARCH,
	}
}
