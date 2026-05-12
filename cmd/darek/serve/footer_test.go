package serve

import (
	"runtime/debug"
	"testing"
)

func TestPickVersion(t *testing.T) {
	tests := []struct {
		name string
		info *debug.BuildInfo
		want string
	}{
		{
			name: "module version present",
			info: &debug.BuildInfo{Main: debug.Module{Version: "v1.2.3"}},
			want: "v1.2.3",
		},
		{
			name: "module version devel falls back to vcs",
			info: &debug.BuildInfo{
				Main: debug.Module{Version: "(devel)"},
				Settings: []debug.BuildSetting{
					{Key: "vcs.revision", Value: "abcdef1234567890"},
				},
			},
			want: "abcdef1",
		},
		{
			name: "no module, no vcs, returns dev",
			info: &debug.BuildInfo{Main: debug.Module{Version: ""}},
			want: "dev",
		},
		{
			name: "nil build info returns dev",
			info: nil,
			want: "dev",
		},
		{
			name: "short vcs revision falls back to dev",
			info: &debug.BuildInfo{
				Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "abc"}},
			},
			want: "dev",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := pickVersion(tc.info)
			if got != tc.want {
				t.Errorf("pickVersion = %q, want %q", got, tc.want)
			}
		})
	}
}
