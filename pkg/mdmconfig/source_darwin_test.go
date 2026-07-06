package mdmconfig

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"howett.net/plist"
)

// TestPlistSource_XMLAndBinary covers both encodings managed
// preferences show up in on disk: profiles delivered by an MDM land as
// binary plists; hand-written test/dev files are XML.
func TestPlistSource_XMLAndBinary(t *testing.T) {
	payload := map[string]any{
		KeyServerURL:     "https://obot.example.com",
		KeyEnrollmentKey: "ode1-1-2-secret",
	}

	for _, enc := range []struct {
		name   string
		format int
	}{
		{"xml", plist.XMLFormat},
		{"binary", plist.BinaryFormat},
	} {
		t.Run(enc.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := plist.NewEncoderForFormat(&buf, enc.format).Encode(payload); err != nil {
				t.Fatal(err)
			}
			p := filepath.Join(t.TempDir(), "com.obot.obocop.plist")
			if err := os.WriteFile(p, buf.Bytes(), 0o644); err != nil {
				t.Fatal(err)
			}

			cfg, err := FromSource(plistSource{paths: []string{"/nonexistent/first.plist", p}})
			if err != nil {
				t.Fatal(err)
			}
			if cfg.ServerURL != "https://obot.example.com" {
				t.Errorf("ServerURL = %q", cfg.ServerURL)
			}
			if cfg.EnrollmentKey != "ode1-1-2-secret" {
				t.Errorf("EnrollmentKey = %q", cfg.EnrollmentKey)
			}
		})
	}
}

func TestPlistSource_MissingFilesNotAnError(t *testing.T) {
	cfg, err := FromSource(plistSource{paths: []string{"/nonexistent/a.plist", "/nonexistent/b.plist"}})
	if err != nil {
		t.Fatalf("missing plists should not error: %v", err)
	}
	if cfg != (Config{}) {
		t.Errorf("expected zero config, got %+v", cfg)
	}
}
