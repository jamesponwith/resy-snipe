package resy

import (
	"encoding/json"
	"fmt"
	"strings"

	"resy-snipe/config"
)

// NotifyRequest is one Resy Notify waitlist subscription.
type NotifyRequest struct {
	VenueID   int
	Date      string
	PartySize int
	// StartTime and EndTime bound the window Resy should watch, as HH:MM:SS.
	StartTime string
	EndTime   string
}

// NotifyResult reports what happened for one subscription.
type NotifyResult struct {
	Label string
	OK    bool
	Raw   string
	Err   error
}

// NotifyWindow derives the Notify time window from a target's requested times.
// Resy Notify takes a range rather than a list, so the earliest and latest
// requested times become the bounds.
func NotifyWindow(resTimeTypes []config.ReservationTimeType) (string, string, error) {
	if len(resTimeTypes) == 0 {
		return "", "", fmt.Errorf("no reservation times to derive a notify window from")
	}

	earliest, latest := "", ""
	for _, r := range resTimeTypes {
		if earliest == "" || r.ReservationTime < earliest {
			earliest = r.ReservationTime
		}
		if latest == "" || r.ReservationTime > latest {
			latest = r.ReservationTime
		}
	}
	return earliest, latest, nil
}

// JoinNotify subscribes to Resy's Notify waitlist.
//
// UNVERIFIED: the request shape is inferred from Resy's booking widget and has
// not been confirmed against a live account. The raw response is always
// returned so a rejection can be inspected and the payload corrected, rather
// than the call silently appearing to succeed.
func (rc *ResyClient) JoinNotify(req NotifyRequest) (string, error) {
	resp, err := rc.resyApi.CreateNotify(req.VenueID, req.Date, req.PartySize, req.StartTime, req.EndTime)
	if err != nil {
		return resp, err
	}

	// Resy returns a body even on logical failure, so treat an explicit error
	// field as a failure rather than trusting the HTTP status alone.
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(resp), &parsed); err != nil {
		return resp, fmt.Errorf("unexpected notify response: %w", err)
	}
	if message, bad := parsed["message"]; bad {
		if text, ok := message.(string); ok && strings.TrimSpace(text) != "" {
			if _, hasID := parsed["id"]; !hasID {
				return resp, fmt.Errorf("resy rejected the notify request: %s", text)
			}
		}
	}

	return resp, nil
}
