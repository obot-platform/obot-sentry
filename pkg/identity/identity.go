// Package identity manages the machine's shared device identity: a
// stable derived device ID plus an Ed25519 keypair whose public half is
// registered with the obot server at enrollment (trust-on-first-use)
// and whose private half signs the device access JWTs presented on
// every scan submission.
//
// The key lives in a machine-scoped directory readable by every user,
// so all users on a machine present a single device. The device ID is
// derived from the machine ID and the key's fingerprint: a lost and
// regenerated key therefore yields a fresh device ID automatically,
// which sidesteps the server's one-key-per-device (TOFU) conflict
// without any recovery state.
package identity

import (
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
)

// Identity is a loaded device identity.
type Identity struct {
	// DeviceID is the derived logical device ID (see DeriveDeviceID).
	DeviceID string
	// Key is the Ed25519 identity key.
	Key ed25519.PrivateKey
	// PublicKeyDER is the PKIX/SubjectPublicKeyInfo encoding of the
	// public key — the exact bytes sent in DeviceEnrollRequest.PublicKey.
	PublicKeyDER []byte
}

// Load loads (or generates, on the machine's first run) the shared
// identity key under dir and derives the device ID. dir should be the
// machine-scoped data directory so every user resolves the same
// identity; a per-user directory also works (each user then appears as
// its own device).
func Load(dir string) (*Identity, error) {
	machID, err := machineIDOrFallback(dir)
	if err != nil {
		return nil, err
	}

	key, err := loadOrCreateKey(dir)
	if err != nil {
		return nil, err
	}
	pubDER, err := x509.MarshalPKIXPublicKey(key.Public())
	if err != nil {
		return nil, err
	}

	return &Identity{
		DeviceID:     DeriveDeviceID(machID, fingerprint(pubDER)),
		Key:          key,
		PublicKeyDER: pubDER,
	}, nil
}

// PublicKeyFingerprint returns the SHA-256 of the PKIX DER, base64
// (std) encoded — a compact stable handle for state and status output.
func (i *Identity) PublicKeyFingerprint() string {
	return fingerprint(i.PublicKeyDER)
}

func fingerprint(pubDER []byte) string {
	sum := sha256.Sum256(pubDER)
	return base64.StdEncoding.EncodeToString(sum[:])
}
