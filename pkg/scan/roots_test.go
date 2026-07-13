package scan

import (
	"slices"
	"testing"
)

func TestParseWSLDistros(t *testing.T) {
	// wsl --list --quiet output: one name per line, CRLF line endings,
	// utility distros interleaved.
	out := "Ubuntu-24.04\r\ndocker-desktop\r\nDebian\r\n\r\nDocker-Desktop-Data\r\n"
	if got, want := parseWSLDistros(out), []string{"Ubuntu-24.04", "Debian"}; !slices.Equal(got, want) {
		t.Errorf("parseWSLDistros = %v, want %v", got, want)
	}
	if got := parseWSLDistros(""); got != nil {
		t.Errorf("parseWSLDistros(empty) = %v, want nil", got)
	}
}
