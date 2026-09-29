package resy

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// On-hit policies.
const (
	OnHitBook  = "book"
	OnHitAlert = "alert"
)

// Watcher polls targets on an interval and reacts when a table appears.
//
// Alerts are edge-triggered: a target alerts when it goes from unavailable to
// available, not on every poll, so a table that sits open for an hour does not
// produce hundreds of emails.
type Watcher struct {
	resyClient ResyClient
	targets    []Target
	alerter    *Alerter
	interval   time.Duration

	// MaxBookings caps how many reservations may be booked across the whole
	// watch. Watching many nights with auto-book would otherwise reserve one
	// table per night; usually you want a single dinner. 0 means unlimited.
	MaxBookings int

	mu sync.Mutex
	// wasAvailable drives edge-triggering, keyed by target label.
	wasAvailable map[string]bool
	// bookedNights stops polling a date once it has been won, since Resy only
	// allows one reservation per night.
	bookedNights map[string]bool
	bookedCount  int
}

func NewWatcher(resyClient ResyClient, targets []Target, alerter *Alerter, interval time.Duration) *Watcher {
	return &Watcher{
		resyClient:   resyClient,
		targets:      targets,
		alerter:      alerter,
		interval:     interval,
		wasAvailable: make(map[string]bool),
		bookedNights: make(map[string]bool),
	}
}

// claimBookingSlot atomically reserves the right to book one reservation for a
// date. Checking the cap and then booking as separate steps would let every
// concurrent poll goroutine pass the check before any of them booked, so the
// claim and the counter increment happen under one lock.
func (w *Watcher) claimBookingSlot(date string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.MaxBookings > 0 && w.bookedCount >= w.MaxBookings {
		return false
	}
	if w.bookedNights[date] {
		return false
	}

	w.bookedNights[date] = true
	w.bookedCount++
	return true
}

// releaseBookingSlot hands a claim back when the booking attempt failed, so a
// failed attempt does not permanently consume the cap.
func (w *Watcher) releaseBookingSlot(date string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.bookedNights, date)
	w.bookedCount--
}

// Watch polls until the context is cancelled or every night has been booked.
// It returns the number of reservations booked.
func (w *Watcher) Watch(ctx context.Context) (int, error) {
	booked := 0
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	for {
		n, done := w.poll(ctx)
		booked += n
		if done {
			return booked, nil
		}
		// Re-check after polling too: booking the last open night during this
		// pass means there is nothing left, and waiting out a full interval
		// before noticing would stall for the length of the poll interval.
		if len(w.activeTargets()) == 0 {
			return booked, nil
		}

		select {
		case <-ctx.Done():
			return booked, ctx.Err()
		case <-ticker.C:
		}
	}
}

// poll checks every still-active target once. The bool reports whether there is
// nothing left to watch.
func (w *Watcher) poll(ctx context.Context) (int, bool) {
	active := w.activeTargets()
	if len(active) == 0 {
		return 0, true
	}

	var (
		wg     sync.WaitGroup
		mu     sync.Mutex
		booked int
	)

	for _, t := range active {
		wg.Add(1)
		go func(t Target) {
			defer wg.Done()
			if ctx.Err() != nil {
				return
			}
			if w.checkTarget(t) {
				mu.Lock()
				booked++
				mu.Unlock()
			}
		}(t)
	}
	wg.Wait()

	return booked, false
}

func (w *Watcher) activeTargets() []Target {
	w.mu.Lock()
	defer w.mu.Unlock()

	capped := w.MaxBookings > 0 && w.bookedCount >= w.MaxBookings

	var active []Target
	for _, t := range w.targets {
		if w.bookedNights[t.Details.Date] {
			continue
		}
		// Once the booking cap is hit there is nothing left to auto-book, but
		// alert-only targets are still worth watching.
		if capped && t.OnHit != OnHitAlert {
			continue
		}
		active = append(active, t)
	}
	return active
}

// checkTarget polls one target and applies its on-hit policy. It reports
// whether a reservation was booked.
func (w *Watcher) checkTarget(t Target) bool {
	workflow := NewResyBookingWorkflow(w.resyClient, t.Details)

	// A single attempt per poll; the watch loop provides the retrying.
	configIds, err := workflow.Find(0)
	available := err == nil && len(configIds) > 0

	w.mu.Lock()
	previously := w.wasAvailable[t.Label]
	w.wasAvailable[t.Label] = available
	alreadyWon := w.bookedNights[t.Details.Date]
	w.mu.Unlock()

	if alreadyWon || !available || previously {
		return false
	}

	if t.OnHit == OnHitAlert {
		w.alerter.Send(Alert{
			Kind:    AlertAvailable,
			Target:  t.Label,
			Date:    t.Details.Date,
			Message: fmt.Sprintf("%d slot(s) open at %s — book it manually, it will not hold.", len(configIds), t.Label),
		})
		return false
	}

	// Claim before booking so the cap and the one-per-night rule hold even when
	// several targets find tables in the same poll.
	if !w.claimBookingSlot(t.Details.Date) {
		return false
	}

	token, err := workflow.Book(configIds)
	if err != nil {
		w.releaseBookingSlot(t.Details.Date)
		w.alerter.Send(Alert{
			Kind:    AlertFailed,
			Target:  t.Label,
			Date:    t.Details.Date,
			Message: fmt.Sprintf("Found %d slot(s) but booking failed: %v", len(configIds), err),
		})
		return false
	}

	w.mu.Lock()
	remaining := w.MaxBookings - w.bookedCount
	w.mu.Unlock()

	capNote := ""
	if w.MaxBookings > 0 && remaining <= 0 {
		capNote = " Booking cap reached; no further auto-booking."
	}

	w.alerter.Send(Alert{
		Kind:    AlertBooked,
		Target:  t.Label,
		Date:    t.Details.Date,
		Message: fmt.Sprintf("Booked %s automatically. No further watching for this night.%s", t.Label, capNote),
		Token:   token,
	})
	return true
}
