//go:build windows

package hookinstall

import (
	"errors"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// wtsLocalServer is the WTSEnumerateSessions handle for the local session table.
const wtsLocalServer = windows.Handle(0)

// serviceAccountSIDs are the built-in accounts that own a logon session but are
// never the interactive console user.
//
// Every decision here is made on the session token's SID, never on the account
// name that comes back with it. A SID is the identity; the name is a rendering of
// it, and Windows localizes the built-in ones per install language, so comparing
// names would pass a service session through as a real user on any system whose
// language we did not anticipate.
var serviceAccountSIDs = []windows.WELL_KNOWN_SID_TYPE{
	windows.WinLocalSystemSid,
	windows.WinLocalServiceSid,
	windows.WinNetworkServiceSid,
}

// sessionUser is an account resolved from a logon session's access token. sid is
// the identity every decision is made on; name and home are read back out of it.
type sessionUser struct {
	sid  *windows.SID
	name string
	home string
}

// isServiceAccount reports whether u is one of the built-in service accounts.
func (u sessionUser) isServiceAccount() bool {
	for _, wellKnown := range serviceAccountSIDs {
		if u.sid.IsWellKnown(wellKnown) {
			return true
		}
	}
	return false
}

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

// isSystemToken reports whether token's user is the local SYSTEM account. This
// asks a narrower question than sessionUser.isServiceAccount above — SYSTEM alone,
// rather than any of the three built-in service accounts — because SYSTEM is the
// one an MDM agent legitimately runs the installer as, while the other two are
// only ever the wrong answer to "who is at the console".
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

// resolveTargetUser resolves the active console user on Windows. A SYSTEM
// install reads the identity out of the interactive session's own access token;
// an interactive install cannot open that token and falls back to its own, which
// is the same account for any normal elevated-Administrator run.
func resolveTargetUser() (*TargetUser, error) {
	user, err := findWindowsConsoleUser(interactiveSessionUsers, callingProcessUser)
	if err != nil {
		return nil, err
	}
	if err := validateHomeDir(user.home); err != nil {
		return nil, err
	}
	return &TargetUser{Username: user.name, HomeDir: user.home}, nil
}

// findWindowsConsoleUser picks the console user from the accounts owning the
// machine's interactive sessions, falling back to the account the installer
// itself runs as. Both platform lookups are injected so the service-account
// filtering and the fallback ordering can be exercised without a particular live
// session table.
func findWindowsConsoleUser(
	sessionUsers func() ([]sessionUser, error),
	callingUser func() (sessionUser, error),
) (sessionUser, error) {
	users, sessionErr := sessionUsers()
	for _, u := range users {
		if !u.isServiceAccount() {
			return u, nil
		}
	}
	if sessionErr == nil {
		sessionErr = errors.New("no interactive session is owned by a real user")
	}

	// WTSQueryUserToken requires SE_TCB_NAME, which only SYSTEM holds, so an
	// elevated-Administrator install resolves no session above and lands here:
	// its own token is the console user. A service caller has no console user to
	// stand in for, so it is rejected rather than pointed at its own profile.
	self, callerErr := callingUser()
	if callerErr == nil && self.isServiceAccount() {
		callerErr = fmt.Errorf("calling process runs as service account %q", self.name)
	}
	if callerErr == nil {
		return self, nil
	}
	return sessionUser{}, fmt.Errorf("no usable console user: %w", errors.Join(sessionErr, callerErr))
}

// interactiveSessionUsers returns the account owning each Active WTS session
// other than the services session. A session whose token cannot be opened is
// skipped, not fatal: that is the expected result for every session when the
// caller is not SYSTEM.
func interactiveSessionUsers() ([]sessionUser, error) {
	var (
		sessions *windows.WTS_SESSION_INFO
		count    uint32
	)
	if err := windows.WTSEnumerateSessions(wtsLocalServer, 0, 1, &sessions, &count); err != nil {
		return nil, fmt.Errorf("enumerating WTS sessions: %w", err)
	}
	defer windows.WTSFreeMemory(uintptr(unsafe.Pointer(sessions)))

	// sessions points at a C array of count WTS_SESSION_INFO structs.
	users := make([]sessionUser, 0, count)
	for _, s := range unsafe.Slice(sessions, count) {
		// Session 0 is the non-interactive services session.
		if s.State != windows.WTSActive || s.SessionID == 0 {
			continue
		}
		u, err := sessionTokenUser(s.SessionID)
		if err != nil {
			continue
		}
		users = append(users, u)
	}
	return users, nil
}

// sessionTokenUser resolves the account logged on to sessionID from that
// session's own primary access token. WTSQueryUserToken hands back a token owned
// by the caller, so the profile directory comes from GetUserProfileDirectory
// rather than an assumed C:\Users\<name>.
func sessionTokenUser(sessionID uint32) (sessionUser, error) {
	var token windows.Token
	if err := windows.WTSQueryUserToken(sessionID, &token); err != nil {
		return sessionUser{}, fmt.Errorf("querying user token for WTS session %d: %w", sessionID, err)
	}
	defer func() { _ = token.Close() }()
	return tokenUser(token)
}

// callingProcessUser resolves the account the installer itself runs as. It opens
// a real token handle rather than reusing the GetCurrentProcessToken pseudo
// handle, which userenv's GetUserProfileDirectory does not accept.
func callingProcessUser() (sessionUser, error) {
	var token windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY, &token); err != nil {
		return sessionUser{}, fmt.Errorf("opening current process token: %w", err)
	}
	defer func() { _ = token.Close() }()
	return tokenUser(token)
}

// tokenUser reads the account identity and profile directory out of an access
// token.
func tokenUser(token windows.Token) (sessionUser, error) {
	info, err := token.GetTokenUser()
	if err != nil {
		return sessionUser{}, fmt.Errorf("reading token user: %w", err)
	}
	sid := info.User.Sid
	home, err := token.GetUserProfileDirectory()
	if err != nil {
		return sessionUser{}, fmt.Errorf("querying profile directory for %s: %w", sid, err)
	}
	return sessionUser{sid: sid, name: accountName(sid), home: home}, nil
}

// accountName renders sid for operator-facing output. The name is never used to
// decide anything, so a lookup that cannot reach the account database — a
// domain account on a machine off the corporate network — degrades to the SID
// string instead of failing an install whose home directory resolved fine.
func accountName(sid *windows.SID) string {
	account, domain, _, err := sid.LookupAccount("")
	switch {
	case err != nil:
		return sid.String()
	case domain == "":
		return account
	default:
		return domain + `\` + account
	}
}
