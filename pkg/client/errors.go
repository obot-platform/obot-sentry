package client

import (
	"errors"
	"net/http"

	"github.com/obot-platform/obot/apiclient/types"
)

// IsUnauthorized reports whether err is an auth failure (401/403) —
// e.g. the server no longer recognizes the device — which the scan flow
// answers with one re-enroll + retry.
func IsUnauthorized(err error) bool {
	var httpErr *types.ErrHTTP
	return errors.As(err, &httpErr) &&
		(httpErr.Code == http.StatusUnauthorized || httpErr.Code == http.StatusForbidden)
}
