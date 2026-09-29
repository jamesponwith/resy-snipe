package resy

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"resy-snipe/config"
)

// Target pairs a plan entry with the reservation details it resolves to.
type Target struct {
	Label    string
	Priority int
	Details  config.ReservationDetails
	// OnHit is OnHitBook or OnHitAlert and only applies in watch mode.
	OnHit string
	// Notify requests that Resy's own Notify waitlist also be joined.
	Notify bool
}

// TargetResult records what happened to a single target.
type TargetResult struct {
	Label   string
	Booked  bool
	Token   string
	Skipped bool
	Err     error
}

// MultiTargetWorkflow snipes several targets at once.
//
// Targets are grouped by date because Resy only allows one reservation per
// night: within a date the group finds availability for every target
// concurrently, then books strictly in priority order and stops at the first
// success. Different dates are independent and run in parallel, so each night
// can land its own reservation.
type MultiTargetWorkflow struct {
	resyClient ResyClient
	targets    []Target
}

func NewMultiTargetWorkflow(resyClient ResyClient, targets []Target) *MultiTargetWorkflow {
	return &MultiTargetWorkflow{resyClient: resyClient, targets: targets}
}

// Run executes every date group concurrently. retryFor bounds each target's
// availability search. decisionWindow is how long a group waits, after some
// target reports availability, for a higher-priority target to come through
// before committing to a booking.
func (m *MultiTargetWorkflow) Run(retryFor time.Duration, decisionWindow time.Duration) []TargetResult {
	byDate := make(map[string][]Target)
	var dates []string
	for _, t := range m.targets {
		if _, seen := byDate[t.Details.Date]; !seen {
			dates = append(dates, t.Details.Date)
		}
		byDate[t.Details.Date] = append(byDate[t.Details.Date], t)
	}
	sort.Strings(dates)

	var (
		mu      sync.Mutex
		results []TargetResult
		wg      sync.WaitGroup
	)

	for _, date := range dates {
		wg.Add(1)
		go func(date string, targets []Target) {
			defer wg.Done()
			groupResults := m.runDateGroup(date, targets, retryFor, decisionWindow)

			mu.Lock()
			defer mu.Unlock()
			results = append(results, groupResults...)
		}(date, byDate[date])
	}
	wg.Wait()

	sort.SliceStable(results, func(i, j int) bool { return results[i].Label < results[j].Label })
	return results
}

// DryRunReport describes what a venue is offering, without booking anything.
type DryRunReport struct {
	Label     string
	Date      string
	Offered   ReservationMap
	Matched   int
	TotalCfgs int
	Err       error
}

// DryRun reports current availability for every target and books nothing. It
// makes exactly one request per target, so it is safe to run against a live
// account at any time.
func (m *MultiTargetWorkflow) DryRun() []DryRunReport {
	reports := make([]DryRunReport, len(m.targets))

	var wg sync.WaitGroup
	for i, t := range m.targets {
		wg.Add(1)
		go func(i int, t Target) {
			defer wg.Done()
			report := DryRunReport{Label: t.Label, Date: t.Details.Date}

			offered, err := m.resyClient.Availability(t.Details.Date, t.Details.PartySize, t.Details.VenueId)
			if err != nil {
				report.Err = err
				reports[i] = report
				return
			}

			report.Offered = offered
			for _, tableTypes := range offered {
				report.TotalCfgs += len(tableTypes)
			}
			report.Matched = matchConfigIDs(offered, t.Details.ResTimeTypes).Len()
			reports[i] = report
		}(i, t)
	}
	wg.Wait()

	return reports
}

type findOutcome struct {
	index     int
	configIds []string
	err       error
}

// bookRetryPause is how long a date group cools off after a failed booking
// before hunting again, giving a tripped rate limiter time to reset.
const bookRetryPause = 3 * time.Second

func (m *MultiTargetWorkflow) runDateGroup(date string, targets []Target, retryFor time.Duration, decisionWindow time.Duration) []TargetResult {
	ordered := make([]Target, len(targets))
	copy(ordered, targets)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Priority < ordered[j].Priority })

	// A failed booking (a 429 during a drop, a slot stolen mid-book) must not
	// end the attempt while retry budget remains: cool off and hunt again.
	deadline := time.Now().Add(retryFor)
	findBudget := retryFor
	for pass := 1; ; pass++ {
		results, booked := m.huntDateGroup(date, ordered, findBudget, decisionWindow)
		remaining := time.Until(deadline)
		if booked || remaining <= bookRetryPause {
			return results
		}
		fmt.Printf("[%s] pass %d booked nothing; cooling off %s, then rehunting for up to %s\n",
			date, pass, bookRetryPause, remaining.Round(time.Second))
		time.Sleep(bookRetryPause)
		findBudget = time.Until(deadline)
	}
}

// huntDateGroup runs one find-and-book pass over a date group and reports
// whether anything was booked.
func (m *MultiTargetWorkflow) huntDateGroup(date string, ordered []Target, retryFor time.Duration, decisionWindow time.Duration) ([]TargetResult, bool) {
	fmt.Printf("\n[%s] hunting %d target(s)...\n", date, len(ordered))

	// Buffered so late finds never block; abandoned goroutines exit on their own
	// once their retry budget runs out.
	outcomes := make(chan findOutcome, len(ordered))
	for i, t := range ordered {
		go func(i int, t Target) {
			workflow := NewResyBookingWorkflow(m.resyClient, t.Details)
			configIds, err := workflow.Find(retryFor)
			outcomes <- findOutcome{index: i, configIds: configIds, err: err}
		}(i, t)
	}

	available := make([][]string, len(ordered))
	findErrs := make([]error, len(ordered))
	settled := make([]bool, len(ordered))
	settledCount := 0
	var decisionTimer <-chan time.Time

collect:
	for settledCount < len(ordered) {
		select {
		case outcome := <-outcomes:
			settledCount++
			settled[outcome.index] = true
			available[outcome.index] = outcome.configIds
			findErrs[outcome.index] = outcome.err

			if outcome.err != nil || len(outcome.configIds) == 0 {
				continue
			}
			fmt.Printf("[%s] available: %s\n", date, ordered[outcome.index].Label)

			// The top priority is available, so nothing better can arrive.
			if outcome.index == 0 {
				break collect
			}
			// Something is available but a better option may still land. Give
			// the higher priorities a bounded head start.
			if decisionTimer == nil && decisionWindow > 0 {
				fmt.Printf("[%s] holding %s for a higher priority...\n", date, decisionWindow)
				decisionTimer = time.After(decisionWindow)
			} else if decisionWindow == 0 {
				break collect
			}
		case <-decisionTimer:
			fmt.Printf("[%s] decision window elapsed; booking the best option found\n", date)
			break collect
		}
	}

	results := make([]TargetResult, len(ordered))
	for i, t := range ordered {
		results[i] = TargetResult{Label: t.Label}
		if findErrs[i] != nil {
			results[i].Err = findErrs[i]
		}
	}

	booked := false
	for i, t := range ordered {
		if booked {
			results[i].Skipped = true
			results[i].Err = nil
			continue
		}
		if len(available[i]) == 0 {
			if !settled[i] {
				results[i].Skipped = true
				results[i].Err = fmt.Errorf("still searching when a higher priority was booked or the window closed")
			}
			continue
		}

		fmt.Printf("[%s] booking %s (priority %d)\n", date, t.Label, t.Priority)
		workflow := NewResyBookingWorkflow(m.resyClient, t.Details)
		token, err := workflow.Book(available[i])
		if err != nil {
			results[i].Err = err
			fmt.Printf("[%s] %s failed: %v\n", date, t.Label, err)
			continue
		}

		results[i] = TargetResult{Label: t.Label, Booked: true, Token: token}
		booked = true
	}

	if !booked {
		fmt.Printf("[%s] nothing booked\n", date)
	}
	return results, booked
}
