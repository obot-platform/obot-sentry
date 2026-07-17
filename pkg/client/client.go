// Package client wraps the obot apiclient for the calls a device
// makes: enrolling its identity key (bearer: ode1 enrollment
// credential), submitting scans, and submitting local-agent audit logs
// (bearer: self-signed device JWT).
package client

import (
	"context"
	"os"
	"runtime"
	"strings"

	"github.com/obot-platform/obot/apiclient"
	"github.com/obot-platform/obot/apiclient/types"

	"github.com/obot-platform/obot-sentry/pkg/identity"
)

type Client struct {
	api *apiclient.Client
}

// New builds a client for serverURL, normalizing to the .../api base
// path the apiclient expects.
func New(serverURL string) *Client {
	return &Client{api: &apiclient.Client{BaseURL: NormalizeBaseURL(serverURL)}}
}

// NormalizeBaseURL appends /api to a server base URL, tolerating
// trailing slashes and an already-present /api suffix.
func NormalizeBaseURL(serverURL string) string {
	u := strings.TrimRight(strings.TrimSpace(serverURL), "/")
	u = strings.TrimSuffix(u, "/api")
	return u + "/api"
}

// Enroll registers the identity's public key with the server
// (trust-on-first-use), authenticating with the ode1 enrollment
// credential. Re-enrolling the same device ID with the same key is an
// idempotent update server-side.
func (c *Client) Enroll(ctx context.Context, credential string, id *identity.Identity) (*types.Device, error) {
	hostname, _ := os.Hostname()
	resp, err := c.api.EnrollDevice(ctx, credential, types.DeviceEnrollRequest{
		DeviceID:  id.DeviceID,
		PublicKey: id.PublicKeyDER,
		Hostname:  hostname,
		OS:        runtime.GOOS,
	})
	if err != nil {
		return nil, err
	}
	return &resp.Device, nil
}

// SubmitScan mints a fresh short-lived device JWT and submits the
// manifest. The server stamps the scan's DeviceID from the JWT
// principal; the manifest's own DeviceID is informational.
func (c *Client) SubmitScan(ctx context.Context, id *identity.Identity, manifest types.DeviceScanManifest) (*types.DeviceScan, error) {
	tok, err := id.MintDeviceJWT(identity.DefaultTokenTTL)
	if err != nil {
		return nil, err
	}
	return c.api.WithToken(tok).SubmitDeviceScan(ctx, manifest)
}

// SubmitLocalAgentAuditLogs mints a fresh short-lived device JWT and submits
// completed local-agent audit logs. The server stamps authoritative device
// attribution from the JWT principal.
func (c *Client) SubmitLocalAgentAuditLogs(ctx context.Context, id *identity.Identity, logs []types.LocalAgentToolCallAuditLogInput) error {
	tok, err := id.MintDeviceJWT(identity.DefaultTokenTTL)
	if err != nil {
		return err
	}
	return c.submitLocalAgentAuditLogsWithBearer(ctx, tok, logs)
}

func (c *Client) submitLocalAgentAuditLogsWithBearer(ctx context.Context, token string, logs []types.LocalAgentToolCallAuditLogInput) error {
	return c.api.WithToken(token).SubmitLocalAgentAuditLogs(ctx, logs)
}
