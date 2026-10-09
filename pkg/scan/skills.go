package scan

import (
	"io/fs"
	"path"
	"strings"

	"github.com/obot-platform/obot/apiclient/types"
)

// MultiClient is the wire client tag for skills no known client
// discovers (e.g. a free-floating SKILL.md in a repo). The server's
// aggregations special-case this sentinel, so it stays on the wire
// until the schema can carry a client set per skill row.
const MultiClient = "multi"

// skill is one discovered skill plus the set of clients that can
// discover it. The embedded wire row's Client field is left empty;
// build fans the row out per client.
type skill struct {
	types.DeviceScanSkill
	// clients are the canonical names of the clients that read the
	// skill's location. Empty when no known client discovers it.
	clients []string
}

// skillExtensions is the extension allowlist for files counted as part
// of a skill's manifest. The paths are listed on Skill.Files but only
// SKILL.md content is uploaded into the scan's top-level files[] — the
// rest are path-only references.
var skillExtensions = map[string]bool{
	".md":  true,
	".mdc": true,
	".txt": true,
	".sh":  true,
	".py":  true,
	".js":  true,
	".ts":  true,
}

// Scope is where a Location applies: at the root of a scanned home, or
// inside a project anywhere the walk reaches. A location documented for
// both scopes with the same readers sets both bits; one whose readers
// differ per scope gets an entry each.
type Scope uint8

const (
	Home Scope = 1 << iota
	Project
)

func (s Scope) has(other Scope) bool { return s&other != 0 }

// Location is a place artifacts live — a path whose layout is a
// documented convention — and the clients that read it.
//
// Locations are keyed by path rather than by client because the
// conventions are increasingly client-neutral: ~/.agents/skills is read
// by four clients and owned by none. Attribution is a property of where
// an artifact was found, not of any one client, and Readers may name
// clients that turn out not to be installed — build intersects them
// with what detection found.
type Location struct {
	Path    string
	Scope   Scope
	Readers []string
}

// skillDirs are the documented skills directories: a directory whose
// immediate child directories are skills (<dir>/<name>/SKILL.md), and
// the clients that discover skills there. Readers are sorted.
//
//   - Claude Code: https://code.claude.com/docs/en/skills
//   - Cursor: https://cursor.com/docs/skills.md
//   - Codex: https://developers.openai.com/codex/skills
//   - OpenCode: https://opencode.ai/docs/skills/
//   - VS Code / Copilot: https://code.visualstudio.com/docs/agent-customization/agent-skills
//   - Antigravity: https://antigravity.google/docs/skills
//   - Kiro: https://kiro.dev/docs/skills/
var skillDirs = []Location{
	// Legacy Antigravity layout.
	{".agent/skills", Project, []string{"antigravity"}},
	// Antigravity reads .agents/skills in a project but not at home.
	{".agents/skills", Home, []string{"codex", "cursor", "opencode", "vscode"}},
	{".agents/skills", Project, []string{"antigravity", "codex", "cursor", "opencode", "vscode"}},
	{".claude/skills", Home | Project, []string{"claude_code", "cursor", "opencode", "vscode"}},
	// Cursor compatibility path; current Codex reads .agents/skills.
	{".codex/skills", Home | Project, []string{"cursor"}},
	{".config/opencode/skills", Home, []string{"opencode"}},
	{".copilot/skills", Home, []string{"vscode"}},
	{".cursor/skills", Home | Project, []string{"cursor"}},
	{".gemini/config/skills", Home, []string{"antigravity"}},
	// Skills Antigravity ships with itself; its plugin skills come out
	// with the plugin that owns them.
	{antigravityBuiltinSkills, Home, []string{"antigravity"}},
	{".github/skills", Project, []string{"vscode"}},
	{".kiro/skills", Home | Project, []string{"kiro"}},
	{".opencode/skills", Project, []string{"opencode"}},
}

// skillTrees are directories where a SKILL.md *anywhere* below counts,
// used as a last resort for markers outside every skillDirs entry — e.g.
// .hermes/skills/official/apple/notes/SKILL.md, which is nested deeper
// than the documented <dir>/<name>/SKILL.md layout.
//
// These deliberately carry Readers like any other location rather than
// naming a single owner. A client's directory is not the same thing as a
// client: ~/.claude/skills is read by four clients (see skillDirs),
// while a SKILL.md elsewhere under ~/.claude is only meaningful to
// Claude Code, because the others read that one documented path and not
// the tree around it. Stating each set here makes that reviewable
// instead of implied.
//
// Matched only at the root of a home, and only after skillDirs.
var skillTrees = []Location{
	{".claude", Home, []string{"claude_code"}},
	{".codex", Home, []string{"codex"}},
	{".cursor", Home, []string{"cursor"}},
	{".hermes", Home, []string{"hermes"}},
}

// readersOfTree returns the clients that read the skill tree rel sits
// under, if any.
func readersOfTree(rel string) ([]string, bool) {
	first, _, _ := strings.Cut(rel, "/")
	if first == "" {
		return nil, false
	}
	for _, loc := range skillTrees {
		if loc.Path == first {
			return loc.Readers, true
		}
	}
	return nil, false
}

// scanSkills discovers skills under one root: the documented global
// skills directories first (enumerated directly, so they don't depend
// on walk depth), then the SKILL.md markers the walk collected.
func scanSkills(s *state, markers []string) []skill {
	var out []skill

	homeDirs := make([]string, 0, len(skillDirs))
	for _, loc := range skillDirs {
		if !loc.Scope.has(Home) {
			continue
		}
		homeDirs = append(homeDirs, loc.Path)
		entries, err := fs.ReadDir(s.fsys, loc.Path)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			if sk, ok := ingestSkill(s, path.Join(loc.Path, e.Name()), loc.Readers, ""); ok {
				out = append(out, sk)
			}
		}
	}

	seen := map[string]bool{}
	for _, marker := range markers {
		if underAnyDir(marker, homeDirs) || s.claimedUnder(marker) {
			// Already handled above (or by a plugin scan); also
			// suppresses nested SKILL.md files inside a global skill's
			// own directory.
			continue
		}
		skillDir, clients, projectPath := classifySkillMarker(s, marker)
		if seen[skillDir] {
			continue
		}
		seen[skillDir] = true
		if sk, ok := ingestSkill(s, skillDir, clients, projectPath); ok {
			out = append(out, sk)
		}
	}
	return out
}

// classifySkillMarker attributes one walk-discovered SKILL.md path.
// The deepest documented skills directory on the path wins; the skill
// directory is normalized to that directory's immediate child, so
// nested SKILL.md files (vendored examples, references) don't become
// phantom skills. Markers outside any documented skills directory fall
// back to the owning home dot-directory, or to no clients at all.
func classifySkillMarker(s *state, rel string) (skillDir string, readers []string, projectPath string) {
	bestStart := -1
	var best Location
	for _, loc := range skillDirs {
		if !loc.Scope.has(Project) {
			continue
		}
		if i := strings.LastIndex(rel, "/"+loc.Path+"/"); i >= 0 && i+1 > bestStart {
			bestStart = i + 1
			best = loc
		}
	}
	// A project skills dir at the very root of the home counts too: a
	// location documented only for Project scope still applies there,
	// with the home itself as the project.
	if bestStart < 0 {
		for _, loc := range skillDirs {
			if loc.Scope.has(Project) && strings.HasPrefix(rel, loc.Path+"/") {
				bestStart = 0
				best = loc
				break
			}
		}
	}

	if bestStart >= 0 {
		dirEnd := bestStart + len(best.Path)
		skillDir = rel[:dirEnd]
		if child, _, ok := strings.Cut(rel[dirEnd+1:], "/"); ok {
			skillDir = skillDir + "/" + child
		}
		projectRel := "."
		if bestStart > 0 {
			projectRel = rel[:bestStart-1]
		}
		return skillDir, best.Readers, s.abs(projectRel)
	}

	skillDir = path.Dir(rel)
	if readers, ok := readersOfTree(rel); ok {
		return skillDir, readers, ""
	}
	return skillDir, nil, s.abs(skillDir)
}

// underAnyDir reports whether rel sits below any of the given
// root-relative directories.
func underAnyDir(rel string, dirs []string) bool {
	for _, d := range dirs {
		if strings.HasPrefix(rel, d+"/") {
			return true
		}
	}
	return false
}

// ingestSkill builds a skill for the directory at skillDirRel. clients
// may be empty for free-floating SKILL.md files with no discovering
// client. projectPath is the absolute project root for project-scope
// skills, "" otherwise. ok=false when the directory has no readable
// SKILL.md at its root.
func ingestSkill(s *state, skillDirRel string, clients []string, projectPath string) (skill, bool) {
	markerRel := path.Join(skillDirRel, "SKILL.md")
	markerData, err := fs.ReadFile(s.fsys, markerRel)
	if err != nil {
		return skill{}, false
	}
	name, description := parseFrontmatter(markerData)
	if name == "" {
		name = clipRunes(path.Base(skillDirRel), skillNameMaxRunes)
	}

	return skill{
		DeviceScanSkill: types.DeviceScanSkill{
			ProjectPath:  projectPath,
			File:         s.addFileOrAbs(markerRel),
			Name:         name,
			Description:  description,
			Files:        s.listArtifactPaths(skillDirRel, skillExtensions),
			HasScripts:   dirExists(s.fsys, path.Join(skillDirRel, "scripts")),
			GitRemoteURL: readGitOrigin(s.fsys, skillDirRel),
		},
		clients: clients,
	}, true
}
