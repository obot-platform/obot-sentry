package client

import (
	"context"
	"time"

	"github.com/obot-platform/obot/apiclient/types"

	"github.com/obot-platform/obot-sentry/pkg/identity"
)

// This is a var only so tests can exercise the timeout path in milliseconds
// instead of seconds; nothing outside this package changes it, and a test
// pins the shipped value.
var decisionTimeout = 5 * time.Second

// Decide asks obot for a verdict on a normalized tool call, bounded by
// decisionTimeout, authenticated with a freshly minted device JWT. The server
// resolves the MDMConfiguration from that identity and never from the request body.
//
// Every failure — mint, transport, timeout, non-2xx, undecodable body — comes
// back as a non-nil error with a zero-valued response. This never turns an
// error into a verdict of its own: the caller fails closed, and a response is
// only meaningful when the error is nil. Callers must also treat any decision
// other than types.EnforcementDecisionAllow as a deny, since a zero-valued
// Decision is "" rather than "deny".
//
// Unresolvable calls go through this same method (they are an ordinary request
// carrying Unresolved plus a reason), so there is no second entry point and no
// second timeout. There are no retries: a retry is indistinguishable from added
// latency before a deny, and no spooling, because a decision is worthless after
// the fact.
func (c *Client) Decide(ctx context.Context, id *identity.Identity, req types.EnforcementDecisionRequest) (types.EnforcementDecisionResponse, error) {
	tok, err := id.MintDeviceJWT(identity.DefaultTokenTTL)
	if err != nil {
		return types.EnforcementDecisionResponse{}, err
	}

	ctx, cancel := context.WithTimeout(ctx, decisionTimeout)
	defer cancel()

	return c.api.WithToken(tok).Decide(ctx, req)
}
