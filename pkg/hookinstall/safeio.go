package hookinstall

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// This file implements privileged-safe filesystem access for the config
// commit. A root/SYSTEM installer writes per-user files into a home a normal
// user fully controls, so every read, directory creation, and write must be
// anchored: a symlink planted by the user (final component or an ancestor) must
// never redirect a privileged operation outside the trusted anchor.
//
// Two layers enforce this for user-scoped paths:
//
//   - os.Root anchors every operation and refuses to resolve outside the
//     anchor, so an escape is impossible even under a symlink race.
//   - On top of that, we reject any symlink component outright rather than
//     follow it. os.Root by itself would still follow a symlink that stays
//     within the anchor; we walk each component with Lstat and refuse a symlink
//     anywhere in the path. A symlink already sitting at the final write target
//     is replaced by the atomic rename (which operates on the link, never its
//     target), so a pre-planted link self-heals without ever being followed.
//
// Machine-scoped paths keep the os.Root containment guarantee but permit
// existing intermediate directory symlinks. Their fixed ancestors are
// administrator-owned, and macOS relies on this behavior because /etc is a
// root-owned symlink to private/etc. Final config symlinks are still rejected
// on reads and safely replaced on direct writes.

// configModes returns the directory and file permissions for a newly created
// destination. Machine files are shared (root-owned, world-readable); per-user
// files are created private and then chowned to the target user.
func configModes(scope Scope) (dirPerm, filePerm os.FileMode) {
	if scope == ScopeMachine {
		return 0o755, 0o644
	}
	return 0o700, 0o600
}

// readConfigFile reads the existing config at absPath through an anchor so no
// symlink can redirect the read. It returns (data, true, nil) for a regular
// file, (nil, false, nil) when the file or a path component is absent, and an
// error for a hostile target: a symlinked final component or a non-regular file
// (device, FIFO, socket, directory).
func readConfigFile(scope Scope, home, absPath string) ([]byte, bool, error) {
	anchorDir, rel, err := anchorFor(scope, home, absPath)
	if err != nil {
		return nil, false, err
	}
	root, err := os.OpenRoot(anchorDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, err
	}
	defer func() { _ = root.Close() }()

	if scope == ScopeUser {
		if err := assertNoSymlinkComponents(root, rel); err != nil {
			return nil, false, err
		}
	}
	info, err := root.Lstat(rel)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, err
	}
	if !info.Mode().IsRegular() {
		return nil, false, fmt.Errorf("refusing to read non-regular config %q", absPath)
	}
	data, err := root.ReadFile(rel)
	if err != nil {
		return nil, false, err
	}
	return data, true, nil
}

// commitConfigFile writes data to absPath, creating any missing parent
// directories, replacing the file atomically, and stamping the correct
// ownership (POSIX chown for user files, a Windows ACL). All access is anchored
// so no symlink can redirect a privileged write outside the trusted root.
func commitConfigFile(scope Scope, u *TargetUser, absPath string, data []byte) error {
	home := ""
	if u != nil {
		home = u.HomeDir
	}
	anchorDir, rel, err := anchorFor(scope, home, absPath)
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(anchorDir)
	if err != nil {
		return fmt.Errorf("opening anchor %q: %w", anchorDir, err)
	}
	defer func() { _ = root.Close() }()

	dirPerm, filePerm := configModes(scope)
	created, err := mkdirParents(root, rel, dirPerm, scope == ScopeMachine)
	if err != nil {
		return err
	}
	if err := writeAnchoredFile(root, rel, data, filePerm); err != nil {
		return err
	}
	return applyOwnership(root, rel, created, scope, u)
}

// assertNoSymlinkComponents walks each existing component of rel within root and
// refuses if any is a symlink, so a read never follows a link an attacker
// planted anywhere along the path. It stops at the first missing component (a
// deeper path cannot exist). os.Root already guarantees no escape; this adds the
// no-follow policy on top via a per-component O_NOFOLLOW-style check.
func assertNoSymlinkComponents(root *os.Root, rel string) error {
	cur := ""
	for part := range strings.SplitSeq(rel, string(filepath.Separator)) {
		if part == "" || part == "." {
			continue
		}
		if cur == "" {
			cur = part
		} else {
			cur = filepath.Join(cur, part)
		}
		info, err := root.Lstat(cur)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("refusing to follow symlinked path component %q", cur)
		}
	}
	return nil
}

// mkdirParents creates the parent directories of rel within root, one component
// at a time so it can report exactly the components it created (only those are
// re-owned to the target user). A non-directory always aborts. A symlinked
// directory aborts for user paths; trusted machine paths may traverse it while
// os.Root keeps the operation contained beneath the volume root.
func mkdirParents(root *os.Root, rel string, perm os.FileMode, allowSymlinkDirs bool) ([]string, error) {
	dir := filepath.Dir(rel)
	if dir == "." || dir == "" || dir == string(filepath.Separator) {
		return nil, nil
	}

	var created []string
	cur := ""
	for part := range strings.SplitSeq(dir, string(filepath.Separator)) {
		if part == "" {
			continue
		}
		if cur == "" {
			cur = part
		} else {
			cur = filepath.Join(cur, part)
		}

		err := root.Mkdir(cur, perm)
		if err == nil {
			created = append(created, cur)
			continue
		}
		if !errors.Is(err, fs.ErrExist) {
			return created, err
		}
		info, lerr := root.Lstat(cur)
		if lerr != nil {
			return created, lerr
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			if !allowSymlinkDirs {
				return created, fmt.Errorf("refusing to traverse symlinked config directory %q", cur)
			}
			// Machine ancestors are administrator-owned, so they may use a
			// symlink for filesystem layout (notably macOS /etc -> private/etc).
			// Stat follows it through os.Root, which still rejects an absolute
			// link or any relative link that escapes the volume root.
			targetInfo, serr := root.Stat(cur)
			if serr != nil {
				return created, serr
			}
			if !targetInfo.IsDir() {
				return created, fmt.Errorf("config path component %q is not a directory", cur)
			}
			continue
		}
		if !info.IsDir() {
			return created, fmt.Errorf("config path component %q is not a directory", cur)
		}
	}
	return created, nil
}

// writeAnchoredFile atomically replaces rel within root with data: it writes a
// temporary file in the same directory, flushes it, and renames it over the
// destination. A pre-existing final symlink is replaced by the rename (rename
// never writes through it), but a non-regular destination (directory, FIFO,
// socket) aborts.
func writeAnchoredFile(root *os.Root, rel string, data []byte, perm os.FileMode) error {
	if info, err := root.Lstat(rel); err == nil {
		if mode := info.Mode(); mode&fs.ModeSymlink == 0 && !mode.IsRegular() {
			return fmt.Errorf("refusing to overwrite non-regular file %q", rel)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}

	dir := filepath.Dir(rel)
	// Scope the temp name to this process id so two concurrent installers writing
	// the same destination use distinct temp files and cannot clobber each
	// other's in-flight write (the final rename is still atomic, so whichever
	// commits last wins with identical content).
	tmpName := fmt.Sprintf(".%s.%d.obot-sentry-tmp", filepath.Base(rel), os.Getpid())
	tmp := tmpName
	if dir != "." && dir != "" {
		tmp = filepath.Join(dir, tmpName)
	}

	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if errors.Is(err, fs.ErrExist) {
		// A stale temp left by an interrupted run of this same pid; remove it and
		// retry once.
		_ = root.Remove(tmp)
		f, err = root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	}
	if err != nil {
		return fmt.Errorf("creating temporary file for %q: %w", rel, err)
	}

	cleanup := func() { _ = root.Remove(tmp) }
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		cleanup()
		return err
	}
	if err := f.Chmod(perm); err != nil {
		_ = f.Close()
		cleanup()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		cleanup()
		return err
	}
	if err := f.Close(); err != nil {
		cleanup()
		return err
	}
	if err := root.Rename(tmp, rel); err != nil {
		cleanup()
		return fmt.Errorf("replacing %q: %w", rel, err)
	}
	return nil
}
