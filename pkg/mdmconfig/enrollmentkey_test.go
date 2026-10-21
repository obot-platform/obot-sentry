package mdmconfig

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateEnrollmentKey(t *testing.T) {
	for _, tt := range []struct {
		name    string
		key     string
		wantErr string
	}{
		{"valid", "ode1-3-17-Zm9vYmFy_c2VjcmV0-LQ", ""},
		{"padded", "  ode1-3-17-secret\n", ""},
		{"empty", "", "no enrollment key configured"},
		{"blank", "   ", "no enrollment key configured"},
		{"placeholder", "REPLACE_WITH_ENROLLMENT_KEY", "still the REPLACE_WITH_ENROLLMENT_KEY placeholder"},
		{"wrong prefix", "ode2-3-17-secret", "malformed"},
		{"missing secret", "ode1-3-17-", "malformed"},
		{"non-numeric ids", "ode1-a-b-secret", "malformed"},
		{"truncated", "ode1-3-17", "malformed"},
		{"embedded space", "ode1-3-17-sec ret", "malformed"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateEnrollmentKey(tt.key)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidateEnrollmentKey(%q) = %v, want nil", tt.key, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("ValidateEnrollmentKey(%q) = %v, want an error containing %q", tt.key, err, tt.wantErr)
			}
			if _, ok := errors.AsType[*EnrollmentKeyError](err); !ok {
				t.Fatalf("ValidateEnrollmentKey(%q) returned %T, want *EnrollmentKeyError", tt.key, err)
			}
		})
	}
}

func TestRedactEnrollmentKey(t *testing.T) {
	for key, want := range map[string]string{
		"ode1-3-17-Zm9vYmFy_c2VjcmV0-LQ": "ode1-3-17-…",
		" ode1-3-17-secret ":             "ode1-3-17-…",
		"REPLACE_WITH_ENROLLMENT_KEY":    "",
		"ode1-3-17-":                     "",
		"":                               "",
	} {
		if got := RedactEnrollmentKey(key); got != want {
			t.Errorf("RedactEnrollmentKey(%q) = %q, want %q", key, got, want)
		}
		if got := RedactEnrollmentKey(key); strings.Contains(got, "secret") || strings.Contains(got, "Zm9v") {
			t.Errorf("RedactEnrollmentKey(%q) = %q leaks the secret", key, got)
		}
	}
}
