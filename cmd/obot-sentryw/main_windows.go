// Command obot-sentryw runs obot-sentry.exe with no console window. The scan
// task runs in interactive sessions, where console-subsystem binaries are
// visible; obot-sentry.exe is one, so that it stays usable by hand.
package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/windows"
)

const target = "obot-sentry.exe"

func main() {
	self, err := os.Executable()
	if err != nil {
		os.Exit(1) // No console to report to.
	}

	cmd := exec.Command(filepath.Join(filepath.Dir(self), target), os.Args[1:]...)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
	// Nil streams hand the child os.DevNull; the scan logs its own outcome.
	// Run, not Start: the scheduler's time limit and IgnoreNew track this pid.
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			os.Exit(exitErr.ExitCode())
		}
		os.Exit(1)
	}
}
