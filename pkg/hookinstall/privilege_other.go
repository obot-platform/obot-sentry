//go:build !darwin && !windows

package hookinstall

// checkPrivilege is never reached on unsupported platforms: the platform check
// in install.go rejects them before any privilege or config work. It exists so
// the package compiles and links on Linux and other GOOS values (the repo runs
// `go vet` and `go build` on Linux and cross-compiles for Windows).
func checkPrivilege() error {
	return errUnsupportedPlatform
}
