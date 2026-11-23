//go:build !windows

package enforce

import "os/user"

// accountHomeDir returns the home directory the operating system records for
// the account the process runs as. os/user reads the account database, and
// unlike os.UserHomeDir never consults HOME.
func accountHomeDir() (string, error) {
	u, err := user.Current()
	if err != nil {
		return "", err
	}
	return u.HomeDir, nil
}
