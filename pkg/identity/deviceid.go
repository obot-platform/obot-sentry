package identity

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

// deviceIDNamespace is the fixed UUIDv5 namespace device IDs are
// derived under. Deriving (rather than transmitting the raw machine
// GUID) keeps hardware identifiers off the wire while staying stable
// across re-installs.
var deviceIDNamespace = uuid.NewSHA1(uuid.NameSpaceDNS, []byte("device-id.obocop.obot.ai"))

// DeriveDeviceID computes the machine's logical device ID from its
// hardware ID and the identity key's fingerprint. Binding the key into
// the ID means a device ID can never collide with a different key
// server-side (TOFU-safe): losing the key mints a fresh device ID and
// the machine re-enrolls cleanly, orphaning the old device record.
func DeriveDeviceID(machineID, keyFingerprint string) string {
	seed := strings.ToLower(machineID) + "\x00" + keyFingerprint
	return uuid.NewSHA1(deviceIDNamespace, []byte(seed)).String()
}

// fallbackMachineIDFile persists a random stand-in machine ID for hosts
// where the hardware identifier is unreadable.
const fallbackMachineIDFile = "machine_id"

// machineIDOrFallback returns the hardware machine ID, falling back to
// a random UUID persisted in dir so the derived device ID stays stable
// across runs. Like the key file, the fallback is shared (0644,
// exclusive create) so concurrent users converge on one value.
func machineIDOrFallback(dir string) (string, error) {
	if id, err := machineID(); err == nil && id != "" {
		return id, nil
	}

	p := filepath.Join(dir, fallbackMachineIDFile)
	if b, err := os.ReadFile(p); err == nil {
		if id := strings.TrimSpace(string(b)); id != "" {
			return id, nil
		}
	}

	id := uuid.NewString()
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if os.IsExist(err) {
		if b, err := os.ReadFile(p); err == nil {
			if id := strings.TrimSpace(string(b)); id != "" {
				return id, nil
			}
		}
		return "", fmt.Errorf("fallback machine id %s exists but is unreadable", p)
	} else if err != nil {
		return "", fmt.Errorf("persisting fallback machine id: %w", err)
	}
	if _, err := f.Write([]byte(id + "\n")); err != nil {
		_ = f.Close()
		return "", err
	}
	return id, f.Close()
}
