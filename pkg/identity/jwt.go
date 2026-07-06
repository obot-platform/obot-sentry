package identity

import (
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// DeviceTokenAudience is the audience the obot server's device
// authenticator claims; every device access JWT must carry it.
const DeviceTokenAudience = "obot/device"

// DefaultTokenTTL bounds a device access JWT's lifetime. A scan submit
// is a single request and the device can re-sign at will, so tokens are
// minted fresh per request and kept short to limit replay.
const DefaultTokenTTL = 5 * time.Minute

// MintDeviceJWT signs a short-lived device access token with the
// identity key, shaped exactly as the server's DeviceAuthenticator
// validates: iss == sub == deviceID, aud contains obot/device, exp
// required, asymmetric alg (EdDSA for our Ed25519 keys).
func (i *Identity) MintDeviceJWT(ttl time.Duration) (string, error) {
	now := time.Now()
	claims := jwt.RegisteredClaims{
		Issuer:    i.DeviceID,
		Subject:   i.DeviceID,
		Audience:  jwt.ClaimStrings{DeviceTokenAudience},
		ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
		IssuedAt:  jwt.NewNumericDate(now),
		NotBefore: jwt.NewNumericDate(now.Add(-30 * time.Second)), // clock skew
		ID:        uuid.NewString(),
	}
	return jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims).SignedString(i.Key)
}
