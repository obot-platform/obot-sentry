//go:build !darwin && !windows

package enforce

import (
	"bytes"
	"errors"
	"os"
	"strconv"
	"strings"
)

// obot-sentry ships for darwin and windows only; this reads Linux's /proc so the
// package still builds and runs elsewhere.

func (osProcesses) parent(pid int) (int, error) {
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, err
	}
	// The command name is parenthesized and may contain anything, so the fields
	// after it are read from the last ')'.
	i := bytes.LastIndexByte(stat, ')')
	if i < 0 {
		return 0, errors.New("malformed /proc stat")
	}
	fields := strings.Fields(string(stat[i+1:]))
	if len(fields) < 2 {
		return 0, errors.New("malformed /proc stat")
	}
	return strconv.Atoi(fields[1])
}

func (osProcesses) args(pid int) ([]string, error) {
	cmdline, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
	if err != nil {
		return nil, err
	}
	return strings.Split(strings.TrimSuffix(string(cmdline), "\x00"), "\x00"), nil
}
