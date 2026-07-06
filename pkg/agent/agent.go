// Package agent orchestrates the device lifecycle the commands share:
// make sure the machine's shared identity is enrolled with the
// configured server, submit scan manifests, and persist the per-user
// state and last-scan marker MDM detection scripts rely on.
package agent

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/obot-platform/obot/apiclient/types"

	"github.com/obot-platform/obocop/pkg/client"
	"github.com/obot-platform/obocop/pkg/identity"
	"github.com/obot-platform/obocop/pkg/mdmconfig"
	"github.com/obot-platform/obocop/pkg/state"
)

// Agent binds a resolved config to the machine-scoped identity dir and
// the per-user data dir.
type Agent struct {
	// DataDir is the per-user dir holding enrollment state and the
	// last-scan marker.
	DataDir string
	// IdentityDir is the (normally machine-scoped) dir holding the
	// shared device identity key.
	IdentityDir string
	Config      mdmconfig.Config
	Client      *client.Client
}

// New builds an Agent for cfg. cfg.ServerURL must be set.
func New(dataDir, identityDir string, cfg mdmconfig.Config) *Agent {
	return &Agent{
		DataDir:     dataDir,
		IdentityDir: identityDir,
		Config:      cfg,
		Client:      client.New(cfg.ServerURL),
	}
}

// EnsureEnrolled returns the machine identity enrolled with the
// configured server, enrolling first if this user's persisted state
// doesn't already record the exact (server, device, key) triple.
// Enrollment is idempotent server-side, so every user on the machine
// safely enrolls the same shared identity once.
func (a *Agent) EnsureEnrolled(ctx context.Context) (*identity.Identity, state.State, error) {
	st, err := state.Load(a.DataDir)
	if err != nil {
		return nil, st, err
	}

	serverURL := client.NormalizeBaseURL(a.Config.ServerURL)
	id, err := identity.Load(a.IdentityDir)
	if err != nil {
		return nil, st, err
	}
	if st.Enrolled(serverURL, id.DeviceID, id.PublicKeyFingerprint()) {
		return id, st, nil
	}

	if a.Config.EnrollmentKey == "" {
		return nil, st, fmt.Errorf("device %s is not enrolled with %s and no enrollment key is configured", id.DeviceID, serverURL)
	}

	device, err := a.Client.Enroll(ctx, a.Config.EnrollmentKey, id, a.Config.DeviceName)
	if err != nil {
		return nil, st, fmt.Errorf("enrolling device: %w", err)
	}

	now := time.Now().UTC()
	st.DeviceID = id.DeviceID
	st.ServerURL = serverURL
	st.MDMDeploymentID = device.MDMDeploymentID
	st.PublicKeyFingerprint = id.PublicKeyFingerprint()
	st.EnrolledAt = &now
	if err := st.Save(a.DataDir); err != nil {
		return nil, st, err
	}
	return id, st, nil
}

// SubmitScan submits the manifest as id. If the server rejects the
// device's credentials (e.g. its record was removed), it re-enrolls
// once and retries once. On success it updates the state and writes the
// last-scan marker for MDM freshness checks.
func (a *Agent) SubmitScan(ctx context.Context, id *identity.Identity, st state.State, manifest types.DeviceScanManifest) (*types.DeviceScan, error) {
	scan, err := a.Client.SubmitScan(ctx, id, manifest)
	if client.IsUnauthorized(err) {
		slog.Warn("device credentials rejected; re-enrolling and retrying once", "err", err)
		st.EnrolledAt = nil // force EnsureEnrolled to re-enroll
		if saveErr := st.Save(a.DataDir); saveErr != nil {
			return nil, saveErr
		}
		if id, st, err = a.EnsureEnrolled(ctx); err != nil {
			return nil, err
		}
		manifest.DeviceID = id.DeviceID
		scan, err = a.Client.SubmitScan(ctx, id, manifest)
	}
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	st.LastSubmitAt = &now
	if err := st.Save(a.DataDir); err != nil {
		return nil, err
	}
	if err := state.WriteLastScanMarker(a.DataDir, now); err != nil {
		return nil, err
	}
	return scan, nil
}
