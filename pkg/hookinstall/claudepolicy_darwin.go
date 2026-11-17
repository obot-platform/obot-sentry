//go:build darwin

package hookinstall

func detectClaudePolicy(u *TargetUser) ClaudePolicy {
	return claudePolicyFromPlists(claudePlistPaths(u))
}
