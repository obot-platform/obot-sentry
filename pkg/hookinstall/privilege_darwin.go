//go:build darwin

package hookinstall

import (
	"fmt"
	"os"
)

// checkPrivilege requires an effective UID of 0 on macOS. Machine policy and
// per-user files under other accounts' homes both require root; anything less
// is rejected before the first config write.
func checkPrivilege() error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("obot-sentry hook-install must run as root on macOS; rerun with sudo")
	}
	return nil
}
