package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writePlan(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "targets.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadPlanAssignsFileOrderPriority(t *testing.T) {
	path := writePlan(t, `{
		"snipe_time": "00:00",
		"retry": "90s",
		"decision_window": "3s",
		"targets": [
			{"name":"Don Angie","venue_id":1505,"date":"2026-09-15","party_size":2,"res_times":["19:00"]},
			{"name":"4 Charles","venue_id":999,"date":"2026-09-15","party_size":2,"res_times":["19:00"]}
		]
	}`)

	plan, err := LoadPlan(path)
	if err != nil {
		t.Fatalf("LoadPlan() error: %v", err)
	}
	if plan.Targets[0].Priority != 1 || plan.Targets[1].Priority != 2 {
		t.Fatalf("priorities = %d, %d; want 1, 2 from file order", plan.Targets[0].Priority, plan.Targets[1].Priority)
	}
	if plan.Retry.Duration().Seconds() != 90 {
		t.Fatalf("retry = %s, want 90s", plan.Retry.Duration())
	}
	if plan.DecisionWindow.Duration().Seconds() != 3 {
		t.Fatalf("decision_window = %s, want 3s", plan.DecisionWindow.Duration())
	}
}

// Two same-night targets at the same priority is ambiguous: only one can be
// booked, so the file must say which.
func TestLoadPlanRejectsDuplicatePriorityOnOneNight(t *testing.T) {
	path := writePlan(t, `{
		"targets": [
			{"name":"Don Angie","venue_id":1505,"date":"2026-09-15","party_size":2,"res_times":["19:00"],"priority":1},
			{"name":"4 Charles","venue_id":999,"date":"2026-09-15","party_size":2,"res_times":["19:00"],"priority":1}
		]
	}`)

	_, err := LoadPlan(path)
	if err == nil {
		t.Fatal("LoadPlan() error = nil, want a duplicate-priority error")
	}
	if !strings.Contains(err.Error(), "distinct priorities") {
		t.Fatalf("error %q should explain the priority clash", err)
	}
}

// The same priority on different nights is fine; they never compete.
func TestLoadPlanAllowsSamePriorityOnDifferentNights(t *testing.T) {
	path := writePlan(t, `{
		"targets": [
			{"name":"Don Angie","venue_id":1505,"date":"2026-09-15","party_size":2,"res_times":["19:00"],"priority":1},
			{"name":"Don Angie","venue_id":1505,"date":"2026-09-22","party_size":2,"res_times":["19:00"],"priority":1}
		]
	}`)

	if _, err := LoadPlan(path); err != nil {
		t.Fatalf("LoadPlan() error: %v", err)
	}
}

func TestLoadPlanRejectsBadFields(t *testing.T) {
	cases := map[string]string{
		"missing venue_id": `{"targets":[{"name":"x","date":"2026-09-15","party_size":2,"res_times":["19:00"]}]}`,
		"bad date":         `{"targets":[{"name":"x","venue_id":1,"date":"09/15/2026","party_size":2,"res_times":["19:00"]}]}`,
		"zero party":       `{"targets":[{"name":"x","venue_id":1,"date":"2026-09-15","party_size":0,"res_times":["19:00"]}]}`,
		"no times":         `{"targets":[{"name":"x","venue_id":1,"date":"2026-09-15","party_size":2,"res_times":[]}]}`,
		"bad time":         `{"targets":[{"name":"x","venue_id":1,"date":"2026-09-15","party_size":2,"res_times":["7pm"]}]}`,
		"bad duration":     `{"retry":"ninety","targets":[{"name":"x","venue_id":1,"date":"2026-09-15","party_size":2,"res_times":["19:00"]}]}`,
		"unknown field":    `{"targets":[{"name":"x","venue_id":1,"date":"2026-09-15","party_size":2,"res_times":["19:00"],"typo":1}]}`,
		"no targets":       `{"targets":[]}`,
	}

	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := LoadPlan(writePlan(t, body)); err == nil {
				t.Fatalf("LoadPlan() error = nil, want rejection for %s", name)
			}
		})
	}
}

func TestTargetDetailsExpandsTableTypes(t *testing.T) {
	target := Target{
		Name: "Don Angie", VenueID: 1505, Date: "2026-09-15", PartySize: 2,
		ResTimes: []string{"19:00"}, TableTypes: []string{"Dining Room"},
	}

	details, err := target.Details()
	if err != nil {
		t.Fatalf("Details() error: %v", err)
	}
	// One entry for the requested table type, plus an "any table" fallback.
	if len(details.ResTimeTypes) != 2 {
		t.Fatalf("ResTimeTypes = %d, want 2", len(details.ResTimeTypes))
	}
	if details.ResTimeTypes[0].ReservationTime != "19:00:00" {
		t.Fatalf("time = %q, want normalized 19:00:00", details.ResTimeTypes[0].ReservationTime)
	}
	if details.ResTimeTypes[1].TableType != nil {
		t.Fatal("last entry should be the any-table fallback")
	}
}

// An explicit "0s" must override a non-zero command-line default, so absent
// and zero have to stay distinguishable.
func TestPlanDistinguishesZeroFromAbsentDurations(t *testing.T) {
	explicit := writePlan(t, `{
		"retry": "0s",
		"decision_window": "0s",
		"targets": [{"name":"x","venue_id":1,"date":"2026-09-15","party_size":2,"res_times":["19:00"]}]
	}`)
	absent := writePlan(t, `{
		"targets": [{"name":"x","venue_id":1,"date":"2026-09-15","party_size":2,"res_times":["19:00"]}]
	}`)

	withZero, err := LoadPlan(explicit)
	if err != nil {
		t.Fatal(err)
	}
	if got := withZero.RetryOr(60 * time.Second); got != 0 {
		t.Fatalf("RetryOr() = %s, want an explicit 0 to win", got)
	}
	if got := withZero.DecisionWindowOr(2 * time.Second); got != 0 {
		t.Fatalf("DecisionWindowOr() = %s, want an explicit 0 to win", got)
	}

	withoutAny, err := LoadPlan(absent)
	if err != nil {
		t.Fatal(err)
	}
	if got := withoutAny.RetryOr(60 * time.Second); got != 60*time.Second {
		t.Fatalf("RetryOr() = %s, want the 60s fallback", got)
	}
	if got := withoutAny.DecisionWindowOr(2 * time.Second); got != 2*time.Second {
		t.Fatalf("DecisionWindowOr() = %s, want the 2s fallback", got)
	}
}
