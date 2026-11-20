package enforce

import (
	"errors"
	"unsafe"

	"golang.org/x/sys/windows"
)

func (osProcesses) parent(pid int) (int, error) {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return 0, err
	}
	defer func() { _ = windows.CloseHandle(snapshot) }()
	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	for err = windows.Process32First(snapshot, &entry); err == nil; err = windows.Process32Next(snapshot, &entry) {
		if int(entry.ProcessID) == pid {
			return int(entry.ParentProcessID), nil
		}
	}
	return 0, errors.New("no such process")
}

// args reads the process's command line and splits it the way the C runtime
// does.
func (osProcesses) args(pid int) ([]string, error) {
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return nil, err
	}
	defer func() { _ = windows.CloseHandle(process) }()
	var size uint32
	_ = windows.NtQueryInformationProcess(process, windows.ProcessCommandLineInformation, nil, 0, &size)
	if size == 0 {
		return nil, errors.New("empty command line")
	}
	buf := make([]byte, size)
	if err := windows.NtQueryInformationProcess(process, windows.ProcessCommandLineInformation, unsafe.Pointer(&buf[0]), size, &size); err != nil {
		return nil, err
	}
	line := (*windows.NTUnicodeString)(unsafe.Pointer(&buf[0]))
	return windows.DecomposeCommandLine(line.String())
}
