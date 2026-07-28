package hookinstall

import (
	"fmt"
	"os"
)

type TargetUser struct {
	Username string
	HomeDir  string
	UID      int
	GID      int
}

func validateHomeDir(home string) error {
	if home == "" {
		return fmt.Errorf("resolved console user has no home directory")
	}
	info, err := os.Lstat(home)
	if err != nil {
		return fmt.Errorf("resolved home %q is not accessible: %w", home, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("resolved home %q is not a directory", home)
	}
	return nil
}
