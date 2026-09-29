package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"resy-snipe/config"
	"resy-snipe/resy"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type cliOptions struct {
	interactive    bool
	resDate        string
	partySize      int
	venueID        int
	resTimes       string
	tableTypes     string
	snipeDate      string
	snipeTime      string
	retryFor       time.Duration
	decisionWindow time.Duration
	targetsFile    string
	findVenue      string
	validate       bool
	dryRun         bool
	watch          bool
	notify         bool
	pollInterval   time.Duration
	alertLog       string
	maxBookings    int
}

type venueOption struct {
	Name string
	ID   int
}

var venueOptions = []venueOption{
	{Name: "Dead Rabbit", ID: config.DeadRabbit},
	{Name: "Rubirosa", ID: config.Rubirosa},
	{Name: "Red Pearl", ID: config.RedPearl},
	{Name: "Rafs", ID: config.Rafs},
	{Name: "Carbone", ID: config.Carbone},
	{Name: "Don Angie", ID: config.DonAngie},
	{Name: "San Sabino", ID: config.SanSabino},
	{Name: "Gertrudes", ID: config.Gertrudes},
	{Name: "Au Cheval", ID: config.AuCheval},
	{Name: "HOWOO", ID: config.HOWOO},
}

func main() {
	opts := parseFlags()

	// Validation is offline on purpose, so plan files can be checked long
	// before the drop and without credentials.
	if opts.validate {
		if opts.targetsFile == "" {
			exitWithError(fmt.Errorf("-validate requires -targets FILE"))
		}
		plan, err := config.LoadPlan(opts.targetsFile)
		if err != nil {
			exitWithError(err)
		}
		printPlan(plan, opts)
		fmt.Printf("\n%s is valid.\n", opts.targetsFile)
		return
	}

	if err := requireCredentials(); err != nil {
		exitWithError(err)
	}

	resyApi := resy.NewResyAPI(config.ResyKeyss)
	resyClient := resy.NewResyClient(resyApi)

	if opts.findVenue != "" {
		if err := runVenueSearch(resyClient, opts.findVenue); err != nil {
			exitWithError(err)
		}
		return
	}

	if opts.targetsFile != "" {
		if err := runPlan(resyClient, opts); err != nil {
			exitWithError(err)
		}
		return
	}

	resDate := config.ReservationDetailss.Date
	partySize := config.ReservationDetailss.PartySize
	venueID := config.ReservationDetailss.VenueId
	defaultResTimes, defaultTableTypes := resTimeDefaults(config.ReservationDetailss.ResTimeTypes)
	resTimesInput := strings.Join(defaultResTimes, ",")
	tableTypesInput := strings.Join(defaultTableTypes, ",")

	snipeDate := resDate
	snipeTimeInput := fmt.Sprintf("%02d:%02d", config.SnipeTimee.Hours, config.SnipeTimee.Minutes)
	retryFor := opts.retryFor

	if opts.resDate != "" {
		resDate = opts.resDate
	}
	if opts.partySize >= 0 {
		partySize = opts.partySize
	}
	if opts.venueID >= 0 {
		venueID = opts.venueID
	}
	if opts.resTimes != "" {
		resTimesInput = opts.resTimes
	}
	if opts.tableTypes != "" {
		tableTypesInput = opts.tableTypes
	}
	if opts.snipeDate != "" {
		snipeDate = opts.snipeDate
	} else {
		snipeDate = resDate
	}
	if opts.snipeTime != "" {
		snipeTimeInput = opts.snipeTime
	}

	if opts.interactive {
		reader := bufio.NewReader(os.Stdin)
		var err error

		resDate, err = promptDate(reader, "Reservation date (YYYY-MM-DD)", resDate)
		if err != nil {
			exitWithError(err)
		}
		partySize, err = promptInt(reader, "Party size", partySize)
		if err != nil {
			exitWithError(err)
		}
		venueID, err = promptVenue(reader, venueID)
		if err != nil {
			exitWithError(err)
		}

		defaultResTimesInput := resTimesInput
		resTimesInput, err = promptResTimes(reader, "Reservation times (comma-separated, HH:MM)", defaultResTimesInput)
		if err != nil {
			exitWithError(err)
		}
		tableTypesInput, err = promptTableTypes(reader, "Table types (comma-separated, optional, or 'none')", tableTypesInput)
		if err != nil {
			exitWithError(err)
		}

		if opts.snipeDate == "" {
			snipeDate = resDate
		}
		snipeDate, err = promptDate(reader, "Snipe date (YYYY-MM-DD)", snipeDate)
		if err != nil {
			exitWithError(err)
		}
		snipeTimeInput, err = promptClockTime(reader, "Snipe time (HH:MM)", snipeTimeInput)
		if err != nil {
			exitWithError(err)
		}
		retryFor, err = promptDuration(reader, "Retry availability search for", retryFor)
		if err != nil {
			exitWithError(err)
		}
	}

	resTimes := parseCommaList(resTimesInput)
	tableTypes := parseTableTypesInput(tableTypesInput)
	resTimeTypes, err := config.BuildResTimeTypes(resTimes, tableTypes)
	if err != nil {
		exitWithError(err)
	}

	scheduledTime, err := buildScheduledTime(snipeDate, snipeTimeInput)
	if err != nil {
		exitWithError(err)
	}

	duration := scheduledTime.Sub(time.Now())
	snipeTime := config.SnipeTime{Hours: scheduledTime.Hour(), Minutes: scheduledTime.Minute()}
	reservationDetails := config.ReservationDetails{
		Date:         resDate,
		PartySize:    partySize,
		VenueId:      venueID,
		ResTimeTypes: resTimeTypes,
	}

	if opts.dryRun {
		label := venueNameByID(venueID)
		if label == "" {
			label = fmt.Sprintf("venue %d", venueID)
		}
		target := resy.Target{
			Label:    fmt.Sprintf("%s (%d) %s", label, venueID, resDate),
			Priority: 1,
			Details:  reservationDetails,
		}
		if err := reportDryRun(resyClient, []resy.Target{target}); err != nil {
			exitWithError(err)
		}
		return
	}

	resyBookingWorkflow := resy.NewResyBookingWorkflow(*resyClient, reservationDetails)

	fmt.Printf("Sleeping for %v until %v:%v local time.\n", duration, snipeTime.Hours, snipeTime.Minutes)

	// Wait until the scheduled time
	time.Sleep(duration)

	resyToken, err := resyBookingWorkflow.Run(retryFor)
	if err != nil {
		exitWithError(err)
	}

	// Execute the program
	fmt.Println("Reservation token:", resyToken)
	fmt.Println("Program executed at", scheduledTime)
}

func requireCredentials() error {
	var missing []string
	if strings.TrimSpace(config.ResyKeyss.ApiKey) == "" {
		missing = append(missing, "RESY_API_KEY")
	}
	if strings.TrimSpace(config.ResyKeyss.AuthToken) == "" {
		missing = append(missing, "RESY_AUTH_TOKEN")
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing credentials: %s\nSet them first, for example:\n  cp .env.example .env   # fill in your values\n  set -a && source .env && set +a", strings.Join(missing, ", "))
	}
	return config.CheckTokenValidAt(config.ResyKeyss.AuthToken, time.Now())
}

func runVenueSearch(resyClient *resy.ResyClient, query string) error {
	hits, raw, err := resyClient.SearchVenues(query)
	if err != nil {
		if raw != "" {
			fmt.Println("Raw response:", raw)
		}
		return err
	}
	if len(hits) == 0 {
		fmt.Printf("No venues matched %q.\n", query)
		fmt.Println("Raw response:", raw)
		return nil
	}

	fmt.Printf("Venues matching %q:\n", query)
	fmt.Printf("%-7s %s\n", "ID", "NAME")
	for _, hit := range hits {
		fmt.Println(hit)
	}
	return nil
}

// reportDryRun prints what each venue is currently offering. Nothing is booked,
// so this is the safe way to check credentials, venue IDs and time choices.
func reportDryRun(resyClient *resy.ResyClient, targets []resy.Target) error {
	fmt.Println("DRY RUN — reporting availability only, nothing will be booked.")

	reports := resy.NewMultiTargetWorkflow(*resyClient, targets).DryRun()

	failures := 0
	for _, report := range reports {
		fmt.Printf("\n%s\n", report.Label)
		if report.Err != nil {
			failures++
			fmt.Printf("  error: %v\n", report.Err)
			continue
		}
		if len(report.Offered) == 0 {
			fmt.Println("  venue is offering nothing for this date/party size")
			continue
		}

		startTimes := make([]string, 0, len(report.Offered))
		for start := range report.Offered {
			startTimes = append(startTimes, start)
		}
		sort.Strings(startTimes)

		fmt.Printf("  %d slot(s) offered across %d start time(s):\n", report.TotalCfgs, len(startTimes))
		for _, start := range startTimes {
			tableTypes := make([]string, 0, len(report.Offered[start]))
			for tableType := range report.Offered[start] {
				tableTypes = append(tableTypes, tableType)
			}
			sort.Strings(tableTypes)
			fmt.Printf("    %s  %s\n", start, strings.Join(tableTypes, ", "))
		}
		fmt.Printf("  matching your requested times: %d\n", report.Matched)
		if report.Matched == 0 {
			fmt.Println("  (a real run would report \"No Hits\" and keep retrying)")
		}
	}

	if failures > 0 {
		return fmt.Errorf("%d target(s) failed to report availability", failures)
	}
	return nil
}

// buildAlerter wires up the terminal, log file and email sinks.
func buildAlerter(opts cliOptions) (*resy.Alerter, error) {
	email, err := resy.EmailConfigFromEnv()
	if err != nil {
		return nil, err
	}

	logPath := opts.alertLog
	if logPath != "" {
		if dir := filepath.Dir(logPath); dir != "." {
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return nil, fmt.Errorf("could not create alert log directory: %w", err)
			}
		}
	}

	if email != nil {
		fmt.Printf("Email alerts: %s\n", strings.Join(email.To, ", "))
	} else {
		fmt.Println("Email alerts: off (set ALERT_EMAIL_TO, SMTP_USER, SMTP_PASS to enable)")
	}
	if logPath != "" {
		fmt.Printf("Alert log:    %s\n", logPath)
	}

	return resy.NewAlerter(logPath, email, true), nil
}

// runWatch polls until interrupted, applying each target's on_hit policy.
func runWatch(resyClient *resy.ResyClient, targets []resy.Target, interval time.Duration, maxBookings int, opts cliOptions) error {
	if interval < 30*time.Second {
		fmt.Printf("WARNING: a %s poll interval across %d target(s) is aggressive and risks 429 rate limiting.\n", interval, len(targets))
	}

	alerter, err := buildAlerter(opts)
	if err != nil {
		return err
	}

	booking, alerting := 0, 0
	for _, t := range targets {
		if t.OnHit == resy.OnHitAlert {
			alerting++
		} else {
			booking++
		}
	}
	fmt.Printf("Watching %d target(s) every %s — %d will auto-book, %d alert only.\n", len(targets), interval, booking, alerting)
	if maxBookings > 0 {
		fmt.Printf("Auto-booking capped at %d reservation(s) total.\n", maxBookings)
	} else if booking > 0 {
		fmt.Println("WARNING: max_bookings is 0 (unlimited) — this can book one table per night watched.")
	}
	fmt.Println("Press Ctrl-C to stop.")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	watcher := resy.NewWatcher(*resyClient, targets, alerter, interval)
	watcher.MaxBookings = maxBookings
	booked, err := watcher.Watch(ctx)

	fmt.Printf("\nStopped after booking %d reservation(s).\n", booked)
	if err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

// joinNotify subscribes to Resy's own Notify waitlist for opted-in targets.
func joinNotify(resyClient *resy.ResyClient, plan *config.Plan, targets []resy.Target) error {
	var wanted []resy.Target
	for _, t := range targets {
		if t.Notify {
			wanted = append(wanted, t)
		}
	}
	if len(wanted) == 0 {
		return fmt.Errorf("no targets have \"notify\": true")
	}

	fmt.Printf("Joining Resy Notify for %d target(s).\n", len(wanted))
	fmt.Println("NOTE: the Notify request shape is inferred from Resy's widget and is unverified.")
	fmt.Println("      Raw responses are printed below so a rejection can be diagnosed.")

	failures := 0
	for _, t := range wanted {
		start, end, err := resy.NotifyWindow(t.Details.ResTimeTypes)
		if err != nil {
			failures++
			fmt.Printf("\n%s\n  error: %v\n", t.Label, err)
			continue
		}

		raw, err := resyClient.JoinNotify(resy.NotifyRequest{
			VenueID:   t.Details.VenueId,
			Date:      t.Details.Date,
			PartySize: t.Details.PartySize,
			StartTime: start,
			EndTime:   end,
		})
		fmt.Printf("\n%s  (%s-%s)\n", t.Label, start, end)
		if err != nil {
			failures++
			fmt.Printf("  FAILED: %v\n", err)
			continue
		}
		fmt.Printf("  joined. raw: %s\n", raw)
	}

	if failures > 0 {
		return fmt.Errorf("%d of %d notify request(s) failed", failures, len(wanted))
	}
	return nil
}

// printPlan shows what will be attempted, grouped by night, so the priority
// order is obvious before anything runs.
func printPlan(plan *config.Plan, opts cliOptions) {
	retryFor := plan.RetryOr(opts.retryFor)
	decisionWindow := plan.DecisionWindowOr(opts.decisionWindow)

	byDate := make(map[string][]config.Target)
	var dates []string
	for _, t := range plan.Targets {
		if _, seen := byDate[t.Date]; !seen {
			dates = append(dates, t.Date)
		}
		byDate[t.Date] = append(byDate[t.Date], t)
	}
	sort.Strings(dates)

	fmt.Printf("Plan: %d target(s) across %d night(s), retry %s, decision window %s\n",
		len(plan.Targets), len(dates), retryFor, decisionWindow)
	if plan.SnipeDate != "" && plan.SnipeTime != "" {
		fmt.Printf("Snipe at: %s %s local\n", plan.SnipeDate, plan.SnipeTime)
	}

	for _, date := range dates {
		targets := byDate[date]
		sort.SliceStable(targets, func(i, j int) bool { return targets[i].Priority < targets[j].Priority })
		fmt.Printf("\n  %s  (one reservation max — first available in priority order wins)\n", date)
		for _, t := range targets {
			tableTypes := "any table"
			if len(t.TableTypes) > 0 {
				tableTypes = strings.Join(t.TableTypes, "/")
			}
			action := "AUTO-BOOKS"
			if t.OnHit == config.OnHitAlert {
				action = "alert only"
			}
			notify := ""
			if t.Notify {
				notify = " +notify"
			}
			fmt.Printf("    %d. %-22s venue %-7d party %d  %-12s%s  [%s]\n       %s\n",
				t.Priority, t.Name, t.VenueID, t.PartySize, action, notify, tableTypes,
				strings.Join(t.ResTimes, ", "))
		}
	}
}

func runPlan(resyClient *resy.ResyClient, opts cliOptions) error {
	plan, err := config.LoadPlan(opts.targetsFile)
	if err != nil {
		return err
	}

	retryFor := plan.RetryOr(opts.retryFor)
	decisionWindow := plan.DecisionWindowOr(opts.decisionWindow)

	var targets []resy.Target
	for _, t := range plan.Targets {
		details, err := t.Details()
		if err != nil {
			return fmt.Errorf("%s: %w", t.Label(), err)
		}
		targets = append(targets, resy.Target{
			Label:    t.Label(),
			Priority: t.Priority,
			Details:  details,
			OnHit:    t.OnHit,
			Notify:   t.Notify,
		})
	}

	snipeDate := plan.SnipeDate
	if opts.snipeDate != "" {
		snipeDate = opts.snipeDate
	}
	snipeTime := plan.SnipeTime
	if opts.snipeTime != "" {
		snipeTime = opts.snipeTime
	}

	printPlan(plan, opts)

	if opts.dryRun {
		fmt.Println()
		return reportDryRun(resyClient, targets)
	}

	if opts.notify {
		fmt.Println()
		return joinNotify(resyClient, plan, targets)
	}

	if opts.watch {
		fmt.Println()
		return runWatch(resyClient, targets, plan.PollIntervalOr(opts.pollInterval), plan.MaxBookingsOr(opts.maxBookings), opts)
	}

	if snipeDate != "" && snipeTime != "" {
		scheduledTime, err := buildScheduledTime(snipeDate, snipeTime)
		if err != nil {
			return err
		}
		// Catch a token that works now but will have lapsed by the drop,
		// rather than discovering it at midnight.
		if err := config.CheckTokenValidAt(config.ResyKeyss.AuthToken, scheduledTime); err != nil {
			return err
		}

		duration := time.Until(scheduledTime)
		fmt.Printf("Sleeping for %v until %v local time.\n", duration, scheduledTime)
		time.Sleep(duration)
	} else {
		fmt.Println("No snipe_date/snipe_time set; running immediately.")
	}

	results := resy.NewMultiTargetWorkflow(*resyClient, targets).Run(retryFor, decisionWindow)

	fmt.Println("\n=== Results ===")
	booked := 0
	for _, result := range results {
		switch {
		case result.Booked:
			booked++
			fmt.Printf("  BOOKED   %s  token=%s\n", result.Label, result.Token)
		case result.Skipped:
			fmt.Printf("  skipped  %s\n", result.Label)
		default:
			fmt.Printf("  failed   %s: %v\n", result.Label, result.Err)
		}
	}
	if booked == 0 {
		return fmt.Errorf("no reservations booked")
	}
	fmt.Printf("Booked %d reservation(s).\n", booked)
	return nil
}

func parseFlags() cliOptions {
	var opts cliOptions
	flag.BoolVar(&opts.interactive, "interactive", false, "Prompt for settings interactively.")
	flag.StringVar(&opts.resDate, "date", "", "Reservation date (YYYY-MM-DD).")
	flag.IntVar(&opts.partySize, "party-size", -1, "Party size.")
	flag.IntVar(&opts.venueID, "venue-id", -1, "Venue ID.")
	flag.StringVar(&opts.resTimes, "res-times", "", "Reservation times (comma-separated, HH:MM or HH:MM:SS).")
	flag.StringVar(&opts.tableTypes, "table-types", "", "Table types (comma-separated). Use 'none' for any.")
	flag.StringVar(&opts.snipeDate, "snipe-date", "", "Snipe date (YYYY-MM-DD). Defaults to reservation date.")
	flag.StringVar(&opts.snipeTime, "snipe-time", "", "Snipe time (HH:MM). Defaults to midnight.")
	flag.DurationVar(&opts.retryFor, "retry", 60*time.Second, "How long to keep retrying the availability search (e.g. 30s, 2m).")
	flag.DurationVar(&opts.decisionWindow, "decision-window", 2*time.Second, "With -targets, how long to wait for a higher-priority venue after some option becomes available.")
	flag.StringVar(&opts.targetsFile, "targets", "", "Path to a JSON plan file describing multiple venue/date targets.")
	flag.StringVar(&opts.findVenue, "find-venue", "", "Look up venue IDs by name and exit.")
	flag.BoolVar(&opts.validate, "validate", false, "Check a -targets plan file and exit without contacting Resy.")
	flag.BoolVar(&opts.dryRun, "dry-run", false, "Report current availability and exit. Books nothing, ignores the snipe schedule.")
	flag.BoolVar(&opts.watch, "watch", false, "Poll targets continuously and act on each target's on_hit policy. Ctrl-C to stop.")
	flag.BoolVar(&opts.notify, "notify", false, "Join Resy's Notify waitlist for targets with \"notify\": true, then exit.")
	flag.DurationVar(&opts.pollInterval, "poll-interval", 60*time.Second, "How often -watch polls each target. Below ~30s risks 429 rate limiting.")
	flag.StringVar(&opts.alertLog, "alert-log", "logs/alerts.jsonl", "Append-only JSON log of alerts and bookings.")
	flag.IntVar(&opts.maxBookings, "max-bookings", 1, "Cap on total auto-bookings during -watch. 0 means unlimited.")
	flag.Parse()
	return opts
}

func resTimeDefaults(resTimeTypes []config.ReservationTimeType) ([]string, []string) {
	var times []string
	var tableTypes []string
	timeSeen := make(map[string]bool)
	tableSeen := make(map[string]bool)
	for _, r := range resTimeTypes {
		if !timeSeen[r.ReservationTime] {
			timeSeen[r.ReservationTime] = true
			times = append(times, r.ReservationTime)
		}
		if r.TableType != nil && !tableSeen[*r.TableType] {
			tableSeen[*r.TableType] = true
			tableTypes = append(tableTypes, *r.TableType)
		}
	}
	return times, tableTypes
}

func promptDate(reader *bufio.Reader, label string, defaultVal string) (string, error) {
	for {
		raw, err := promptRaw(reader, label, defaultVal)
		if err != nil {
			return "", err
		}
		if raw == "" {
			raw = defaultVal
		}
		if _, err := time.ParseInLocation("2006-01-02", raw, time.Local); err != nil {
			fmt.Println("Invalid date. Use YYYY-MM-DD.")
			continue
		}
		return raw, nil
	}
}

func promptClockTime(reader *bufio.Reader, label string, defaultVal string) (string, error) {
	for {
		raw, err := promptRaw(reader, label, defaultVal)
		if err != nil {
			return "", err
		}
		if raw == "" {
			raw = defaultVal
		}
		if _, err := time.Parse("15:04", raw); err != nil {
			fmt.Println("Invalid time. Use HH:MM (24h).")
			continue
		}
		return raw, nil
	}
}

func promptDuration(reader *bufio.Reader, label string, defaultVal time.Duration) (time.Duration, error) {
	for {
		raw, err := promptRaw(reader, label, defaultVal.String())
		if err != nil {
			return 0, err
		}
		if raw == "" {
			return defaultVal, nil
		}
		parsed, err := time.ParseDuration(raw)
		if err != nil || parsed < 0 {
			fmt.Println("Invalid duration. Use e.g. 30s, 2m.")
			continue
		}
		return parsed, nil
	}
}

func promptInt(reader *bufio.Reader, label string, defaultVal int) (int, error) {
	for {
		raw, err := promptRaw(reader, label, strconv.Itoa(defaultVal))
		if err != nil {
			return 0, err
		}
		if raw == "" {
			return defaultVal, nil
		}
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			fmt.Println("Please enter a number.")
			continue
		}
		return parsed, nil
	}
}

func promptVenue(reader *bufio.Reader, defaultVal int) (int, error) {
	fmt.Println("Venues (or enter custom venue ID):")
	for i, venue := range venueOptions {
		fmt.Printf("  %d) %s (%d)\n", i+1, venue.Name, venue.ID)
	}
	defaultName := venueNameByID(defaultVal)
	if defaultName == "" {
		defaultName = "Custom"
	}
	fmt.Printf("Default venue: %s (%d)\n", defaultName, defaultVal)
	for {
		raw, err := promptRaw(reader, "Venue (list number, venue id, or name)", strconv.Itoa(defaultVal))
		if err != nil {
			return 0, err
		}
		if raw == "" {
			return defaultVal, nil
		}
		if value, err := strconv.Atoi(raw); err == nil {
			if value >= 1 && value <= len(venueOptions) {
				return venueOptions[value-1].ID, nil
			}
			return value, nil
		}
		if venueID, ok := venueIDByName(raw); ok {
			return venueID, nil
		}
		fmt.Println("Invalid venue. Enter a list number, venue id, or name.")
	}
}

func promptResTimes(reader *bufio.Reader, label string, defaultVal string) (string, error) {
	for {
		raw, err := promptRaw(reader, label, defaultVal)
		if err != nil {
			return "", err
		}
		if raw == "" {
			raw = defaultVal
		}
		times := parseCommaList(raw)
		if len(times) == 0 {
			fmt.Println("Provide at least one reservation time.")
			continue
		}
		valid := true
		for _, t := range times {
			if _, err := config.NormalizeResTime(t); err != nil {
				fmt.Printf("Invalid time %q. Use HH:MM or HH:MM:SS.\n", t)
				valid = false
				break
			}
		}
		if !valid {
			continue
		}
		return raw, nil
	}
}

func promptTableTypes(reader *bufio.Reader, label string, defaultVal string) (string, error) {
	raw, err := promptRaw(reader, label, defaultVal)
	if err != nil {
		return "", err
	}
	if raw == "" {
		raw = defaultVal
	}
	return raw, nil
}

func promptRaw(reader *bufio.Reader, label string, defaultVal string) (string, error) {
	fmt.Printf("%s [%s]: ", label, defaultVal)
	raw, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(raw), nil
}

func venueNameByID(id int) string {
	for _, venue := range venueOptions {
		if venue.ID == id {
			return venue.Name
		}
	}
	return ""
}

func venueIDByName(name string) (int, bool) {
	name = strings.TrimSpace(strings.ToLower(name))
	for _, venue := range venueOptions {
		if strings.ToLower(venue.Name) == name {
			return venue.ID, true
		}
	}
	return 0, false
}

func parseCommaList(input string) []string {
	parts := strings.Split(input, ",")
	var values []string
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		values = append(values, part)
	}
	return values
}

func parseTableTypesInput(input string) []string {
	values := parseCommaList(input)
	if len(values) == 1 && (strings.EqualFold(values[0], "none") || strings.EqualFold(values[0], "nil")) {
		return nil
	}
	var cleaned []string
	for _, value := range values {
		if strings.EqualFold(value, "none") || strings.EqualFold(value, "nil") {
			continue
		}
		cleaned = append(cleaned, value)
	}
	return cleaned
}

func buildScheduledTime(dateInput string, timeInput string) (time.Time, error) {
	datePart, err := time.ParseInLocation("2006-01-02", dateInput, time.Local)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid snipe date %q", dateInput)
	}
	timePart, err := time.Parse("15:04", timeInput)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid snipe time %q", timeInput)
	}
	return time.Date(datePart.Year(), datePart.Month(), datePart.Day(), timePart.Hour(), timePart.Minute(), 0, 0, time.Local), nil
}

func exitWithError(err error) {
	fmt.Println("Error:", err)
	os.Exit(1)
}
