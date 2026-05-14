package email_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"resy-snipe/internal/alerts"
	"resy-snipe/internal/alerts/email"
	"resy-snipe/internal/domain"
)

func newTestSource(t *testing.T) *email.Source {
	t.Helper()
	src, err := email.New(email.Config{
		IMAPAddr: "imap.example.com:993",
		Username: "u",
		Password: "p",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = src.Close() })
	return src
}

// TestSourcePollMissesWithoutRegisteredVenue asserts that the Source
// surfaces ErrEnrollmentRequired when the caller has not mapped a
// VenueRef to a display name yet — the engine's terminal-fail path.
func TestSourcePollMissesWithoutRegisteredVenue(t *testing.T) {
	t.Parallel()
	src := newTestSource(t)

	_, err := src.Poll(context.Background(), alerts.Request{
		User:      "u1",
		Venue:     domain.VenueRef{Provider: "resy", Ref: "v1"},
		Date:      domain.NewDate(2026, time.January, 29),
		PartySize: 2,
	})
	if !errors.Is(err, alerts.ErrEnrollmentRequired) {
		t.Fatalf("got %v, want ErrEnrollmentRequired", err)
	}
}

// TestSourcePollReturnsUnfiredAfterRegister asserts that once a venue
// name is registered, Poll returns a clean Fired:false (not an
// enrollment-required error) until an email lands.
func TestSourcePollReturnsUnfiredAfterRegister(t *testing.T) {
	t.Parallel()
	src := newTestSource(t)
	venue := domain.VenueRef{Provider: "resy", Ref: "v1"}
	src.RegisterVenueName("COQODAQ", venue)

	got, err := src.Poll(context.Background(), alerts.Request{
		User:      "u1",
		Venue:     venue,
		Date:      domain.NewDate(2026, time.January, 29),
		PartySize: 2,
	})
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if got.Fired {
		t.Errorf("Fired before IngestEmail: %+v", got)
	}
}

// TestSourceIngestThenPollHits is the happy path: register the
// venue name, ingest the real COQODAQ fixture, Poll matches.
func TestSourceIngestThenPollHits(t *testing.T) {
	t.Parallel()
	src := newTestSource(t)
	venue := domain.VenueRef{Provider: "resy", Ref: "v_coqodaq"}
	src.RegisterVenueName("COQODAQ", venue)

	raw, err := os.ReadFile(filepath.Join("testdata", "coqodaq.eml"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	if err := src.IngestEmail(raw); err != nil {
		t.Fatalf("IngestEmail: %v", err)
	}

	got, err := src.Poll(context.Background(), alerts.Request{
		User:      "u1",
		Venue:     venue,
		Date:      domain.NewDate(2026, time.January, 29),
		PartySize: 2,
	})
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if !got.Fired {
		t.Fatal("Poll did not fire after IngestEmail")
	}
	wantFiredAt := time.Date(2026, time.January, 28, 15, 50, 43, 0, time.UTC)
	if !got.FiredAt.Equal(wantFiredAt) {
		t.Errorf("FiredAt: %s want %s", got.FiredAt, wantFiredAt)
	}
}

// TestSourceIngestDoesNotMatchWrongDate asserts that a fired alert
// for one date does NOT satisfy a Poll for a different date.
func TestSourceIngestDoesNotMatchWrongDate(t *testing.T) {
	t.Parallel()
	src := newTestSource(t)
	venue := domain.VenueRef{Provider: "resy", Ref: "v_coqodaq"}
	src.RegisterVenueName("COQODAQ", venue)

	raw, err := os.ReadFile(filepath.Join("testdata", "coqodaq.eml"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	if err := src.IngestEmail(raw); err != nil {
		t.Fatalf("IngestEmail: %v", err)
	}

	got, err := src.Poll(context.Background(), alerts.Request{
		User:      "u1",
		Venue:     venue,
		Date:      domain.NewDate(2026, time.February, 14), // different date
		PartySize: 2,
	})
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if got.Fired {
		t.Errorf("Poll fired for wrong date: %+v", got)
	}
}

// TestSourceIngestSilentlyDropsNonResyEmail asserts that IngestEmail
// returns ErrNotAResyAlert (callers should treat as skip) when a
// non-alert email is fed in.
func TestSourceIngestSilentlyDropsNonResyEmail(t *testing.T) {
	t.Parallel()
	src := newTestSource(t)
	raw := []byte(`From: newsletter@example.com
Subject: Weekly digest
Date: Wed, 28 Jan 2026 15:50:43 +0000

Top stories this week.
`)
	if err := src.IngestEmail(raw); !errors.Is(err, email.ErrNotAResyAlert) {
		t.Fatalf("got %v, want ErrNotAResyAlert", err)
	}
}

func TestNewRejectsMissingConfig(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		cfg  email.Config
	}{
		{"no addr", email.Config{Username: "u", Password: "p"}},
		{"no user", email.Config{IMAPAddr: "h:993", Password: "p"}},
		{"no pass", email.Config{IMAPAddr: "h:993", Username: "u"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if _, err := email.New(c.cfg); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}
