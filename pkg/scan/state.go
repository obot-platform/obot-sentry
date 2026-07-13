package scan

import (
	"errors"
	"io/fs"
	"path"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/obot-platform/obot/apiclient/types"
)

// maxFileBytes is the per-file content cap. Files over this are
// recorded with Oversized=true and no Content.
const maxFileBytes int64 = 1 << 20 // 1 MiB

// artifactSkipDirs are dependency / build directories we never descend
// into when listing files inside a skill or plugin directory.
var artifactSkipDirs = map[string]bool{
	"node_modules": true,
	".venv":        true,
	"venv":         true,
	"vendor":       true,
	"dist":         true,
	".tox":         true,
	".git":         true,
	"__pycache__":  true,
}

// state binds one scan root to the dedup-by-key tables shared across
// roots — the file table (keyed by absolute path) and the client table
// (keyed by name).
type state struct {
	fsys       fs.FS
	base       string // Root.Path
	nativeBase string // Root.NativePath, defaulted to base
	platform   string
	primary    bool
	maxDepth   int

	files   map[string]types.DeviceScanFile
	clients map[string]types.DeviceScanClient

	// claimed are root-relative directories already inventoried as part
	// of a larger unit (plugin installs), whose contents the walk must
	// not re-derive observations from.
	claimed []string
}

func newState(root Root, maxDepth int, files map[string]types.DeviceScanFile, clients map[string]types.DeviceScanClient) *state {
	nativeBase := root.NativePath
	if nativeBase == "" {
		nativeBase = root.Path
	}
	return &state{
		fsys:       root.FS,
		base:       root.Path,
		nativeBase: nativeBase,
		platform:   root.Platform,
		primary:    root.Primary,
		maxDepth:   maxDepth,
		files:      files,
		clients:    clients,
	}
}

// abs converts a root-relative path to the absolute path used in wire
// output.
func (s *state) abs(rel string) string {
	return filepath.Join(s.base, filepath.FromSlash(rel))
}

// relToHome converts an absolute path found inside a config file back
// to root-relative form, ok=false when it lies outside the root.
// Configs reference the root's native path form (e.g. /home/user inside
// a WSL root mounted at \\wsl$\...), so the native base is matched
// first.
func (s *state) relToHome(absPath string) (string, bool) {
	target := filepath.ToSlash(absPath)
	for _, base := range []string{filepath.ToSlash(s.nativeBase), filepath.ToSlash(s.base)} {
		if rest, ok := strings.CutPrefix(target, base+"/"); ok && rest != "" {
			return path.Clean(rest), true
		}
	}
	return "", false
}

// claim marks dirRel's subtree as inventoried, so walk hits inside it
// are dropped (see claimedUnder). Called by plugin scans, which read
// their install directories — including nested MCP configs and skills —
// directly.
func (s *state) claim(dirRel string) {
	s.claimed = append(s.claimed, dirRel)
}

// claimedUnder reports whether rel sits inside a claimed subtree.
func (s *state) claimedUnder(rel string) bool {
	return underAnyDir(rel, s.claimed)
}

// addFile reads and records the file at rel. Returns the absolute path
// observations should reference. Idempotent. Files larger than
// maxFileBytes are recorded with Oversized=true and no Content.
func (s *state) addFile(rel string) (string, error) {
	abs := s.abs(rel)
	if _, ok := s.files[abs]; ok {
		return abs, nil
	}

	info, err := fs.Stat(s.fsys, rel)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", errors.New("addFile: path is a directory")
	}

	f := types.DeviceScanFile{
		Path:      abs,
		SizeBytes: info.Size(),
	}

	if info.Size() > maxFileBytes {
		f.Oversized = true
		s.files[abs] = f
		return abs, nil
	}

	data, err := fs.ReadFile(s.fsys, rel)
	if err != nil {
		// Unreadable (perms / IO). Record path + size only; leave
		// Oversized=false so the UI doesn't mislabel it.
		s.files[abs] = f
		return abs, nil
	}

	if utf8.Valid(data) {
		f.Content = string(data)
	}
	s.files[abs] = f
	return abs, nil
}

// addFileOrAbs is a convenience wrapper that returns the absolute path
// regardless of whether the file could be read. Many callers want "the
// abs path to record on an observation, even if the file itself is
// missing."
func (s *state) addFileOrAbs(rel string) string {
	if abs, err := s.addFile(rel); err == nil && abs != "" {
		return abs
	}
	return s.abs(rel)
}

// addClient upserts a presence-detected client record. Version and the
// path fields take the first non-empty value when called more than
// once for the same name.
func (s *state) addClient(c types.DeviceScanClient) {
	if c.Name == "" {
		return
	}
	existing, ok := s.clients[c.Name]
	if !ok {
		s.clients[c.Name] = c
		return
	}
	if existing.Version == "" {
		existing.Version = c.Version
	}
	if existing.BinaryPath == "" {
		existing.BinaryPath = c.BinaryPath
	}
	if existing.InstallPath == "" {
		existing.InstallPath = c.InstallPath
	}
	if existing.ConfigPath == "" {
		existing.ConfigPath = c.ConfigPath
	}
	s.clients[c.Name] = existing
}

// listArtifactPaths walks dirRel and returns absolute paths for every
// file whose extension is in allowedExts. Files are NOT registered in
// s.files — only their paths are listed. Used by skills/plugins where
// we want a manifest of related files without uploading their contents.
func (s *state) listArtifactPaths(dirRel string, allowedExts map[string]bool) []string {
	var paths []string
	_ = fs.WalkDir(s.fsys, dirRel, func(rel string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if rel != dirRel && artifactSkipDirs[d.Name()] {
				return fs.SkipDir
			}
			return nil
		}
		if !allowedExts[path.Ext(rel)] {
			return nil
		}
		paths = append(paths, s.abs(rel))
		return nil
	})
	return paths
}

// dirExists reports whether rel exists and is a directory.
func dirExists(fsys fs.FS, rel string) bool {
	info, err := fs.Stat(fsys, rel)
	return err == nil && info.IsDir()
}
