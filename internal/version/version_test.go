package version

import (
	"runtime/debug"
	"testing"
)

func TestApplyBuildInfoFallback(t *testing.T) {
	info := Info{Version: "dev", Commit: "unknown", Date: "unknown"}
	applyBuildInfo(&info, &debug.BuildInfo{
		Main: debug.Module{Version: "(devel)"},
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "0123456789abcdef0123"},
			{Key: "vcs.time", Value: "2026-01-01T00:00:00Z"},
			{Key: "vcs.modified", Value: "true"},
		},
	})
	if info.Version != "dev" || info.Commit != "0123456789ab-dirty" || info.Date != "2026-01-01T00:00:00Z" {
		t.Errorf("fallback: %+v", info)
	}

	info = Info{Version: "dev", Commit: "unknown", Date: "unknown"}
	applyBuildInfo(&info, &debug.BuildInfo{Main: debug.Module{Version: "v0.2.0"}})
	if info.Version != "v0.2.0" || info.Commit != "unknown" {
		t.Errorf("go install version: %+v", info)
	}

	// Values injected with -ldflags win.
	info = Info{Version: "v0.1.0", Commit: "abc1234", Date: "2026-02-02T00:00:00Z"}
	applyBuildInfo(&info, &debug.BuildInfo{Main: debug.Module{Version: "v9.9.9"},
		Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "ffffffffffffffff"}}})
	if info.Version != "v0.1.0" || info.Commit != "abc1234" {
		t.Errorf("ldflags must win: %+v", info)
	}
}
