package audit

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/obot-platform/obocop/pkg/datadir"
	"github.com/obot-platform/obocop/pkg/fileutil"
	"github.com/obot-platform/obot/apiclient/types"
)

const (
	defaultSpoolMaxBytes = 50 * 1024 * 1024 // 50 MiB
	defaultSpoolMaxAge   = 7 * 24 * time.Hour
)

// Spool stores local-agent audit log batches for retry after transient
// submission failures.
type Spool struct {
	// Dir is the directory containing spool files.
	Dir string
	// MaxBytes is the maximum total size of retained spool files. A non-positive
	// value disables size-based pruning.
	MaxBytes int64
	// MaxAge is the maximum age of retained spool files. A non-positive value
	// disables age-based pruning.
	MaxAge time.Duration
	// Now returns the current time. It defaults to time.Now and is injectable for
	// deterministic tests.
	Now func() time.Time
}

// DefaultSpool returns the production audit spool rooted under the current
// user's cache directory.
func DefaultSpool() (*Spool, error) {
	cacheDir, err := datadir.CacheDir()
	if err != nil {
		return nil, err
	}
	return &Spool{
		Dir:      filepath.Join(cacheDir, "audit-spool"),
		MaxBytes: defaultSpoolMaxBytes,
		MaxAge:   defaultSpoolMaxAge,
		Now:      time.Now,
	}, nil
}

// Enqueue appends logs to the spool as a single retry batch. Empty batches are
// ignored.
func (s *Spool) Enqueue(logs []types.LocalAgentToolCallAuditLogInput) error {
	if len(logs) == 0 {
		return nil
	}
	if err := fileutil.MkdirAllPrivate(s.Dir); err != nil {
		return err
	}
	data, err := json.MarshalIndent(logs, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	name, err := s.nextName()
	if err != nil {
		return err
	}
	if err := fileutil.WriteFileAtomic(filepath.Join(s.Dir, name), data, 0o600); err != nil {
		return err
	}
	return s.enforceLimits()
}

// Drain submits up to limit oldest batches in order and removes each batch after
// a successful submit. If submit returns an error accepted by discard, Drain
// removes that batch and continues; otherwise it stops and returns the number of
// batches removed before the error. Unreadable batches are removed because
// malformed spool data cannot be recovered.
func (s *Spool) Drain(limit int, submit func([]types.LocalAgentToolCallAuditLogInput) error, discard func(error) bool) (int, error) {
	if limit <= 0 {
		return 0, nil
	}
	files, err := s.oldestFiles()
	if err != nil {
		return 0, err
	}
	if len(files) == 0 {
		return 0, nil
	}

	var drained int
	for _, path := range files {
		if drained >= limit {
			break
		}
		logs, err := readSpoolFile(path)
		if err != nil {
			if removeErr := os.Remove(path); removeErr != nil {
				return drained, removeErr
			}
			drained++
			continue
		}
		if err := submit(logs); err != nil {
			if discard != nil && discard(err) {
				if removeErr := os.Remove(path); removeErr != nil {
					return drained, removeErr
				}
				drained++
				continue
			}
			return drained, err
		}
		if err := os.Remove(path); err != nil {
			return drained, err
		}
		drained++
	}
	return drained, nil
}

// nextName returns a sortable spool filename with a random suffix to avoid
// collisions between batches created in the same nanosecond.
func (s *Spool) nextName() (string, error) {
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return "", err
	}
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	return fmt.Sprintf("%020d-%s.json", now().UTC().UnixNano(), hex.EncodeToString(suffix[:])), nil
}

// oldestFiles returns spool data files sorted from oldest to newest by filename.
func (s *Spool) oldestFiles() ([]string, error) {
	entries, err := os.ReadDir(s.Dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	files := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		files = append(files, filepath.Join(s.Dir, entry.Name()))
	}
	sort.Strings(files)
	return files, nil
}

// enforceLimits prunes expired files and then removes oldest remaining files
// until the spool is within its configured size limit.
func (s *Spool) enforceLimits() error {
	files, err := s.oldestFiles()
	if err != nil {
		return err
	}
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}

	var kept []string
	var total int64

	// Delete files older than the MaxAge, and track the size of kept files.
	for _, path := range files {
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		if s.MaxAge > 0 && now().Sub(info.ModTime()) > s.MaxAge {
			if err := os.Remove(path); err != nil {
				return err
			}
			continue
		}
		kept = append(kept, path)
		total += info.Size()
	}
	if s.MaxBytes <= 0 {
		return nil
	}

	// Delete files until the total size is below MaxBytes, deleting the oldest first.
	for _, path := range kept {
		if total <= s.MaxBytes {
			return nil
		}
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		if err := os.Remove(path); err != nil {
			return err
		}
		total -= info.Size()
	}

	return nil
}

// readSpoolFile decodes one spool file into its audit log batch.
func readSpoolFile(path string) ([]types.LocalAgentToolCallAuditLogInput, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var logs []types.LocalAgentToolCallAuditLogInput
	if err := json.Unmarshal(data, &logs); err != nil {
		return nil, err
	}
	return logs, nil
}
