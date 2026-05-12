package serve

import "runtime/debug"

// pickVersion picks a human-friendly version string from build info.
// Prefers info.Main.Version (when set and not "(devel)"), then the first 7
// chars of vcs.revision, then "dev".
//
// Split out from buildVersion so tests can drive the logic without
// monkey-patching debug.ReadBuildInfo.
func pickVersion(info *debug.BuildInfo) string {
	if info != nil {
		if v := info.Main.Version; v != "" && v != "(devel)" {
			return v
		}
		for _, s := range info.Settings {
			if s.Key == "vcs.revision" && len(s.Value) >= 7 {
				return s.Value[:7]
			}
		}
	}
	return "dev"
}

// buildVersion reads the running binary's build info and returns a short
// version string. Called once at server startup.
func buildVersion() string {
	info, _ := debug.ReadBuildInfo()
	return pickVersion(info)
}
