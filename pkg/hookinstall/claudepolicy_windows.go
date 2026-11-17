//go:build windows

package hookinstall

import (
	"errors"

	"golang.org/x/sys/windows/registry"
)

// detectClaudePolicy reads the machine policy value. The per-user HKCU value is
// not consulted: Claude Code uses it only when no administrator source exists,
// so it never outranks managed-settings.json.
//
// Only a key or value that does not exist means there is no policy. One that
// exists and cannot be opened or read is treated as a policy, like any other
// unreadable one.
func detectClaudePolicy(*TargetUser) ClaudePolicy {
	source := `HKLM\` + claudePolicyRegistryKey
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, claudePolicyRegistryKey, registry.QUERY_VALUE|registry.WOW64_64KEY)
	if errors.Is(err, registry.ErrNotExist) {
		return ClaudePolicy{}
	}
	if err != nil {
		return unreadableClaudePolicy(source)
	}
	defer func() { _ = k.Close() }()

	document, _, err := k.GetStringValue(claudePolicyRegistryName)
	if errors.Is(err, registry.ErrNotExist) {
		return ClaudePolicy{}
	}
	if err != nil {
		return unreadableClaudePolicy(source)
	}
	return claudePolicyFromJSON(source, document)
}
