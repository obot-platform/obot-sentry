package enforce

import "golang.org/x/sys/windows"

// accountHomeDir returns the profile directory Windows records for the account
// the process runs as, read from the process token rather than USERPROFILE.
//
// os/user.Current would give the same directory, but it also looks up the
// account's display name, which on a domain-joined machine can wait on a domain
// controller. The hook runs before every tool call, so it asks only for the
// directory.
func accountHomeDir() (string, error) {
	var token windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY, &token); err != nil {
		return "", err
	}
	defer func() { _ = token.Close() }()
	return token.GetUserProfileDirectory()
}
