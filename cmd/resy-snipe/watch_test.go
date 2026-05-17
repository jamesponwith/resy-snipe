package main

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"resy-snipe/internal/domain"
)

func TestDedupTrackerSingleClaimWins(t *testing.T) {
	t.Parallel()
	d := newDedupTracker()
	venueDate := domain.NewDate(2026, time.May, 22)

	winner, ok := d.claim("u1", venueDate, "snp_a")
	if !ok {
		t.Fatal("first claim must succeed")
	}
	if winner != "snp_a" {
		t.Errorf("winner = %q, want snp_a", winner)
	}

	winner2, ok2 := d.claim("u1", venueDate, "snp_b")
	if ok2 {
		t.Fatal("second claim must fail")
	}
	if winner2 != "snp_a" {
		t.Errorf("second claim reports winner = %q, want snp_a", winner2)
	}
}

func TestDedupTrackerDifferentUsersDoNotCollide(t *testing.T) {
	t.Parallel()
	d := newDedupTracker()
	date := domain.NewDate(2026, time.May, 22)

	if _, ok := d.claim("u1", date, "snp_a"); !ok {
		t.Fatal("u1 claim must succeed")
	}
	if _, ok := d.claim("u2", date, "snp_b"); !ok {
		t.Fatal("u2 claim must succeed (different user)")
	}
}

func TestDedupTrackerDifferentDatesDoNotCollide(t *testing.T) {
	t.Parallel()
	d := newDedupTracker()
	if _, ok := d.claim("u1", domain.NewDate(2026, time.May, 22), "snp_a"); !ok {
		t.Fatal("first date claim must succeed")
	}
	if _, ok := d.claim("u1", domain.NewDate(2026, time.May, 23), "snp_b"); !ok {
		t.Fatal("second date claim must succeed (different date)")
	}
}

func TestDedupTrackerReleaseAllowsReclaim(t *testing.T) {
	t.Parallel()
	d := newDedupTracker()
	date := domain.NewDate(2026, time.May, 22)

	if _, ok := d.claim("u1", date, "snp_a"); !ok {
		t.Fatal("first claim must succeed")
	}
	if _, ok := d.claim("u1", date, "snp_b"); ok {
		t.Fatal("second claim must fail before release")
	}
	d.release("u1", date, "snp_a")
	winner, ok := d.claim("u1", date, "snp_b")
	if !ok {
		t.Fatal("post-release claim must succeed")
	}
	if winner != "snp_b" {
		t.Errorf("winner after re-claim = %q, want snp_b", winner)
	}
}

// TestDedupTrackerReleaseFromNonOwnerNoOps asserts that an old/stale
// release call (snipe B trying to release a claim held by snipe A)
// is a no-op — the original holder keeps the slot.
func TestDedupTrackerReleaseFromNonOwnerNoOps(t *testing.T) {
	t.Parallel()
	d := newDedupTracker()
	date := domain.NewDate(2026, time.May, 22)
	if _, ok := d.claim("u1", date, "snp_a"); !ok {
		t.Fatal("first claim must succeed")
	}
	d.release("u1", date, "snp_b") // wrong owner
	if _, ok := d.claim("u1", date, "snp_c"); ok {
		t.Fatal("claim should still be held by snp_a after a non-owner release")
	}
}

// TestDedupTrackerConcurrentClaimsOneWinner: only one of N concurrent
// claims succeeds — the rest see the same winner. Exercises the
// mutex under -race.
func TestDedupTrackerConcurrentClaimsOneWinner(t *testing.T) {
	t.Parallel()
	d := newDedupTracker()
	date := domain.NewDate(2026, time.May, 22)

	const n = 32
	var wins atomic.Int32
	var wg sync.WaitGroup
	wg.Add(n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		id := domain.SnipeID("snp_" + string(rune('a'+i)))
		go func() {
			defer wg.Done()
			<-start
			if _, ok := d.claim("u1", date, id); ok {
				wins.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()

	if got := wins.Load(); got != 1 {
		t.Errorf("wins = %d, want 1 (exactly one concurrent claim must succeed)", got)
	}
}
