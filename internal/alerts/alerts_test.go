package alerts_test

import (
	"context"
	"testing"
	"time"

	"resy-snipe/internal/alerts"
	"resy-snipe/internal/domain"
)

func TestMemorySourceReturnsFalseUntilFired(t *testing.T) {
	t.Parallel()
	src := alerts.NewMemorySource(0)
	t.Cleanup(func() { _ = src.Close() })

	req := alerts.Request{
		User:      "u1",
		Venue:     domain.VenueRef{Provider: "resy", Ref: "38660"},
		Date:      domain.NewDate(2026, time.June, 1),
		PartySize: 2,
	}
	got, err := src.Poll(context.Background(), req)
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if got.Fired {
		t.Fatal("Poll fired before Fire")
	}

	now := time.Date(2026, 5, 13, 9, 0, 0, 0, time.UTC)
	src.Fire(req.User, req.Venue, req.Date, req.PartySize, now)

	got, err = src.Poll(context.Background(), req)
	if err != nil {
		t.Fatalf("Poll after Fire: %v", err)
	}
	if !got.Fired || !got.FiredAt.Equal(now) {
		t.Fatalf("Poll after Fire: %+v", got)
	}
}

func TestMemorySourceDoesNotFireOnMismatch(t *testing.T) {
	t.Parallel()
	src := alerts.NewMemorySource(0)
	t.Cleanup(func() { _ = src.Close() })

	venue := domain.VenueRef{Provider: "resy", Ref: "38660"}
	date := domain.NewDate(2026, time.June, 1)
	src.Fire("u1", venue, date, 2, time.Now())

	cases := []alerts.Request{
		{User: "u2", Venue: venue, Date: date, PartySize: 2},                                                 // wrong user
		{User: "u1", Venue: domain.VenueRef{Provider: "resy", Ref: "999"}, Date: date, PartySize: 2},         // wrong venue
		{User: "u1", Venue: venue, Date: domain.NewDate(2026, time.June, 2), PartySize: 2},                   // wrong date
		{User: "u1", Venue: venue, Date: date, PartySize: 4},                                                 // wrong party
	}
	for _, req := range cases {
		got, err := src.Poll(context.Background(), req)
		if err != nil {
			t.Fatalf("Poll(%+v): %v", req, err)
		}
		if got.Fired {
			t.Errorf("Poll(%+v): unexpected fire", req)
		}
	}
}

func TestMemorySourceMinPollInterval(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in, want time.Duration
	}{
		{0, 100 * time.Millisecond},
		{-1, 100 * time.Millisecond},
		{500 * time.Millisecond, 500 * time.Millisecond},
		{30 * time.Second, 30 * time.Second},
	}
	for _, c := range cases {
		src := alerts.NewMemorySource(c.in)
		if got := src.MinPollInterval(); got != c.want {
			t.Errorf("min(%s) = %s, want %s", c.in, got, c.want)
		}
	}
}
