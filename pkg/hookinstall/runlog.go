package hookinstall

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
)

// The run directory holds what hook-install keeps between runs: its log and
// the schedule state. A user who could write the schedule state could
// suppress drift repair, and one who could hold the log open could blind it,
// so the directory must sit under a parent no unprivileged user can create
// entries in, and is itself readable and writable only by root (macOS) or
// SYSTEM and Administrators (Windows):
//
//   - macOS: /Library/Application Support/obot/obot-sentry-hook-install. Its
//     parent is root-only; the machine data directory beside it is mode 1777
//     (any user creates the shared identity there), so it is not usable.
//   - Windows: hook-install in the package's install directory under Program
//     Files. The machine data directory under ProgramData is not usable for
//     the same reason: any user can create entries there, and so could
//     pre-create the run directory and keep a handle to it that survives any
//     later ACL change.
const (
	// RunLogName is the hook-install log inside the run directory.
	RunLogName = "hook-install.log"
	// maxRunLogBytes caps the log before it rotates to RunLogName + ".1". Only
	// changes and failures are logged, so 1 MiB holds months of normal runs;
	// two generations bound the total.
	maxRunLogBytes = 1 << 20
)

// DefaultRunDir resolves the run directory, creating it and restricting it to
// root (macOS) or SYSTEM and Administrators (Windows). Its parent must already
// exist: the package creates it.
func DefaultRunDir() (string, error) {
	base, dir, err := runDirLocation()
	if err != nil {
		return "", err
	}
	if err := makeRunDir(dir); err != nil && !errors.Is(err, fs.ErrExist) {
		return "", fmt.Errorf("creating %s: %w", dir, err)
	}
	if err := secureRunDir(base, dir); err != nil {
		return "", fmt.Errorf("securing %s: %w", dir, err)
	}
	return dir, nil
}

// openRunLog opens the run log in dir for appending, rotating it first when it
// has grown past maxRunLogBytes. An existing log that is not a regular file or
// was created by an untrusted account is discarded rather than appended to.
func openRunLog(dir string) (*slog.Logger, io.Closer, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = root.Close() }()

	for _, name := range []string{RunLogName, RunLogName + ".1"} {
		if err := checkRunFile(root, dir, name); err != nil && !errors.Is(err, fs.ErrNotExist) {
			if rmErr := root.Remove(name); rmErr != nil {
				return nil, nil, fmt.Errorf("removing untrusted %s: %w", filepath.Join(dir, name), errors.Join(err, rmErr))
			}
		}
	}
	if info, err := root.Lstat(RunLogName); err == nil && info.Size() >= maxRunLogBytes {
		if err := root.Rename(RunLogName, RunLogName+".1"); err != nil {
			return nil, nil, fmt.Errorf("rotating %s: %w", filepath.Join(dir, RunLogName), withoutRootPath(err))
		}
	}

	f, err := root.OpenFile(RunLogName, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return nil, nil, fmt.Errorf("opening %s: %w", filepath.Join(dir, RunLogName), withoutRootPath(err))
	}
	claimRunFile(filepath.Join(dir, RunLogName))
	logger := slog.New(slog.NewTextHandler(f, &slog.HandlerOptions{Level: slog.LevelInfo}))
	return logger, f, nil
}

// discardLogger is used when no run log is available.
func discardLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}
