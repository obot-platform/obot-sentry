package identity

import (
	"fmt"
	"os/exec"
	"strings"
)

// machineID reads the stable IOPlatformUUID via ioreg. No cgo/IOKit
// binding needed; the value survives OS reinstalls on the same board.
func machineID() (string, error) {
	out, err := exec.Command("ioreg", "-rd1", "-c", "IOPlatformExpertDevice").Output()
	if err != nil {
		return "", err
	}
	for line := range strings.Lines(string(out)) {
		if !strings.Contains(line, "IOPlatformUUID") {
			continue
		}
		_, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		return strings.Trim(strings.TrimSpace(value), `"`), nil
	}
	return "", fmt.Errorf("IOPlatformUUID not found in ioreg output")
}
