package enforce

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/obot-platform/obot/apiclient/types"
)

// fixture is a temporary machine the resolvers can be pointed at: a home
// directory, a machine-scoped root standing in for /Library and /etc, and
// somewhere to put project trees.
type fixture struct {
	t *testing.T
	// Root is the temp directory everything lives under.
	Root string
	// Home is the fixture's home directory.
	Home string
	// Env is the resolver environment for this fixture.
	Env Env

	env map[string]string
}

// newFixture builds an empty fixture for the given platform layout.
func newFixture(t *testing.T, goos string) *fixture {
	t.Helper()
	root := t.TempDir()
	f := &fixture{
		t:    t,
		Root: root,
		Home: filepath.Join(root, "home"),
		env:  map[string]string{},
	}
	f.Env = Env{
		Home:        f.Home,
		GOOS:        goos,
		MachineRoot: filepath.Join(root, "machine"),
		Getenv:      func(key string) string { return f.env[key] },
	}
	if err := os.MkdirAll(f.Home, 0o755); err != nil {
		t.Fatal(err)
	}
	return f
}

// hostRoot is the root a path on this host has to start with to be absolute:
// "/" on Unix, and the working volume's root ("C:\") on Windows, where a rooted
// path carrying no volume is not absolute at all.
func hostRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.VolumeName(wd) + string(filepath.Separator)
}

// setenv sets a variable the fixture's Env resolves.
func (f *fixture) setenv(key, value string) {
	f.env[key] = value
}

// path returns an absolute path under the fixture root.
func (f *fixture) path(elem ...string) string {
	return filepath.Join(append([]string{f.Root}, elem...)...)
}

// homePath returns an absolute path under the fixture home.
func (f *fixture) homePath(elem ...string) string {
	return filepath.Join(append([]string{f.Home}, elem...)...)
}

// machinePath returns the fixture's stand-in for a machine-scoped absolute path.
func (f *fixture) machinePath(abs string) string {
	return f.Env.machinePath(abs)
}

// write creates a file at an absolute path, making parent directories.
func (f *fixture) write(abs, content string) string {
	f.t.Helper()
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
	return abs
}

// mkdir creates a directory at an absolute path.
func (f *fixture) mkdir(abs string) string {
	f.t.Helper()
	if err := os.MkdirAll(abs, 0o755); err != nil {
		f.t.Fatal(err)
	}
	return abs
}

// resolveTrace renders a resolution trace as one line per step, for assertions
// and for diagnosing a failing expectation.
func resolveTrace(res Resolution) string {
	var b strings.Builder
	for _, step := range res.Trace {
		b.WriteString(step.Path)
		if step.Key != "" {
			b.WriteString("  ")
			b.WriteString(step.Key)
		}
		switch {
		case step.Matched:
			b.WriteString("  FOUND")
		case !step.Exists:
			b.WriteString("  absent")
		default:
			b.WriteString("  ")
			b.WriteString(step.Note)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// consultedPaths lists the files a resolution touched, in order.
func consultedPaths(res Resolution) []string {
	out := make([]string, 0, len(res.Trace))
	for _, step := range res.Trace {
		out = append(out, step.Path)
	}
	return out
}

// assertURL fails unless the resolution identified the server by URL.
func assertURL(t *testing.T, res Resolution, want string) {
	t.Helper()
	if res.Unresolved {
		t.Fatalf("resolution was unresolved (%s)\n%s", res.Reason, resolveTrace(res))
	}
	if res.Identity.URL != want {
		t.Fatalf("URL = %q, want %q\n%s", res.Identity.URL, want, resolveTrace(res))
	}
}

// assertPackage fails unless the resolution identified the server by package.
func assertPackage(t *testing.T, res Resolution, source, name, version string) {
	t.Helper()
	if res.Unresolved {
		t.Fatalf("resolution was unresolved (%s)\n%s", res.Reason, resolveTrace(res))
	}
	pkg := res.Identity.Package
	if pkg == nil {
		t.Fatalf("no package identity, got %+v\n%s", res.Identity, resolveTrace(res))
	}
	if string(pkg.Source) != source || pkg.Name != name || pkg.Version != version {
		t.Fatalf("package = {%s %s %q}, want {%s %s %q}\n%s",
			pkg.Source, pkg.Name, pkg.Version, source, name, version, resolveTrace(res))
	}
}

// assertUnresolved fails unless the resolution reported the given reason.
func assertUnresolved(t *testing.T, res Resolution, reason string) {
	t.Helper()
	if !res.Unresolved {
		t.Fatalf("resolution succeeded as %+v, want unresolved naming %q\n%s",
			res.Identity, reason, resolveTrace(res))
	}
	if !strings.Contains(res.Reason, reason) {
		t.Fatalf("reason = %q, want it to name %q", res.Reason, reason)
	}
}

// mustJSON marshals v, failing loudly on the impossible.
func mustJSON(v any) []byte {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return data
}

// emptyIdentity is the zero server identity, for comparing against a resolution
// that identified a server by name alone.
var emptyIdentity = types.EnforcementDecisionServer{}
