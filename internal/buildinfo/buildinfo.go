package buildinfo

import (
	"fmt"
	"runtime/debug"
	"strings"
)

// Version is the application version reported by the CLI. Tagged release
// builds inject the tag value to keep the artifact and source version aligned.
var Version = "v1.0.0"

// Revision and BuildDate are injected by the release build. Development builds
// fall back to Go's VCS metadata.
var (
	Revision  = ""
	BuildDate = ""
)

// String returns the configured version plus Go's embedded Git revision and
// dirty-worktree marker when VCS build information is available.
func String() string {
	if Revision != "" {
		revision := Revision
		if len(revision) > 7 {
			revision = revision[:7]
		}
		return fmt.Sprintf("%s (%s)", Version, revision)
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return Version
	}
	return format(Version, info.Settings)
}

func format(version string, settings []debug.BuildSetting) string {
	revision := ""
	modified := false
	for _, setting := range settings {
		switch setting.Key {
		case "vcs.revision":
			revision = setting.Value
		case "vcs.modified":
			modified = setting.Value == "true"
		}
	}
	if len(revision) > 7 {
		revision = revision[:7]
	}
	parts := []string{}
	if revision != "" {
		parts = append(parts, revision)
	}
	if modified {
		parts = append(parts, "dirty")
	}
	if len(parts) == 0 {
		return version
	}
	return fmt.Sprintf("%s (%s)", version, strings.Join(parts, ", "))
}
