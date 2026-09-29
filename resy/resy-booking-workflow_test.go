package resy

import (
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"resy-snipe/config"
)

// twoSlotFindResponse offers 18:45:00 as both a Bar and a Parlor table, so a
// request for both table types produces two config tokens and therefore two
// concurrent booking attempts.
const twoSlotFindResponse = `{"results":{"venues":[{"slots":[
	{"config":{"type":"Bar","token":"tok-bar"},"date":{"start":"2026-01-22 18:45:00"}},
	{"config":{"type":"Parlor","token":"tok-parlor"},"date":{"start":"2026-01-22 18:45:00"}}
]}]}}`

const emptyFindResponse = `{"results":{"venues":[{"slots":[]}]}}`

const detailsResponse = `{"user":{"payment_methods":[{"id":42}]},"book_token":{"value":"book-tok"}}`

type fakeAPI struct {
	findResp  string
	findCalls int64

	bookErr   error
	bookCalls int64
	bookDelay time.Duration

	searchResp string
}

func (f *fakeAPI) GetReservations(date string, partySize int, venueID int) (string, error) {
	atomic.AddInt64(&f.findCalls, 1)
	return f.findResp, nil
}

func (f *fakeAPI) GetReservationDetails(configID string, date string, partySize int) (string, error) {
	return detailsResponse, nil
}

func (f *fakeAPI) SearchVenues(query string) (string, error) {
	return f.searchResp, nil
}

func (f *fakeAPI) CreateNotify(venueID int, date string, partySize int, startTime string, endTime string) (string, error) {
	return `{"id":1}`, nil
}

func (f *fakeAPI) PostReservation(paymentMethodID string, bookToken string) (string, error) {
	// Hold the call open so every goroutine is in flight at once, which is what
	// made the old shared-variable writes race.
	time.Sleep(f.bookDelay)
	atomic.AddInt64(&f.bookCalls, 1)
	if f.bookErr != nil {
		return "", f.bookErr
	}
	return "resy-token-123", nil
}

func newWorkflow(api *fakeAPI, resTimeTypes []config.ReservationTimeType) *ResyBookingWorkflow {
	return NewResyBookingWorkflow(*NewResyClient(api), config.ReservationDetails{
		Date:         "2026-01-22",
		PartySize:    2,
		VenueId:      466,
		ResTimeTypes: resTimeTypes,
	})
}

func tableType(s string) *string { return &s }

// bothTableTypes yields two matching config tokens.
func bothTableTypes() []config.ReservationTimeType {
	return []config.ReservationTimeType{
		config.NewReservationTimeType("18:45:00", tableType("Bar")),
		config.NewReservationTimeType("18:45:00", tableType("Parlor")),
	}
}

// Run under -race: concurrent booking goroutines must not write the result
// variables unguarded, and exactly one token must come back.
func TestRunReturnsOneTokenUnderConcurrency(t *testing.T) {
	api := &fakeAPI{findResp: twoSlotFindResponse, bookDelay: 20 * time.Millisecond}

	token, err := newWorkflow(api, bothTableTypes()).Run(time.Second)
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}
	if token != "resy-token-123" {
		t.Fatalf("Run() token = %q, want %q", token, "resy-token-123")
	}
	if got := atomic.LoadInt64(&api.bookCalls); got != 2 {
		t.Fatalf("book calls = %d, want 2 (both goroutines should be in flight)", got)
	}
}

func TestRunJoinsErrorsWhenEveryBookingFails(t *testing.T) {
	api := &fakeAPI{findResp: twoSlotFindResponse, bookErr: errors.New("boom")}

	token, err := newWorkflow(api, bothTableTypes()).Run(time.Second)
	if err == nil {
		t.Fatal("Run() error = nil, want an error when every booking fails")
	}
	if token != "" {
		t.Fatalf("Run() token = %q, want empty on failure", token)
	}
	if strings.Count(err.Error(), "boom") != 2 {
		t.Fatalf("error %q should carry both attempts' failures", err.Error())
	}
}

// The retry window must come from Run's argument. Before the fix it was
// hard-coded to 2ms inside the workflow, so this only ever made one attempt.
func TestRunRetriesForTheRequestedDuration(t *testing.T) {
	api := &fakeAPI{findResp: emptyFindResponse}

	start := time.Now()
	if _, err := newWorkflow(api, bothTableTypes()).Run(150 * time.Millisecond); err == nil {
		t.Fatal("Run() error = nil, want an error when nothing is available")
	}
	elapsed := time.Since(start)

	if elapsed < 150*time.Millisecond {
		t.Fatalf("gave up after %s, want at least 150ms of retrying", elapsed)
	}
	if got := atomic.LoadInt64(&api.findCalls); got < 2 {
		t.Fatalf("find calls = %d, want repeated attempts across the window", got)
	}
}

func TestRunMakesASingleAttemptWhenRetryIsZero(t *testing.T) {
	api := &fakeAPI{findResp: emptyFindResponse}

	if _, err := newWorkflow(api, bothTableTypes()).Run(0); err == nil {
		t.Fatal("Run() error = nil, want an error when nothing is available")
	}
	if got := atomic.LoadInt64(&api.findCalls); got != 1 {
		t.Fatalf("find calls = %d, want 1", got)
	}
}

// A requested table type the venue never offered used to queue an empty config
// token, which counted as a hit and then panicked on a nil error assertion.
func TestRunTreatsUnofferedTableTypeAsNoHit(t *testing.T) {
	api := &fakeAPI{findResp: twoSlotFindResponse}
	resTimeTypes := []config.ReservationTimeType{
		config.NewReservationTimeType("18:45:00", tableType("Patio")),
	}

	token, err := newWorkflow(api, resTimeTypes).Run(0)
	if err == nil {
		t.Fatal("Run() error = nil, want an error when the table type is unavailable")
	}
	if token != "" {
		t.Fatalf("Run() token = %q, want empty", token)
	}
	if got := atomic.LoadInt64(&api.bookCalls); got != 0 {
		t.Fatalf("book calls = %d, want 0 for an unavailable table type", got)
	}
}

// A nil table type means "any", so the first offered table should be booked.
func TestRunBooksAnyTableWhenNoTypeRequested(t *testing.T) {
	api := &fakeAPI{findResp: twoSlotFindResponse}
	resTimeTypes := []config.ReservationTimeType{
		config.NewReservationTimeType("18:45:00", nil),
	}

	token, err := newWorkflow(api, resTimeTypes).Run(0)
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}
	if token != "resy-token-123" {
		t.Fatalf("Run() token = %q, want %q", token, "resy-token-123")
	}
	if got := atomic.LoadInt64(&api.bookCalls); got != 1 {
		t.Fatalf("book calls = %d, want 1", got)
	}
}

func TestRunErrorsWhenNoTimesRequested(t *testing.T) {
	api := &fakeAPI{findResp: twoSlotFindResponse}

	if _, err := newWorkflow(api, nil).Run(time.Second); err == nil {
		t.Fatal("Run() error = nil, want an error when no times are requested")
	}
	if got := atomic.LoadInt64(&api.findCalls); got != 0 {
		t.Fatalf("find calls = %d, want 0", got)
	}
}
