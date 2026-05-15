package email

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// Start opens an IMAP session and kicks off a polling goroutine that
// reads new messages from the configured Mailbox and feeds them
// through IngestEmail. Bad credentials / unreachable host surface
// synchronously so daemon boot fails loudly rather than logging at
// runtime forever.
//
// The polling cadence is Config.MinPollInterval. Only messages from
// reservations@resy.com with UID greater than the mailbox's UIDNext
// at Start time are processed — pre-existing alerts in the mailbox
// are ignored. UID tracking is in-memory; a daemon restart resets the
// high-water mark, but already-cached fires aren't lost in-process.
//
// Returns an error if the Source has already been started.
func (s *Source) Start(parent context.Context, log *slog.Logger) error {
	if log == nil {
		log = slog.Default()
	}
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return errors.New("email: Source already started")
	}
	s.started = true
	s.mu.Unlock()

	c, lastUID, err := s.dialAndSelect()
	if err != nil {
		return fmt.Errorf("email.Start: %w", err)
	}

	ctx, cancel := context.WithCancel(parent)

	s.mu.Lock()
	s.client = c
	s.lastUID = lastUID
	s.cancel = cancel
	s.log = log
	s.mu.Unlock()

	s.wg.Add(1)
	go s.runLoop(ctx)
	return nil
}

// runLoop is the IMAP poll loop. It ticks at MinPollInterval, runs
// pollOnce, and on any error tears down the connection so the next
// tick will re-establish it.
func (s *Source) runLoop(ctx context.Context) {
	defer s.wg.Done()

	ticker := time.NewTicker(s.cfg.MinPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.pollOnce(ctx); err != nil {
				s.log.WarnContext(ctx, "imap poll failed", "err", err)
				s.reconnect(ctx)
			}
		}
	}
}

// pollOnce searches for new Resy-sender messages with UID greater
// than the running high-water mark, fetches their bodies, and ingests
// each through the existing parser+cache path. Advances the
// high-water mark to the highest UID observed.
func (s *Source) pollOnce(ctx context.Context) error {
	s.mu.Lock()
	c := s.client
	lastUID := s.lastUID
	log := s.log
	s.mu.Unlock()

	if c == nil {
		return errors.New("not connected")
	}

	var uidSet imap.UIDSet
	// IMAP convention: stop=0 is the wildcard "*" (highest UID).
	uidSet.AddRange(lastUID+1, 0)
	criteria := &imap.SearchCriteria{
		UID: []imap.UIDSet{uidSet},
		Header: []imap.SearchCriteriaHeaderField{
			{Key: "From", Value: resyAlertSender},
		},
	}

	searchData, err := c.UIDSearch(criteria, nil).Wait()
	if err != nil {
		return fmt.Errorf("UIDSearch: %w", err)
	}
	uids := searchData.AllUIDs()
	if len(uids) == 0 {
		return nil
	}

	fetchOpts := &imap.FetchOptions{
		UID:         true,
		BodySection: []*imap.FetchItemBodySection{{Peek: true}},
	}
	msgs, err := c.Fetch(imap.UIDSetNum(uids...), fetchOpts).Collect()
	if err != nil {
		return fmt.Errorf("Fetch: %w", err)
	}

	var highest imap.UID
	for _, m := range msgs {
		for _, bs := range m.BodySection {
			if err := s.IngestEmail(bs.Bytes); err != nil {
				// ErrNotAResyAlert is the common case for any non-Resy
				// mail caught by a loose SearchCriteria — skip quietly.
				if !errors.Is(err, ErrNotAResyAlert) {
					log.WarnContext(ctx, "ingest failed",
						"uid", uint32(m.UID),
						"err", err,
					)
				}
			}
		}
		if m.UID > highest {
			highest = m.UID
		}
	}

	s.mu.Lock()
	if highest > s.lastUID {
		s.lastUID = highest
	}
	s.mu.Unlock()
	return nil
}

// reconnect closes the current client and dials a fresh one. The
// existing lastUID is preserved so we resume where we left off; UID
// stability across reconnects is guaranteed by IMAP UIDVALIDITY (a
// rare change is treated as a known v1 limitation — operator must
// restart the daemon to re-bootstrap).
func (s *Source) reconnect(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	s.mu.Lock()
	old := s.client
	s.client = nil
	s.mu.Unlock()
	if old != nil {
		_ = old.Logout().Wait()
		_ = old.Close()
	}

	c, _, err := s.dialAndSelect()
	if err != nil {
		s.log.WarnContext(ctx, "imap reconnect failed", "err", err)
		return
	}
	s.mu.Lock()
	s.client = c
	s.mu.Unlock()
}

// dialAndSelect dials TLS, logs in, and SELECTs the configured
// mailbox. Returns the high-water mark UID (UIDNext - 1) so a fresh
// session ignores pre-existing alerts.
func (s *Source) dialAndSelect() (*imapclient.Client, imap.UID, error) {
	c, err := imapclient.DialTLS(s.cfg.IMAPAddr, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("dial %q: %w", s.cfg.IMAPAddr, err)
	}
	if err := c.Login(s.cfg.Username, s.cfg.Password).Wait(); err != nil {
		_ = c.Close()
		return nil, 0, fmt.Errorf("login as %q: %w", s.cfg.Username, err)
	}
	sel, err := c.Select(s.cfg.Mailbox, nil).Wait()
	if err != nil {
		_ = c.Logout().Wait()
		_ = c.Close()
		return nil, 0, fmt.Errorf("select %q: %w", s.cfg.Mailbox, err)
	}
	var highWater imap.UID
	if sel.UIDNext > 0 {
		highWater = sel.UIDNext - 1
	}
	return c, highWater, nil
}
