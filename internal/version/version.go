// Package version exposes the build-time version metadata for both the binary
// CLI and the admin console. The three values are injected at release time
// by GitHub Actions via -ldflags; a local `go run`/`go build` without
// ldflags falls back to the "(dev)" strings so dev builds are still
// distinguishable from a real release.
package version

import "fmt"

// These variables are the single source of truth for the binary version.
// The release workflow sets them with:
//   -ldflags "-X github.com/local/relayhub/internal/version.Version=X.Y.Z \
//             -X github.com/local/relayhub/internal/version.Commit=abcdef \
//             -X github.com/local/relayhub/internal/version.BuildTime=2025-01-01T00:00:00Z"
var (
	Version   = "(dev)"
	Commit    = "unknown"
	BuildTime = "unknown"
)

// String returns a human-readable version string, e.g. "1.0.1 (abc123)
// built 2025-09-01". Useful for the binary --version flag and the admin
// console status banner.
func String() string {
	return fmt.Sprintf("%s (%s) built %s", Version, Commit, BuildTime)
}
