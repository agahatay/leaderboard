package main

import (
	"runtime/debug"
	"testing"
)

func TestBuildCommit(t *testing.T) {
	vcs := func(kv ...string) *debug.BuildInfo {
		info := &debug.BuildInfo{}
		for i := 0; i < len(kv); i += 2 {
			info.Settings = append(info.Settings, debug.BuildSetting{Key: kv[i], Value: kv[i+1]})
		}
		return info
	}
	env := func(v string) func(string) string {
		return func(k string) string {
			if k == "RENDER_GIT_COMMIT" {
				return v
			}
			return ""
		}
	}

	tests := []struct {
		name    string
		ldflags string
		render  string
		info    *debug.BuildInfo
		want    string
	}{
		{"ldflags wins", "aaa", "bbb", vcs("vcs.revision", "ccc"), "aaa"},
		{"render env next", "", "bbb", vcs("vcs.revision", "ccc"), "bbb"},
		{"vcs stamp", "", "", vcs("vcs.revision", "ccc", "vcs.modified", "false"), "ccc"},
		{"vcs stamp, dirty tree", "", "", vcs("vcs.revision", "ccc", "vcs.modified", "true"), "ccc-dirty"},
		{"no vcs stamp", "", "", vcs("GOOS", "linux"), "unknown"},
		{"no build info", "", "", nil, "unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			old := commit
			commit = tt.ldflags
			t.Cleanup(func() { commit = old })

			if got := buildCommit(env(tt.render), tt.info); got != tt.want {
				t.Errorf("buildCommit = %q, want %q", got, tt.want)
			}
		})
	}
}
