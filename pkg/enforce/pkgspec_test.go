package enforce

import (
	"strings"
	"testing"

	"github.com/obot-platform/obot/apiclient/types"
)

// resolveFor is resolvePackage with a shorter call for the tables below.
func resolveFor(t *testing.T, goos, command string, args ...string) (*types.AllowlistServerPackage, error) {
	t.Helper()
	return resolvePackage(command, args, goos)
}

func TestResolvePackageWorkedExamples(t *testing.T) {
	cases := []struct {
		command string
		args    []string
		source  types.AllowlistServerPackageSource
		name    string
		version string
	}{
		{"npx", []string{"-y", "@modelcontextprotocol/server-github"}, "npm", "@modelcontextprotocol/server-github", ""},
		{"npx", []string{"-y", "linear-mcp@1.2.3"}, "npm", "linear-mcp", "1.2.3"},
		{"npx", []string{"-p", "some-pkg", "-y", "other"}, "npm", "some-pkg", ""},
		{"npx", []string{"--package=some-pkg", "other"}, "npm", "some-pkg", ""},
		{"npx", []string{"pkg@latest"}, "npm", "pkg", "latest"},
		{"npx", []string{"pkg@^1.0"}, "npm", "pkg", "^1.0"},
		{"npx", []string{"-y", "--", "pkg"}, "npm", "pkg", ""},
		{"npx", []string{"@scope/pkg@2.0.0"}, "npm", "@scope/pkg", "2.0.0"},
		// Canonicalization: the evaluator compares names as exact strings, so a
		// case-only rewrite must not sidestep an allowlist entry.
		{"npx", []string{"@Scope/Pkg"}, "npm", "@scope/pkg", ""},

		{"uvx", []string{"mcp-server-git"}, "pypi", "mcp-server-git", ""},
		{"uvx", []string{"awslabs.core-mcp-server@latest"}, "pypi", "awslabs-core-mcp-server", "latest"},
		{"uvx", []string{"--from", "mcp-server-fetch", "mcp-server-fetch"}, "pypi", "mcp-server-fetch", ""},
		{"uvx", []string{"--from", "pkg==1.4.0", "entry"}, "pypi", "pkg", "1.4.0"},
		{"uvx", []string{"--from=pkg==1.4.0", "entry"}, "pypi", "pkg", "1.4.0"},
		// -p is --python for uvx, so the package is the positional, not the version.
		{"uvx", []string{"-p", "3.11", "mcp-server-git"}, "pypi", "mcp-server-git", ""},
		{"uvx", []string{"--python", "3.11", "mcp-server-git"}, "pypi", "mcp-server-git", ""},
		{"uvx", []string{"--python=3.11", "mcp-server-git"}, "pypi", "mcp-server-git", ""},
		{"uvx", []string{"-q", "-n", "--offline", "mcp-server-git"}, "pypi", "mcp-server-git", ""},
		{"uvx", []string{"Mcp_Server.Git"}, "pypi", "mcp-server-git", ""},
		// Non-exact PEP 508 operators stay in the reported version verbatim.
		{"uvx", []string{"--from", "pkg>=2", "entry"}, "pypi", "pkg", ">=2"},
		{"uvx", []string{"--from", "pkg~=1.4", "entry"}, "pypi", "pkg", "~=1.4"},
		{"uvx", []string{"--from", "pkg!=1.4", "entry"}, "pypi", "pkg", "!=1.4"},
		{"uvx", []string{"--from", "pkg<2", "entry"}, "pypi", "pkg", "<2"},
		{"uvx", []string{"--from", "pkg<=2", "entry"}, "pypi", "pkg", "<=2"},
		{"uvx", []string{"pkg@1.2.3"}, "pypi", "pkg", "1.2.3"},
	}

	for _, tc := range cases {
		pkg, err := resolveFor(t, "darwin", tc.command, tc.args...)
		if err != nil {
			t.Errorf("%s %v: unexpected error: %v", tc.command, tc.args, err)
			continue
		}
		if pkg.Source != tc.source || pkg.Name != tc.name || pkg.Version != tc.version {
			t.Errorf("%s %v = {%s %s %q}, want {%s %s %q}",
				tc.command, tc.args, pkg.Source, pkg.Name, pkg.Version, tc.source, tc.name, tc.version)
		}
	}
}

func TestResolvePackageUnresolvable(t *testing.T) {
	cases := []struct {
		command string
		args    []string
		// reason is a substring the error must name, so the decision log says
		// something specific rather than "could not resolve".
		reason string
	}{
		// Unsupported runners. uv tool run is literally the same operation as uvx
		// and is still refused: the accepted set is exactly two commands.
		{"uv", []string{"tool", "run", "pkg"}, `"uv" is not a supported package runner`},
		{"uv", []string{"run", "pkg"}, `"uv" is not a supported package runner`},
		{"npm", []string{"exec", "pkg"}, `"npm" is not a supported package runner`},
		{"npm", []string{"x", "pkg"}, `"npm" is not a supported package runner`},
		{"pnpm", []string{"dlx", "pkg"}, `"pnpm" is not a supported package runner`},
		{"yarn", []string{"dlx", "pkg"}, `"yarn" is not a supported package runner`},
		{"bunx", []string{"pkg"}, `"bunx" is not a supported package runner`},
		{"bun", []string{"x", "pkg"}, `"bun" is not a supported package runner`},
		{"pipx", []string{"run", "pkg"}, `"pipx" is not a supported package runner`},
		{"docker", []string{"run", "-i", "ghcr.io/x/y"}, `"docker" is not a supported package runner`},
		{"node", []string{"/srv/server.js"}, `"node" is not a supported package runner`},
		{"python", []string{"-m", "server"}, `"python" is not a supported package runner`},
		{"python3", []string{"-m", "server"}, `"python3" is not a supported package runner`},
		{"sh", []string{"-c", "server"}, `"sh" is not a supported package runner`},
		{"bash", []string{"-c", "server"}, `"bash" is not a supported package runner`},
		{"cmd", []string{"/c", "server"}, `"cmd" is not a supported package runner`},
		{"powershell", []string{"-c", "server"}, `"powershell" is not a supported package runner`},

		// Any path separator, including an absolute runner path.
		{"/opt/homebrew/bin/uvx", []string{"pkg"}, "is a path, not a bare package runner"},
		{"./node_modules/.bin/npx", []string{"pkg"}, "is a path, not a bare package runner"},
		{`C:\tools\npx.cmd`, []string{"pkg"}, "is a path, not a bare package runner"},

		// npx flags that defeat identity.
		{"npx", []string{"-c", "foo && bar"}, "npx flag -c is not allowed"},
		{"npx", []string{"--call", "foo"}, "npx flag --call is not allowed"},
		{"npx", []string{"--node-options", "--require=x", "pkg"}, "npx flag --node-options is not allowed"},
		{"npx", []string{"--registry", "https://x/", "pkg"}, "npx flag --registry is not allowed"},
		{"npx", []string{"--registry=https://x/", "pkg"}, "npx flag --registry is not allowed"},
		{"npx", []string{"--userconfig", "x", "pkg"}, "npx flag --userconfig is not allowed"},
		{"npx", []string{"--globalconfig", "x", "pkg"}, "npx flag --globalconfig is not allowed"},
		{"npx", []string{"--prefix", "x", "pkg"}, "npx flag --prefix is not allowed"},
		{"npx", []string{"--cache", "x", "pkg"}, "npx flag --cache is not allowed"},
		{"npx", []string{"--ignore-existing", "pkg"}, "npx flag --ignore-existing is not allowed"},
		{"npx", []string{"--shell", "sh", "pkg"}, "npx flag --shell is not allowed"},
		{"npx", []string{"--shell-auto-fallback", "pkg"}, "npx flag --shell-auto-fallback is not allowed"},
		{"npx", []string{"--npm", "pnpm", "pkg"}, "npx flag --npm is not allowed"},
		{"npx", []string{"-p", "a", "-p", "b"}, "more than one --package spec"},
		{"npx", []string{"-p", "a", "--package=b"}, "more than one --package spec"},
		{"npx", []string{"-p"}, "npx flag -p has no value"},
		{"npx", nil, "npx was given no package to run"},

		// uvx flags that defeat identity.
		{"uvx", []string{"--with", "pandas", "pkg"}, "uvx flag --with is not allowed"},
		{"uvx", []string{"--with-editable", ".", "pkg"}, "uvx flag --with-editable is not allowed"},
		{"uvx", []string{"--with-requirements", "r.txt", "pkg"}, "uvx flag --with-requirements is not allowed"},
		{"uvx", []string{"--index", "https://x/", "pkg"}, "uvx flag --index is not allowed"},
		{"uvx", []string{"--index-url", "https://x/", "pkg"}, "uvx flag --index-url is not allowed"},
		{"uvx", []string{"--extra-index-url", "https://x/", "pkg"}, "uvx flag --extra-index-url is not allowed"},
		{"uvx", []string{"--default-index", "https://x/", "pkg"}, "uvx flag --default-index is not allowed"},
		{"uvx", []string{"--find-links", "x", "pkg"}, "uvx flag --find-links is not allowed"},
		{"uvx", []string{"--index-strategy", "x", "pkg"}, "uvx flag --index-strategy is not allowed"},
		{"uvx", []string{"--keyring-provider", "x", "pkg"}, "uvx flag --keyring-provider is not allowed"},
		{"uvx", []string{"--constraints", "c.txt", "pkg"}, "uvx flag --constraints is not allowed"},
		{"uvx", []string{"--overrides", "o.txt", "pkg"}, "uvx flag --overrides is not allowed"},
		{"uvx", []string{"--refresh", "pkg"}, "uvx flag --refresh is not allowed"},
		{"uvx", []string{"--refresh-package", "x", "pkg"}, "uvx flag --refresh-package is not allowed"},
		{"uvx", []string{"--isolated", "pkg"}, "uvx flag --isolated is not allowed"},
		{"uvx", []string{"--project", "x", "pkg"}, "uvx flag --project is not allowed"},
		{"uvx", []string{"--no-project", "pkg"}, "uvx flag --no-project is not allowed"},
		{"uvx", []string{"--directory", "x", "pkg"}, "uvx flag --directory is not allowed"},
		{"uvx", []string{"--native-tls", "pkg"}, "uvx flag --native-tls is not allowed"},
		{"uvx", []string{"--from", "a", "--from", "b"}, "more than one --from spec"},
		{"uvx", []string{"--from"}, "uvx flag --from has no value"},
		{"uvx", nil, "uvx was given no package to run"},

		// Specs that name something other than a registry package.
		{"uvx", []string{"--from", "git+https://example.com/x.git", "entry"}, "not a registry package"},
		{"uvx", []string{"--from", "./local", "entry"}, "not a registry package"},
		{"uvx", []string{"--from", "~/local", "entry"}, "not a registry package"},
		{"uvx", []string{"--from", "/opt/local", "entry"}, "not a registry package"},
		{"npx", []string{"github:user/repo"}, "not a registry package"},
		{"npx", []string{"file:./pkg"}, "not a registry package"},
		{"npx", []string{"npm:pkg"}, "not a registry package"},
		{"npx", []string{"https://example.com/pkg.tgz"}, "is a URL, not a registry package"},
		{"npx", []string{"user/repo"}, "GitHub shorthand"},
		{"npx", []string{"@scope"}, "scope with no package name"},
		{"npx", []string{"Pkg!"}, "not a valid npm name"},

		// Extras pull additional distributions in, the same property that makes
		// --with unacceptable.
		{"uvx", []string{"--from", "mcp-server[all]", "run"}, "package extras are not supported"},
		{"uvx", []string{"mcp-server[all]"}, "package extras are not supported"},
		{"uvx", []string{"pkg!"}, "not a valid PyPI name"},
	}

	for _, tc := range cases {
		pkg, err := resolveFor(t, "darwin", tc.command, tc.args...)
		if err == nil {
			t.Errorf("%s %v resolved to %+v, want an error naming %q", tc.command, tc.args, pkg, tc.reason)
			continue
		}
		if !strings.Contains(err.Error(), tc.reason) {
			t.Errorf("%s %v error = %q, want it to name %q", tc.command, tc.args, err, tc.reason)
		}
	}
}

func TestResolvePackageUnknownFlagsFailClosed(t *testing.T) {
	if _, err := resolveFor(t, "darwin", "npx", "--unknown-flag", "value", "pkg"); err == nil {
		t.Errorf("npx %s resolved; an unreviewed flag must fail closed", "--unknown-flag")
	}
	if _, err := resolveFor(t, "darwin", "uvx", "--unknown-flag", "value", "pkg"); err == nil {
		t.Errorf("uvx %s resolved; an unreviewed flag must fail closed", "--unknown-flag")
	}
}

// TestResolvePackageWindowsCommandForms covers the shim spellings a Windows MCP
// config legitimately uses, and confirms they are Windows-only.
func TestResolvePackageWindowsCommandForms(t *testing.T) {
	windowsOK := []string{"npx", "npx.cmd", "npx.exe", "NPX.CMD"}
	for _, command := range windowsOK {
		pkg, err := resolveFor(t, "windows", command, "-y", "pkg")
		if err != nil {
			t.Errorf("windows %s: unexpected error: %v", command, err)
			continue
		}
		if pkg.Source != types.AllowlistServerPackageSourceNPM || pkg.Name != "pkg" {
			t.Errorf("windows %s = %+v, want npm/pkg", command, pkg)
		}
	}
	for _, command := range []string{"uvx.cmd", "uvx.exe"} {
		pkg, err := resolveFor(t, "windows", command, "pkg")
		if err != nil {
			t.Errorf("windows %s: unexpected error: %v", command, err)
			continue
		}
		if pkg.Source != types.AllowlistServerPackageSourcePyPI {
			t.Errorf("windows %s = %+v, want pypi", command, pkg)
		}
	}

	for _, command := range []string{"npx.cmd", "npx.exe", "NPX"} {
		if _, err := resolveFor(t, "darwin", command, "-y", "pkg"); err == nil {
			t.Errorf("darwin %s resolved; the shim spellings are Windows-only", command)
		}
	}
}

func TestCanonicalPyPIName(t *testing.T) {
	cases := map[string]string{
		"Mcp_Server.Git":          "mcp-server-git",
		"awslabs.core-mcp-server": "awslabs-core-mcp-server",
		"mcp__server":             "mcp-server",
		"mcp-_.server":            "mcp-server",
		"MCP-SERVER-GIT":          "mcp-server-git",
		"already-canonical":       "already-canonical",
	}
	for in, want := range cases {
		if got := canonicalPyPIName(in); got != want {
			t.Errorf("canonicalPyPIName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCanonicalNPMName(t *testing.T) {
	cases := map[string]string{
		"@Scope/Pkg":     "@scope/pkg",
		"Linear-MCP":     "linear-mcp",
		"already-lower":  "already-lower",
		"@scope/already": "@scope/already",
	}
	for in, want := range cases {
		if got := canonicalNPMName(in); got != want {
			t.Errorf("canonicalNPMName(%q) = %q, want %q", in, got, want)
		}
	}
}
