//go:build !darwin && !windows

package hookinstall

func detectClaudePolicy(*TargetUser) ClaudePolicy { return ClaudePolicy{} }
