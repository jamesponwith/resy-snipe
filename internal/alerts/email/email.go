// Package email is the IMAP-driven alerts.Source: it watches a mailbox
// for Resy NotifyMe emails and matches them against active quests.
//
// IMPLEMENTATION STATUS: scaffolded. The constructor, Source-interface
// methods, and configuration shape are stable. The IMAP connection
// loop and the email parser are stubbed — Poll currently returns
// alerts.ErrSourceNotImplemented so anything wired against it fails
// loudly.
//
// To complete:
//
//  1. Open a long-lived IMAP connection per Config (use go-imap or
//     similar; IDLE for near-push delivery, fallback to short polling
//     on servers without IDLE).
//  2. On each new message matching SenderFilter, parse subject + body
//     into (venue, date, party_size). Resy's email shape is the open
//     question — see docs/notify-me.md. The parser belongs in
//     parse.go alongside a golden-test fixture.
//  3. Cache fires in a map keyed by (User, VenueRef, Date, PartySize)
//     so Poll is an O(1) lookup. Use sync.Mutex; this code path is
//     hit by every engine NotifyMeRelease loop iteration.
//  4. Optionally: act on the "your reservation slot was taken" follow-
//     up email Resy sends after a hit, to evict stale cache entries.
package email

import (
	"context"
	"fmt"
	"sync"
	"time"

	"resy-snipe/internal/alerts"
)

// Config carries the IMAP credentials and the matching rules a Source
// needs to filter messages. Password is intended to be loaded via
// internal/secrets so the daemon doesn't pass it through the CLI.
type Config struct {
	// IMAPAddr is host:port (e.g., "imap.gmail.com:993").
	IMAPAddr string
	// Username + Password authenticate the IMAP session. For Gmail,
	// Password must be an app password (regular passwords don't work
	// over IMAP without OAuth).
	Username string
	Password string
	// Mailbox to watch; defaults to "INBOX" when empty.
	Mailbox string
	// SenderFilter restricts which messages are parsed. Defaults to
	// "notify@resy.com" / "alerts@resy.com" — exact value awaits a
	// sample email capture.
	SenderFilter string
	// MinPollInterval is the politeness floor. For Gmail with IDLE,
	// 1s is reasonable; for plain polling, 5s. Defaults to 5s.
	MinPollInterval time.Duration
}

// Source is the IMAP-backed alerts.Source.
type Source struct {
	cfg Config

	mu sync.Mutex
	// Future: connection handle + cached fires keyed by (user, venue,
	// date, party). The current stub has no state.
}

// New constructs a Source. It does NOT open a connection — call Start
// (once the impl lands) or rely on lazy connect on first Poll.
func New(cfg Config) (*Source, error) {
	if cfg.IMAPAddr == "" {
		return nil, fmt.Errorf("email: IMAPAddr required")
	}
	if cfg.Username == "" || cfg.Password == "" {
		return nil, fmt.Errorf("email: credentials required")
	}
	if cfg.Mailbox == "" {
		cfg.Mailbox = "INBOX"
	}
	if cfg.MinPollInterval <= 0 {
		cfg.MinPollInterval = 5 * time.Second
	}
	return &Source{cfg: cfg}, nil
}

// Poll implements alerts.Source. Stubbed until the IMAP layer and
// parser land.
func (s *Source) Poll(_ context.Context, _ alerts.Request) (alerts.State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return alerts.State{}, fmt.Errorf(
		"email.Source.Poll: IMAP loop not implemented; see internal/alerts/email/email.go: %w",
		alerts.ErrSourceNotImplemented,
	)
}

// MinPollInterval implements alerts.Source.
func (s *Source) MinPollInterval() time.Duration { return s.cfg.MinPollInterval }

// Close implements alerts.Source. Currently a no-op; once the IMAP
// connection lands, this tears it down.
func (s *Source) Close() error { return nil }
