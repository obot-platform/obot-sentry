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
	if scope == ScopeUser {
		if err := rejectIntermediateSymlinks(root, rel, absPath); err != nil {
			_ = root.Close()
			return nil, "", err
		}
	}
	return root, rel, nil
}

// rejectIntermediateSymlinks prevents a user-controlled hook path from
// redirecting the privileged installer elsewhere within the user's home. A
// machine path deliberately keeps os.Root's normal contained-symlink behavior
// so macOS /etc -> /private/etc continues to work.
func rejectIntermediateSymlinks(root *os.Root, rel, absPath string) error {
	for _, dir := range dirChain(filepath.Dir(rel)) {
		info, err := root.Lstat(dir)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing user config %q with symlinked parent %q", absPath, dir)
		}
		if !info.IsDir() {
			return fmt.Errorf("refusing user config %q with non-directory parent %q", absPath, dir)
		}
	}
	return nil
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
		if err := validateInstallMachinePath(absPath); err != nil {
			return "", "", err
		}
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
	if scope == ScopeUser {
		if err := rejectIntermediateSymlinks(root, rel, absPath); err != nil {
			return err
		}
	}
	if scope == ScopeMachine {
		// Protect the containing directory before publishing the replacement. On
		// Windows this prevents a normal user from deleting or replacing the hook
		// during the interval between Rename and the destination ACL update.
		if err := secureMachineConfigDir(absPath); err != nil {
			return err
		}
	}
	if err := writeAtomic(root, rel, absPath, data, filePerm); err != nil {
		return err
	}
	if scope == ScopeMachine {
		return secureMachineConfig(absPath)
	}
	if u == nil {
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

// writeAtomic replaces rel under root with data. absPath is the same file as
// the user knows it, and is the only path its errors name.
func writeAtomic(root *os.Root, rel, absPath string, data []byte, perm os.FileMode) error {
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
		return fmt.Errorf("creating temporary file for %q: %w", absPath, withoutRootPath(err))
	}
	if err := writeAndClose(f, data, perm); err != nil {
		_ = root.Remove(tmp)
		return fmt.Errorf("writing temporary file for %q: %w", absPath, withoutRootPath(err))
	}
	if err := root.Rename(tmp, rel); err != nil {
		_ = root.Remove(tmp)
		return fmt.Errorf("replacing %q: %w", absPath, withoutRootPath(err))
	}
	return nil
}

// withoutRootPath strips the path from an os.Root operation's error. That path
// is relative to the root, so on Windows it reads as "Program Files\..." with
// the drive missing, and the caller already names the file in full. The
// underlying error is kept, so errors.Is still sees fs.ErrPermission and the
// like.
func withoutRootPath(err error) error {
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		return pathErr.Err
	}
	var linkErr *os.LinkError
	if errors.As(err, &linkErr) {
		return linkErr.Err
	}
	return err
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
