package hookinstall

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

func configModes(scope Scope) (dirPerm, filePerm os.FileMode) {
	if scope == ScopeMachine {
		return 0o755, 0o644
	}
	return 0o700, 0o600
}

func openConfigRoot(scope Scope, home, absPath string) (*os.Root, string, error) {
	rootDir, rel, err := rootFor(scope, home, absPath)
	if err != nil {
		return nil, "", err
	}
	root, err := os.OpenRoot(rootDir)
	if err != nil {
		return nil, "", fmt.Errorf("opening root %q: %w", rootDir, err)
	}
	return root, rel, nil
}

func rootFor(scope Scope, home, absPath string) (rootDir, rel string, err error) {
	switch scope {
	case ScopeUser:
		if home == "" {
			return "", "", fmt.Errorf("user-scoped path %q has no root", absPath)
		}
		rel, err := relWithin(home, absPath)
		if err != nil {
			return "", "", err
		}
		return home, rel, nil
	case ScopeMachine:
		rootDir := filepath.VolumeName(absPath) + string(filepath.Separator)
		rel, err := relWithin(rootDir, absPath)
		if err != nil {
			return "", "", err
		}
		return rootDir, rel, nil
	default:
		return "", "", fmt.Errorf("unknown scope %q for path %q", scope, absPath)
	}
}

func relWithin(base, target string) (string, error) {
	rel, err := filepath.Rel(base, target)
	if err != nil {
		return "", fmt.Errorf("path %q is not under root dir %q: %w", target, base, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("path %q escapes root dir %q", target, base)
	}
	return rel, nil
}

func statConfigFile(root *os.Root, rel, absPath string) (bool, error) {
	info, err := root.Lstat(rel)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("refusing to use non-regular config %q", absPath)
	}
	return true, nil
}

// readConfigFile reads the config at absPath through its root, reporting false
// when the file or its root is absent.
func readConfigFile(scope Scope, home, absPath string) ([]byte, bool, error) {
	root, rel, err := openConfigRoot(scope, home, absPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, err
	}
	defer func() { _ = root.Close() }()

	if ok, err := statConfigFile(root, rel, absPath); err != nil || !ok {
		return nil, false, err
	}
	data, err := root.ReadFile(rel)
	if err != nil {
		return nil, false, err
	}
	return data, true, nil
}

// commitConfigFile creates any missing parents, replaces absPath atomically, and
// stamps per-user ownership. The destination is re-checked even though preflight
// already read it, so a link planted since then is refused.
func commitConfigFile(scope Scope, u *TargetUser, absPath string, data []byte) error {
	home := ""
	if u != nil {
		home = u.HomeDir
	}
	root, rel, err := openConfigRoot(scope, home, absPath)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()

	if _, err := statConfigFile(root, rel, absPath); err != nil {
		return err
	}

	dirPerm, filePerm := configModes(scope)
	dirs := dirChain(filepath.Dir(rel))
	if len(dirs) > 0 {
		if err := root.MkdirAll(dirs[len(dirs)-1], dirPerm); err != nil {
			return fmt.Errorf("creating config directory for %q: %w", absPath, err)
		}
	}
	if err := writeAtomic(root, rel, data, filePerm); err != nil {
		return err
	}
	if scope != ScopeUser || u == nil {
		return nil
	}
	return chownToUser(root, u, append(dirs, rel))
}

// dirChain expands "a/b/c" into ["a", "a/b", "a/b/c"]: the last element is the
// directory to create, the whole chain is what gets re-owned.
func dirChain(dir string) []string {
	if dir == "." || dir == "" || dir == string(filepath.Separator) {
		return nil
	}
	parts := strings.Split(dir, string(filepath.Separator))
	chain := make([]string, 0, len(parts))
	cur := ""
	for _, part := range parts {
		if part == "" {
			continue
		}
		cur = filepath.Join(cur, part)
		chain = append(chain, cur)
	}
	return chain
}

func writeAtomic(root *os.Root, rel string, data []byte, perm os.FileMode) error {
	// The pid keeps concurrent installers off each other's in-flight write; the
	// rename is atomic, so whichever commits last wins with identical content.
	tmp := filepath.Join(filepath.Dir(rel), fmt.Sprintf(".%s.%d.obot-sentry-tmp", filepath.Base(rel), os.Getpid()))

	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if errors.Is(err, fs.ErrExist) {
		// A stale temp from an interrupted run of this same pid.
		_ = root.Remove(tmp)
		f, err = root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	}
	if err != nil {
		return fmt.Errorf("creating temporary file for %q: %w", rel, err)
	}
	if err := writeAndClose(f, data, perm); err != nil {
		_ = root.Remove(tmp)
		return fmt.Errorf("writing temporary file for %q: %w", rel, err)
	}
	if err := root.Rename(tmp, rel); err != nil {
		_ = root.Remove(tmp)
		return fmt.Errorf("replacing %q: %w", rel, err)
	}
	return nil
}

// writeAndClose writes data, forces perm past the process umask, and flushes so
// the rename can only publish complete content. Both the chmod and the sync are
// on the descriptor, so neither is exposed to a path race.
func writeAndClose(f *os.File, data []byte, perm os.FileMode) (err error) {
	defer func() {
		if cerr := f.Close(); err == nil {
			err = cerr
		}
	}()
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Chmod(perm); err != nil {
		return err
	}
	return f.Sync()
}
