//go:build !windows && !darwin

package identity

import (
	"fmt"
	"os"
	"strings"
)

// machineID reads the systemd/dbus machine ID.
func machineID() (string, error) {
	for _, p := range []string{"/etc/machine-id", "/var/lib/dbus/machine-id"} {
		if b, err := os.ReadFile(p); err == nil {
			if id := strings.TrimSpace(string(b)); id != "" {
				return id, nil
			}
		}
	}
	return "", fmt.Errorf("no machine id found")
}
