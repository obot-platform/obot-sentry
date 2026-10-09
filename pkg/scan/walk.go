package scan

import (
	"context"
	"io/fs"
	"path"
	"strings"
)

// walkSkipDirs are basenames the walk prunes when descending. The set
// covers dependency caches, build outputs, system / app-support trees
// that can't host project configs, and OS trash dirs. Matching is by
// basename, which loses some precision (the entire ~/Library tree is
// skipped, not just ~/Library/Caches) but is acceptable because client
// global configs are opened directly by ScanHome, not via the walk.
var walkSkipDirs = map[string]bool{
	"node_modules": true,
	".git":         true,
	".venv":        true,
	"venv":         true,
	"__pycache__":  true,
	"vendor":       true,
	"dist":         true,
	"build":        true,
	"target":       true,
	".next":        true,
	".nuxt":        true,
	".turbo":       true,
	".cache":       true,
	".npm":         true,
	".yarn":        true,
	"Library":      true,
	"AppData":      true,
	".Trash":       true,
	"tmp":          true,
	"temp":         true,
}

// projectHit is one matched config file paired with the source that
// describes it.
type projectHit struct {
	path   string
	source Source
}

// walk descends the root's fs once, matching every file against every
// scanner's ProjectConfigs suffixes and the SKILL.md marker name,
// returning two streams: scanner-attributed project config hits, and
// SKILL.md marker paths for scanSkills.
//
// Depth is counted in path components: a top-level entry under the root
// is depth 1, so s.maxDepth=N means files match at depths 1…N
// inclusive.
//
// Files at root-relative paths in skipPaths are dropped before
// dispatching (used to suppress the redundant hit on a path already
// opened as a global config).
//
// Honors ctx: if cancelled, the walk aborts early and returns whatever
// was matched so far.
func walk(ctx context.Context, s *state, srcs []Source, skipPaths map[string]bool) ([]projectHit, []string) {
	if s.fsys == nil {
		return nil, nil
	}

	var matchers, dirMatchers []Source
	for _, src := range srcs {
		switch {
		case !src.Scope.has(Project):
		case src.Dir:
			dirMatchers = append(dirMatchers, src)
		default:
			matchers = append(matchers, src)
		}
	}

	var (
		hits      []projectHit
		skillHits []string
	)
	_ = fs.WalkDir(s.fsys, ".", func(rel string, d fs.DirEntry, err error) error {
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if rel == "." {
				return nil
			}
			if walkSkipDirs[d.Name()] {
				return fs.SkipDir
			}
			// depth=1 for top-level entries. SkipDir on a dir at
			// depth==maxDepth means we don't descend into it, so files
			// match at depths 1…maxDepth (inclusive).
			if depth := strings.Count(rel, "/") + 1; depth >= s.maxDepth {
				return fs.SkipDir
			}
			// A directory source reads its own tree, so the walk hands
			// it over and doesn't descend: nothing inside is matched
			// twice.
			for _, m := range dirMatchers {
				if rel == m.Path || strings.HasSuffix(rel, "/"+m.Path) {
					if !skipPaths[rel] {
						hits = append(hits, projectHit{path: rel, source: m})
					}
					return fs.SkipDir
				}
			}
			return nil
		}
		if skipPaths[rel] {
			return nil
		}
		// A SKILL.md at the very root of the tree is not a skill (a
		// skill directory contains it), and ingesting the root as one
		// would sweep the whole tree into its file list.
		if path.Base(rel) == "SKILL.md" && rel != "SKILL.md" {
			skillHits = append(skillHits, rel)
			return nil
		}
		for _, m := range matchers {
			if rel == m.Path || strings.HasSuffix(rel, "/"+m.Path) {
				hits = append(hits, projectHit{path: rel, source: m})
				return nil
			}
		}
		return nil
	})
	return hits, skillHits
}
