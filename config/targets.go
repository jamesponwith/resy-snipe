package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

// Duration is a time.Duration that unmarshals from a Go duration string such
// as "60s" or "2m", so plan files stay readable.
type Duration time.Duration

func (d *Duration) UnmarshalJSON(b []byte) error {
	var raw string
	if err := json.Unmarshal(b, &raw); err != nil {
		return fmt.Errorf("duration must be a string like \"60s\": %w", err)
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", raw, err)
	}
	if parsed < 0 {
		return fmt.Errorf("duration %q must not be negative", raw)
	}
	*d = Duration(parsed)
	return nil
}

func (d Duration) Duration() time.Duration { return time.Duration(d) }

// Target is one venue+date combination to snipe.
type Target struct {
	Name       string   `json:"name"`
	VenueID    int      `json:"venue_id"`
	Date       string   `json:"date"`
	PartySize  int      `json:"party_size"`
	ResTimes   []string `json:"res_times"`
	TableTypes []string `json:"table_types"`
	// Priority orders booking within a single date. 1 is the most wanted.
	// Targets sharing a date are mutually exclusive because Resy only allows
	// one reservation per night, so the best available priority wins and the
	// rest are skipped. Omit it to fall back to the order in the file.
	Priority int `json:"priority"`
	// OnHit is what watch mode does when a table appears: "book" (default) to
	// reserve it automatically, or "alert" to only notify you.
	OnHit string `json:"on_hit"`
	// Notify also joins Resy's own Notify waitlist for this target.
	Notify bool `json:"notify"`
}

// On-hit policies.
const (
	OnHitBook  = "book"
	OnHitAlert = "alert"
)

// Plan is a batch of targets plus the schedule they share.
//
// Retry and DecisionWindow are pointers so an explicit "0s" in the file is
// distinguishable from the field being absent, and can therefore override a
// non-zero command-line default.
type Plan struct {
	SnipeDate      string    `json:"snipe_date"`
	SnipeTime      string    `json:"snipe_time"`
	Retry          *Duration `json:"retry"`
	DecisionWindow *Duration `json:"decision_window"`
	PollInterval   *Duration `json:"poll_interval"`
	// MaxBookings caps total auto-bookings across the whole watch. Watching
	// many nights would otherwise book one table per night. Defaults to 1;
	// set 0 for unlimited.
	MaxBookings *int     `json:"max_bookings"`
	Targets     []Target `json:"targets"`
}

// MaxBookingsOr returns the booking cap, defaulting to fallback when unset.
func (p *Plan) MaxBookingsOr(fallback int) int {
	if p.MaxBookings == nil {
		return fallback
	}
	return *p.MaxBookings
}

// PollIntervalOr returns the watch-mode poll interval, falling back when the
// plan does not set one.
func (p *Plan) PollIntervalOr(fallback time.Duration) time.Duration {
	if p.PollInterval == nil {
		return fallback
	}
	return p.PollInterval.Duration()
}

// RetryOr returns the plan's retry window, falling back to fallback when the
// plan does not set one.
func (p *Plan) RetryOr(fallback time.Duration) time.Duration {
	if p.Retry == nil {
		return fallback
	}
	return p.Retry.Duration()
}

// DecisionWindowOr returns the plan's decision window, falling back to fallback
// when the plan does not set one.
func (p *Plan) DecisionWindowOr(fallback time.Duration) time.Duration {
	if p.DecisionWindow == nil {
		return fallback
	}
	return p.DecisionWindow.Duration()
}

// Details converts a target into the reservation details the booking workflow
// consumes.
func (t Target) Details() (ReservationDetails, error) {
	resTimeTypes, err := BuildResTimeTypes(t.ResTimes, t.TableTypes)
	if err != nil {
		return ReservationDetails{}, err
	}
	return ReservationDetails{
		Date:         t.Date,
		PartySize:    t.PartySize,
		VenueId:      t.VenueID,
		ResTimeTypes: resTimeTypes,
	}, nil
}

// Label is a human-readable name for logs.
func (t Target) Label() string {
	if t.Name != "" {
		return fmt.Sprintf("%s (%d) %s", t.Name, t.VenueID, t.Date)
	}
	return fmt.Sprintf("venue %d %s", t.VenueID, t.Date)
}

// LoadPlan reads and validates a plan file.
func LoadPlan(path string) (*Plan, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var plan Plan
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&plan); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}

	if err := plan.validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &plan, nil
}

func (p *Plan) validate() error {
	if len(p.Targets) == 0 {
		return fmt.Errorf("plan contains no targets")
	}

	// Priorities only need to be unique within a date, since only same-night
	// targets ever compete with each other.
	seenPriority := make(map[string]map[int]string)

	for i := range p.Targets {
		t := &p.Targets[i]
		if t.VenueID <= 0 {
			return fmt.Errorf("target %d (%s): venue_id must be set and positive", i+1, t.Name)
		}
		if _, err := time.Parse("2006-01-02", t.Date); err != nil {
			return fmt.Errorf("target %d (%s): date %q must be YYYY-MM-DD", i+1, t.Name, t.Date)
		}
		if t.PartySize <= 0 {
			return fmt.Errorf("target %d (%s): party_size must be positive", i+1, t.Name)
		}
		if len(t.ResTimes) == 0 {
			return fmt.Errorf("target %d (%s): res_times must list at least one time", i+1, t.Name)
		}
		for _, resTime := range t.ResTimes {
			if _, err := NormalizeResTime(resTime); err != nil {
				return fmt.Errorf("target %d (%s): invalid res_time %q, use HH:MM or HH:MM:SS", i+1, t.Name, resTime)
			}
		}
		if t.Priority == 0 {
			t.Priority = i + 1
		}
		switch t.OnHit {
		case "":
			t.OnHit = OnHitBook
		case OnHitBook, OnHitAlert:
		default:
			return fmt.Errorf("target %d (%s): on_hit %q must be %q or %q", i+1, t.Name, t.OnHit, OnHitBook, OnHitAlert)
		}

		if seenPriority[t.Date] == nil {
			seenPriority[t.Date] = make(map[int]string)
		}
		if other, clash := seenPriority[t.Date][t.Priority]; clash {
			return fmt.Errorf("targets %q and %q share date %s and priority %d; only one reservation per night is possible, so give them distinct priorities", other, t.Name, t.Date, t.Priority)
		}
		seenPriority[t.Date][t.Priority] = t.Name
	}

	if p.SnipeTime != "" {
		if _, err := time.Parse("15:04", p.SnipeTime); err != nil {
			return fmt.Errorf("snipe_time %q must be HH:MM", p.SnipeTime)
		}
	}
	if p.SnipeDate != "" {
		if _, err := time.Parse("2006-01-02", p.SnipeDate); err != nil {
			return fmt.Errorf("snipe_date %q must be YYYY-MM-DD", p.SnipeDate)
		}
	}
	return nil
}

// NormalizeResTime accepts HH:MM or HH:MM:SS and returns HH:MM:SS, which is the
// format Resy reports slot start times in.
func NormalizeResTime(input string) (string, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return "", fmt.Errorf("empty reservation time")
	}
	if parsed, err := time.Parse("15:04:05", input); err == nil {
		return parsed.Format("15:04:05"), nil
	}
	parsed, err := time.Parse("15:04", input)
	if err != nil {
		return "", err
	}
	return parsed.Format("15:04:05"), nil
}

// BuildResTimeTypes expands the requested times against the requested table
// types. Each time also gets a nil ("any table") entry so a venue that offers
// an unexpected table type is still bookable.
func BuildResTimeTypes(times []string, tableTypes []string) ([]ReservationTimeType, error) {
	if len(times) == 0 {
		return nil, fmt.Errorf("at least one reservation time is required")
	}
	var resTimeTypes []ReservationTimeType
	for _, timeValue := range times {
		normalized, err := NormalizeResTime(timeValue)
		if err != nil {
			return nil, fmt.Errorf("invalid reservation time %q", timeValue)
		}
		for _, tableType := range tableTypes {
			tableType = strings.TrimSpace(tableType)
			if tableType == "" {
				continue
			}
			table := tableType
			resTimeTypes = append(resTimeTypes, NewReservationTimeType(normalized, &table))
		}
		resTimeTypes = append(resTimeTypes, NewReservationTimeType(normalized, nil))
	}
	return resTimeTypes, nil
}
