package hookinstall

import (
	"errors"
	"fmt"
	"io"
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
// when the file or its root is absent. u is the user whose home holds a
// user-scoped file, and nil for a machine-wide one.
//
// The file is checked through the descriptor it is read from, so it cannot be
// swapped between the check and the read: it must be a regular file with a
// single link, and a user-scoped one must belong to that user (or root). A hard
// link is refused because it lets whoever made it point the privileged reader at
// a file they cannot read themselves, whose contents the merge would then copy
// into a file they own.
func readConfigFile(scope Scope, u *TargetUser, absPath string) ([]byte, bool, error) {
	root, rel, err := openConfigRoot(scope, homeOf(u), absPath)
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
	// Opened without blocking: a FIFO swapped in after the Lstat above would
	// otherwise hold the open, and with it the whole group, until timed out.
	f, err := root.OpenFile(rel, configReadFlags, 0)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, false, err
	}
	if !info.Mode().IsRegular() {
		return nil, false, fmt.Errorf("refusing to use non-regular config %q", absPath)
	}
	if err := checkConfigFileIdentity(f, info, scope, u, absPath); err != nil {
		return nil, false, err
	}
	data, err := io.ReadAll(f)
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
	owner := u
	if scope == ScopeMachine {
		owner = nil
	}
	if err := writeAtomic(root, rel, absPath, data, filePerm, owner); err != nil {
		return err
	}
	if scope == ScopeMachine {
		return secureMachineConfig(absPath)
	}
	if u == nil {
		return nil
	}
	// The file itself was handed to the user before it was renamed into place.
	// Directories are re-owned by name, which is safe where the file would not
	// be: a directory cannot be hard-linked, and Lchown does not follow a
	// symlink planted in its place.
	return chownToUser(root, u, dirs)
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

// chownOpenFile is chownFile, replaceable in tests to observe when the hand-over
// happens.
var chownOpenFile = chownFile

// writeAtomic replaces rel under root with data. absPath is the same file as
// the user knows it, and is the only path its errors name. A non-nil owner is
// given the file through its descriptor before it is renamed into place: once
// the name is in a directory the user controls, they could swap it for a hard
// link to any file on the volume, and a chown by name would then hand them that
// file instead.
func writeAtomic(root *os.Root, rel, absPath string, data []byte, perm os.FileMode, owner *TargetUser) error {
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
	if owner != nil {
		if err := chownOpenFile(f, owner); err != nil {
			_ = f.Close()
			_ = root.Remove(tmp)
			return fmt.Errorf("handing temporary file for %q to %s: %w", absPath, owner.Username, withoutRootPath(err))
		}
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
