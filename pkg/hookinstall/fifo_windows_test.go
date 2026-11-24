//go:build windows

package hookinstall

import "errors"

func mkfifo(string) error { return errors.New("unsupported") }
