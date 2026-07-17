// Package state persists obot-sentry's per-user enrollment state (data
// dir), plus the scan state and scan log records (cache dir) that the
// scheduled per-user scans throttle against and report through.
package state

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/obot-platform/obot-sentry/pkg/fileutil"
)

const stateFile = "state.json"

// State records the outcome of the most recent enrollment so scans can
// tell whether the current identity is already enrolled with the
// configured server.
type State struct {
	// DeviceID the identity enrolled as.
	DeviceID string `json:"deviceID,omitempty"`
	// ServerURL the enrollment was performed against (normalized).
	ServerURL string `json:"serverURL,omitempty"`
	// MDMDeploymentID the server placed the device in.
	MDMDeploymentID uint `json:"mdmDeploymentID,omitempty"`
	// PublicKeyFingerprint of the enrolled key (identity.PublicKeyFingerprint).
	PublicKeyFingerprint string     `json:"publicKeyFingerprint,omitempty"`
	EnrolledAt           *time.Time `json:"enrolledAt,omitempty"`
	LastSubmitAt         *time.Time `json:"lastSubmitAt,omitempty"`
}

// Load reads the state from dir. A missing file returns the zero State.
func Load(dir string) (State, error) {
	b, err := os.ReadFile(filepath.Join(dir, stateFile))
	if os.IsNotExist(err) {
		return State{}, nil
	} else if err != nil {
		return State{}, err
	}
	var s State
	if err := json.Unmarshal(b, &s); err != nil {
		// A corrupt state file shouldn't brick the agent; treat it as
		// unenrolled and let the next enroll overwrite it.
		return State{}, nil
	}
	return s, nil
}

// Save writes the state to dir (atomic, 0600).
func (s State) Save(dir string) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return fileutil.WriteFileAtomic(filepath.Join(dir, stateFile), append(b, '\n'), 0o600)
}

// Enrolled reports whether s records an enrollment for this exact
// (server, device, key) triple; anything else means enroll again.
func (s State) Enrolled(serverURL, deviceID, keyFingerprint string) bool {
	return s.EnrolledAt != nil &&
		s.ServerURL == serverURL &&
		s.DeviceID == deviceID &&
		s.PublicKeyFingerprint == keyFingerprint
}

const scanStateFile = "scan.json"

// ScanState records the most recent scan attempt, written to the
// per-user cache dir after every run (success and failure). The OS
// scheduler polls every few minutes; scan --submit throttles real
// submissions against LastSubmitAt and the configured interval.
type ScanState struct {
	LastScanAt     *time.Time `json:"lastScanAt,omitempty"`
	LastSubmitAt   *time.Time `json:"lastSubmitAt,omitempty"`
	LastStatus     string     `json:"lastStatus,omitempty"` // "ok" | "error"
	LastError      string     `json:"lastError,omitempty"`
	DeviceID       string     `json:"deviceID,omitempty"`
	ScannerVersion string     `json:"scannerVersion,omitempty"`
}

// LoadScanState reads the scan state from dir. A missing or corrupt
// file returns the zero ScanState — throttling then simply allows the
// next scan.
func LoadScanState(dir string) (ScanState, error) {
	b, err := os.ReadFile(filepath.Join(dir, scanStateFile))
	if os.IsNotExist(err) {
		return ScanState{}, nil
	} else if err != nil {
		return ScanState{}, err
	}
	var s ScanState
	if err := json.Unmarshal(b, &s); err != nil {
		return ScanState{}, nil
	}
	return s, nil
}

// Save writes the scan state to dir (atomic, 0600), creating dir if
// needed.
func (s ScanState) Save(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return fileutil.WriteFileAtomic(filepath.Join(dir, scanStateFile), append(b, '\n'), 0o600)
}

// SubmittedWithin reports whether the last successful submission is
// fresher than interval as of now — the scan --submit throttle.
func (s ScanState) SubmittedWithin(interval time.Duration, now time.Time) bool {
	return s.LastSubmitAt != nil && now.Sub(*s.LastSubmitAt) < interval
}

// Scan log records: one JSON file per scan run under
// <cachedir>/scan-logs, with timestamp-sortable names so a directory
// listing reads chronologically. Retention is enforced at write time —
// prune by age, then oldest-first down to the size cap.
const (
	scanLogDirName  = "scan-logs"
	scanLogMaxAge   = 14 * 24 * time.Hour
	scanLogMaxBytes = 10 * 1024 * 1024
)

// ScanLogRecord is one scan run's outcome, written for support and MDM
// freshness checks.
type ScanLogRecord struct {
	StartedAt      time.Time `json:"startedAt"`
	FinishedAt     time.Time `json:"finishedAt"`
	Outcome        string    `json:"outcome"` // "submitted" | "scanned" | "skipped" | "error"
	Error          string    `json:"error,omitempty"`
	DeviceID       string    `json:"deviceID,omitempty"`
	ScannerVersion string    `json:"scannerVersion,omitempty"`
}

// AppendScanLog writes record as its own file under dir's scan-logs
// directory and prunes old records.
func AppendScanLog(dir string, record ScanLogRecord) error {
	logDir := filepath.Join(dir, scanLogDirName)
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	name, err := scanLogName(record.StartedAt)
	if err != nil {
		return err
	}
	if err := fileutil.WriteFileAtomic(filepath.Join(logDir, name), append(b, '\n'), 0o600); err != nil {
		return err
	}
	return pruneScanLogs(logDir, time.Now())
}

// scanLogName returns a timestamp-sortable filename with a random
// suffix to avoid collisions between runs in the same nanosecond.
func scanLogName(startedAt time.Time) (string, error) {
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return "", err
	}
	return fmt.Sprintf("%020d-%s.json", startedAt.UTC().UnixNano(), hex.EncodeToString(suffix[:])), nil
}

// pruneScanLogs deletes records older than the age cap, then the
// oldest remaining records until the directory is within the size cap.
func pruneScanLogs(logDir string, now time.Time) error {
	entries, err := os.ReadDir(logDir)
	if err != nil {
		return err
	}
	var names []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names) // timestamp prefix: oldest first

	// Overlapping per-user scans can prune the same directory
	// concurrently, so a file may vanish between ReadDir and Stat/Remove.
	// That's the directory being cleaned as intended — treat a missing
	// file as already-pruned rather than a failure.
	var kept []string
	var total int64
	for _, name := range names {
		path := filepath.Join(logDir, name)
		info, err := os.Stat(path)
		if os.IsNotExist(err) {
			continue
		} else if err != nil {
			return err
		}
		if now.Sub(info.ModTime()) > scanLogMaxAge {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return err
			}
			continue
		}
		kept = append(kept, path)
		total += info.Size()
	}
	for _, path := range kept {
		if total <= scanLogMaxBytes {
			return nil
		}
		info, err := os.Stat(path)
		if os.IsNotExist(err) {
			continue
		} else if err != nil {
			return err
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		total -= info.Size()
	}
	return nil
}
