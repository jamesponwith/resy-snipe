package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"resy-snipe/internal/clock"
	"resy-snipe/internal/domain"
	"resy-snipe/internal/resy"
	"resy-snipe/internal/resy/sign"
	"resy-snipe/internal/store"
)

// authClient is the slim subset of *resy.Client the CLI's login + snipe
// paths depend on. Declaring it locally lets tests substitute a fake
// without spinning up an httptest server, and keeps the cmd/ package
// from coupling to resy.Client's full surface.
type authClient interface {
	Login(ctx context.Context, creds resy.Credentials) (*resy.Session, error)
	CompleteMFA(ctx context.Context, challenge, code string) (*resy.Session, error)
	LoadSession(ctx context.Context, user domain.UserID) (*resy.Session, error)
	ImportSession(ctx context.Context, user domain.UserID, jwt string) (*resy.Session, error)
}

// clientAdapter wraps *resy.Client to expose the typed Login signature
// the cmd/ layer wants (resy.Credentials directly, without the
// providers.Credentials boxing the engine uses). The compile-time
// check on the var line below ensures the wrapper actually satisfies
// the local seam.
type clientAdapter struct{ inner *resy.Client }

var _ authClient = (*clientAdapter)(nil)

func (a *clientAdapter) Login(ctx context.Context, creds resy.Credentials) (*resy.Session, error) {
	return a.inner.Login(ctx, creds)
}

func (a *clientAdapter) CompleteMFA(ctx context.Context, challenge, code string) (*resy.Session, error) {
	return a.inner.CompleteMFA(ctx, challenge, code)
}

func (a *clientAdapter) LoadSession(ctx context.Context, user domain.UserID) (*resy.Session, error) {
	return a.inner.LoadSession(ctx, user)
}

func (a *clientAdapter) ImportSession(ctx context.Context, user domain.UserID, jwt string) (*resy.Session, error) {
	return a.inner.ImportSession(ctx, user, jwt)
}

// sessionStoreAdapter wraps *store.SQLiteStore so it satisfies
// resy.SessionStore. The two SessionRow types are field-equivalent;
// the adapter exists because internal/resy intentionally does not
// import internal/store (the seam keeps store/ out of the provider
// layer's dependency graph).
type sessionStoreAdapter struct {
	inner *store.SQLiteStore
}

func newSessionStoreAdapter(s *store.SQLiteStore) *sessionStoreAdapter {
	return &sessionStoreAdapter{inner: s}
}

func (a *sessionStoreAdapter) UpsertSession(ctx context.Context, s resy.SessionRow) error {
	return a.inner.UpsertSession(ctx, toStoreRow(s))
}

func (a *sessionStoreAdapter) GetSession(
	ctx context.Context,
	user domain.UserID,
	provider domain.ProviderID,
	now time.Time,
) (resy.SessionRow, error) {
	row, err := a.inner.GetSession(ctx, user, provider, now)
	if err != nil {
		return resy.SessionRow{}, err
	}
	return fromStoreRow(row), nil
}

func toStoreRow(s resy.SessionRow) store.SessionRow {
	return store.SessionRow{
		UserID:    s.UserID,
		Provider:  s.Provider,
		JWT:       s.JWT,
		ExpiresAt: s.ExpiresAt,
		CreatedAt: s.CreatedAt,
		UpdatedAt: s.UpdatedAt,
	}
}

func fromStoreRow(s store.SessionRow) resy.SessionRow {
	return resy.SessionRow{
		UserID:    s.UserID,
		Provider:  s.Provider,
		JWT:       s.JWT,
		ExpiresAt: s.ExpiresAt,
		CreatedAt: s.CreatedAt,
		UpdatedAt: s.UpdatedAt,
	}
}

// runLogin walks the Phase 1 login flow:
//
//  1. Prompt the user for email + password.
//  2. Call client.Login. On success, the resy adapter has already
//     persisted the session via its SessionStore.
//  3. On *resy.MFAError, prompt for a code and call CompleteMFA. The
//     Phase 1 stub returns "not implemented" — the error surface here
//     forwards the message verbatim so the user sees an actionable
//     diagnostic rather than a silent retry loop.
//
// The ctx may carry no deadline — the prompt loop is interactive,
// and the resy adapter applies a per-call timeout internally for
// each HTTP round trip (see invariant I-11).
//
// stdin must be a fresh reader; a bufio.Reader is wrapped over it so
// the existing promptRaw helper from intent.go works unchanged. out
// is where prompts and confirmation messages are written.
func runLogin(ctx context.Context, args []string, stdin io.Reader, out io.Writer, client authClient) error {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	fs.SetOutput(out)
	var (
		userFlag    string
		tokenEnv    string
	)
	fs.StringVar(&userFlag, "user", "",
		"Resy account email. Required with -token-env; prompted otherwise.")
	fs.StringVar(&tokenEnv, "token-env", "",
		"Name of the env var holding a Resy JWT. When set, skips the email+password "+
			"flow and imports the token directly — useful for phone-based accounts.")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(out, "Usage: resy-snipe login [-user <email>] [-token-env <ENV>]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}

	if tokenEnv != "" {
		return runLoginImportToken(ctx, out, client, userFlag, tokenEnv)
	}

	reader := bufio.NewReader(stdin)

	email := strings.TrimSpace(userFlag)
	if email == "" {
		var err error
		email, err = promptRaw(reader, out, "Email", "")
		if err != nil {
			return fmt.Errorf("prompt email: %w", err)
		}
	}
	if email == "" {
		return errors.New("login: email is required")
	}

	password, err := promptRaw(reader, out, "Password", "")
	if err != nil {
		return fmt.Errorf("prompt password: %w", err)
	}
	if password == "" {
		return errors.New("login: password is required")
	}

	creds := resy.Credentials{Email: email, Password: password}
	sess, err := client.Login(ctx, creds)
	if err == nil {
		fprintf(out, "Logged in as %s (session expires %s).\n",
			sess.User(), sess.ExpiresAt().Format("2006-01-02 15:04:05 MST"))
		return nil
	}

	// MFA branch: Login returned an MFAError that wraps
	// providers.ErrMFARequired. We extract the opaque challenge token
	// and pass it back to CompleteMFA along with the user's code.
	var mfaErr *resy.MFAError
	if errors.As(err, &mfaErr) {
		code, perr := promptRaw(reader, out, "MFA code", "")
		if perr != nil {
			return fmt.Errorf("prompt mfa code: %w", perr)
		}
		if code == "" {
			return errors.New("login: MFA code is required")
		}
		mfaSess, mfaCompleteErr := client.CompleteMFA(ctx, mfaErr.Challenge, code)
		if mfaCompleteErr != nil {
			return fmt.Errorf("login: complete MFA: %w", mfaCompleteErr)
		}
		fprintf(out, "Logged in as %s (session expires %s).\n",
			mfaSess.User(), mfaSess.ExpiresAt().Format("2006-01-02 15:04:05 MST"))
		return nil
	}

	return fmt.Errorf("login: %w", err)
}

// runLoginImportToken bypasses the interactive email+password flow by
// reading a Resy JWT from the env var named tokenEnvName and sealing
// it into the local session store under user. Used by callers whose
// Resy account is phone-based (no settable password) and who have
// captured a token from the mobile app or browser.
//
// The token is read from an env var (rather than a CLI flag) so it
// never appears in shell history or process listings — same shape as
// the IMAP password seam.
func runLoginImportToken(
	ctx context.Context,
	out io.Writer,
	client authClient,
	user string,
	tokenEnvName string,
) error {
	if strings.TrimSpace(user) == "" {
		return errors.New("login: -user is required with -token-env")
	}
	token := os.Getenv(tokenEnvName)
	if token == "" {
		return fmt.Errorf("login: env var %q is empty (set it before invoking)", tokenEnvName)
	}
	sess, err := client.ImportSession(ctx, domain.UserID(user), token)
	if err != nil {
		return fmt.Errorf("login: %w", err)
	}
	fprintf(out, "Imported token for %s (session expires %s).\n",
		sess.User(), sess.ExpiresAt().Format("2006-01-02 15:04:05 MST"))
	return nil
}

// errNoSession is the sentinel surfaced when a snipe path tries to
// load a non-existent or expired session. Callers turn this into the
// canonical "run 'resy-snipe login' first" message at the user-facing
// boundary; engine-side branching uses errors.Is to recognize it.
var errNoSession = errors.New("no valid session — run 'resy-snipe login' first")

// loadSessionForSnipe attempts to rehydrate a previously persisted
// session for `user`. ErrNotFound and ErrSessionExpired both translate
// into errNoSession at this seam — the user-facing copy must be the
// same in both cases (the spec is explicit: "expired session triggers
// a useful 'run resy-snipe login' message rather than mid-snipe
// failure"). Other errors are returned wrapped.
func loadSessionForSnipe(ctx context.Context, client authClient, user domain.UserID, logger *slog.Logger) (*resy.Session, error) {
	sess, err := client.LoadSession(ctx, user)
	if err == nil {
		return sess, nil
	}
	if errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrSessionExpired) {
		logger.Debug("no usable session",
			slog.String("user", string(user)),
			slog.String("reason", err.Error()))
		return nil, errNoSession
	}
	return nil, fmt.Errorf("load session: %w", err)
}

// openCLIClient wires the production *resy.Client + a session-store
// adapter against the default sqlite path. It returns the client and
// a cleanup func the caller is responsible for invoking; the cleanup
// closes the underlying *sql.DB.
//
// Split out so cmd/ tests can bypass the real DB by constructing a
// fake authClient directly and calling runLogin / runSnipe.
func openCLIClient(ctx context.Context, logger *slog.Logger, clk clock.Clock) (*clientAdapter, func() error, error) {
	db, err := store.Open(ctx, "")
	if err != nil {
		return nil, nil, fmt.Errorf("open db: %w", err)
	}
	if err := store.Migrate(ctx, db); err != nil {
		_ = db.Close()
		return nil, nil, fmt.Errorf("migrate db: %w", err)
	}
	sqlStore := store.NewSQLiteStore(db)
	c := resy.NewClient(logger, clk, resy.WithStore(newSessionStoreAdapter(sqlStore)))
	return &clientAdapter{inner: c}, db.Close, nil
}

// signerBinEnv is the env var the user sets to point the resy adapter
// at a PerimeterX-aware signing binary. When set, openSnipeBackend
// constructs a sign.Subprocess wrapper around it; when empty, the
// adapter falls back to sign.Noop and behaves exactly as before R7.
//
// The expected wire format is documented in internal/resy/sign/doc.go;
// see also docs/anti-bot.md for the full integration shape.
const signerBinEnv = "RESY_SNIPE_SIGNER_BIN"

// openSnipeBackend wires the production stack the snipe path needs:
// a *resy.Client (for Login/Find/Book and the SlotPreparer surface
// the engine asserts at runtime) and the same *store.SQLiteStore
// that the engine and the session adapter both read from. The single
// underlying *sql.DB is shared across both — the cleanup closes it
// exactly once.
//
// If RESY_SNIPE_SIGNER_BIN is set, the resy.Client is also wired with
// a sign.Subprocess Signer pointing at that binary. The Signer is
// best-effort: a misconfigured binary surfaces as logged warnings on
// the per-call Sign path; the adapter still attempts the request and
// the existing classifier handles the resulting upstream failure. A
// missing env var falls back to sign.Noop, matching pre-R7 behavior.
//
// Distinct from openCLIClient because the snipe path needs the typed
// Resy client and the SQL store handle directly (one to wrap as the
// providers.Provider, the other to feed engine.New). Login does not
// need either, so its bootstrap stays narrower.
func openSnipeBackend(ctx context.Context, logger *slog.Logger, clk clock.Clock) (*resy.Client, *store.SQLiteStore, func() error, error) {
	db, err := store.Open(ctx, "")
	if err != nil {
		return nil, nil, nil, fmt.Errorf("open db: %w", err)
	}
	if err := store.Migrate(ctx, db); err != nil {
		_ = db.Close()
		return nil, nil, nil, fmt.Errorf("migrate db: %w", err)
	}
	sqlStore := store.NewSQLiteStore(db)
	opts := []resy.Option{resy.WithStore(newSessionStoreAdapter(sqlStore))}
	if signer := buildSigner(logger, clk); signer != nil {
		opts = append(opts, resy.WithSigner(signer))
	}
	c := resy.NewClient(logger, clk, opts...)
	return c, sqlStore, db.Close, nil
}

// buildSigner returns a sign.Subprocess pinned to the binary at
// RESY_SNIPE_SIGNER_BIN, or nil when the env var is unset. A nil
// return tells openSnipeBackend to skip WithSigner entirely so the
// Client's default sign.Noop stays in place.
//
// Either branch logs at info level so a user reading the snipe boot
// output can always tell which signer is active without trawling
// `env`. The unwired-case message includes the env var name so
// enabling signing is one grep away.
func buildSigner(logger *slog.Logger, clk clock.Clock) sign.Signer {
	bin := os.Getenv(signerBinEnv)
	if bin == "" {
		logger.Info("anti-bot signer: noop (set "+signerBinEnv+" to enable per-call signing)",
			slog.String("signer", "noop"))
		return nil
	}
	s, err := sign.NewSubprocess(sign.SubprocessConfig{
		Bin:    bin,
		Logger: logger,
		Clock:  clk,
	})
	if err != nil {
		logger.Warn("signer construction failed; falling back to noop",
			slog.String("bin", bin),
			slog.String("err", err.Error()))
		return nil
	}
	logger.Info("anti-bot signer: subprocess",
		slog.String("signer", "subprocess"),
		slog.String("bin", bin))
	return s
}
