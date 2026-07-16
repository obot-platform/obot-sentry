package cmd

import (
	"bytes"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func TestHookInstallVisibleInRootHelp(t *testing.T) {
	root := New()
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs([]string{"--help"})

	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	cmds := listedCommands(stdout.String())
	if !slices.Contains(cmds, "hook-install") {
		t.Fatalf("expected hook-install command in root help, got %v", cmds)
	}
	// The audit plumbing stays hidden even though hook-install (whose
	// description mentions "audit") is public.
	if slices.Contains(cmds, "audit") {
		t.Fatalf("audit must remain hidden from root help, got %v", cmds)
	}
}

// listedCommands extracts the command names from the "Available Commands:"
// section of cobra help output, so tests can assert on the command list rather
// than loose substrings that also match command descriptions.
func listedCommands(help string) []string {
	var cmds []string
	inSection := false
	for line := range strings.SplitSeq(help, "\n") {
		if strings.HasPrefix(line, "Available Commands:") {
			inSection = true
			continue
		}
		if !inSection {
			continue
		}
		if strings.TrimSpace(line) == "" {
			break
		}
		if fields := strings.Fields(line); len(fields) > 0 {
			cmds = append(cmds, fields[0])
		}
	}
	return cmds
}

func TestHookInstallRejectsPositionalArgs(t *testing.T) {
	root := New()
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs([]string{"hook-install", "unexpected"})

	err := root.Execute()
	if err == nil {
		t.Fatal("expected positional argument to be rejected")
	}
}

// TestHookInstallUnsupportedPlatformMakesNoChanges exercises the command on the
// test host. On Linux (the CI/dev platform for this suite) hook-install must
// report an unsupported-platform error and write nothing to stdout.
// This test is skipped when not run on Linux
func TestHookInstallUnsupportedPlatformMakesNoChanges(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("skipping test on non-Linux platform")
	}

	root := New()
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs([]string{"hook-install"})

	err := root.Execute()
	if err == nil {
		t.Fatal("expected hook-install to error without a supported platform and privilege")
	}
	if stdout.Len() != 0 {
		t.Fatalf("expected no stdout when preflight fails, got %q", stdout.String())
	}
}
