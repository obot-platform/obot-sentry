package scan

import (
	"context"
	"path/filepath"
	"slices"
	"testing"
	"testing/fstest"

	"github.com/obot-platform/obot/apiclient/types"
)

// mapRoot builds a darwin-platform primary Root over an in-memory fs
// rooted at /home/test.
func mapRoot(files map[string]string) Root {
	mapfs := fstest.MapFS{}
	for p, body := range files {
		mapfs[p] = &fstest.MapFile{Data: []byte(body)}
	}
	return Root{FS: mapfs, Path: "/home/test", Platform: "darwin", Primary: true}
}

// runScan scans an in-memory home with host detection neutralized, so
// only evidence inside the fixture counts.
func runScan(t *testing.T, files map[string]string) types.DeviceScanManifest {
	t.Helper()
	return runScanRoots(t, []Root{mapRoot(files)})
}

// isolateHost points absolute Installed globs at an empty directory so
// detection never sees the developer's real /Applications tree.
func isolateHost(t *testing.T) {
	t.Helper()
	t.Setenv("OPENCLAW_PROFILE", "")
	hostRoot = t.TempDir()
	t.Cleanup(func() { hostRoot = "" })
}

func runScanRoots(t *testing.T, roots []Root) types.DeviceScanManifest {
	t.Helper()
	isolateHost(t)

	manifest, err := Scan(context.Background(), Options{Roots: roots, MaxDepth: 8})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	return manifest
}

// skillClients returns the sorted set of Client values on skill rows
// whose File ends with the given SKILL.md suffix.
func skillClients(manifest types.DeviceScanManifest, fileSuffix string) []string {
	var out []string
	for _, sk := range manifest.Skills {
		if filepath.ToSlash(sk.File) == fileSuffix || len(sk.File) > len(fileSuffix) &&
			filepath.ToSlash(sk.File)[len(sk.File)-len(fileSuffix):] == fileSuffix {
			out = append(out, sk.Client)
		}
	}
	slices.Sort(out)
	return out
}

func clientNames(manifest types.DeviceScanManifest) []string {
	out := make([]string, 0, len(manifest.Clients))
	for _, c := range manifest.Clients {
		out = append(out, c.Name)
	}
	return out
}

func findClient(manifest types.DeviceScanManifest, name string) *types.DeviceScanClient {
	for i, c := range manifest.Clients {
		if c.Name == name {
			return &manifest.Clients[i]
		}
	}
	return nil
}

func namedSkill(name string) string {
	return "---\nname: " + name + "\ndescription: does things\n---\nbody\n"
}

// TestSkillDirs_ReadersResolve is the guard against the drift that made
// antigravity unreportable: it appeared in the skills
// table but had no client, so nothing could ever detect them and their
// rows could only be conjured from a skill file.
//
// Readers are free-form strings so the table reads like the vendor docs
// it's transcribed from; this is what keeps them honest.
func TestSkillDirs_ReadersResolve(t *testing.T) {
	for _, platform := range []string{"darwin", "linux", "windows"} {
		known := map[string]bool{}
		for _, c := range clients(platform) {
			known[c.Name] = true
		}
		for _, table := range [][]Location{skillDirs, skillTrees} {
			for _, loc := range table {
				if loc.Scope == 0 {
					t.Errorf("%s: %q has no scope", platform, loc.Path)
				}
				if len(loc.Readers) == 0 {
					t.Errorf("%s: %q has no readers", platform, loc.Path)
				}
				for _, r := range loc.Readers {
					if !known[r] {
						t.Errorf("%s: %q is read by %q, which has no Client entry", platform, loc.Path, r)
					}
				}
			}
		}
	}
}

// TestClients_Wellformed: every client needs a name and evidence, or it
// can never be reported.
func TestClients_Wellformed(t *testing.T) {
	for _, platform := range []string{"darwin", "linux", "windows"} {
		seen := map[string]bool{}
		for _, c := range clients(platform) {
			if c.Name == "" || c.Name == MultiClient {
				t.Errorf("%s: bad client name %q", platform, c.Name)
			}
			if seen[c.Name] {
				t.Errorf("%s: duplicate client %q", platform, c.Name)
			}
			seen[c.Name] = true
			if len(c.Installed) == 0 {
				t.Errorf("%s: client %q has no install evidence", platform, c.Name)
			}
		}
	}
}

// TestScan_SkillClientSets verifies that a skill in a shared directory
// is attributed to every client that both reads that location and is
// actually on the device. Claude Code and Cursor are installed here
// (runtime state); Codex, OpenCode, VS Code and Antigravity are not.
//
// This is the regression for obot#7288: attributing ~/.claude/skills to
// all four documented readers reported clients that were never
// installed.
func TestScan_SkillClientSets(t *testing.T) {
	manifest := runScan(t, map[string]string{
		".claude/history.jsonl":              "{}\n",
		".cursor/ide_state.json":             "{}\n",
		".agents/skills/doc/SKILL.md":        namedSkill("doc"),
		".claude/skills/pdf/SKILL.md":        namedSkill("pdf"),
		".gemini/config/skills/gem/SKILL.md": namedSkill("gem"),
	})

	// .agents/skills is read by codex, cursor, opencode and vscode; only
	// cursor is here.
	if got, want := skillClients(manifest, ".agents/skills/doc/SKILL.md"), []string{"cursor"}; !slices.Equal(got, want) {
		t.Errorf("~/.agents/skills clients = %v, want %v", got, want)
	}
	if got, want := skillClients(manifest, ".claude/skills/pdf/SKILL.md"), []string{"claude_code", "cursor"}; !slices.Equal(got, want) {
		t.Errorf("~/.claude/skills clients = %v, want %v", got, want)
	}
	// Antigravity reads ~/.gemini/config/skills but isn't installed, so
	// the skill is still inventoried, just unattributed.
	if got, want := skillClients(manifest, ".gemini/config/skills/gem/SKILL.md"), []string{MultiClient}; !slices.Equal(got, want) {
		t.Errorf("~/.gemini/config/skills clients = %v, want %v", got, want)
	}

	for _, name := range []string{"claude_code", "cursor"} {
		c := findClient(manifest, name)
		if c == nil {
			t.Errorf("no clients[] row for installed client %q", name)
			continue
		}
		if !c.HasSkills {
			t.Errorf("HasSkills = false for %q", name)
		}
	}
	for _, name := range []string{"codex", "opencode", "vscode", "antigravity", MultiClient, ""} {
		if c := findClient(manifest, name); c != nil {
			t.Errorf("clients[] row for %q, which is not on the device: %+v", name, c)
		}
	}
}

// TestScan_AntigravityConfigPath: Antigravity's configuration home is
// ~/.gemini/config, reported as ConfigPath ahead of the dot-directories
// — and without them, since not every install has one.
func TestScan_AntigravityConfigPath(t *testing.T) {
	t.Run("gemini config preferred", func(t *testing.T) {
		manifest := runScan(t, map[string]string{
			".gemini/antigravity/installation_id": "x\n",
			".gemini/config/mcp_config.json":      `{"mcpServers":{}}`,
			".antigravity-ide/argv.json":          "{}\n",
		})
		c := findClient(manifest, "antigravity")
		if c == nil {
			t.Fatal("no clients[] row for antigravity")
		}
		if want := filepath.Join("/home/test", ".gemini/config"); c.ConfigPath != want {
			t.Errorf("ConfigPath = %q, want %q", c.ConfigPath, want)
		}
	})

	// The dot-directory is the fallback when ~/.gemini/config is absent.
	t.Run("dot-directory fallback", func(t *testing.T) {
		manifest := runScan(t, map[string]string{
			".gemini/antigravity/installation_id": "x\n",
			".antigravity/argv.json":              "{}\n",
		})
		c := findClient(manifest, "antigravity")
		if c == nil {
			t.Fatal("no clients[] row for antigravity")
		}
		if want := filepath.Join("/home/test", ".antigravity"); c.ConfigPath != want {
			t.Errorf("ConfigPath = %q, want %q", c.ConfigPath, want)
		}
	})
}

// TestScan_Issue7288 reproduces both machines from the bug report.
//
// macOS: five clients installed and configured, plus skills in
// ~/.claude/skills and ~/.agents/skills. OpenCode reads both directories
// but is not on the device, and used to get a row anyway.
//
// Windows: only VS Code installed. ~/.claude exists because hook-install
// created it, and holds a skill. claude_code, codex and cursor used to
// be reported off the back of that one file.
func TestScan_Issue7288(t *testing.T) {
	t.Run("macos", func(t *testing.T) {
		manifest := runScan(t, map[string]string{
			".claude/history.jsonl":  "{}\n",
			".claude/settings.json":  "{}\n", // hook-install's own file
			".codex/installation_id": "x\n",
			".cursor/ide_state.json": "{}\n",
			"Library/Application Support/Code/User/globalStorage/state.db": "",
			"Library/Application Support/Claude/Local Storage/x":           "",

			".claude/skills/pdf/SKILL.md": namedSkill("pdf"),
			".agents/skills/doc/SKILL.md": namedSkill("doc"),
		})

		if c := findClient(manifest, "opencode"); c != nil {
			t.Errorf("opencode reported without being installed: %+v", c)
		}
		if got, want := clientNames(manifest), []string{"claude_code", "claude_desktop", "codex", "cursor", "vscode"}; !slices.Equal(got, want) {
			t.Errorf("clients = %v, want %v", got, want)
		}
		// Both skills survive, attributed only to installed readers.
		if got, want := skillClients(manifest, ".claude/skills/pdf/SKILL.md"), []string{"claude_code", "cursor", "vscode"}; !slices.Equal(got, want) {
			t.Errorf("~/.claude/skills clients = %v, want %v", got, want)
		}
		if got, want := skillClients(manifest, ".agents/skills/doc/SKILL.md"), []string{"codex", "cursor", "vscode"}; !slices.Equal(got, want) {
			t.Errorf("~/.agents/skills clients = %v, want %v", got, want)
		}
	})

	t.Run("windows", func(t *testing.T) {
		root := mapRoot(map[string]string{
			".claude/settings.json":                            "{}\n", // hook-install's own file
			".claude/skills/pdf/SKILL.md":                      namedSkill("pdf"),
			".agents/skills/doc/SKILL.md":                      namedSkill("doc"),
			"AppData/Roaming/Code/User/globalStorage/state.db": "",
		})
		root.Platform = "windows"
		manifest := runScanRoots(t, []Root{root})

		if got, want := clientNames(manifest), []string{"vscode"}; !slices.Equal(got, want) {
			t.Errorf("clients = %v, want %v", got, want)
		}
		// VS Code reads both directories, so both skills stay attributed
		// to it; only the uninstalled readers drop.
		if got, want := skillClients(manifest, ".claude/skills/pdf/SKILL.md"), []string{"vscode"}; !slices.Equal(got, want) {
			t.Errorf("~/.claude/skills clients = %v, want %v", got, want)
		}
		if got, want := skillClients(manifest, ".agents/skills/doc/SKILL.md"), []string{"vscode"}; !slices.Equal(got, want) {
			t.Errorf("~/.agents/skills clients = %v, want %v", got, want)
		}
	})
}

// TestScan_NoClientsInstalled: skills are never dropped just because
// nothing that reads them is installed — they fall to the MultiClient
// sentinel, which never becomes a clients[] row.
func TestScan_NoClientsInstalled(t *testing.T) {
	manifest := runScan(t, map[string]string{
		".claude/settings.json":       "{}\n", // what hook-install writes
		".claude/skills/pdf/SKILL.md": namedSkill("pdf"),
		".agents/skills/doc/SKILL.md": namedSkill("doc"),
	})

	if len(manifest.Clients) != 0 {
		t.Errorf("Clients = %+v, want none", manifest.Clients)
	}
	if len(manifest.Skills) != 2 {
		t.Fatalf("want both skills inventoried, got %+v", manifest.Skills)
	}
	for _, sk := range manifest.Skills {
		if sk.Client != MultiClient {
			t.Errorf("skill %q attributed to %q, want %q", sk.Name, sk.Client, MultiClient)
		}
	}
}

// TestScan_ProjectSkills covers project-scope attribution: the client
// set can differ from the global set for the same directory name
// (Antigravity reads project .agents/skills but not ~/.agents/skills),
// and ProjectPath points at the enclosing project.
func TestScan_ProjectSkills(t *testing.T) {
	manifest := runScan(t, map[string]string{
		"Library/Application Support/Code/User/globalStorage/state.db": "",
		".cursor/ide_state.json":                  "{}\n",
		"work/app/.agents/skills/deploy/SKILL.md": namedSkill("deploy"),
		"work/app/.github/skills/gh/SKILL.md":     namedSkill("gh"),
	})

	// Project .agents/skills is read by antigravity, codex, cursor,
	// opencode and vscode; cursor and vscode are the installed ones.
	if got, want := skillClients(manifest, "work/app/.agents/skills/deploy/SKILL.md"), []string{"cursor", "vscode"}; !slices.Equal(got, want) {
		t.Errorf("project .agents/skills clients = %v, want %v", got, want)
	}
	if got, want := skillClients(manifest, "work/app/.github/skills/gh/SKILL.md"), []string{"vscode"}; !slices.Equal(got, want) {
		t.Errorf("project .github/skills clients = %v, want %v", got, want)
	}

	wantProject := filepath.Join("/home/test", "work", "app")
	for _, sk := range manifest.Skills {
		if sk.Name == "deploy" && sk.ProjectPath != wantProject {
			t.Errorf("ProjectPath = %q, want %q", sk.ProjectPath, wantProject)
		}
	}
}

// TestScan_SkillFallbacks covers markers outside any documented skills
// directory: ones under a client's own dot-dir attribute to that
// client, the rest are emitted once under the MultiClient wire sentinel
// — which never becomes a clients[] row.
func TestScan_SkillFallbacks(t *testing.T) {
	manifest := runScan(t, map[string]string{
		".hermes/auth.json":                            "{}\n",
		".hermes/skills/official/apple/notes/SKILL.md": namedSkill("apple-notes"),
		"repos/tools/loose/SKILL.md":                   namedSkill("loose"),
	})

	if got, want := skillClients(manifest, ".hermes/skills/official/apple/notes/SKILL.md"), []string{"hermes"}; !slices.Equal(got, want) {
		t.Errorf("~/.hermes skill clients = %v, want %v", got, want)
	}
	if got, want := skillClients(manifest, "repos/tools/loose/SKILL.md"), []string{MultiClient}; !slices.Equal(got, want) {
		t.Errorf("free-floating skill clients = %v, want %v", got, want)
	}
	if c := findClient(manifest, MultiClient); c != nil {
		t.Errorf("clients[] row synthesized for the %s sentinel", MultiClient)
	}
}

// TestScan_RootSkillMarkerIgnored: a SKILL.md sitting directly at the
// root of a scan root is not a skill — ingesting the root itself would
// sweep the whole home into one skill's file list.
func TestScan_RootSkillMarkerIgnored(t *testing.T) {
	manifest := runScan(t, map[string]string{
		"SKILL.md":  namedSkill("home-itself"),
		"notes.md":  "unrelated\n",
		"scripts/x": "unrelated\n",
	})
	if len(manifest.Skills) != 0 {
		t.Errorf("root-level SKILL.md produced skills: %+v", manifest.Skills)
	}
}

// TestScan_NestedSkillMarkersSuppressed: SKILL.md files nested inside a
// skill's own directory (vendored examples, references) don't become
// separate skills — in documented skills dirs the skill is normalized
// to the immediate child directory.
func TestScan_NestedSkillMarkersSuppressed(t *testing.T) {
	manifest := runScan(t, map[string]string{
		".claude/skills/creator/SKILL.md":                namedSkill("creator"),
		".claude/skills/creator/examples/demo/SKILL.md":  namedSkill("demo"),
		"app/.agents/skills/top/SKILL.md":                namedSkill("top"),
		"app/.agents/skills/top/references/ref/SKILL.md": namedSkill("ref"),
	})

	names := map[string]int{}
	for _, sk := range manifest.Skills {
		names[sk.Name]++
	}
	if names["demo"] != 0 {
		t.Errorf("nested SKILL.md under a global skill dir produced %d rows", names["demo"])
	}
	if names["ref"] != 0 {
		t.Errorf("nested SKILL.md under a project skill dir produced %d rows", names["ref"])
	}
	if names["creator"] == 0 || names["top"] == 0 {
		t.Errorf("top-level skills missing: %v", names)
	}
}

// TestScan_SkillMetadata verifies frontmatter parsing, the directory
// name fallback, artifact listing, scripts detection, and SKILL.md
// content capture in files[].
func TestScan_SkillMetadata(t *testing.T) {
	manifest := runScan(t, map[string]string{
		".claude/skills/full/SKILL.md":       "---\nname: full-skill\ndescription: a described skill\n---\nbody\n",
		".claude/skills/full/scripts/run.sh": "#!/bin/sh\n",
		".claude/skills/full/notes.md":       "notes\n",
		".claude/skills/full/image.png":      "binary",
		".claude/skills/bare/SKILL.md":       "no frontmatter\n",
	})

	byName := map[string]types.DeviceScanSkill{}
	for _, sk := range manifest.Skills {
		byName[sk.Name] = sk
	}

	full, ok := byName["full-skill"]
	if !ok {
		t.Fatalf("full-skill not found: %+v", manifest.Skills)
	}
	if full.Description != "a described skill" {
		t.Errorf("Description = %q", full.Description)
	}
	if !full.HasScripts {
		t.Errorf("HasScripts = false")
	}
	wantFiles := []string{
		filepath.Join("/home/test", ".claude/skills/full/SKILL.md"),
		filepath.Join("/home/test", ".claude/skills/full/notes.md"),
		filepath.Join("/home/test", ".claude/skills/full/scripts/run.sh"),
	}
	gotFiles := slices.Clone(full.Files)
	slices.Sort(gotFiles)
	slices.Sort(wantFiles)
	if !slices.Equal(gotFiles, wantFiles) {
		t.Errorf("Files = %v, want %v (png excluded)", gotFiles, wantFiles)
	}

	if _, ok := byName["bare"]; !ok {
		t.Errorf("skill without frontmatter should fall back to directory name; got %+v", manifest.Skills)
	}

	var markerCaptured bool
	for _, f := range manifest.Files {
		if filepath.ToSlash(f.Path) == "/home/test/.claude/skills/full/SKILL.md" && f.Content != "" {
			markerCaptured = true
		}
	}
	if !markerCaptured {
		t.Errorf("SKILL.md content not captured in files[]: %+v", manifest.Files)
	}
}

// TestScan_MultiRoot verifies observations from a second root (e.g. a
// WSL home scanned from Windows) merge into one manifest with paths
// anchored at each root.
func TestScan_MultiRoot(t *testing.T) {
	rootA := mapRoot(map[string]string{".claude/skills/a/SKILL.md": namedSkill("a")})
	rootB := Root{
		FS:         fstest.MapFS{".agents/skills/b/SKILL.md": &fstest.MapFile{Data: []byte(namedSkill("b"))}},
		Path:       "/mnt/wsl/ubuntu/home/other",
		NativePath: "/home/other",
		Platform:   "linux",
	}

	manifest := runScanRoots(t, []Root{rootA, rootB})

	var haveA, haveB bool
	for _, sk := range manifest.Skills {
		switch sk.Name {
		case "a":
			haveA = true
			if want := filepath.Join("/home/test", ".claude/skills/a/SKILL.md"); sk.File != want {
				t.Errorf("root A skill file = %q, want %q", sk.File, want)
			}
		case "b":
			haveB = true
			if want := filepath.Join("/mnt/wsl/ubuntu/home/other", ".agents/skills/b/SKILL.md"); sk.File != want {
				t.Errorf("root B skill file = %q, want %q", sk.File, want)
			}
		}
	}
	if !haveA || !haveB {
		t.Fatalf("skills from both roots expected; got %+v", manifest.Skills)
	}
}

func TestRelToHome(t *testing.T) {
	s := newState(Root{
		Path:       `/mnt/wsl/ubuntu/home/other`,
		NativePath: "/home/other",
	}, DefaultMaxDepth, nil, nil)

	if rel, ok := s.relToHome("/home/other/.claude/plugins/x"); !ok || rel != ".claude/plugins/x" {
		t.Errorf("native-base path: rel=%q ok=%v", rel, ok)
	}
	if rel, ok := s.relToHome("/mnt/wsl/ubuntu/home/other/.claude"); !ok || rel != ".claude" {
		t.Errorf("host-base path: rel=%q ok=%v", rel, ok)
	}
	if _, ok := s.relToHome("/etc/passwd"); ok {
		t.Errorf("outside path should not resolve")
	}
}

func TestWalk(t *testing.T) {
	fsys := fstest.MapFS{
		"proj/.fake/config.json":           &fstest.MapFile{Data: []byte("{}")},
		".fake/config.json":                &fstest.MapFile{Data: []byte("{}")}, // suppressed via skipPaths
		"proj/skills/x/SKILL.md":           &fstest.MapFile{Data: []byte("x")},
		"node_modules/p/.fake/config.json": &fstest.MapFile{Data: []byte("{}")}, // pruned dir
		"a/b/c/d/e/f/.fake/config.json":    &fstest.MapFile{Data: []byte("{}")}, // beyond depth
	}
	src := Source{Path: ".fake/config.json", Scope: Project, Read: noopRead}
	s := newState(Root{FS: fsys, Path: "/home/test"}, 4, nil, nil)

	hits, markers := walk(context.Background(), s, []Source{src}, map[string]bool{".fake/config.json": true})

	var hitPaths []string
	for _, h := range hits {
		hitPaths = append(hitPaths, h.path)
	}
	if want := []string{"proj/.fake/config.json"}; !slices.Equal(hitPaths, want) {
		t.Errorf("hits = %v, want %v", hitPaths, want)
	}
	if want := []string{"proj/skills/x/SKILL.md"}; !slices.Equal(markers, want) {
		t.Errorf("markers = %v, want %v", markers, want)
	}
}

func TestParseFrontmatter(t *testing.T) {
	cases := []struct {
		name, in, wantName, wantDescription string
	}{
		{"happy", "---\nname: pdf\ndescription: handles PDFs\n---\nbody", "pdf", "handles PDFs"},
		{"no frontmatter", "just a doc", "", ""},
		{"unterminated", "---\nname: pdf\n", "", ""},
		{"malformed yaml", "---\n: : :\n---\n", "", ""},
		{"non-string values", "---\nname: [a, b]\ndescription: 42\n---\n", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			name, description := parseFrontmatter([]byte(c.in))
			if name != c.wantName || description != c.wantDescription {
				t.Errorf("parseFrontmatter = (%q, %q), want (%q, %q)", name, description, c.wantName, c.wantDescription)
			}
		})
	}
}

func TestReadGitOrigin(t *testing.T) {
	fsys := fstest.MapFS{
		"repo/.git/config": &fstest.MapFile{Data: []byte(
			"[core]\n\trepositoryformatversion = 0\n[remote \"upstream\"]\n\turl = https://example.com/upstream.git\n[remote \"origin\"]\n\turl = https://example.com/origin.git\n",
		)},
		"norepo/file.txt": &fstest.MapFile{Data: []byte("x")},
	}
	if got := readGitOrigin(fsys, "repo"); got != "https://example.com/origin.git" {
		t.Errorf("readGitOrigin = %q", got)
	}
	if got := readGitOrigin(fsys, "norepo"); got != "" {
		t.Errorf("readGitOrigin on non-repo = %q", got)
	}
}

// noopRead is a decoder for walk tests, which only assert routing.
func noopRead(*state, string, string) observations { return observations{} }
