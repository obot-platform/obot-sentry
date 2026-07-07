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
	httpErr, ok := errors.AsType[*types.ErrHTTP](err)
	return ok &&
		(httpErr.Code == http.StatusUnauthorized || httpErr.Code == http.StatusForbidden)
}

func IsClientError(err error) bool {
	httpErr, ok := errors.AsType[*types.ErrHTTP](err)
	return ok &&
		httpErr.Code >= http.StatusBadRequest && httpErr.Code < http.StatusInternalServerError
}

func IsTransient(err error) bool {
	if err == nil {
		return false
	}
	if httpErr, ok := errors.AsType[*types.ErrHTTP](err); ok {
		return httpErr.Code >= http.StatusInternalServerError
	}
	return true
}
