package resy

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func watchTarget(label string, venueID int, date string, onHit string) Target {
	t := target(label, venueID, date, 1)
	t.OnHit = onHit
	return t
}

func testAlerter(t *testing.T) (*Alerter, string) {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), "alerts.jsonl")
	alerter := NewAlerter(logPath, nil, false)
	return alerter, logPath
}

func readAlerts(t *testing.T, path string) []Alert {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}

	var alerts []Alert
	for _, line := range splitLines(raw) {
		var alert Alert
		if err := json.Unmarshal(line, &alert); err != nil {
			t.Fatalf("bad log line %q: %v", line, err)
		}
		alerts = append(alerts, alert)
	}
	return alerts
}

func splitLines(raw []byte) [][]byte {
	var out [][]byte
	start := 0
	for i, b := range raw {
		if b == '\n' {
			if i > start {
				out = append(out, raw[start:i])
			}
			start = i + 1
		}
	}
	return out
}

// An alert-only target must never book, however available the table is.
func TestWatchAlertOnlyTargetNeverBooks(t *testing.T) {
	api := newMultiFake()
	api.available[1505] = true
	alerter, logPath := testAlerter(t)

	watcher := NewWatcher(*NewResyClient(api), []Target{
		watchTarget("don-angie", 1505, "2026-09-15", OnHitAlert),
	}, alerter, time.Hour)

	watcher.checkTarget(watcher.targets[0])

	if got := api.bookedTokens(); len(got) != 0 {
		t.Fatalf("alert-only target booked %v, want nothing", got)
	}
	alerts := readAlerts(t, logPath)
	if len(alerts) != 1 || alerts[0].Kind != AlertAvailable {
		t.Fatalf("alerts = %+v, want one %q alert", alerts, AlertAvailable)
	}
}

func TestWatchBookTargetBooksAndAlerts(t *testing.T) {
	api := newMultiFake()
	api.available[7777] = true
	alerter, logPath := testAlerter(t)

	watcher := NewWatcher(*NewResyClient(api), []Target{
		watchTarget("cote", 7777, "2026-09-15", OnHitBook),
	}, alerter, time.Hour)

	if !watcher.checkTarget(watcher.targets[0]) {
		t.Fatal("checkTarget() = false, want a booking")
	}

	if got := api.bookedTokens(); len(got) != 1 {
		t.Fatalf("booked %v, want exactly 1", got)
	}
	alerts := readAlerts(t, logPath)
	if len(alerts) != 1 || alerts[0].Kind != AlertBooked {
		t.Fatalf("alerts = %+v, want one %q alert", alerts, AlertBooked)
	}
	if alerts[0].Token == "" {
		t.Fatal("booked alert should carry the reservation token for the audit log")
	}
}

// Alerts are edge-triggered, so a table that stays open must not re-alert on
// every poll.
func TestWatchAlertsOnlyOnTransitionToAvailable(t *testing.T) {
	api := newMultiFake()
	api.available[1505] = true
	alerter, logPath := testAlerter(t)

	watcher := NewWatcher(*NewResyClient(api), []Target{
		watchTarget("don-angie", 1505, "2026-09-15", OnHitAlert),
	}, alerter, time.Hour)

	for i := 0; i < 4; i++ {
		watcher.checkTarget(watcher.targets[0])
	}

	if alerts := readAlerts(t, logPath); len(alerts) != 1 {
		t.Fatalf("got %d alerts across 4 polls, want 1 edge-triggered alert", len(alerts))
	}
}

// Going unavailable and back again is a new edge and should alert again.
func TestWatchReAlertsAfterTableDisappears(t *testing.T) {
	api := newMultiFake()
	api.available[1505] = true
	alerter, logPath := testAlerter(t)

	watcher := NewWatcher(*NewResyClient(api), []Target{
		watchTarget("don-angie", 1505, "2026-09-15", OnHitAlert),
	}, alerter, time.Hour)

	watcher.checkTarget(watcher.targets[0])
	api.mu.Lock()
	api.available[1505] = false
	api.mu.Unlock()
	watcher.checkTarget(watcher.targets[0])
	api.mu.Lock()
	api.available[1505] = true
	api.mu.Unlock()
	watcher.checkTarget(watcher.targets[0])

	if alerts := readAlerts(t, logPath); len(alerts) != 2 {
		t.Fatalf("got %d alerts, want 2 (one per transition to available)", len(alerts))
	}
}

// Once a night is booked, nothing else should be attempted for that night.
func TestWatchStopsWatchingABookedNight(t *testing.T) {
	api := newMultiFake()
	api.available[7777] = true
	api.available[1505] = true
	alerter, _ := testAlerter(t)

	watcher := NewWatcher(*NewResyClient(api), []Target{
		watchTarget("cote", 7777, "2026-09-15", OnHitBook),
		watchTarget("don-angie", 1505, "2026-09-15", OnHitAlert),
	}, alerter, time.Hour)

	watcher.checkTarget(watcher.targets[0])

	if active := watcher.activeTargets(); len(active) != 0 {
		t.Fatalf("activeTargets() = %d, want 0 once the night is won", len(active))
	}
}

// Watch must return promptly on cancellation rather than sleeping out its interval.
func TestWatchStopsOnContextCancel(t *testing.T) {
	api := newMultiFake()
	alerter, _ := testAlerter(t)

	watcher := NewWatcher(*NewResyClient(api), []Target{
		watchTarget("don-angie", 1505, "2026-09-15", OnHitAlert),
	}, alerter, time.Hour)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := watcher.Watch(ctx); err == nil {
			t.Error("Watch() error = nil, want context.Canceled")
		}
	}()

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Watch() did not stop within 5s of cancellation")
	}
}

// A watch with no targets left has nothing to do and should not spin.
func TestWatchReturnsWhenEveryNightIsBooked(t *testing.T) {
	api := newMultiFake()
	api.available[7777] = true
	alerter, _ := testAlerter(t)

	watcher := NewWatcher(*NewResyClient(api), []Target{
		watchTarget("cote", 7777, "2026-09-15", OnHitBook),
	}, alerter, time.Hour)

	booked, err := watcher.Watch(context.Background())
	if err != nil {
		t.Fatalf("Watch() error: %v", err)
	}
	if booked != 1 {
		t.Fatalf("booked = %d, want 1", booked)
	}
}

// Watching many nights with auto-book must not reserve one table per night.
func TestWatchRespectsBookingCap(t *testing.T) {
	api := newMultiFake()
	for _, venue := range []int{7777} {
		api.available[venue] = true
	}
	alerter, _ := testAlerter(t)

	var targets []Target
	for _, date := range []string{"2026-09-10", "2026-09-11", "2026-09-12", "2026-09-13"} {
		targets = append(targets, watchTarget("cote-"+date, 7777, date, OnHitBook))
	}

	watcher := NewWatcher(*NewResyClient(api), targets, alerter, time.Hour)
	watcher.MaxBookings = 1

	booked, err := watcher.Watch(context.Background())
	if err != nil {
		t.Fatalf("Watch() error: %v", err)
	}
	if booked != 1 {
		t.Fatalf("booked = %d across 4 available nights, want the cap of 1", booked)
	}
	if got := api.bookedTokens(); len(got) != 1 {
		t.Fatalf("booked %v, want exactly 1 reservation", got)
	}
}

// Hitting the booking cap must not silence alert-only targets.
func TestWatchKeepsAlertingAfterBookingCap(t *testing.T) {
	api := newMultiFake()
	api.available[7777] = true
	api.available[1505] = true
	alerter, _ := testAlerter(t)

	watcher := NewWatcher(*NewResyClient(api), []Target{
		watchTarget("cote", 7777, "2026-09-10", OnHitBook),
		watchTarget("don-angie", 1505, "2026-09-11", OnHitAlert),
	}, alerter, time.Hour)
	watcher.MaxBookings = 1

	watcher.checkTarget(watcher.targets[0])

	active := watcher.activeTargets()
	if len(active) != 1 || active[0].OnHit != OnHitAlert {
		t.Fatalf("activeTargets() = %+v, want only the alert-only target", active)
	}
}

// Two auto-book targets on the same night must not both book, even when they
// find tables in the same concurrent poll.
func TestWatchNeverDoubleBooksOneNightConcurrently(t *testing.T) {
	for attempt := 0; attempt < 20; attempt++ {
		api := newMultiFake()
		api.available[7777] = true
		api.available[8888] = true
		alerter, _ := testAlerter(t)

		watcher := NewWatcher(*NewResyClient(api), []Target{
			watchTarget("cote", 7777, "2026-09-10", OnHitBook),
			watchTarget("other", 8888, "2026-09-10", OnHitBook),
		}, alerter, time.Hour)
		watcher.MaxBookings = 0 // unlimited: only the one-per-night rule applies

		if _, err := watcher.Watch(context.Background()); err != nil {
			t.Fatalf("Watch() error: %v", err)
		}
		if got := api.bookedTokens(); len(got) != 1 {
			t.Fatalf("attempt %d: booked %v on one night, want exactly 1", attempt, got)
		}
	}
}

// A failed booking must hand its claim back rather than burning the cap.
func TestWatchReleasesClaimWhenBookingFails(t *testing.T) {
	api := newMultiFake()
	api.available[7777] = true
	api.bookErr = errors.New("payment declined")
	alerter, logPath := testAlerter(t)

	watcher := NewWatcher(*NewResyClient(api), []Target{
		watchTarget("cote", 7777, "2026-09-10", OnHitBook),
	}, alerter, time.Hour)
	watcher.MaxBookings = 1

	if watcher.checkTarget(watcher.targets[0]) {
		t.Fatal("checkTarget() = true, want false when booking fails")
	}

	// The night is free again, so a later poll can retry it.
	if active := watcher.activeTargets(); len(active) != 1 {
		t.Fatalf("activeTargets() = %d, want the failed night still watchable", len(active))
	}
	alerts := readAlerts(t, logPath)
	if len(alerts) != 1 || alerts[0].Kind != AlertFailed {
		t.Fatalf("alerts = %+v, want one %q alert", alerts, AlertFailed)
	}
}
