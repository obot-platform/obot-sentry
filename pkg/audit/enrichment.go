package audit

import (
	"os"
	"os/exec"
	"os/user"
	"runtime"
	"strings"
)

type Enrichment struct {
	CWD           string
	GitRepoRoot   string
	GitRemoteURLs []string
	GitBranch     string
	GitCommitSHA  string
	Hostname      string
	OS            string
	Arch          string
	LocalUsername string
}

func CollectEnrichment(cwd string) Enrichment {
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	enrichment := Enrichment{
		CWD:  cwd,
		OS:   runtime.GOOS,
		Arch: runtime.GOARCH,
	}
	enrichment.Hostname, _ = os.Hostname()
	if u, err := user.Current(); err == nil {
		enrichment.LocalUsername = u.Username
	}

	if cwd != "" {
		enrichment.GitRepoRoot = git(cwd, "rev-parse", "--show-toplevel")
		enrichment.GitBranch = git(cwd, "branch", "--show-current")
		enrichment.GitCommitSHA = git(cwd, "rev-parse", "HEAD")
		for name := range strings.SplitSeq(git(cwd, "remote"), "\n") {
			if name = strings.TrimSpace(name); name != "" {
				for remoteURL := range strings.SplitSeq(git(cwd, "remote", "get-url", "--all", name), "\n") {
					if remoteURL = strings.TrimSpace(remoteURL); remoteURL != "" {
						enrichment.GitRemoteURLs = append(enrichment.GitRemoteURLs, remoteURL)
					}
				}
			}
		}
	}
	return enrichment
}

func git(cwd string, args ...string) string {
	cmd := exec.Command("git", args...)
	cmd.Dir = cwd
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
