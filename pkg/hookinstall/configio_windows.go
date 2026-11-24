//go:build windows

package hookinstall

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// configReadFlags is a plain read on Windows, which has no FIFOs in the file
// system; an open held up by an oplock is bounded by the group time limit.
const configReadFlags = os.O_RDONLY

// chownToUser is a no-op on Windows: new files inherit their DACL from the parent.
func chownToUser(_ *os.Root, _ *TargetUser, _ []string) error {
	return nil
}

// chownFile is a no-op on Windows, for the same reason.
func chownFile(_ *os.File, _ *TargetUser) error {
	return nil
}

// checkConfigFileIdentity refuses a config with more than one link. Windows
// owners are not compared: files SYSTEM creates in a profile are owned by SYSTEM
// or Administrators, and the user owns the ones they create.
func checkConfigFileIdentity(f *os.File, _ os.FileInfo, _ Scope, _ *TargetUser, absPath string) error {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(f.Fd()), &info); err != nil {
		return fmt.Errorf("reading the identity of config %q: %w", absPath, err)
	}
	if info.NumberOfLinks != 1 {
		return fmt.Errorf("refusing config %q with %d hard links", absPath, info.NumberOfLinks)
	}
	return nil
}

// secureMachineConfigDir is a no-op on Windows: lockMachineDirs has already
// created or taken over the directory and set its ACL through a handle, and
// holds it for the run. Setting it again here by path is what let a planted
// junction redirect the ACL change.
func secureMachineConfigDir(string) error { return nil }

// secureMachineConfig gives the hook file the machine-config ACL — users may
// read and run it, only SYSTEM and Administrators change it — through a handle
// to the file itself, refused if it is a link or has a second name.
func secureMachineConfig(absPath string) error {
	name, err := windows.UTF16PtrFromString(absPath)
	if err != nil {
		return err
	}
	h, err := windows.CreateFile(name,
		windows.READ_CONTROL|windows.WRITE_DAC|windows.WRITE_OWNER|windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return fmt.Errorf("opening %q to secure it: %w", absPath, err)
	}
	defer func() { _ = windows.CloseHandle(h) }()
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		return fmt.Errorf("reading %q: %w", absPath, err)
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || info.NumberOfLinks != 1 {
		return fmt.Errorf("refusing to secure %q: it is a link", absPath)
	}
	if err := setSecurityOnHandle(h, machineConfigFileSDDL, true); err != nil {
		return fmt.Errorf("securing machine config %q: %w", absPath, err)
	}
	return nil
}
