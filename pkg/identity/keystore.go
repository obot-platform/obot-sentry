package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// keyFile is the on-disk name of the device identity key, stored as
// PKCS#8 PEM. It lives in the machine-scoped data dir and must be
// readable by every user (scans run per user, all presenting the same
// device identity), so it is written 0644; in the per-user fallback
// dir the 0700 directory still keeps it private. The key authorizes
// nothing beyond submitting scans as this device.
const keyFile = "device_key.pem"

// loadOrCreateKey returns the shared Ed25519 identity key from dir,
// generating and persisting one on the machine's first run. Creation
// uses O_EXCL so concurrent first runs by different users converge on
// a single key: the losers re-read the winner's file, waiting briefly
// if the winner has created it but not yet finished writing it.
func loadOrCreateKey(dir string) (ed25519.PrivateKey, error) {
	p := filepath.Join(dir, keyFile)

	if b, err := os.ReadFile(p); err == nil {
		if key, err := parseKeyPEM(b); err == nil {
			return key, nil
		}
		// Present but not parseable: another first run may be mid-write.
		return awaitKey(p)
	} else if !os.IsNotExist(err) {
		return nil, err
	}

	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	b := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})

	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if os.IsExist(err) {
		// Another user's first run won the race; use their key.
		return awaitKey(p)
	} else if err != nil {
		return nil, err
	}

	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("persisting identity key: %w", err)
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	return key, nil
}

// awaitKey re-reads the key file at p, which is known to exist, retrying
// briefly in case the first run that created it is still writing it.
func awaitKey(p string) (ed25519.PrivateKey, error) {
	var lastErr error
	for range 5 {
		time.Sleep(100 * time.Millisecond)
		b, err := os.ReadFile(p)
		if err == nil {
			key, perr := parseKeyPEM(b)
			if perr == nil {
				return key, nil
			}
			err = perr
		}
		lastErr = err
	}
	return nil, fmt.Errorf("device key %s exists but is unreadable: %w", p, lastErr)
}

func parseKeyPEM(b []byte) (ed25519.PrivateKey, error) {
	block, _ := pem.Decode(b)
	if block == nil || block.Type != "PRIVATE KEY" {
		return nil, fmt.Errorf("device key: no PRIVATE KEY block")
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("device key: %w", err)
	}
	key, ok := k.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("device key: unsupported key type %T", k)
	}
	return key, nil
}
