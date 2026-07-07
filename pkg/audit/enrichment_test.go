package audit

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

func TestCollectEnrichmentCollectsURLsFromAllGitRemotes(t *testing.T) {
	// Ignore machine-specific URL rewrites such as url.*.insteadOf so the
	// expected URLs come only from the repository configuration below.
	globalConfig := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(globalConfig, nil, 0o600); err != nil {
		t.Fatalf("write empty global Git config: %v", err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", globalConfig)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")

	dir := t.TempDir()
	runGit(t, dir, "init", "--initial-branch=main")
	runGit(t, dir, "remote", "add", "origin", "https://github.com/obot-platform/obocop.git")
	runGit(t, dir, "remote", "set-url", "--add", "origin", "git@github.com:obot-platform/obocop.git")
	runGit(t, dir, "remote", "add", "upstream", "https://github.com/obot-platform/obot.git")

	got := CollectEnrichment(dir).GitRemoteURLs
	want := []string{
		"https://github.com/obot-platform/obocop.git",
		"git@github.com:obot-platform/obocop.git",
		"https://github.com/obot-platform/obot.git",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected remote URLs:\n got: %q\nwant: %q", got, want)
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}
