// Package alerts is the seam between the engine's NotifyMeRelease loop
// and whatever origin actually observes that a venue+date has opened.
//
// The origin is intentionally decoupled from the booking provider: a
// Resy snipe can be triggered by a Resy-side API alert, by an inbound
// email forwarded from Resy's NotifyMe feature, by a webhook, or by
// a test harness's in-memory fake. The engine consumes whichever
// Source is wired in via engine.WithAlertSource and never branches on
// which one it is.
//
// Concrete implementations:
//
//   - MemorySource (this package) — in-memory, for engine tests and
//     for the daemon's "received via webhook" path.
//   - internal/alerts/email — IMAP-driven; v1's primary path. Watches
//     a mailbox for Resy NotifyMe emails and matches them against
//     active quests.
package alerts

import (
	"context"
	"errors"
	"sync"
	"time"

	"resy-snipe/internal/domain"
)

// Request is what the engine asks a Source about: "has the alert for
// this (user, venue, date) pair fired?". PartySize is included so
// Sources that key alerts by enrollment shape (Resy's NotifyMe does)
// can disambiguate; Sources that don't need it ignore the field.
type Request struct {
	User      domain.UserID
	Venue     domain.VenueRef
	Date      domain.Date
	PartySize int
}

// State is the engine-visible snapshot from one Poll. The engine only
// reads Fired; FiredAt is supplied by Sources that can observe an
// arrival time (email Date header, webhook receipt time) so audit
// events can record latency.
//
// Fired==true is a one-way signal — once a Source returns it, the
// engine transitions Awaiting and the Source's subsequent state for
// that Request is meaningless.
type State struct {
	Fired   bool
	FiredAt time.Time
}

// Source is the interface engine.runNotifyMeRelease drives. Stateful
// sources (IMAP-IDLE, webhook receiver) maintain internal caches and
// return Poll near-instantly; polling sources do a single round-trip.
// The engine treats both uniformly.
type Source interface {
	// Poll reports the current alert state for req. Implementations
	// must not block longer than ctx allows.
	Poll(ctx context.Context, req Request) (State, error)

	// MinPollInterval is the politeness/safety floor. The engine
	// clamps any per-quest poll-interval override to this minimum so
	// upstream IMAP servers / webhook handlers aren't hammered.
	MinPollInterval() time.Duration

	// Close releases resources (open IMAP connection, listening
	// socket). Safe to call multiple times.
	Close() error
}

// Sentinel errors. Sources must wrap one of these so engine code can
// branch via errors.Is without string-matching transport noise.
var (
	// ErrEnrollmentRequired indicates the caller has not enrolled for
	// alerts on this (user, venue, date) on the Source's origin. For
	// the email source this means no matching NotifyMe email has ever
	// arrived; for a Resy-API source it means no POST /3/notify has
	// been issued. The engine fails the snipe — there is no in-loop
	// recovery; enrollment is an out-of-band step.
	ErrEnrollmentRequired = errors.New("alerts: enrollment required")

	// ErrSourceUnavailable indicates a transient transport failure
	// (IMAP disconnect, webhook handler 5xx). The engine surfaces it
	// rather than swallowing — the operator decides whether to retry.
	ErrSourceUnavailable = errors.New("alerts: source unavailable")

	// ErrSourceNotImplemented is the sentinel scaffolded Sources
	// return until their wire-format implementation lands. Engine code
	// classifies it the same as ErrSourceUnavailable.
	ErrSourceNotImplemented = errors.New("alerts: source not implemented")
)

// MemorySource is an in-memory Source. Tests inject fired alerts via
// Fire; the daemon's webhook receiver uses the same type to land
// HTTP-delivered alerts into the engine's poll loop. Safe for
// concurrent use.
type MemorySource struct {
	mu  sync.Mutex
	min time.Duration
	hit map[memKey]time.Time
}

type memKey struct {
	user  domain.UserID
	venue domain.VenueRef
	date  domain.Date
	party int
}

// NewMemorySource returns a MemorySource whose MinPollInterval is
// minPoll. A zero or negative value clamps to 100ms.
func NewMemorySource(minPoll time.Duration) *MemorySource {
	if minPoll <= 0 {
		minPoll = 100 * time.Millisecond
	}
	return &MemorySource{
		min: minPoll,
		hit: make(map[memKey]time.Time),
	}
}

// Fire records that an alert matching (user, venue, date, party) has
// arrived at firedAt. Subsequent Poll calls for a matching Request
// return State{Fired: true, FiredAt: firedAt}.
func (m *MemorySource) Fire(user domain.UserID, venue domain.VenueRef, date domain.Date, party int, firedAt time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.hit[memKey{user, venue, date, party}] = firedAt
}

// Poll implements Source.
func (m *MemorySource) Poll(_ context.Context, req Request) (State, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	at, ok := m.hit[memKey{req.User, req.Venue, req.Date, req.PartySize}]
	if !ok {
		return State{}, nil
	}
	return State{Fired: true, FiredAt: at}, nil
}

// MinPollInterval implements Source.
func (m *MemorySource) MinPollInterval() time.Duration { return m.min }

// Close implements Source. MemorySource holds no external resources.
func (m *MemorySource) Close() error { return nil }
