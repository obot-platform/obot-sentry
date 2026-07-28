package enforce

import (
	"fmt"
	"strings"

	"github.com/obot-platform/obot/apiclient/types"
)

type runner int

const (
	runnerNPX runner = iota
	runnerUVX
)

func parseRunner(command string, goos string) (runner, error) {
	command = strings.TrimSpace(command)
	if command == "" {
		return 0, fmt.Errorf("the MCP server entry has no command")
	}
	if strings.ContainsAny(command, `/\`) {
		return 0, fmt.Errorf("stdio command %q is a path, not a bare package runner", command)
	}

	name := command
	if goos == "windows" {
		// Windows resolves npx/uvx through a shim, so a config entry legitimately
		// names npx.cmd or uvx.exe, and executable names are case-insensitive.
		// Both relaxations are Windows-only: on macOS a trailing .cmd is not the
		// runner.
		name = strings.ToLower(name)
		for _, ext := range []string{".cmd", ".exe"} {
			if trimmed, ok := strings.CutSuffix(name, ext); ok {
				name = trimmed
				break
			}
		}
	}

	switch name {
	case "npx":
		return runnerNPX, nil
	case "uvx":
		return runnerUVX, nil
	default:
		return 0, fmt.Errorf("stdio command %q is not a supported package runner", command)
	}
}

// resolvePackage derives the canonical package identity a stdio MCP server
// launches, from the entry's command and arguments.
func resolvePackage(command string, args []string, goos string) (*types.AllowlistServerPackage, error) {
	run, err := parseRunner(command, goos)
	if err != nil {
		return nil, err
	}

	var spec string
	switch run {
	case runnerNPX:
		spec, err = npxSpec(args)
	case runnerUVX:
		spec, err = uvxSpec(args)
	}
	if err != nil {
		return nil, err
	}

	switch run {
	case runnerNPX:
		return npmPackage(spec)
	default:
		return pypiPackage(spec)
	}
}

// npxBooleanFlags are npx flags that cannot change which package runs.
var npxBooleanFlags = map[string]struct{}{
	"-y":               {},
	"--yes":            {},
	"-q":               {},
	"--quiet":          {},
	"--silent":         {},
	"--offline":        {},
	"--prefer-offline": {},
	"--no-install":     {},
}

// npxRejectedFlags name why each disallowed npx flag defeats package identity.
// A flag that is not listed here and is not allowed above is refused as
// unrecognized, so a future npm flag fails closed rather than silently
// resolving to the wrong package.
var npxRejectedFlags = map[string]string{
	"-c":                    "runs an arbitrary command string, so the package that executes is not the positional argument",
	"--call":                "runs an arbitrary command string, so the package that executes is not the positional argument",
	"--node-options":        "can require arbitrary code into the process",
	"--registry":            "redirects where the package is fetched from",
	"--userconfig":          "redirects where the package is fetched from",
	"--globalconfig":        "redirects where the package is fetched from",
	"--prefix":              "redirects where the package is fetched from",
	"--cache":               "redirects where the package is fetched from",
	"--ignore-existing":     "changes which copy of the package runs",
	"--shell":               "runs the spec through a shell",
	"--shell-auto-fallback": "runs the spec through a shell",
	"--npm":                 "substitutes the package manager that resolves the spec",
}

// npxSpec extracts the package spec from npx arguments.
//
// -p/--package supplies the spec and wins over a positional one, which is npx's
// own behavior. More than one is ambiguous about which is the server.
func npxSpec(args []string) (string, error) {
	var (
		fromFlag   string
		positional string
		seenFlag   bool
		endOfFlags bool
	)
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case endOfFlags || !strings.HasPrefix(arg, "-") || arg == "-":
			if positional == "" {
				positional = arg
			}
			// npx passes every later positional to the package as its own
			// arguments, so they say nothing about identity.
			endOfFlags = true
		case arg == "--":
			endOfFlags = true
		case arg == "-p" || arg == "--package":
			if seenFlag {
				return "", fmt.Errorf("npx was given more than one --package spec, so the MCP server is ambiguous")
			}
			if i+1 >= len(args) {
				return "", fmt.Errorf("npx flag %s has no value", arg)
			}
			seenFlag, fromFlag = true, args[i+1]
			i++
		case strings.HasPrefix(arg, "--package="):
			if seenFlag {
				return "", fmt.Errorf("npx was given more than one --package spec, so the MCP server is ambiguous")
			}
			seenFlag, fromFlag = true, strings.TrimPrefix(arg, "--package=")
		default:
			if err := rejectFlag("npx", arg, npxBooleanFlags, npxRejectedFlags); err != nil {
				return "", err
			}
		}
	}

	if seenFlag {
		return fromFlag, nil
	}
	if positional == "" {
		return "", fmt.Errorf("npx was given no package to run")
	}
	return positional, nil
}

// uvxBooleanFlags are uvx flags that cannot change which package runs.
var uvxBooleanFlags = map[string]struct{}{
	"-q":         {},
	"--quiet":    {},
	"-n":         {},
	"--no-cache": {},
	"--offline":  {},
}

// uvxValueFlags are uvx flags that take a value and leave identity alone. -p is
// --python here, NOT --package: in npx the same letter selects the package. The
// two flag tables share no entries for exactly this reason.
var uvxValueFlags = map[string]struct{}{
	"-p":       {},
	"--python": {},
}

// uvxRejectedFlags name why each disallowed uvx flag defeats package identity.
var uvxRejectedFlags = map[string]string{
	"--with":              "brings additional packages into the environment, so more than the named package runs",
	"--with-editable":     "brings additional packages into the environment, so more than the named package runs",
	"--with-requirements": "brings additional packages into the environment, so more than the named package runs",
	"--index":             "redirects package resolution",
	"--index-url":         "redirects package resolution",
	"--extra-index-url":   "redirects package resolution",
	"--default-index":     "redirects package resolution",
	"--find-links":        "redirects package resolution",
	"--index-strategy":    "redirects package resolution",
	"--keyring-provider":  "redirects package resolution",
	"--constraints":       "can change which version resolves",
	"--overrides":         "can change which version resolves",
	"--refresh":           "changes which copy of the package runs",
	"--refresh-package":   "changes which copy of the package runs",
	"--isolated":          "changes the environment the package runs in",
	"--project":           "changes the environment the package runs in",
	"--no-project":        "changes the environment the package runs in",
	"--directory":         "changes the environment the package runs in",
	"--native-tls":        "changes how packages are fetched",
}

// uvxSpec extracts the package spec from uvx arguments.
//
// With --from, the first positional is the entrypoint name rather than the
// package, so it is ignored. Without it, the first positional is the spec.
func uvxSpec(args []string) (string, error) {
	var (
		from       string
		positional string
		seenFrom   bool
		endOfFlags bool
	)
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case endOfFlags || !strings.HasPrefix(arg, "-") || arg == "-":
			if positional == "" {
				positional = arg
			}
			endOfFlags = true
		case arg == "--":
			endOfFlags = true
		case arg == "--from":
			if seenFrom {
				return "", fmt.Errorf("uvx was given more than one --from spec, so the MCP server is ambiguous")
			}
			if i+1 >= len(args) {
				return "", fmt.Errorf("uvx flag --from has no value")
			}
			seenFrom, from = true, args[i+1]
			i++
		case strings.HasPrefix(arg, "--from="):
			if seenFrom {
				return "", fmt.Errorf("uvx was given more than one --from spec, so the MCP server is ambiguous")
			}
			seenFrom, from = true, strings.TrimPrefix(arg, "--from=")
		default:
			if _, ok := uvxValueFlags[arg]; ok {
				if i+1 >= len(args) {
					return "", fmt.Errorf("uvx flag %s has no value", arg)
				}
				i++
				continue
			}
			if name, _, ok := strings.Cut(arg, "="); ok {
				if _, known := uvxValueFlags[name]; known {
					continue
				}
			}
			if err := rejectFlag("uvx", arg, uvxBooleanFlags, uvxRejectedFlags); err != nil {
				return "", err
			}
		}
	}

	if seenFrom {
		return from, nil
	}
	if positional == "" {
		return "", fmt.Errorf("uvx was given no package to run")
	}
	return positional, nil
}

// rejectFlag accepts a known identity-neutral boolean flag and refuses anything
// else, with the specific reason when one is recorded.
func rejectFlag(command, arg string, allowed map[string]struct{}, rejected map[string]string) error {
	if _, ok := allowed[arg]; ok {
		return nil
	}
	name, _, hasValue := strings.Cut(arg, "=")
	if reason, ok := rejected[name]; ok {
		return fmt.Errorf("%s flag %s is not allowed: it %s", command, name, reason)
	}
	if hasValue {
		if _, ok := allowed[name]; ok {
			return fmt.Errorf("%s flag %s does not take a value", command, name)
		}
	}
	return fmt.Errorf("%s flag %s is not recognized, so the MCP server cannot be identified", command, name)
}

// rejectedSpecPrefixes are spec forms that name something other than a registry
// package: a local path, a git or archive URL, or an alias.
var rejectedSpecPrefixes = []string{".", "/", "~", "github:", "git+", "file:", "npm:", "http:", "https:"}

// checkRegistrySpec refuses a spec that does not name a published package.
func checkRegistrySpec(spec string) error {
	if spec == "" {
		return fmt.Errorf("the MCP server's package spec is empty")
	}
	if strings.Contains(spec, "://") {
		return fmt.Errorf("package spec %q is a URL, not a registry package", spec)
	}
	for _, prefix := range rejectedSpecPrefixes {
		if strings.HasPrefix(spec, prefix) {
			return fmt.Errorf("package spec %q is not a registry package", spec)
		}
	}
	return nil
}

// npmPackage parses `[@scope/]name[@version]` into a canonical npm identity.
func npmPackage(spec string) (*types.AllowlistServerPackage, error) {
	spec = strings.TrimSpace(spec)
	if err := checkRegistrySpec(spec); err != nil {
		return nil, err
	}
	// A slash outside a scope is npm's user/repo GitHub shorthand.
	if strings.Contains(spec, "/") && !strings.HasPrefix(spec, "@") {
		return nil, fmt.Errorf("package spec %q is a GitHub shorthand, not a registry package", spec)
	}

	name, version := spec, ""
	// The version separator is the first @ after the scope's slash for a scoped
	// spec, and the first @ at a non-zero index otherwise.
	searchFrom := 1
	if strings.HasPrefix(spec, "@") {
		slash := strings.Index(spec, "/")
		if slash < 0 {
			return nil, fmt.Errorf("package spec %q is a scope with no package name", spec)
		}
		searchFrom = slash + 1
	}
	if at := strings.Index(spec[searchFrom:], "@"); at >= 0 {
		cut := searchFrom + at
		name, version = spec[:cut], spec[cut+1:]
	}

	name = canonicalNPMName(name)
	if err := validNPMName(name); err != nil {
		return nil, err
	}
	return &types.AllowlistServerPackage{
		Source:  types.AllowlistServerPackageSourceNPM,
		Name:    name,
		Version: version,
	}, nil
}

// pypiVersionOperators split a PEP 508 requirement into name and version,
// longest-first so `>=` wins over `>` at the same position.
//
// drop is how much of the operator is cut from the reported version: `==` is the
// exact-version form and is dropped, while every other operator is part of the
// constraint and stays in the string. Versions are reported verbatim either way
// — the evaluator compares them as exact strings, and an empty version means
// "any version".
var pypiVersionOperators = []struct {
	op   string
	drop int
}{
	{"==", 2},
	{">=", 0},
	{"<=", 0},
	{"~=", 0},
	{"!=", 0},
	{">", 0},
	{"<", 0},
}

// pypiPackage parses `name[==version]` (or another PEP 508 operator, or `@`)
// into a canonical PyPI identity.
func pypiPackage(spec string) (*types.AllowlistServerPackage, error) {
	spec = strings.TrimSpace(spec)
	if err := checkRegistrySpec(spec); err != nil {
		return nil, err
	}
	if strings.Contains(spec, "[") {
		// Extras pull additional distributions into the environment, which is the
		// same property that makes --with unacceptable.
		return nil, fmt.Errorf("package extras are not supported")
	}

	name, version := spec, ""
	cut, drop := -1, 0
	for _, o := range pypiVersionOperators {
		if i := strings.Index(spec, o.op); i > 0 && (cut < 0 || i < cut) {
			cut, drop = i, o.drop
		}
	}
	if cut < 0 {
		if i := strings.Index(spec, "@"); i > 0 {
			// `pkg@1.2.3` is uv's shorthand for an exact version, so the separator
			// is not part of the version.
			cut, drop = i, 1
		}
	}
	if cut >= 0 {
		name, version = spec[:cut], spec[cut+drop:]
	}

	if err := validPyPIName(name); err != nil {
		return nil, err
	}
	return &types.AllowlistServerPackage{
		Source:  types.AllowlistServerPackageSourcePyPI,
		Name:    canonicalPyPIName(name),
		Version: version,
	}, nil
}

func canonicalNPMName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

// canonicalPyPIName applies PEP 503 normalization: lowercase, then collapse
// every run of -, _, and . to a single -. PyPI itself treats the results as the
// same project, so `Mcp_Server.Git` and `mcp-server-git` must not be two
// different strings to the evaluator.
func canonicalPyPIName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	var (
		b       strings.Builder
		lastSep bool
	)
	b.Grow(len(name))
	for _, r := range name {
		if r == '-' || r == '_' || r == '.' {
			if !lastSep {
				b.WriteByte('-')
				lastSep = true
			}
			continue
		}
		b.WriteRune(r)
		lastSep = false
	}
	return b.String()
}

// validNPMName checks a canonicalized npm name, which may carry a leading
// @scope/ and is otherwise restricted to lowercase alphanumerics, dot, dash, and
// underscore.
func validNPMName(name string) error {
	if name == "" {
		return fmt.Errorf("the MCP server's package spec has no package name")
	}
	body := name
	if scope, rest, ok := strings.Cut(name, "/"); ok {
		if !strings.HasPrefix(scope, "@") || len(scope) < 2 || rest == "" {
			return fmt.Errorf("package name %q is not a valid npm name", name)
		}
		if err := validNPMNameChars(scope[1:]); err != nil {
			return fmt.Errorf("package name %q is not a valid npm name", name)
		}
		body = rest
	}
	if err := validNPMNameChars(body); err != nil {
		return fmt.Errorf("package name %q is not a valid npm name", name)
	}
	return nil
}

func validNPMNameChars(s string) error {
	if s == "" {
		return fmt.Errorf("empty")
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
		default:
			return fmt.Errorf("invalid character %q", r)
		}
	}
	return nil
}

// validPyPIName checks a PyPI name before canonicalization: alphanumerics, dot,
// dash, and underscore in any case.
func validPyPIName(name string) error {
	if name == "" {
		return fmt.Errorf("the MCP server's package spec has no package name")
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
		default:
			return fmt.Errorf("package name %q is not a valid PyPI name", name)
		}
	}
	return nil
}
