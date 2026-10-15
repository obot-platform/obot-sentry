package mdmconfig

import (
	"regexp"
	"strings"
)

// EnrollmentKeyPlaceholder is the stand-in the install packages ship where the
// administrator's enrollment key goes. Seeing it at runtime means the key was
// never filled in.
const EnrollmentKeyPlaceholder = "REPLACE_WITH_ENROLLMENT_KEY"

// enrollmentKeyPattern matches the credentials obot mints:
// ode1-<configuration_id>-<key_id>-<secret>, with a base64url secret. The
// first group is everything but the secret, which is safe to print.
var enrollmentKeyPattern = regexp.MustCompile(`^(ode1-\d+-\d+)-[A-Za-z0-9_-]+$`)

// EnrollmentKeyError reports an enrollment key the device can't enroll with:
// missing, malformed, or rejected by the server. Commands treat it as a
// configuration problem (exit 2). Err carries the server's response for a
// rejected key, so callers that discard on 401 still see one.
type EnrollmentKeyError struct {
	Reason string
	Err    error
}

func (e *EnrollmentKeyError) Error() string {
	if e.Err == nil {
		return e.Reason
	}
	return e.Reason + ": " + e.Err.Error()
}

func (e *EnrollmentKeyError) Unwrap() error { return e.Err }

// ValidateEnrollmentKey checks that key is shaped like an enrollment key,
// without contacting the server. It catches the placeholder and copy-paste
// damage before a device spends a round trip on a guaranteed 401.
// Surrounding whitespace is ignored; configuration loading trims it before the
// key is ever sent.
func ValidateEnrollmentKey(key string) error {
	switch key = strings.TrimSpace(key); {
	case key == "":
		return &EnrollmentKeyError{Reason: "no enrollment key configured"}
	case key == EnrollmentKeyPlaceholder:
		return &EnrollmentKeyError{Reason: "the enrollment key is still the " + EnrollmentKeyPlaceholder + " placeholder"}
	case !enrollmentKeyPattern.MatchString(key):
		return &EnrollmentKeyError{Reason: "the enrollment key is malformed: Obot enrollment keys look like ode1-<number>-<number>-<secret>"}
	}
	return nil
}

// RedactEnrollmentKey returns key with its secret elided (ode1-3-17-…), or ""
// if key isn't a well-formed enrollment key. A malformed key is never echoed,
// since it may be a real secret with a typo in it.
func RedactEnrollmentKey(key string) string {
	m := enrollmentKeyPattern.FindStringSubmatch(strings.TrimSpace(key))
	if m == nil {
		return ""
	}
	return m[1] + "-…"
}
