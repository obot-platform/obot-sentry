//go:build darwin

package hookinstall

import (
	"time"

	"golang.org/x/sys/unix"
)

// bootTime returns when the system last started, from kern.boottime.
func bootTime() (time.Time, error) {
	tv, err := unix.SysctlTimeval("kern.boottime")
	if err != nil {
		return time.Time{}, err
	}
	return time.Unix(tv.Sec, int64(tv.Usec)*int64(time.Microsecond)), nil
}
