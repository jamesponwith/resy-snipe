package resy

import (
	"context"
	"errors"
	"fmt"

	"resy-snipe/internal/providers"
)

// PollAlerts is the Resy-side seam for the NotifyMeRelease engine path.
// It reads the account's NotifyMe enrollment + alert state for the
// (venue, date) pair carried in req and returns providers.AlertState.
//
// IMPLEMENTATION STATUS: stubbed. The engine layer (release.go
// runNotifyMeRelease) is fully wired against this method, but the HTTP
// wire format for Resy's NotifyMe alert-state endpoint is not currently
// captured in this repo. To complete the integration, a follow-up
// needs to:
//
//  1. Identify the endpoint (likely GET /3/notify or similar — confirm
//     with a packet capture from the Resy mobile app's NotifyMe flow).
//  2. Map the response shape onto AlertState{Fired: bool} — typically
//     a per-enrollment "state" field that goes "pending" → "fired".
//  3. Add an enrollment-creation method if Resy requires explicit POST
//     before alerts will populate (the engine already classifies
//     ErrAlertEnrollmentRequired as terminal so the CLI can prompt for
//     out-of-band enrollment).
//  4. Route through doSignedAndRetry like the other endpoints; the
//     account-side alert endpoint is far less anti-bot-watched than
//     /4/find but still benefits from the unified retry envelope.
//
// Until that work lands, PollAlerts returns a sentinel that the engine
// classifies as a non-recoverable misconfiguration — it's loud and
// obvious if something tries to use NotifyMeRelease in production
// against this stub.
func (c *Client) PollAlerts(ctx context.Context, req providers.AlertRequest) (providers.AlertState, error) {
	if req.Venue.Provider != providerID {
		return providers.AlertState{}, fmt.Errorf("resy.PollAlerts: provider mismatch %q", req.Venue.Provider)
	}
	if req.Venue.Ref == "" {
		return providers.AlertState{}, errors.New("resy.PollAlerts: empty venue ref")
	}
	if req.Date.IsZero() {
		return providers.AlertState{}, errors.New("resy.PollAlerts: date is required")
	}
	return providers.AlertState{}, fmt.Errorf(
		"resy.PollAlerts: NotifyMe endpoint not implemented; see internal/resy/notify_alerts.go: %w",
		providers.ErrAlertEnrollmentRequired,
	)
}
