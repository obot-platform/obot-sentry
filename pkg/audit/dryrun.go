package audit

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"

	"github.com/obot-platform/obocop/pkg/datadir"
	"github.com/obot-platform/obocop/pkg/fileutil"
	"github.com/obot-platform/obot/apiclient/types"
)

const dryRunLogDirName = "audit-logs"

// DryRunLogDir returns the directory used for audit logs produced by
// obocop audit submit --dry-run. The directory is rooted in the current
// user's platform-specific cache directory, but is not created by this
// function.
func DryRunLogDir() (string, error) {
	cacheDir, err := datadir.CacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(cacheDir, dryRunLogDirName), nil
}

// WriteDryRunLogs writes each log as a separate, user-private JSON file and
// returns the paths written. Empty batches do not create the log directory.
func WriteDryRunLogs(logs []types.LocalAgentToolCallAuditLogInput) ([]string, error) {
	if len(logs) == 0 {
		return nil, nil
	}

	dir, err := DryRunLogDir()
	if err != nil {
		return nil, err
	}
	if err := fileutil.MkdirAllPrivate(dir); err != nil {
		return nil, err
	}

	paths := make([]string, 0, len(logs))
	for _, log := range logs {
		data, err := json.MarshalIndent(log, "", "  ")
		if err != nil {
			return paths, err
		}
		data = append(data, '\n')

		name, err := nextDryRunLogName()
		if err != nil {
			return paths, err
		}
		path := filepath.Join(dir, name)
		if err := fileutil.WriteFileAtomic(path, data, 0o600); err != nil {
			return paths, err
		}
		paths = append(paths, path)
	}
	return paths, nil
}

func nextDryRunLogName() (string, error) {
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return "", err
	}
	return fmt.Sprintf("%020d-%s.json", time.Now().UTC().UnixNano(), hex.EncodeToString(suffix[:])), nil
}
