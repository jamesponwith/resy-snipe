package email_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"resy-snipe/internal/alerts/email"
	"resy-snipe/internal/domain"
)

// TestParseGoldenCOQODAQ verifies the parser against a real Resy
// NotifyMe email (sanitized — see testdata/coqodaq.eml). If this test
// fails on a future Resy template change, the parser regex in
// parse.go needs to be revisited.
func TestParseGoldenCOQODAQ(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(filepath.Join("testdata", "coqodaq.eml"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	got, err := email.Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	want := email.Alert{
		VenueName: "COQODAQ",
		Date:      domain.NewDate(2026, time.January, 29),
		PartySize: 2,
		Sender:    "reservations@resy.com",
		FiredAt:   time.Date(2026, time.January, 28, 15, 50, 43, 0, time.UTC),
	}
	if got.VenueName != want.VenueName {
		t.Errorf("VenueName: %q want %q", got.VenueName, want.VenueName)
	}
	if got.Date != want.Date {
		t.Errorf("Date: %s want %s", got.Date, want.Date)
	}
	if got.PartySize != want.PartySize {
		t.Errorf("PartySize: %d want %d", got.PartySize, want.PartySize)
	}
	if got.Sender != want.Sender {
		t.Errorf("Sender: %q want %q", got.Sender, want.Sender)
	}
	if !got.FiredAt.Equal(want.FiredAt) {
		t.Errorf("FiredAt: %s want %s", got.FiredAt, want.FiredAt)
	}
}

func TestParseRejectsNonResySender(t *testing.T) {
	t.Parallel()
	raw := []byte(`From: someone@example.com
Subject: =?UTF-8?B?Q09RT0RBUfCflJQ=?= Table may be available
Date: Wed, 28 Jan 2026 15:50:43 +0000

A table for 2 at COQODAQ on January 29 may now be available.
`)
	_, err := email.Parse(raw)
	if !errors.Is(err, email.ErrNotAResyAlert) {
		t.Fatalf("got %v, want ErrNotAResyAlert", err)
	}
}

func TestParseRejectsNonAlertSubject(t *testing.T) {
	t.Parallel()
	raw := []byte(`From: Reservations Desk <reservations@resy.com>
Subject: Your reservation is confirmed
Date: Wed, 28 Jan 2026 15:50:43 +0000

Confirmation details follow.
`)
	_, err := email.Parse(raw)
	if !errors.Is(err, email.ErrNotAResyAlert) {
		t.Fatalf("got %v, want ErrNotAResyAlert", err)
	}
}

func TestParseFailsLoudlyOnMissingSummary(t *testing.T) {
	t.Parallel()
	// Looks like a Resy alert (right sender + subject marker) but the
	// body sentence is absent — Resy changed the template, or this is
	// a malformed alert. The parser must error rather than silently
	// returning a zero-value Alert.
	raw := []byte(`From: Reservations Desk <reservations@resy.com>
Subject: =?UTF-8?B?Q09RT0RBUfCflJQ=?= Table may be available
Date: Wed, 28 Jan 2026 15:50:43 +0000

<html><body>Hot table alert. Click the button below.</body></html>
`)
	_, err := email.Parse(raw)
	if !errors.Is(err, email.ErrUnparseable) {
		t.Fatalf("got %v, want ErrUnparseable", err)
	}
}

// TestParseYearWrapNextYear: alert sent on Dec 28 for "January 29"
// must resolve to next year, not the same year.
func TestParseYearWrapNextYear(t *testing.T) {
	t.Parallel()
	raw := []byte(`From: Reservations Desk <reservations@resy.com>
Subject: =?UTF-8?B?Q09RT0RBUfCflJQ=?= Table may be available
Date: Mon, 28 Dec 2026 15:50:43 +0000

A table for 2 at COQODAQ on January 29 may now be available.
`)
	got, err := email.Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := domain.NewDate(2027, time.January, 29)
	if got.Date != want {
		t.Errorf("Date: %s want %s (year wrap)", got.Date, want)
	}
}
