//go:build !darwin && !windows

package hookinstall

import "time"

func bootTime() (time.Time, error) {
	return time.Time{}, errUnsupportedPlatform
}
