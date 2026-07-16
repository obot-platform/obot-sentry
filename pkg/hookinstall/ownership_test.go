package hookinstall

import "testing"

func TestIsOwnedCommand(t *testing.T) {
	tests := []struct {
		name    string
		command string
		want    bool
	}{
		{
			name:    "space-separated marker",
			command: "/usr/local/bin/obocop audit submit --agent codex --phase post-tool --managed-by obocop",
			want:    true,
		},
		{
			name:    "quoted windows path with marker",
			command: `"C:\Program Files\Obot\Obocop\obocop.exe" audit submit --agent claude-code --phase post-tool --managed-by obocop`,
			want:    true,
		},
		{
			name:    "windows call-operator form",
			command: `& "C:\Program Files\Obot\Obocop\obocop.exe" audit submit --agent codex --phase post-tool --managed-by obocop`,
			want:    true,
		},
		{
			name:    "quoted posix path with spaces and marker",
			command: `'/opt/Obot Tools/obocop' audit submit --agent cursor --phase failure --managed-by obocop`,
			want:    true,
		},
		{
			name:    "marker points at a different obocop path",
			command: "/old/versioned/path/obocop audit submit --agent vscode --phase post-tool --managed-by obocop",
			want:    true,
		},
		{
			name:    "no marker",
			command: "/usr/local/bin/obocop audit submit --agent codex --phase post-tool",
			want:    false,
		},
		{
			name:    "different marker value",
			command: "/usr/local/bin/obocop audit submit --agent codex --managed-by someone-else",
			want:    false,
		},
		{
			name:    "obocop appears only in the path, no marker",
			command: "/usr/local/bin/obocop-wrapper run --managed-by other",
			want:    false,
		},
		{
			name:    "dangling marker with no value",
			command: "/usr/local/bin/obocop audit submit --managed-by",
			want:    false,
		},
		{
			name:    "third-party command mentioning obocop text without the flag",
			command: "/usr/bin/echo installing obocop managed-by obocop",
			want:    false,
		},
		{
			name:    "empty",
			command: "",
			want:    false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsOwnedCommand(tc.command); got != tc.want {
				t.Fatalf("IsOwnedCommand(%q) = %v, want %v", tc.command, got, tc.want)
			}
		})
	}
}
