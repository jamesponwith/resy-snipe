package email

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/mail"
	"regexp"
	"strconv"
	"strings"
	"time"

	"resy-snipe/internal/domain"
)

// Alert is one parsed Resy NotifyMe email.
//
// VenueName is the display name Resy puts in the email body (e.g.
// "COQODAQ", "Carbone"). It is not a resolved domain.VenueRef —
// matching display name → ref is the email Source's responsibility,
// downstream of the parser.
type Alert struct {
	VenueName string
	Date      domain.Date
	PartySize int
	Sender    string
	FiredAt   time.Time
}

// Sentinel errors. Callers that want to silently drop non-Resy mail
// (newsletter, social, etc.) should branch on ErrNotAResyAlert via
// errors.Is and treat anything else as a parser-side bug.
var (
	ErrNotAResyAlert = errors.New("email: not a Resy NotifyMe alert")
	ErrUnparseable   = errors.New("email: alert body did not match expected shape")
)

// resyAlertSubjectMarker is the literal tail Resy NotifyMe emails
// share. Decoded subject form: "<VENUE>🔔 Table may be available".
const resyAlertSubjectMarker = "Table may be available"

// resyAlertSender is the address Resy NotifyMe emails are sent from
// at the time of writing. Allow callers to override via Config.
const resyAlertSender = "reservations@resy.com"

// summaryRE matches the load-bearing sentence Resy puts in the body:
//
//	"A table for <N> at <VENUE> on <Month Day> may now be available."
//
// The venue capture is non-greedy and bounded by " on " so a venue
// name like "Carbone" stays intact. (A venue whose display name
// contains " on " would mis-extract — known v1 limitation; the
// shape is consistent enough that a more rigorous HTML walker isn't
// worth the complexity until we see one.)
var summaryRE = regexp.MustCompile(`A table for (\d+) at (.+?) on ([A-Z][a-zA-Z]+ \d{1,2}) may now be available\.`)

// Parse extracts an Alert from a raw RFC822 message.
//
// Returns ErrNotAResyAlert (wrapped) when sender/subject don't match
// the Resy NotifyMe shape — callers should treat that as "skip, not
// our concern" rather than a hard error.
//
// Returns ErrUnparseable (wrapped) when the message looks like a
// Resy alert but the body sentence couldn't be located — that's a
// real bug (Resy changed the template, or parser drifted) and
// should page someone.
func Parse(raw []byte) (Alert, error) {
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return Alert{}, fmt.Errorf("email.Parse: ReadMessage: %w", err)
	}

	fromAddr, err := mail.ParseAddress(msg.Header.Get("From"))
	if err != nil {
		return Alert{}, fmt.Errorf("email.Parse: From: %w", err)
	}
	if !strings.EqualFold(fromAddr.Address, resyAlertSender) {
		return Alert{}, fmt.Errorf("from=%s: %w", fromAddr.Address, ErrNotAResyAlert)
	}

	dec := new(mime.WordDecoder)
	subject, err := dec.DecodeHeader(msg.Header.Get("Subject"))
	if err != nil {
		// DecodeHeader on a malformed encoded-word returns an error;
		// fall back to the raw value so a slightly-off encoding
		// doesn't lose the email entirely.
		subject = msg.Header.Get("Subject")
	}
	if !strings.Contains(subject, resyAlertSubjectMarker) {
		return Alert{}, fmt.Errorf("subject=%q: %w", subject, ErrNotAResyAlert)
	}

	when, err := mail.ParseDate(msg.Header.Get("Date"))
	if err != nil {
		return Alert{}, fmt.Errorf("email.Parse: Date: %w", err)
	}

	body, err := io.ReadAll(msg.Body)
	if err != nil {
		return Alert{}, fmt.Errorf("email.Parse: read body: %w", err)
	}

	m := summaryRE.FindSubmatch(body)
	if m == nil {
		return Alert{}, fmt.Errorf("body summary not found: %w", ErrUnparseable)
	}
	party, err := strconv.Atoi(string(m[1]))
	if err != nil {
		return Alert{}, fmt.Errorf("email.Parse: party-size %q: %w", m[1], err)
	}
	venueName := strings.TrimSpace(string(m[2]))
	dateStr := strings.TrimSpace(string(m[3]))

	date, err := resolveDate(dateStr, when)
	if err != nil {
		return Alert{}, fmt.Errorf("email.Parse: %w", err)
	}

	return Alert{
		VenueName: venueName,
		Date:      date,
		PartySize: party,
		Sender:    fromAddr.Address,
		FiredAt:   when,
	}, nil
}

// resolveDate combines a "Month Day" string (no year) with the email's
// arrival time and applies the always-future heuristic: if the parsed
// month/day is before the arrival date, the alert is for next year.
//
// The email arrives in the user's locale's near-now, so this picks up
// the right year for the overwhelming majority of cases. Edge case:
// an alert that arrives months before the reservation (e.g., received
// Jan 1 for an alert for Dec 31 the same year) would pick this year
// correctly only because Dec 31 > Jan 1. There's no in-band signal
// in the email itself for the reservation year.
func resolveDate(monthDay string, arrival time.Time) (domain.Date, error) {
	parsed, err := time.Parse("January 2", monthDay)
	if err != nil {
		return domain.Date{}, fmt.Errorf("date %q: %w", monthDay, err)
	}
	year := arrival.Year()
	candidate := time.Date(year, parsed.Month(), parsed.Day(), 0, 0, 0, 0, arrival.Location())
	arrivalDay := time.Date(arrival.Year(), arrival.Month(), arrival.Day(), 0, 0, 0, 0, arrival.Location())
	if candidate.Before(arrivalDay) {
		year++
	}
	return domain.NewDate(year, parsed.Month(), parsed.Day()), nil
}
