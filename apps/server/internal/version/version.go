// Package version reports the build version of a Content Fabrique binary.
package version

import "runtime/debug"

// Version is set at build time with
// -ldflags "-X github.com/nilpntr/content-fabrique/internal/version.Version=v1.2.3".
// When unset, Get falls back to the VCS revision embedded by the Go toolchain.
var Version = ""

// Get returns the build version, or "dev" when nothing better is known.
func Get() string {
	if Version != "" {
		return Version
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "dev"
	}
	var rev string
	var dirty bool
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if rev == "" {
		return "dev"
	}
	if len(rev) > 12 {
		rev = rev[:12]
	}
	if dirty {
		rev += "-dirty"
	}
	return rev
}
