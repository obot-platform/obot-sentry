//go:build darwin

package hookinstall

import (
	"time"

	"golang.org/x/sys/unix"
)

// The run log sits with the system's other logs, where Console.app shows it.
// The schedule's record lives under /Library/Application Support/obot, which
// postinstall creates root-owned 0755 — unlike the 1777 obot-sentry directory
// beneath it, which any user can write.
const (
	darwinRunLogPath   = "/Library/Logs/obot/hook-install.log"
	darwinRunStatePath = "/Library/Application Support/obot/hook-install/state.json"
)

func runLogPath() (string, error)   { return darwinRunLogPath, nil }
func runStatePath() (string, error) { return darwinRunStatePath, nil }

func systemBootTime() (time.Time, error) {
	tv, err := unix.SysctlTimeval("kern.boottime")
	if err != nil {
		return time.Time{}, err
	}
	return time.Unix(tv.Sec, int64(tv.Usec)*int64(time.Microsecond)), nil
}
