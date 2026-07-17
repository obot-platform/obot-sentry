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
			command: "/usr/local/bin/obot-sentry audit submit --agent codex --phase post-tool --managed-by obot-sentry",
			want:    true,
		},
		{
			name:    "quoted windows path with marker",
			command: `"C:\Program Files\Obot\obot-sentry\obot-sentry.exe" audit submit --agent claude-code --phase post-tool --managed-by obot-sentry`,
			want:    true,
		},
		{
			name:    "windows call-operator form",
			command: `& "C:\Program Files\Obot\obot-sentry\obot-sentry.exe" audit submit --agent codex --phase post-tool --managed-by obot-sentry`,
			want:    true,
		},
		{
			name:    "quoted posix path with spaces and marker",
			command: `'/opt/Obot Tools/obot-sentry' audit submit --agent cursor --phase failure --managed-by obot-sentry`,
			want:    true,
		},
		{
			name:    "marker points at a different obot-sentry path",
			command: "/old/versioned/path/obot-sentry audit submit --agent vscode --phase post-tool --managed-by obot-sentry",
			want:    true,
		},
		{
			name:    "no marker",
			command: "/usr/local/bin/obot-sentry audit submit --agent codex --phase post-tool",
			want:    false,
		},
		{
			name:    "different marker value",
			command: "/usr/local/bin/obot-sentry audit submit --agent codex --managed-by someone-else",
			want:    false,
		},
		{
			name:    "obot-sentry appears only in the path, no marker",
			command: "/usr/local/bin/obot-sentry-wrapper run --managed-by other",
			want:    false,
		},
		{
			name:    "dangling marker with no value",
			command: "/usr/local/bin/obot-sentry audit submit --managed-by",
			want:    false,
		},
		{
			name:    "third-party command mentioning obot-sentry text without the flag",
			command: "/usr/bin/echo installing obot-sentry managed-by obot-sentry",
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
