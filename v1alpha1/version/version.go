// Package version carries the build's compiled-in identity. The linker sets
// Version (and Commit) at release time via -ldflags "-X"; plain go build /
// go install leave the "dev" default, in which case the module version from
// the build info is used as a fallback.
package version

import (
	"fmt"
	"runtime/debug"
	"sync"
)

// Version is the release version, injected at build time. Defaults to "dev".
var Version = "dev"

// Commit is the VCS revision, injected for release builds. May be empty.
var Commit = ""

// resolved reports the effective version: the injected Version when set,
// otherwise the module version from the build info (for go install ...@vX.Y.Z),
// falling back to "dev".
var resolved = sync.OnceValue(func() string {
	if Version != "" && Version != "dev" {
		return Version
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		if v := bi.Main.Version; v != "" && v != "(devel)" {
			return v
		}
	}
	return "dev"
})

// String returns the effective build version for display (e.g. --version).
func String() string { return resolved() }

// UserAgent is the User-Agent this build sends on outbound HTTP requests.
func UserAgent() string {
	return fmt.Sprintf("kubectl-add/%s (+https://github.com/scaffoldly/kubectl-add)", resolved())
}
