package hookinstall

import (
	"fmt"
	"io"
	"os"
	"time"
)

// runDir is the verified, open directory holding the run log and the
// schedule's record. Every file operation in it goes through root, a handle to
// the directory checked by openRunDir, never through its path again, so the
// directory cannot be swapped for a link between the check and the use.
type runDir struct {
	path    string
	root    *os.Root
	release func()
}

func (d *runDir) Close() {
	_ = d.root.Close()
	d.release()
}

// runFile is an open file in a runDir that closes the directory with it.
type runFile struct {
	*os.File
	dir *runDir
}

func (f runFile) Close() error {
	err := f.File.Close()
	f.dir.Close()
	return err
}

var _ io.WriteCloser = runFile{}

// runDirTimeout bounds opening the run directory. It runs outside any
// convergence group, so without a limit a directory that blocks its opener
// would hold up the whole check.
const runDirTimeout = 30 * time.Second

// openRunDir is openRunDirNow under runDirTimeout. If the open completes after
// the deadline, the late runDir is closed.
func openRunDir(dir string, create bool) (*runDir, error) {
	type result struct {
		d   *runDir
		err error
	}
	// Unbuffered, so a result that arrives after the deadline is never left
	// in a buffer: the opener either hands it over or, abandoned, closes it.
	done := make(chan result)
	timer := time.NewTimer(runDirTimeout)
	defer timer.Stop()
	abandoned := make(chan struct{})
	go func() {
		d, err := openRunDirNow(dir, create)
		select {
		case done <- result{d, err}:
		case <-abandoned:
			if d != nil {
				d.Close()
			}
		}
	}()
	select {
	case r := <-done:
		return r.d, r.err
	case <-timer.C:
		close(abandoned)
		select {
		case r := <-done:
			return r.d, r.err
		default:
		}
		return nil, fmt.Errorf("opening %s timed out after %s", dir, runDirTimeout)
	}
}
