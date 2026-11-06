// Package eventlog writes failures an administrator needs to see to the operating system's event log.
// Installer checks and scheduled scans run with no console anyone watches, so without it their errors would only
// reach per-user state files.
// Only Windows has an implementation; on other platforms every call is a no-op.
package eventlog

// Source is the Application event log source the MSI registers (see build/windows/eventlog-source.ps1).
const Source = "Obot Sentry"

// Event IDs, documented in the Windows install instructions.
const (
	// EnrollSucceeded: the installer enrolled the device.
	EnrollSucceeded uint32 = 100
	// EnrollRejected: the server rejected the installer's enrollment, so the install stopped.
	EnrollRejected uint32 = 101
	// EnrollDeferred: the installer couldn't enroll for another reason; scans enroll the device later.
	EnrollDeferred uint32 = 102
	// ScanFailed: a user's scans started failing, or failed for a new reason.
	ScanFailed uint32 = 110
	// ScanRecovered: a user's scans are submitting again after failing.
	ScanRecovered uint32 = 111
)

type level int

const (
	levelInfo level = iota
	levelWarning
	levelError
)

// Info writes an informational event. Like Warning and Error, it's best-effort: failures are dropped, since a
// missing event shouldn't fail the command that reports it.
func Info(id uint32, msg string) { write(levelInfo, id, msg) }

// Warning writes a warning event.
func Warning(id uint32, msg string) { write(levelWarning, id, msg) }

// Error writes an error event.
func Error(id uint32, msg string) { write(levelError, id, msg) }
