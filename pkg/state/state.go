// Package state persists obocop's per-user enrollment state and the
// last-scan marker MDM detection scripts read for freshness checks.
package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

const (
	stateFile = "state.json"
	// LastScanFile is the marker written after a successful submit.
	// Intune detection scripts stat/parse this file directly rather
	// than spawning obocop.
	LastScanFile = "last_scan"
)

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
	return writeFileAtomic(filepath.Join(dir, stateFile), append(b, '\n'), 0o600)
}

// Enrolled reports whether s records an enrollment for this exact
// (server, device, key) triple; anything else means enroll again.
func (s State) Enrolled(serverURL, deviceID, keyFingerprint string) bool {
	return s.EnrolledAt != nil &&
		s.ServerURL == serverURL &&
		s.DeviceID == deviceID &&
		s.PublicKeyFingerprint == keyFingerprint
}

// WriteLastScanMarker records a successful submission at t (RFC3339
// UTC) for MDM freshness checks.
func WriteLastScanMarker(dir string, t time.Time) error {
	line := t.UTC().Format(time.RFC3339) + "\n"
	return writeFileAtomic(filepath.Join(dir, LastScanFile), []byte(line), 0o600)
}

func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()

	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
