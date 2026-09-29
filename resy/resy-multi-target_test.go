package resy

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"resy-snipe/config"
)

// multiFakeAPI serves per-venue availability and records which config tokens
// were actually booked, so priority arbitration can be asserted.
type multiFakeAPI struct {
	mu        sync.Mutex
	available map[int]bool
	findDelay map[int]time.Duration
	booked    []string
	bookErr   error
}

func newMultiFake() *multiFakeAPI {
	return &multiFakeAPI{
		available: make(map[int]bool),
		findDelay: make(map[int]time.Duration),
	}
}

func (f *multiFakeAPI) GetReservations(date string, partySize int, venueID int) (string, error) {
	f.mu.Lock()
	delay := f.findDelay[venueID]
	isAvailable := f.available[venueID]
	f.mu.Unlock()

	if delay > 0 {
		time.Sleep(delay)
	}
	if !isAvailable {
		return `{"results":{"venues":[{"slots":[]}]}}`, nil
	}
	// The token carries venue and date so bookings can be attributed.
	return fmt.Sprintf(`{"results":{"venues":[{"slots":[
		{"config":{"type":"Bar","token":"tok-%d-%s"},"date":{"start":"%s 19:00:00"}}
	]}]}}`, venueID, date, date), nil
}

// Echo the config id back as the book token so PostReservation can record it.
func (f *multiFakeAPI) GetReservationDetails(configID string, date string, partySize int) (string, error) {
	return fmt.Sprintf(`{"user":{"payment_methods":[{"id":42}]},"book_token":{"value":%q}}`, configID), nil
}

func (f *multiFakeAPI) PostReservation(paymentMethodID string, bookToken string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.bookErr != nil {
		return "", f.bookErr
	}
	f.booked = append(f.booked, bookToken)
	return "confirmed-" + bookToken, nil
}

func (f *multiFakeAPI) SearchVenues(query string) (string, error) { return "", nil }

func (f *multiFakeAPI) CreateNotify(venueID int, date string, partySize int, startTime string, endTime string) (string, error) {
	return `{"id":1}`, nil
}

func (f *multiFakeAPI) bookedTokens() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.booked))
	copy(out, f.booked)
	return out
}

func target(label string, venueID int, date string, priority int) Target {
	resTimeTypes, err := config.BuildResTimeTypes([]string{"19:00"}, nil)
	if err != nil {
		panic(err)
	}
	return Target{
		Label:    label,
		Priority: priority,
		Details: config.ReservationDetails{
			Date:         date,
			PartySize:    2,
			VenueId:      venueID,
			ResTimeTypes: resTimeTypes,
		},
	}
}

func resultFor(t *testing.T, results []TargetResult, label string) TargetResult {
	t.Helper()
	for _, r := range results {
		if r.Label == label {
			return r
		}
	}
	t.Fatalf("no result for %q in %+v", label, results)
	return TargetResult{}
}

// Resy allows one reservation per night, so same-date targets must resolve to
// exactly one booking and it must be the highest priority available.
func TestPriorityWinsWithinANight(t *testing.T) {
	api := newMultiFake()
	api.available[1505] = true
	api.available[999] = true

	targets := []Target{
		target("don-angie", 1505, "2026-09-15", 1),
		target("four-charles", 999, "2026-09-15", 2),
	}

	results := NewMultiTargetWorkflow(*NewResyClient(api), targets).Run(0, 2*time.Second)

	if got := api.bookedTokens(); len(got) != 1 || got[0] != "tok-1505-2026-09-15" {
		t.Fatalf("booked %v, want exactly [tok-1505-2026-09-15]", got)
	}
	if r := resultFor(t, results, "don-angie"); !r.Booked {
		t.Fatalf("don-angie should be booked, got %+v", r)
	}
	if r := resultFor(t, results, "four-charles"); r.Booked {
		t.Fatal("four-charles must not be booked on a night already won")
	}
}

func TestFallsBackToLowerPriorityWhenTopIsUnavailable(t *testing.T) {
	api := newMultiFake()
	api.available[999] = true // top priority venue 1505 has nothing

	targets := []Target{
		target("don-angie", 1505, "2026-09-15", 1),
		target("four-charles", 999, "2026-09-15", 2),
	}

	results := NewMultiTargetWorkflow(*NewResyClient(api), targets).Run(0, 0)

	if got := api.bookedTokens(); len(got) != 1 || got[0] != "tok-999-2026-09-15" {
		t.Fatalf("booked %v, want exactly [tok-999-2026-09-15]", got)
	}
	if r := resultFor(t, results, "four-charles"); !r.Booked {
		t.Fatalf("four-charles should be booked, got %+v", r)
	}
}

// Different nights don't compete, so each can land its own reservation.
func TestDifferentNightsBothBook(t *testing.T) {
	api := newMultiFake()
	api.available[1505] = true

	targets := []Target{
		target("don-angie-15th", 1505, "2026-09-15", 1),
		target("don-angie-22nd", 1505, "2026-09-22", 1),
	}

	results := NewMultiTargetWorkflow(*NewResyClient(api), targets).Run(0, 0)

	if got := api.bookedTokens(); len(got) != 2 {
		t.Fatalf("booked %v, want 2 reservations across 2 nights", got)
	}
	for _, label := range []string{"don-angie-15th", "don-angie-22nd"} {
		if r := resultFor(t, results, label); !r.Booked {
			t.Fatalf("%s should be booked, got %+v", label, r)
		}
	}
}

// A slower top-priority find should still win if it lands inside the window.
func TestDecisionWindowHoldsForHigherPriority(t *testing.T) {
	api := newMultiFake()
	api.available[1505] = true
	api.available[999] = true
	api.findDelay[1505] = 150 * time.Millisecond

	targets := []Target{
		target("don-angie", 1505, "2026-09-15", 1),
		target("four-charles", 999, "2026-09-15", 2),
	}

	results := NewMultiTargetWorkflow(*NewResyClient(api), targets).Run(0, 3*time.Second)

	if got := api.bookedTokens(); len(got) != 1 || got[0] != "tok-1505-2026-09-15" {
		t.Fatalf("booked %v, want the higher priority [tok-1505-2026-09-15]", got)
	}
	if r := resultFor(t, results, "don-angie"); !r.Booked {
		t.Fatalf("don-angie should win inside the decision window, got %+v", r)
	}
}

// With no decision window the first available option is taken immediately,
// even though a higher priority was about to land.
func TestZeroDecisionWindowTakesFirstAvailable(t *testing.T) {
	api := newMultiFake()
	api.available[1505] = true
	api.available[999] = true
	api.findDelay[1505] = 200 * time.Millisecond

	targets := []Target{
		target("don-angie", 1505, "2026-09-15", 1),
		target("four-charles", 999, "2026-09-15", 2),
	}

	NewMultiTargetWorkflow(*NewResyClient(api), targets).Run(0, 0)

	if got := api.bookedTokens(); len(got) != 1 || got[0] != "tok-999-2026-09-15" {
		t.Fatalf("booked %v, want the first available [tok-999-2026-09-15]", got)
	}
}

func TestNothingBookedWhenNoAvailability(t *testing.T) {
	api := newMultiFake()

	targets := []Target{
		target("don-angie", 1505, "2026-09-15", 1),
		target("four-charles", 999, "2026-09-15", 2),
	}

	results := NewMultiTargetWorkflow(*NewResyClient(api), targets).Run(0, 0)

	if got := api.bookedTokens(); len(got) != 0 {
		t.Fatalf("booked %v, want nothing", got)
	}
	for _, r := range results {
		if r.Booked {
			t.Fatalf("%s reported booked with no availability", r.Label)
		}
	}
}

// A dry run must never book, whatever the venue is offering.
func TestDryRunNeverBooks(t *testing.T) {
	api := newMultiFake()
	api.available[1505] = true
	api.available[999] = true

	targets := []Target{
		target("don-angie", 1505, "2026-09-15", 1),
		target("four-charles", 999, "2026-09-15", 2),
	}

	reports := NewMultiTargetWorkflow(*NewResyClient(api), targets).DryRun()

	if got := api.bookedTokens(); len(got) != 0 {
		t.Fatalf("dry run booked %v, want nothing", got)
	}
	if len(reports) != 2 {
		t.Fatalf("reports = %d, want 2", len(reports))
	}
	for _, report := range reports {
		if report.Err != nil {
			t.Fatalf("%s: unexpected error %v", report.Label, report.Err)
		}
		if report.Matched != 1 {
			t.Fatalf("%s: matched = %d, want 1", report.Label, report.Matched)
		}
		if len(report.Offered) == 0 {
			t.Fatalf("%s: no availability reported", report.Label)
		}
	}
}

func TestDryRunReportsEmptyVenue(t *testing.T) {
	api := newMultiFake()
	targets := []Target{target("don-angie", 1505, "2026-09-15", 1)}

	reports := NewMultiTargetWorkflow(*NewResyClient(api), targets).DryRun()

	if len(reports) != 1 || reports[0].Err != nil {
		t.Fatalf("reports = %+v, want one clean report", reports)
	}
	if reports[0].Matched != 0 || len(reports[0].Offered) != 0 {
		t.Fatalf("report = %+v, want no availability", reports[0])
	}
}
