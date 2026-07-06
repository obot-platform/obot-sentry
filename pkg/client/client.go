// Package client wraps the obot apiclient for the two calls a device
// makes: enrolling its identity key (bearer: ode1 enrollment
// credential) and submitting scans (bearer: self-signed device JWT).
package client

import (
	"context"
	"os"
	"runtime"
	"strings"

	"github.com/obot-platform/obot/apiclient"
	"github.com/obot-platform/obot/apiclient/types"

	"github.com/obot-platform/obocop/pkg/identity"
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
// idempotent update server-side. hostname may be an MDM-configured
// override; empty falls back to os.Hostname.
func (c *Client) Enroll(ctx context.Context, credential string, id *identity.Identity, hostname string) (*types.Device, error) {
	if hostname == "" {
		hostname, _ = os.Hostname()
	}
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
