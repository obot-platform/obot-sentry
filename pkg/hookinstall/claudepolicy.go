package hookinstall

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"

	"howett.net/plist"
)

// Claude Code reads managed settings from more than one place, and by default
// applies only the highest-ranked one that is present: server-managed settings,
// then an MDM policy (a managed preference on macOS, a registry value on
// Windows), then managed-settings.json. A machine with an MDM policy therefore
// ignores the file hook-install writes, hooks included, unless that policy sets
// managedSourcesBehavior to "merge".
//
// This file detects that case so the installer can report Claude Code as not
// converged instead of reporting hooks that do not run as installed. It never
// falls back to the user's settings: hooks there are skipped by a session with a
// different CLAUDE_CONFIG_DIR and can be deleted by the user. Server-managed
// settings have the same effect as an MDM policy but are fetched by Claude Code
// per account at run time, so they cannot be detected here.

// ClaudePolicy describes the MDM-delivered Claude Code policy on this machine.
// The zero value means there is none that matters.
type ClaudePolicy struct {
	// Source names where the policy was read from, for the summary.
	Source string
	// SupersedesFile reports that Claude Code ignores managed-settings.json in
	// favor of this policy.
	SupersedesFile bool
}

const (
	claudePolicyDomain       = "com.anthropic.claudecode"
	claudeManagedPrefsDarwin = "/Library/Managed Preferences"
	claudePolicyRegistryKey  = `SOFTWARE\Policies\ClaudeCode`
	claudePolicyRegistryName = "Settings"
	claudeSourcesBehaviorKey = "managedSourcesBehavior"
	claudeSourcesMerge       = "merge"
)

// claudeNonPolicyKeys are the settings that do not make a source count as
// present: a source holding nothing else does not outrank a lower one.
var claudeNonPolicyKeys = []string{claudeSourcesBehaviorKey, "wslInheritsWindowsSettings"}

// claudePlistPaths returns the managed preference files Claude Code reads on
// macOS, the console user's before the device's.
func claudePlistPaths(u *TargetUser) []string {
	paths := make([]string, 0, 2)
	if u != nil && u.Username != "" && u.Username == filepath.Base(u.Username) {
		paths = append(paths, filepath.Join(claudeManagedPrefsDarwin, u.Username, claudePolicyDomain+".plist"))
	}
	return append(paths, filepath.Join(claudeManagedPrefsDarwin, claudePolicyDomain+".plist"))
}

// claudePolicyFromPlists reads the first managed preference file that settles
// the question: one that turns merging on, or one that holds a policy.
func claudePolicyFromPlists(paths []string) ClaudePolicy {
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return unreadableClaudePolicy(path)
		}
		var settings map[string]any
		if _, err := plist.Unmarshal(data, &settings); err != nil {
			return unreadableClaudePolicy(path)
		}
		if settings[claudeSourcesBehaviorKey] == claudeSourcesMerge {
			return ClaudePolicy{}
		}
		if policy := claudePolicyFromSettings(path, settings); policy.SupersedesFile {
			return policy
		}
	}
	return ClaudePolicy{}
}

// claudePolicyFromJSON reads the policy Claude Code takes from a registry value
// holding a JSON settings document.
func claudePolicyFromJSON(source, document string) ClaudePolicy {
	var settings map[string]any
	if err := json.Unmarshal([]byte(document), &settings); err != nil {
		return unreadableClaudePolicy(source)
	}
	return claudePolicyFromSettings(source, settings)
}

// unreadableClaudePolicy is the answer for a policy that exists and cannot be
// read. It is treated as superseding the file: a failure reported for hooks that
// do run gets looked at, and a success reported for hooks that do not run does
// not.
func unreadableClaudePolicy(source string) ClaudePolicy {
	return ClaudePolicy{Source: source, SupersedesFile: true}
}

// claudePolicyFromSettings decides whether one policy document outranks
// managed-settings.json, by Claude Code's own rule: it holds at least one
// setting with a value, and does not ask for the sources to be merged.
func claudePolicyFromSettings(source string, settings map[string]any) ClaudePolicy {
	if settings[claudeSourcesBehaviorKey] == claudeSourcesMerge {
		return ClaudePolicy{}
	}
	for key, value := range settings {
		if !slices.Contains(claudeNonPolicyKeys, key) && !isEmptySettingsTree(value) {
			return ClaudePolicy{Source: source, SupersedesFile: true}
		}
	}
	return ClaudePolicy{}
}

// isEmptySettingsTree reports whether value is an object holding nothing but
// more such objects, which Claude Code does not count as a setting.
func isEmptySettingsTree(value any) bool {
	object, ok := value.(map[string]any)
	if !ok {
		return false
	}
	for _, v := range object {
		if !isEmptySettingsTree(v) {
			return false
		}
	}
	return true
}

// errClaudePolicySupersedesFile is the failure reported for Claude Code's
// managed settings when a policy outranks them. The file is still written, so
// the hooks take effect as soon as the policy allows it.
func errClaudePolicySupersedesFile(policy ClaudePolicy) error {
	return fmt.Errorf(
		"hooks written but not in effect: Claude Code ignores this file because the policy at %s takes precedence; set \"managedSourcesBehavior\": \"merge\" in that policy",
		policy.Source)
}
