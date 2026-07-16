package hookinstall

import "strings"

const (
	// managedByFlag is the flag that marks a hook command as obocop-owned. It is
	// accepted (and ignored) by `obocop audit submit` precisely so the installer
	// can recognize and replace its own entries on every run.
	managedByFlag = "--managed-by"
	// managedByValue is the sole accepted marker value.
	managedByValue = "obocop"
	// managedMarker is the exact token pair every command this package generates
	// carries, and the signal used to recognize obocop-owned entries.
	managedMarker = managedByFlag + " " + managedByValue
)

// IsOwnedCommand reports whether command is an obocop-managed hook command,
// identified by the `--managed-by obocop` marker. Any command without the marker
// is left untouched. A substring check is sufficient because every command this
// package writes carries the marker in exactly this form; there are no
// pre-existing obocop-owned hook configurations in other layouts to recognize.
func IsOwnedCommand(command string) bool {
	return strings.Contains(command, managedMarker)
}
