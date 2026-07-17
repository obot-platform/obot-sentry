//go:build windows

package hookinstall

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// checkPrivilege accepts either an elevated Administrator token or the SYSTEM
// account. Under UAC, membership in the Administrators group without an elevated
// token is not sufficient to write machine policy, so a filtered (non-elevated)
// admin token is rejected.
func checkPrivilege() error {
	token := windows.GetCurrentProcessToken()
	if isSystemToken(token) {
		return nil
	}
	if token.IsElevated() {
		return nil
	}
	return fmt.Errorf("obot-sentry hook-install must run from an elevated Administrator or SYSTEM token on Windows")
}

// isSystemToken reports whether token's user is the local SYSTEM account.
func isSystemToken(token windows.Token) bool {
	user, err := token.GetTokenUser()
	if err != nil {
		return false
	}
	systemSID, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return false
	}
	return user.User.Sid.Equals(systemSID)
}
