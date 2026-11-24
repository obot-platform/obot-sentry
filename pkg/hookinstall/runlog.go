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

// The run directory holds the log and schedule state. A user who could write
// the state could suppress drift repair, and one who could hold the log open
// could blind it, so it lives under a parent no unprivileged user can create
// entries in — not the machine data directory, which is user-writable on both
// platforms so any user can create the shared identity there.
const (
	RunLogName = "hook-install.log"
	// maxRunLogBytes is the size at which the log rotates to RunLogName + ".1".
	maxRunLogBytes = 10 << 20
)

// DefaultRunDir creates and secures the run directory. Its parent must already
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

// openRunLog discards, rather than appends to, an existing log that is not a
// regular file or was created by an untrusted account.
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

func discardLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}
