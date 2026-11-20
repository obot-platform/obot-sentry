package enforce

import "os"

// processTable reads other processes on the machine: the hook's own ancestry,
// and the arguments of the Claude Code process that ran it.
type processTable interface {
	// self is the calling process's ID.
	self() int
	// parent returns a process's parent process ID.
	parent(pid int) (int, error)
	// args returns a process's command-line arguments, the program first.
	args(pid int) ([]string, error)
}

// osProcesses reads the live process table. Its parent and args are per
// platform.
type osProcesses struct{}

func (osProcesses) self() int { return os.Getpid() }

func (e Env) processes() processTable {
	if e.procs == nil {
		return osProcesses{}
	}
	return e.procs
}
