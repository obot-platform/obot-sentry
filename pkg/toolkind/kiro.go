package toolkind

// kiroKinds classifies Kiro's built-in tools by id, the name its hooks receive
// as tool_name. Kiro names them snake_case (execute_bash, fs_write), which the
// substring heuristics in Kind misread — execute_bash has no "shell" in it — so
// they are listed exactly, with the category Kiro itself tags each one with
// (read, write, shell) in the Kiro 1.2 agent extension.
var kiroKinds = map[string]string{
	"execute_bash":         KindShell,
	"execute_pwsh":         KindShell,
	"control_bash_process": KindShell,
	"control_pwsh_process": KindShell,

	"read_file":          KindRead,
	"read_code":          KindRead,
	"file_search":        KindRead,
	"grep_search":        KindRead,
	"list_directory":     KindRead,
	"get_diagnostics":    KindRead,
	"list_memories":      KindRead,
	"read_page":          KindRead,
	"list_browser_pages": KindRead,
	"capture_screenshot": KindRead,
	"screenshot_page":    KindRead,
	"tool_search":        KindRead,
	"tool_load":          KindRead,

	"fs_write":        KindWrite,
	"fs_append":       KindWrite,
	"str_replace":     KindWrite,
	"delete_file":     KindWrite,
	"edit_code":       KindWrite,
	"semantic_rename": KindWrite,
	"smart_relocate":  KindWrite,
	"add_memory":      KindWrite,
	"update_memory":   KindWrite,
	"delete_memory":   KindWrite,

	"invoke_sub_agent": KindTask,
}

// KiroKind classifies a Kiro tool id. Kiro's MCP tools are mcp_<server>_<tool>,
// which Kind already recognizes; an id Kiro adds later falls back to the same
// heuristics as every other agent.
func KiroKind(name string) string {
	if kind, ok := kiroKinds[name]; ok {
		return kind
	}
	return Kind(name)
}
