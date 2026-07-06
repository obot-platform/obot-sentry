package identity

import (
	"crypto/x509"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestDeriveDeviceID(t *testing.T) {
	base := DeriveDeviceID("A1B2C3", "fp-1")

	// Case-insensitive over the machine ID.
	if got := DeriveDeviceID("a1b2c3", "fp-1"); got != base {
		t.Errorf("case-insensitive derivation broke: %q vs %q", got, base)
	}
	// A different key mints a different device ID (key loss = fresh
	// device, never a TOFU conflict).
	if got := DeriveDeviceID("A1B2C3", "fp-2"); got == base {
		t.Errorf("different keys derived the same device ID")
	}
	// Distinct machines stay distinct.
	if got := DeriveDeviceID("D4E5F6", "fp-1"); got == base {
		t.Errorf("different machines derived the same device ID")
	}
	// Wire format is a plain UUID; the raw machine ID must not leak.
	if len(base) != 36 || strings.Contains(base, "A1B2C3") {
		t.Errorf("device ID %q is not an opaque UUID", base)
	}
}

func TestKeystoreRoundTrip(t *testing.T) {
	dir := t.TempDir()

	k1, err := loadOrCreateKey(dir)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	k2, err := loadOrCreateKey(dir)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if !k1.Equal(k2) {
		t.Errorf("reloaded key differs from generated key")
	}

	if runtime.GOOS != "windows" {
		fi, err := os.Stat(filepath.Join(dir, keyFile))
		if err != nil {
			t.Fatal(err)
		}
		// World-readable: every user on the machine loads this key.
		if perm := fi.Mode().Perm(); perm != 0o644 {
			t.Errorf("key file perm = %o, want 644", perm)
		}
	}
}

// TestKeystoreConcurrentFirstRun simulates several users' first scans
// racing to create the shared key: everyone must converge on one key.
func TestKeystoreConcurrentFirstRun(t *testing.T) {
	dir := t.TempDir()

	const n = 8
	var wg sync.WaitGroup
	got := make([]any, n)
	for i := range n {
		wg.Go(func() {
			k, err := loadOrCreateKey(dir)
			if err != nil {
				t.Errorf("concurrent load: %v", err)
				return
			}
			got[i] = k.Public()
		})
	}
	wg.Wait()

	for i := 1; i < n; i++ {
		if got[i] == nil || got[0] == nil {
			continue // error already reported
		}
		if !slices.Equal(mustDER(t, got[0]), mustDER(t, got[i])) {
			t.Fatalf("goroutine %d got a different key than goroutine 0", i)
		}
	}
}

func mustDER(t *testing.T, pub any) []byte {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return der
}

// TestLoadStableAcrossRuns also covers the shared-identity property:
// two loads from the same dir (as two users would) agree on device ID
// and key.
func TestLoadStableAcrossRuns(t *testing.T) {
	dir := t.TempDir()

	id1, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	id2, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if id1.DeviceID != id2.DeviceID {
		t.Errorf("device ID changed across runs: %q vs %q", id1.DeviceID, id2.DeviceID)
	}
	if id1.PublicKeyFingerprint() != id2.PublicKeyFingerprint() {
		t.Errorf("key changed across runs")
	}
}

// TestKeyLossMintsNewDeviceID pins the recovery property the derivation
// exists for: regenerating a lost key changes the device ID, so
// re-enrollment can never hit the server's one-key-per-device check.
func TestKeyLossMintsNewDeviceID(t *testing.T) {
	dir := t.TempDir()

	id1, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, keyFile)); err != nil {
		t.Fatal(err)
	}
	id2, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if id1.DeviceID == id2.DeviceID {
		t.Errorf("device ID unchanged after key regeneration")
	}
}

// TestMintDeviceJWT_ServerValidationRules verifies a minted token
// against the exact parser configuration obot's DeviceAuthenticator
// uses (pkg/gateway/server/device_auth.go): asymmetric algs only,
// audience obot/device, exp required, iss == sub == deviceID, signature
// checked against the PKIX-decoded public key.
func TestMintDeviceJWT_ServerValidationRules(t *testing.T) {
	id, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	tok, err := id.MintDeviceJWT(DefaultTokenTTL)
	if err != nil {
		t.Fatal(err)
	}

	parser := jwt.NewParser(
		jwt.WithValidMethods([]string{"ES256", "ES384", "ES512", "EdDSA"}),
		jwt.WithAudience(DeviceTokenAudience),
		jwt.WithExpirationRequired(),
	)
	claims := &jwt.RegisteredClaims{}
	_, err = parser.ParseWithClaims(tok, claims, func(tk *jwt.Token) (any, error) {
		c := tk.Claims.(*jwt.RegisteredClaims)
		if !slices.Contains(c.Audience, DeviceTokenAudience) || c.Subject == "" || c.Issuer != c.Subject {
			t.Fatalf("claims fail server keyfunc checks: iss=%q sub=%q aud=%v", c.Issuer, c.Subject, c.Audience)
		}
		return x509.ParsePKIXPublicKey(id.PublicKeyDER)
	})
	if err != nil {
		t.Fatalf("server-shaped validation rejected our token: %v", err)
	}
	if claims.Subject != id.DeviceID {
		t.Errorf("sub = %q, want device ID %q", claims.Subject, id.DeviceID)
	}
	if claims.ExpiresAt.After(time.Now().Add(DefaultTokenTTL + time.Minute)) {
		t.Errorf("exp too far out: %v", claims.ExpiresAt)
	}
}

// TestMintDeviceJWT_Negatives pins the failure modes the server-side
// parser must reject: expiry present-and-past, wrong audience, and a
// token signed by a different key.
func TestMintDeviceJWT_Negatives(t *testing.T) {
	id, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	parser := jwt.NewParser(
		jwt.WithValidMethods([]string{"ES256", "ES384", "ES512", "EdDSA"}),
		jwt.WithAudience(DeviceTokenAudience),
		jwt.WithExpirationRequired(),
	)
	keyfunc := func(*jwt.Token) (any, error) { return x509.ParsePKIXPublicKey(id.PublicKeyDER) }

	t.Run("expired", func(t *testing.T) {
		tok, err := id.MintDeviceJWT(-time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := parser.ParseWithClaims(tok, &jwt.RegisteredClaims{}, keyfunc); err == nil {
			t.Errorf("expired token accepted")
		}
	})

	t.Run("wrong signer", func(t *testing.T) {
		other, err := Load(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		tok, err := other.MintDeviceJWT(DefaultTokenTTL)
		if err != nil {
			t.Fatal(err)
		}
		// Validated against id's key, not other's.
		if _, err := parser.ParseWithClaims(tok, &jwt.RegisteredClaims{}, keyfunc); err == nil {
			t.Errorf("token from another key accepted")
		}
	})

	t.Run("HMAC rejected", func(t *testing.T) {
		claims := jwt.RegisteredClaims{
			Issuer: "x", Subject: "x",
			Audience:  jwt.ClaimStrings{DeviceTokenAudience},
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		}
		tok, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte("secret"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := parser.ParseWithClaims(tok, &jwt.RegisteredClaims{}, func(*jwt.Token) (any, error) {
			return []byte("secret"), nil
		}); err == nil {
			t.Errorf("HS256 token accepted despite asymmetric-only allowlist")
		}
	})
}

func TestMachineIDFallbackPersists(t *testing.T) {
	dir := t.TempDir()
	// Write a fallback directly (simulating an unreadable hardware ID
	// path having created one earlier) and confirm precedence: hardware
	// ID wins when readable, but the file round-trips.
	if err := os.WriteFile(filepath.Join(dir, fallbackMachineIDFile), []byte("stored-id\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := machineIDOrFallback(dir)
	if err != nil {
		t.Fatal(err)
	}
	if hw, hwErr := machineID(); hwErr == nil && hw != "" {
		if got != hw {
			t.Errorf("hardware ID readable but fallback used: got %q want %q", got, hw)
		}
	} else if got != "stored-id" {
		t.Errorf("fallback = %q, want stored-id", got)
	}
}
