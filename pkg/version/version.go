// Package version reports the obot-sentry build version. Tag is stamped at build
// time via ldflags (see the Makefile); the commit and dirty state come from the
// Go toolchain's VCS build info.
package version

import (
	"fmt"
	"runtime/debug"
)

// Tag is the release tag, overridden at build time with:
//
//	-X 'github.com/obot-platform/obot-sentry/pkg/version.Tag=<tag>'
var Tag = "v0.0.0-dev"

// Version is a build version: the release tag plus VCS commit information.
type Version struct {
	Tag    string `json:"tag,omitempty"`
	Commit string `json:"commit,omitempty"`
	Dirty  bool   `json:"dirty,omitempty"`
}

// Get returns the current build's version.
func Get() Version {
	v := Version{Tag: Tag}
	v.Commit, v.Dirty = gitCommit()
	return v
}

func (v Version) String() string {
	if len(v.Commit) < 12 {
		return v.Tag
	} else if v.Dirty {
		return fmt.Sprintf("%s-%s-dirty", v.Tag, v.Commit[:8])
	}
	return fmt.Sprintf("%s+%s", v.Tag, v.Commit[:8])
}

func gitCommit() (commit string, dirty bool) {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return "", false
	}
	for _, setting := range bi.Settings {
		switch setting.Key {
		case "vcs.modified":
			dirty = setting.Value == "true"
		case "vcs.revision":
			commit = setting.Value
		}
	}
	return
}
