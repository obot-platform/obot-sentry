//go:build !windows

package scan

import "context"

// wslRoots only applies on Windows, where WSL homes are reachable via
// the \\wsl.localhost share.
func wslRoots(context.Context) []Root { return nil }
