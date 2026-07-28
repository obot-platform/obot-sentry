package enforce

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"

	"github.com/BurntSushi/toml"
	"github.com/tailscale/hujson"
)

const maxConfigBytes = 8 << 20

// Env is the machine context the resolvers read. Production builds one with
// NewEnv; tests build one pointing at a fixture tree.
type Env struct {
	Home string
	GOOS string
	// Getenv resolves an environment variable. Nil falls back to os.Getenv.
	Getenv func(string) string
	// MachineRoot is prepended to the fixed machine-scoped absolute paths — the
	// macOS Claude Code managed MCP config and Codex's /etc config. It is empty
	// in production and a fixture-tree root in tests. Machine paths that come
	// from an environment variable are not rewritten; a test points those at the
	// fixture tree through Getenv instead.
	MachineRoot string
}

// NewEnv returns the Env for the current process.
func NewEnv() (Env, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Env{}, fmt.Errorf("resolve home dir: %w", err)
	}
	return Env{Home: home, GOOS: runtime.GOOS, Getenv: os.Getenv}, nil
}

func (e Env) windows() bool { return e.GOOS == "windows" }

func (e Env) getenv(key string) string {
	if e.Getenv == nil {
		return os.Getenv(key)
	}
	return e.Getenv(key)
}

// homePath joins slash-separated elements onto the home directory.
func (e Env) homePath(elem ...string) string {
	return filepath.Join(append([]string{e.Home}, elem...)...)
}

// machinePath resolves a fixed machine-scoped absolute path, honoring
// MachineRoot. The argument is always written slash-separated.
func (e Env) machinePath(abs string) string {
	if e.MachineRoot == "" {
		return filepath.FromSlash(abs)
	}
	return filepath.Join(e.MachineRoot, filepath.FromSlash(abs))
}

// envPath resolves a Windows machine path rooted at an environment variable,
// falling back to the conventional location when the variable is unset.
func (e Env) envPath(key, fallback string, elem ...string) string {
	base := e.getenv(key)
	if base == "" {
		return filepath.Join(append([]string{e.machinePath(fallback)}, elem...)...)
	}
	return filepath.Join(append([]string{base}, elem...)...)
}

// loadResult reports what reading one configuration file produced. It separates
// "the file is not there" from "the file is there but told us nothing", because
// a resolution trace has to be able to say which.
type loadResult int

const (
	// loadAbsent means the file does not exist.
	loadAbsent loadResult = iota
	// loadUnusable means the file exists but could not be read or parsed.
	loadUnusable
	// loadOK means the file was read and decoded.
	loadOK
)

// note returns the trace annotation for a non-OK load.
func (r loadResult) note() string {
	switch r {
	case loadUnusable:
		return "unreadable or malformed"
	default:
		return ""
	}
}

// utf8BOM is a leading UTF-8 byte-order mark. Editors on Windows write one, and
// both encoding/json and hujson reject it — which would make a working config
// unreadable, and an unreadable config is a deny.
var utf8BOM = []byte{0xef, 0xbb, 0xbf}

// readConfig reads path, refusing anything over maxConfigBytes and removing one
// leading byte-order mark.
func readConfig(path string) ([]byte, loadResult) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, loadAbsent
		}
		return nil, loadUnusable
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		return nil, loadUnusable
	}
	if info.IsDir() || info.Size() > maxConfigBytes {
		return nil, loadUnusable
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, loadUnusable
	}
	return bytes.TrimPrefix(data, utf8BOM), loadOK
}

// loadJSON decodes the JSON file at path into out.
//
// Editor-adjacent configs are commonly JSONC — VS Code and Cursor both document
// comments in mcp.json — so a strict-parse failure is retried with the comments
// and trailing commas standardized away. Being tolerant here matters: a config
// we refuse to parse becomes an unresolved call, and an unresolved call is
// denied.
func loadJSON(path string, out any) loadResult {
	data, res := readConfig(path)
	if res != loadOK {
		return res
	}
	if err := json.Unmarshal(data, out); err == nil {
		return loadOK
	}
	standard, err := hujson.Standardize(data)
	if err != nil {
		return loadUnusable
	}
	if err := json.Unmarshal(standard, out); err != nil {
		return loadUnusable
	}
	return loadOK
}

// loadTOML decodes the TOML file at path into out.
func loadTOML(path string, out any) loadResult {
	data, res := readConfig(path)
	if res != loadOK {
		return res
	}
	if _, err := toml.Decode(string(data), out); err != nil {
		return loadUnusable
	}
	return loadOK
}
