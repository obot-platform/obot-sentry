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

// skillDirRule is one documented skills directory: a directory whose
// immediate child directories are skills (<dir>/<name>/SKILL.md), and
// the clients that discover skills there. The same directory name can
// be read by different client sets depending on whether it sits at the
// root of a home directory (global) or inside a project (project);
// nil means the location is not documented for that scope.
type skillDirRule struct {
	dir     string
	global  []string
	project []string
}

// skillDirRules captures each client's documented skill discovery
// paths (client sets are sorted):
//
//   - Claude Code: https://code.claude.com/docs/en/skills
//   - Cursor: https://cursor.com/docs/skills.md
//   - Codex: https://developers.openai.com/codex/skills
//   - OpenCode: https://opencode.ai/docs/skills/
//   - VS Code / Copilot: https://code.visualstudio.com/docs/agent-customization/agent-skills
//   - Antigravity: https://antigravity.google/docs/skills
//
// Shared directories are the reason a skill carries a client *set*: a
// skill under ~/.agents/skills is discoverable by every client that
// respects the .agents convention, not by one owner.
var skillDirRules = []skillDirRule{
	{dir: ".agent/skills", // legacy Antigravity layout
		project: []string{"antigravity"}},
	{dir: ".agents/skills",
		global:  []string{"codex", "cursor", "opencode", "vscode"},
		project: []string{"antigravity", "codex", "cursor", "opencode", "vscode"}},
	{dir: ".claude/skills",
		global:  []string{"claude_code", "cursor", "opencode", "vscode"},
		project: []string{"claude_code", "cursor", "opencode", "vscode"}},
	{dir: ".codex/skills", // Cursor compatibility path; current Codex reads .agents/skills
		global:  []string{"cursor"},
		project: []string{"cursor"}},
	{dir: ".config/opencode/skills",
		global: []string{"opencode"}},
	{dir: ".copilot/skills",
		global: []string{"vscode"}},
	{dir: ".cursor/skills",
		global:  []string{"cursor"},
		project: []string{"cursor"}},
	{dir: ".gemini/config/skills",
		global: []string{"antigravity"}},
	{dir: ".github/skills",
		project: []string{"vscode"}},
	{dir: ".opencode/skills",
		project: []string{"opencode"}},
	{dir: ".skillport/skills",
		global: []string{"skillport"}},
}

// homeClientTool maps the first home-relative path component to a
// client, used as a fallback to attribute SKILL.md files found
// anywhere under a client's own dot-directory but outside a documented
// skills directory (e.g. .hermes/skills/official/apple/…/SKILL.md).
var homeClientTool = map[string]string{
	".claude":    "claude_code",
	".codeium":   "windsurf",
	".codex":     "codex",
	".cursor":    "cursor",
	".hermes":    "hermes",
	".skillport": "skillport",
	".windsurf":  "windsurf",
}

// scanSkills discovers skills under one root: the documented global
// skills directories first (enumerated directly, so they don't depend
// on walk depth), then the SKILL.md markers the walk collected.
func scanSkills(s *state, markers []string) []skill {
	var out []skill

	globalDirs := make([]string, 0, len(skillDirRules))
	for _, rule := range skillDirRules {
		if rule.global == nil {
			continue
		}
		globalDirs = append(globalDirs, rule.dir)
		entries, err := fs.ReadDir(s.fsys, rule.dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			if sk, ok := ingestSkill(s, path.Join(rule.dir, e.Name()), rule.global, ""); ok {
				out = append(out, sk)
			}
		}
	}

	seen := map[string]bool{}
	for _, marker := range markers {
		if underAnyDir(marker, globalDirs) || s.claimedUnder(marker) {
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
func classifySkillMarker(s *state, rel string) (skillDir string, clients []string, projectPath string) {
	bestStart := -1
	var bestRule skillDirRule
	for _, rule := range skillDirRules {
		if rule.project == nil {
			continue
		}
		if i := strings.LastIndex(rel, "/"+rule.dir+"/"); i >= 0 && i+1 > bestStart {
			bestStart = i + 1
			bestRule = rule
		}
	}
	// A documented project skills dir at the very root of the home
	// counts too (rule.global == nil there means the docs only describe
	// the project scope; treat the home itself as the project).
	if bestStart < 0 {
		for _, rule := range skillDirRules {
			if rule.project != nil && strings.HasPrefix(rel, rule.dir+"/") {
				bestStart = 0
				bestRule = rule
				break
			}
		}
	}

	if bestStart >= 0 {
		dirEnd := bestStart + len(bestRule.dir)
		skillDir = rel[:dirEnd]
		if child, _, ok := strings.Cut(rel[dirEnd+1:], "/"); ok {
			skillDir = skillDir + "/" + child
		}
		projectRel := "."
		if bestStart > 0 {
			projectRel = rel[:bestStart-1]
		}
		return skillDir, bestRule.project, s.abs(projectRel)
	}

	skillDir = path.Dir(rel)
	first, _, _ := strings.Cut(rel, "/")
	if tool, ok := homeClientTool[first]; ok {
		return skillDir, []string{tool}, ""
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
