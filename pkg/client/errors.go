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

// IsRejected reports whether err is the server definitively refusing the
// request: any 4xx except a timeout (408) or rate limit (429).
// Retrying a rejection with the same configuration can't succeed, so callers
// surface it to an administrator. Network errors and 5xx responses aren't
// rejections; they're expected while a device is offline or the server is
// down, and a later retry may succeed.
func IsRejected(err error) bool {
	httpErr, ok := errors.AsType[*types.ErrHTTP](err)
	return ok &&
		httpErr.Code >= http.StatusBadRequest && httpErr.Code < http.StatusInternalServerError &&
		httpErr.Code != http.StatusRequestTimeout && httpErr.Code != http.StatusTooManyRequests
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
