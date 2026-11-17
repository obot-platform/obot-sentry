//go:build windows

package hookinstall

import "golang.org/x/sys/windows/registry"

// detectClaudePolicy reads the machine policy value. The per-user HKCU value is
// not consulted: Claude Code uses it only when no administrator source exists,
// so it never outranks managed-settings.json.
func detectClaudePolicy(*TargetUser) ClaudePolicy {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, claudePolicyRegistryKey, registry.QUERY_VALUE|registry.WOW64_64KEY)
	if err != nil {
		return ClaudePolicy{}
	}
	defer func() { _ = k.Close() }()

	source := `HKLM\` + claudePolicyRegistryKey
	document, _, err := k.GetStringValue(claudePolicyRegistryName)
	if err == registry.ErrNotExist {
		return ClaudePolicy{}
	}
	if err != nil {
		return unreadableClaudePolicy(source)
	}
	return claudePolicyFromJSON(source, document)
}
