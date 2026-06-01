// Package main: `resy-snipe watch -config <path>` — a single-process
// "mini-daemon" that runs multiple NotifyMeRelease snipes against one
// shared IMAP-backed alert source.
//
// Why this subcommand and not the full `serve` daemon: the daemon mode
// in serve.go owns HTTP, sealed secrets, banners, signal handling, and
// is on track to grow an HTTP API for quest management. The watch
// subcommand is a tighter shape — one config file declares the watches,
// the process spawns one snipe goroutine per watch, all share an email
// Source for the inbox poll, and SIGINT/SIGTERM drains them cleanly.
// No HTTP, no auth tokens, no admin UI.
//
// Lifecycle:
//
//  1. Load + validate config file.
//  2. Open the shared store + Resy client.
//  3. Construct email.Source, call Start synchronously so bad creds
//     fail at boot.
//  4. For each watch: load that user's Resy session, register the
//     venue's display name with the Source, submit a domain.Intent
//     with NotifyMeRelease, spawn a goroutine running the engine's
//     Run + RunBookingRace cycle.
//  5. Wait for ctx cancel (signal) or all watches to terminate, then
//     close the Source and the store.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/BurntSushi/toml"

	"resy-snipe/internal/alerts/email"
	"resy-snipe/internal/clock"
	"resy-snipe/internal/domain"
	"resy-snipe/internal/engine"
	"resy-snipe/internal/notify"
)

// WatchConfig is the on-disk shape of the watch-mode config file.
// All durations are TOML strings parsed by parseWatchConfig.
type WatchConfig struct {
	IMAPAddr        string `toml:"imap_addr"`
	IMAPUser        string `toml:"imap_user"`
	IMAPPassEnv     string `toml:"imap_pass_env"`
	Mailbox         string `toml:"mailbox"`
	MinPollInterval string `toml:"min_poll_interval"`

	// DryRun arms every watch in this process in arm-only mode: the
	// booking race runs Find + PrepareSlot (proving the token, venue,
	// and book_token mint) but never POSTs /3/book. Use it to validate
	// the alert → booking path without committing a real reservation.
	DryRun bool `toml:"dry_run"`

	Watches []WatchEntry `toml:"watches"`
}

// WatchEntry is one venue/date/party tuple the watch process snipes
// via NotifyMeRelease. Multiple entries for the same user share one
// Resy session and one IMAP connection.
type WatchEntry struct {
	VenueID      int      `toml:"venue_id"`
	VenueName    string   `toml:"venue_name"`
	Date         string   `toml:"date"` // YYYY-MM-DD
	PartySize    int      `toml:"party_size"`
	ResTimes     []string `toml:"res_times"`     // ["19:00", "19:30", ...]
	TableTypes   []string `toml:"table_types"`   // optional
	RetryWindow  string   `toml:"retry_window"`  // duration, e.g. "168h"
	PollInterval string   `toml:"poll_interval"` // duration; 0 falls back to engine PollFloor
	User         string   `toml:"user"`          // Resy account email
}

// parseWatchConfig reads the TOML file at path, decodes it, and
// validates every field. Returns a structured error listing every
// violation when validation fails (one shot, not first-error).
func parseWatchConfig(path string) (WatchConfig, error) {
	f, err := os.Open(path) // #nosec G304 -- operator-supplied path.
	if err != nil {
		return WatchConfig{}, fmt.Errorf("watch: open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	var cfg WatchConfig
	meta, err := toml.NewDecoder(f).Decode(&cfg)
	if err != nil {
		return WatchConfig{}, fmt.Errorf("watch: parse %s: %w", path, err)
	}
	if u := meta.Undecoded(); len(u) > 0 {
		keys := make([]string, 0, len(u))
		for _, k := range u {
			keys = append(keys, k.String())
		}
		return WatchConfig{}, fmt.Errorf("watch: unknown keys in %s: %s",
			path, strings.Join(keys, ", "))
	}
	if cfg.Mailbox == "" {
		cfg.Mailbox = "INBOX"
	}
	if cfg.IMAPPassEnv == "" {
		cfg.IMAPPassEnv = "RESY_SNIPE_IMAP_PASS"
	}

	if err := validateWatchConfig(&cfg); err != nil {
		return WatchConfig{}, err
	}
	return cfg, nil
}

// validateWatchConfig checks every required field on cfg and reports
// every violation at once. The caller surfaces the aggregated error to
// the operator.
func validateWatchConfig(cfg *WatchConfig) error {
	var violations []string
	add := func(format string, args ...any) {
		violations = append(violations, fmt.Sprintf(format, args...))
	}

	if strings.TrimSpace(cfg.IMAPAddr) == "" {
		add("imap_addr is required (e.g. \"imap.gmail.com:993\")")
	}
	if strings.TrimSpace(cfg.IMAPUser) == "" {
		add("imap_user is required")
	}
	if cfg.MinPollInterval != "" {
		if _, err := time.ParseDuration(cfg.MinPollInterval); err != nil {
			add("min_poll_interval %q: %v", cfg.MinPollInterval, err)
		}
	}

	if len(cfg.Watches) == 0 {
		add("at least one [[watches]] entry is required")
	}

	for i, w := range cfg.Watches {
		p := fmt.Sprintf("watches[%d]", i)
		if w.VenueID <= 0 {
			add("%s.venue_id %d: must be > 0", p, w.VenueID)
		}
		if strings.TrimSpace(w.VenueName) == "" {
			add("%s.venue_name is required (Resy display name)", p)
		}
		if strings.TrimSpace(w.Date) == "" {
			add("%s.date is required (YYYY-MM-DD)", p)
		} else if _, err := time.Parse("2006-01-02", w.Date); err != nil {
			add("%s.date %q: %v", p, w.Date, err)
		}
		if w.PartySize <= 0 {
			add("%s.party_size %d: must be > 0", p, w.PartySize)
		}
		if len(w.ResTimes) == 0 {
			add("%s.res_times is required (at least one HH:MM)", p)
		} else {
			for _, t := range w.ResTimes {
				if _, perr := domain.ParseWallTime(t); perr != nil {
					add("%s.res_times entry %q: %v", p, t, perr)
				}
			}
		}
		if strings.TrimSpace(w.RetryWindow) == "" {
			add("%s.retry_window is required (e.g. \"168h\")", p)
		} else if _, err := time.ParseDuration(w.RetryWindow); err != nil {
			add("%s.retry_window %q: %v", p, w.RetryWindow, err)
		}
		if w.PollInterval != "" {
			if _, err := time.ParseDuration(w.PollInterval); err != nil {
				add("%s.poll_interval %q: %v", p, w.PollInterval, err)
			}
		}
		if strings.TrimSpace(w.User) == "" {
			add("%s.user is required (Resy account email)", p)
		}
	}

	if len(violations) > 0 {
		return fmt.Errorf("watch: config invalid:\n  - %s", strings.Join(violations, "\n  - "))
	}
	return nil
}

// runWatchCmd is the entry point for `resy-snipe watch -config <path>`.
// Returns nil on a signal-driven clean shutdown; non-nil on a boot
// failure or an unrecoverable runtime error.
func runWatchCmd(ctx context.Context, args []string, _ io.Reader, out io.Writer, clk clock.Clock) error {
	fs := flag.NewFlagSet("watch", flag.ContinueOnError)
	fs.SetOutput(out)
	var configPath string
	fs.StringVar(&configPath, "config", "", "path to the watch config TOML")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(out, "Usage: resy-snipe watch -config <path>")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(configPath) == "" {
		return errors.New("watch: -config is required")
	}

	cfg, err := parseWatchConfig(configPath)
	if err != nil {
		return err
	}

	pass := os.Getenv(cfg.IMAPPassEnv)
	if pass == "" {
		return fmt.Errorf("watch: IMAP password env var %q is empty", cfg.IMAPPassEnv)
	}

	logger := slog.New(slog.NewJSONHandler(out, &slog.HandlerOptions{Level: slog.LevelInfo}))

	rclient, sqlStore, cleanupBackend, err := openSnipeBackend(ctx, logger, clk)
	if err != nil {
		return fmt.Errorf("watch: open backend: %w", err)
	}
	defer func() { _ = cleanupBackend() }()

	srcMinPoll := time.Duration(0)
	if cfg.MinPollInterval != "" {
		srcMinPoll, _ = time.ParseDuration(cfg.MinPollInterval) // validated above
	}
	src, err := email.New(email.Config{
		IMAPAddr:        cfg.IMAPAddr,
		Username:        cfg.IMAPUser,
		Password:        pass,
		Mailbox:         cfg.Mailbox,
		MinPollInterval: srcMinPoll,
	})
	if err != nil {
		return fmt.Errorf("watch: email source: %w", err)
	}

	runCtx, cancel := installWatchSignalCancel(ctx)
	defer cancel()

	if err := src.Start(runCtx, logger); err != nil {
		return fmt.Errorf("watch: email source start: %w", err)
	}
	defer func() { _ = src.Close() }()

	logger.Info("watch: email source attached",
		slog.String("imap_addr", cfg.IMAPAddr),
		slog.String("imap_user", cfg.IMAPUser),
		slog.Int("watch_count", len(cfg.Watches)),
	)

	provider := &providerAdapter{Client: rclient}
	engOpts := []engine.Option{
		engine.WithProvider(provider),
		engine.WithAlertSource(src),
	}
	if cfg.DryRun {
		engOpts = append(engOpts, engine.WithBookingDryRun())
		logger.Warn("watch: DRY-RUN mode — booking race will arm but never POST /3/book")
	}
	eng := engine.New(sqlStore, clk, logger, engOpts...)
	notifier := newCLINotifier(out, clk)
	defer func() { _ = notifier.Close() }()

	clientAdapter := &clientAdapter{inner: rclient}
	dedup := newDedupTracker()

	var wg sync.WaitGroup
	for _, w := range cfg.Watches {
		w := w // capture by value
		intent, sess, err := prepareWatchIntent(runCtx, w, clientAdapter, clk.Now(), logger)
		if err != nil {
			logger.Error("watch: skipping invalid entry",
				slog.String("venue_name", w.VenueName),
				slog.String("user", w.User),
				slog.String("err", err.Error()),
			)
			continue
		}
		src.RegisterVenueName(w.VenueName, intent.Venue)

		wg.Add(1)
		go func() {
			defer wg.Done()
			driveOneWatch(runCtx, eng, sess, intent, w, notifier, dedup, logger)
		}()
	}

	// Block until ctx canceled OR all watches finish (e.g., all booked
	// or all expired). The signal handler in installWatchSignalCancel
	// cancels runCtx on SIGINT/SIGTERM.
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		logger.Info("watch: all watches terminated")
	case <-runCtx.Done():
		logger.Info("watch: signal received, draining")
		wg.Wait()
	}
	return nil
}

// prepareWatchIntent loads the user's persisted Resy session and
// builds a domain.Intent with NotifyMeRelease for the watch entry.
// The session is required even pre-Awaiting because the booking race
// will need it once the alert fires.
func prepareWatchIntent(
	ctx context.Context,
	w WatchEntry,
	client *clientAdapter,
	now time.Time,
	logger *slog.Logger,
) (domain.Intent, *resySessionWrapper, error) {
	sess, err := loadSessionForSnipe(ctx, client, domain.UserID(w.User), logger)
	if err != nil {
		return domain.Intent{}, nil, fmt.Errorf("session for %s: %w", w.User, err)
	}

	date, err := time.Parse("2006-01-02", w.Date)
	if err != nil {
		return domain.Intent{}, nil, fmt.Errorf("date %q: %w", w.Date, err)
	}
	rwin, err := time.ParseDuration(w.RetryWindow)
	if err != nil {
		return domain.Intent{}, nil, fmt.Errorf("retry_window %q: %w", w.RetryWindow, err)
	}
	var pollInt time.Duration
	if w.PollInterval != "" {
		pollInt, _ = time.ParseDuration(w.PollInterval)
	}

	slots := make([]domain.SlotPreference, 0, len(w.ResTimes)*max(1, len(w.TableTypes)))
	for _, t := range w.ResTimes {
		wt, err := domain.ParseWallTime(t)
		if err != nil {
			return domain.Intent{}, nil, fmt.Errorf("res_times %q: %w", t, err)
		}
		if len(w.TableTypes) == 0 {
			slots = append(slots, domain.SlotPreference{Time: wt})
			continue
		}
		for _, tt := range w.TableTypes {
			slots = append(slots, domain.SlotPreference{Time: wt, TableType: tt})
		}
	}

	intent := domain.Intent{
		User:      domain.UserID(w.User),
		Venue:     domain.VenueRef{Provider: "resy", Ref: strconv.Itoa(w.VenueID)},
		Date:      domain.NewDate(date.Year(), date.Month(), date.Day()),
		PartySize: w.PartySize,
		SlotPrefs: slots,
		Release: domain.NotifyMeRelease{
			ProbeFrom:    now,
			ProbeUntil:   now.Add(rwin),
			PollInterval: pollInt,
		},
	}
	return intent, &resySessionWrapper{inner: sess}, nil
}

// dedupTracker prevents the watch process from booking more than one
// reservation for the same (user, date) pair across all its in-flight
// watches. The booking race is fully concurrent — every watch races
// independently — so the tracker is the synchronization point that
// turns "first to win" into "winner blocks the rest from trying."
//
// Scope is in-process only: a pre-existing reservation booked outside
// this watch (or by an earlier run) is not detected. That requires a
// "list my reservations" call to Resy and is a separate piece of
// work.
type dedupTracker struct {
	mu     sync.Mutex
	booked map[dedupKey]string // value: snipeID that won the slot, for diagnostics
}

type dedupKey struct {
	user domain.UserID
	date domain.Date
}

func newDedupTracker() *dedupTracker {
	return &dedupTracker{booked: make(map[dedupKey]string)}
}

// claim atomically reserves (user, date) for the supplied snipeID.
// Returns the winning snipeID and ok=true if the claim succeeded; if
// another snipe already claimed the slot, returns that snipeID and
// ok=false.
func (d *dedupTracker) claim(user domain.UserID, date domain.Date, snipeID domain.SnipeID) (string, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	k := dedupKey{user: user, date: date}
	if existing, taken := d.booked[k]; taken {
		return existing, false
	}
	d.booked[k] = string(snipeID)
	return string(snipeID), true
}

// release removes a claim. Called when a snipe's booking race ends
// without a confirmation — the (user, date) goes back to "available"
// so a later-firing watch on the same night can still try.
func (d *dedupTracker) release(user domain.UserID, date domain.Date, snipeID domain.SnipeID) {
	d.mu.Lock()
	defer d.mu.Unlock()
	k := dedupKey{user: user, date: date}
	if existing, ok := d.booked[k]; ok && existing == string(snipeID) {
		delete(d.booked, k)
	}
}

// resySessionWrapper bridges *resy.Session to providers.Session. The
// engine's booking race takes providers.Session; the resy adapter
// returns *resy.Session. Since *resy.Session already implements the
// providers.Session interface this is a no-op typed wrapper kept here
// so prepareWatchIntent's signature is exported-friendly.
type resySessionWrapper struct {
	inner sessionLike
}

type sessionLike interface {
	Provider() domain.ProviderID
	User() domain.UserID
	ExpiresAt() time.Time
}

func (w *resySessionWrapper) Provider() domain.ProviderID { return w.inner.Provider() }
func (w *resySessionWrapper) User() domain.UserID         { return w.inner.User() }
func (w *resySessionWrapper) ExpiresAt() time.Time        { return w.inner.ExpiresAt() }

// driveOneWatch is the per-snipe lifecycle: Submit, Run (release
// strategy loop), then a dedup pre-check + RunBookingRace on
// Awaiting. Mirrors runSnipe's shape so terminal-result semantics are
// identical to the CLI, with the addition of the dedup gate: only
// one snipe per (user, date) gets to attempt a booking. Late-firing
// duplicates transition to Canceled with reason
// duplicate_already_booked.
func driveOneWatch(
	ctx context.Context,
	eng *engine.Engine,
	sess *resySessionWrapper,
	intent domain.Intent,
	w WatchEntry,
	notifier notify.Notifier,
	dedup *dedupTracker,
	logger *slog.Logger,
) {
	log := logger.With(
		slog.String("venue_name", w.VenueName),
		slog.String("date", w.Date),
		slog.String("user", w.User),
	)
	id := newSnipeID(intent)
	cancelSub := eng.Subscribe(notifierBridge(ctx, notifier))
	defer cancelSub()

	if _, err := eng.Submit(ctx, id, intent); err != nil {
		log.Error("watch: submit", slog.String("err", err.Error()))
		return
	}
	log.Info("watch: submitted", slog.String("snipe_id", string(id)))

	if err := eng.Run(ctx, id); err != nil {
		if !errors.Is(err, context.Canceled) {
			log.Error("watch: run", slog.String("err", err.Error()))
		}
		return
	}

	state, err := eng.Load(ctx, id)
	if err != nil {
		log.Error("watch: reload", slog.String("err", err.Error()))
		return
	}
	if state.Status() != domain.StatusAwaiting {
		emitTerminalResult(ctx, notifier, id, state, nil)
		return
	}

	// Dedup pre-check. If another watch already claimed (user, date),
	// cancel this one before the booking race burns a Find/Book on a
	// slot we don't want.
	winner, claimed := dedup.claim(intent.User, intent.Date, id)
	if !claimed {
		log.Info("watch: skipping duplicate; another snipe already holds this (user, date)",
			slog.String("winner_snipe_id", winner),
		)
		if err := state.Transition(ctx, domain.StatusCanceled, domain.EventCanceled,
			slog.String("reason", "duplicate_already_booked"),
			slog.String("winner_snipe_id", winner),
		); err != nil {
			log.Error("watch: cancel duplicate", slog.String("err", err.Error()))
		}
		final, _ := eng.Load(ctx, id)
		if final != nil {
			emitTerminalResult(ctx, notifier, id, final, nil)
		}
		return
	}

	raceErr := eng.RunBookingRace(ctx, state, sess)
	final, loadErr := eng.Load(ctx, id)
	if loadErr != nil {
		log.Error("watch: reload after race", slog.String("err", loadErr.Error()))
		dedup.release(intent.User, intent.Date, id)
		return
	}
	// Release the claim if the race didn't book — a later watch on
	// the same night may still want a shot.
	if final.Status() != domain.StatusBooked {
		dedup.release(intent.User, intent.Date, id)
	}
	emitTerminalResult(ctx, notifier, id, final, raceErr)
}

// installWatchSignalCancel wires SIGINT/SIGTERM into ctx so a single
// process supervising many watches drains cleanly on shutdown.
func installWatchSignalCancel(parent context.Context) (context.Context, context.CancelFunc) {
	return signal.NotifyContext(parent, syscall.SIGINT, syscall.SIGTERM)
}
