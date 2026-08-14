package main

import (
	"runtime/debug"
	"testing"
)

// `go install ...@latest` builds without the release ldflags, so the
// stamped version stays "dev". Reporting that is not cosmetic: the
// updater compares versions numerically, "dev" parses as 0.0.0, and
// every `bwg update --check` then announces an update that is already
// installed. Go knows the module version in that case — ask it.
func TestResolveVersion(t *testing.T) {
	info := func(v string) *debug.BuildInfo {
		bi := &debug.BuildInfo{}
		bi.Main.Version = v
		return bi
	}

	cases := []struct {
		name    string
		stamped string
		bi      *debug.BuildInfo
		ok      bool
		want    string
	}{
		{"a release binary trusts its stamp", "v0.2.0", info("v0.1.0"), true, "v0.2.0"},
		{"go install falls back to the module version", "dev", info("v0.2.0"), true, "v0.2.0"},
		{"a pseudo-version is still a version", "dev",
			info("v0.2.1-0.20260814060512-c831d5d3f2a1"), true,
			"v0.2.1-0.20260814060512-c831d5d3f2a1"},
		{"a local build stays dev", "dev", info("(devel)"), true, "dev"},
		{"no build info stays dev", "dev", nil, false, "dev"},
		{"empty module version stays dev", "dev", info(""), true, "dev"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := resolveVersion(c.stamped, c.bi, c.ok); got != c.want {
				t.Errorf("resolveVersion(%q, %v) = %q, want %q", c.stamped, c.bi, got, c.want)
			}
		})
	}
}

// Whatever the build, the version must be something printable — an
// empty `bwg version` is a bug report nobody can act on.
func TestBuildVersionIsNeverEmpty(t *testing.T) {
	if buildVersion() == "" {
		t.Error("buildVersion() returned an empty string")
	}
}
