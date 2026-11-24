package hookinstall

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
)

// maxRunLogSize is the size at which the run log is rotated: the current file
// becomes <name>.1, replacing the previous one, so the log never takes more
// than about twice this.
const maxRunLogSize = 1 << 20

// openRunLog opens the hook-install run log for appending, creating it and its
// root-only directory as needed and rotating it once it has grown past
// maxRunLogSize.
func openRunLog() (io.WriteCloser, error) {
	path, err := runLogPath()
	if err != nil {
		return nil, err
	}
	return openRunLogAt(path)
}

func openRunLogAt(path string) (io.WriteCloser, error) {
	d, err := openRunDir(filepath.Dir(path), true)
	if err != nil {
		return nil, err
	}
	f, err := openRunLogIn(d, path)
	if err != nil {
		d.Close()
		return nil, err
	}
	return runFile{File: f, dir: d}, nil
}

func openRunLogIn(d *runDir, path string) (*os.File, error) {
	name := filepath.Base(path)
	info, err := d.root.Lstat(name)
	switch {
	case err == nil && !info.Mode().IsRegular():
		return nil, fmt.Errorf("run log %s is not a regular file", path)
	case err == nil && info.Size() >= maxRunLogSize:
		if err := d.root.Rename(name, name+".1"); err != nil {
			return nil, fmt.Errorf("rotating run log %s: %w", path, err)
		}
	}
	f, err := d.root.OpenFile(name, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if checkRunFile(f, path) == nil {
		return f, nil
	}
	// Not ours, or linked elsewhere: start a new log rather than write into
	// it. Removing a name never follows it, and O_EXCL makes sure the file
	// written is the one created here.
	_ = f.Close()
	if err := d.root.Remove(name); err != nil {
		return nil, fmt.Errorf("replacing untrusted run log %s: %w", path, err)
	}
	return d.root.OpenFile(name, os.O_WRONLY|os.O_APPEND|os.O_CREATE|os.O_EXCL, 0o600)
}

// logRun records one run: a line for every destination it changed or could not
// converge, one for each user passed over, and a closing line with the totals.
// Destinations already in their desired state are counted but not listed, so a
// quiet log means nothing drifted.
func logRun(ctx context.Context, log *slog.Logger, command, reason string, plan Plan, skipped []SkippedUser, results []Result) {
	if plan.UsersErr != nil {
		log.Error("could not list all users; per-user hooks were checked only for those found", "command", command, "users", len(plan.Users), "error", plan.UsersErr)
	}
	for _, s := range skipped {
		log.Info("user skipped", "command", command, "user", s.Username, "reason", s.Reason)
	}

	counts := map[Status]int{}
	liveFailed := 0
	for _, r := range results {
		counts[r.Status]++
		attrs := []any{"command", command, "agent", r.Label}
		if r.User != "" {
			attrs = append(attrs, "user", r.User)
		}
		if r.Path != "" {
			attrs = append(attrs, "path", r.Path)
		}
		if r.DuplicatesRemoved > 0 {
			attrs = append(attrs, "duplicates_removed", r.DuplicatesRemoved)
		}
		if r.HooksRemoved > 0 {
			attrs = append(attrs, "hooks_removed", r.HooksRemoved)
		}
		switch r.Status {
		case StatusInstalled:
			log.Info("hook installed", attrs...)
		case StatusUpdated:
			log.Info("hook repaired", attrs...)
		case StatusRemoved:
			log.Info("hook removed", attrs...)
		case StatusFailed:
			if r.Retired {
				// Nothing is missing: an old hook could not be cleaned up.
				log.Warn("old hook could not be removed", append(attrs, "error", r.Err)...)
			} else {
				log.Error("hook check failed", append(attrs, "error", r.Err)...)
				liveFailed++
			}
		}
	}

	level := slog.LevelInfo
	if liveFailed > 0 || plan.UsersErr != nil {
		level = slog.LevelError
	}
	if reason == "" {
		reason = "requested"
	}
	log.Log(ctx, level, "hook check complete",
		"command", command,
		"reason", reason,
		"enforce", plan.Enforce,
		"users", len(plan.Users),
		"skipped_users", len(skipped),
		"installed", counts[StatusInstalled],
		"repaired", counts[StatusUpdated],
		"removed", counts[StatusRemoved],
		"unchanged", counts[StatusUnchanged],
		"failed", counts[StatusFailed],
	)
}
