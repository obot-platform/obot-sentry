package hookinstall

import (
	"fmt"
	"path/filepath"
	"strings"
)

// ResolvePath returns the absolute destination path for d. Machine-scoped
// destinations use their fixed Abs path; user-scoped destinations join the
// active console user's home with the relative template. It performs no symlink
// or containment checks of its own — those are enforced at read/write time by
// the anchored access in safeio.go.
func (d Destination) ResolvePath(u *TargetUser) (string, error) {
	switch d.Scope {
	case ScopeMachine:
		return d.Abs, nil
	case ScopeUser:
		if u == nil || u.HomeDir == "" {
			return "", fmt.Errorf("cannot resolve user-scoped destination %q without an active console user", d.Label)
		}
		return filepath.Join(u.HomeDir, filepath.FromSlash(d.Rel)), nil
	default:
		return "", fmt.Errorf("destination %q has unknown scope %q", d.Label, d.Scope)
	}
}

// anchorFor splits an absolute destination path into a trusted anchor directory
// and the path relative to it, for os.Root-based symlink-safe access:
//
//   - user-scoped paths anchor at the verified home; its parent (/Users, /home,
//     C:\Users) is administrator-owned and not user-swappable, so the symlink
//     defense only needs to cover the components below home that the user
//     controls.
//   - machine-scoped paths anchor at the filesystem/volume root; their ancestors
//     are administrator-owned, and os.Root still refuses any symlink that would
//     escape the anchor.
func anchorFor(scope Scope, home, absPath string) (anchorDir, rel string, err error) {
	switch scope {
	case ScopeUser:
		if home == "" {
			return "", "", fmt.Errorf("user-scoped path %q has no anchor home", absPath)
		}
		rel, err := relWithin(home, absPath)
		if err != nil {
			return "", "", err
		}
		return home, rel, nil
	case ScopeMachine:
		anchorDir := filepath.VolumeName(absPath) + string(filepath.Separator)
		rel, err := relWithin(anchorDir, absPath)
		if err != nil {
			return "", "", err
		}
		return anchorDir, rel, nil
	default:
		return "", "", fmt.Errorf("unknown scope %q for path %q", scope, absPath)
	}
}

// relWithin returns target expressed relative to base, refusing any result that
// escapes base (via ".." or an absolute remainder). This is the containment
// check that keeps a resolved destination inside its trusted anchor.
func relWithin(base, target string) (string, error) {
	rel, err := filepath.Rel(base, target)
	if err != nil {
		return "", fmt.Errorf("path %q is not under anchor %q: %w", target, base, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("path %q escapes anchor %q", target, base)
	}
	return rel, nil
}
