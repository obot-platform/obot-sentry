//go:build !darwin && !windows

package hookinstall

import "time"

func runLogPath() (string, error)   { return "", errUnsupportedPlatform }
func runStatePath() (string, error) { return "", errUnsupportedPlatform }

func systemBootTime() (time.Time, error) { return time.Time{}, errUnsupportedPlatform }
