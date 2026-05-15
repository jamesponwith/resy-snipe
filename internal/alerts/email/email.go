// Package email is the IMAP-driven alerts.Source: it watches a mailbox
// for Resy NotifyMe emails and matches them against active quests.
//
// IMPLEMENTATION STATUS:
//
//   - Parser (parse.go): COMPLETE. Extracts (venue name, date, party
//     size, sender, fired_at) from a real Resy NotifyMe email with a
//     golden-test fixture.
//   - Source matching: COMPLETE. IngestEmail caches parsed alerts and
//     Poll serves them via a name registry (RegisterVenueName) so the
//     engine's VenueRef-keyed Request maps to the email's display
//     name.
//   - IMAP loop: STUBBED. Connect/IDLE/poll-FETCH is not wired —
//     IngestEmail is currently the only way alerts land in the cache.
//     A future commit adds an IMAP client that calls IngestEmail on
//     each new matching message.
//
// Until the IMAP loop lands, the daemon can drive the Source via
// IngestEmail directly (e.g., from a webhook that forwards email
// bodies, or from a sidecar that does the IMAP work).
package email

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"resy-snipe/internal/alerts"
	"resy-snipe/internal/domain"
)

// Config carries the IMAP credentials and the matching rules a Source
// needs to filter messages. Password is intended to be loaded via
// internal/secrets so the daemon doesn't pass it through the CLI.
type Config struct {
	// IMAPAddr is host:port (e.g., "imap.gmail.com:993"). Required.
	IMAPAddr string
	// Username + Password authenticate the IMAP session. For Gmail,
	// Password must be an app password (regular passwords don't work
	// over IMAP without OAuth).
	Username string
	Password string
	// Mailbox to watch; defaults to "INBOX" when empty.
	Mailbox string
	// MinPollInterval is the politeness floor. Defaults to 5s.
	MinPollInterval time.Duration
}

// Source is the IMAP-backed alerts.Source. Safe for concurrent use.
type Source struct {
	cfg Config

	mu       sync.Mutex
	fires    map[fireKey]Alert          // parsed alerts keyed by (lowercase name, date, party)
	venueMap map[string]domain.VenueRef // lowercase display name -> resolved VenueRef

	// IMAP lifecycle state. Populated by Start; torn down by Close.
	// Tests that exercise only the Ingest + Poll surfaces leave these
	// nil — the runLoop goroutine is opt-in.
	started bool
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	log     *slog.Logger
	client  *imapclient.Client
	lastUID imap.UID
}

type fireKey struct {
	name  string // lowercase
	date  domain.Date
	party int
}

// New constructs a Source. It does NOT open an IMAP connection; the
// IMAP loop is stubbed (see package doc) and the Source is fed via
// IngestEmail.
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
	return &Source{
		cfg:      cfg,
		fires:    make(map[fireKey]Alert),
		venueMap: make(map[string]domain.VenueRef),
	}, nil
}

// RegisterVenueName teaches the Source that a venue's Resy display
// name maps to a domain.VenueRef. Callers (the daemon, the CLI snipe
// path) call this when a NotifyMe quest is submitted so Poll can
// translate the engine's VenueRef-keyed Request back to the email's
// display-name payload.
//
// Name matching is case-insensitive and whitespace-trimmed; Resy's
// rendering preserves case but the surrounding HTML can introduce
// stray whitespace.
func (s *Source) RegisterVenueName(name string, venue domain.VenueRef) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.venueMap[normalizeName(name)] = venue
}

// IngestEmail parses raw and caches the resulting Alert keyed by
// (lowercase venue name, date, party). Subsequent Poll calls for a
// matching Request return Fired:true.
//
// Returns ErrNotAResyAlert for non-Resy mail (caller should ignore);
// other errors indicate a parser/format problem worth surfacing.
func (s *Source) IngestEmail(raw []byte) error {
	a, err := Parse(raw)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fires[fireKey{
		name:  normalizeName(a.VenueName),
		date:  a.Date,
		party: a.PartySize,
	}] = a
	return nil
}

// Poll implements alerts.Source. It looks up the cached fire matching
// req via the venue-name registry; if the registry has no mapping for
// req.Venue, it surfaces ErrEnrollmentRequired (the daemon hasn't told
// the Source what display name to look for, which is the same as
// "no enrollment" from the engine's POV).
func (s *Source) Poll(_ context.Context, req alerts.Request) (alerts.State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	name, ok := s.lookupNameLocked(req.Venue)
	if !ok {
		return alerts.State{}, fmt.Errorf("venue=%s: %w", req.Venue, alerts.ErrEnrollmentRequired)
	}
	a, ok := s.fires[fireKey{name: name, date: req.Date, party: req.PartySize}]
	if !ok {
		return alerts.State{}, nil
	}
	return alerts.State{Fired: true, FiredAt: a.FiredAt}, nil
}

// MinPollInterval implements alerts.Source.
func (s *Source) MinPollInterval() time.Duration { return s.cfg.MinPollInterval }

// Close implements alerts.Source. If Start was called, this cancels
// the runLoop goroutine, waits for it to exit, and tears down the
// IMAP connection. Safe to call multiple times and safe to call
// before Start.
func (s *Source) Close() error {
	s.mu.Lock()
	cancel := s.cancel
	s.cancel = nil
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	s.wg.Wait()

	s.mu.Lock()
	client := s.client
	s.client = nil
	s.mu.Unlock()
	if client != nil {
		_ = client.Logout().Wait()
		_ = client.Close()
	}
	return nil
}

// lookupNameLocked returns the registered display name for venue, if
// any. Caller must hold s.mu.
func (s *Source) lookupNameLocked(venue domain.VenueRef) (string, bool) {
	for name, ref := range s.venueMap {
		if ref == venue {
			return name, true
		}
	}
	return "", false
}

func normalizeName(s string) string { return strings.ToLower(strings.TrimSpace(s)) }
