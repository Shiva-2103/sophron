// Package notify delivers rules-engine events to people. Today: Slack.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Shiva-2103/sophron/internal/rules"
)

// SlackConfig configures the Slack notifier.
type SlackConfig struct {
	// Incoming webhook URL. A secret: it is never logged.
	WebhookURL string
	// Cluster name shown in every message, so one channel can serve many clusters.
	Cluster string
	// Findings below this severity are not sent. Default: warning.
	MinSeverity rules.Severity
	// Times are shown in this zone, with UTC alongside. Default: UTC.
	Location *time.Location
	// A problem that comes back within this long of its last alert is not
	// alerted again (its "resolved" is skipped too). Default: 10m.
	Cooldown time.Duration
}

// Slack sends findings to a Slack channel through an incoming webhook.
//
// Notify never blocks the rules engine: events go into a queue and one
// background worker sends them, with retries.
type Slack struct {
	cfg    SlackConfig
	log    *slog.Logger
	client *http.Client
	queue  chan rules.Event

	// For tests.
	now   func() time.Time
	sleep func(context.Context, time.Duration) error

	mu       sync.Mutex
	lastSent map[string]time.Time // finding key → when its last alert went out
	muted    map[string]bool      // keys whose repeat alert was skipped
}

const (
	queueSize   = 100
	maxAttempts = 3
	maxWait     = 30 * time.Second
	minGap      = time.Second // Slack allows about one webhook message per second
)

// NewSlack checks the config and returns a notifier. Call Start to begin sending.
func NewSlack(cfg SlackConfig, log *slog.Logger) (*Slack, error) {
	if err := checkWebhookURL(cfg.WebhookURL); err != nil {
		return nil, err
	}
	if cfg.MinSeverity == "" {
		cfg.MinSeverity = rules.Warning
	}
	if cfg.MinSeverity.Rank() == 0 {
		return nil, fmt.Errorf("unknown severity %q (use info, warning or critical)", cfg.MinSeverity)
	}
	if cfg.Location == nil {
		cfg.Location = time.UTC
	}
	if cfg.Cooldown == 0 {
		cfg.Cooldown = 10 * time.Minute
	}
	if cfg.Cluster == "" {
		cfg.Cluster = "kubernetes"
	}
	return &Slack{
		cfg: cfg, log: log,
		client:   &http.Client{Timeout: 10 * time.Second},
		queue:    make(chan rules.Event, queueSize),
		now:      func() time.Time { return time.Now().UTC() },
		sleep:    sleepCtx,
		lastSent: map[string]time.Time{},
		muted:    map[string]bool{},
	}, nil
}

// checkWebhookURL accepts https URLs, and plain http only for local tests.
// The error never contains the URL itself.
func checkWebhookURL(raw string) error {
	if raw == "" {
		return errors.New("slack webhook URL is empty")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return errors.New("slack webhook URL is not a valid URL")
	}
	local := u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost"
	if u.Scheme != "https" && (u.Scheme != "http" || !local) {
		return errors.New("slack webhook URL must start with https://")
	}
	return nil
}

// Notify queues an event. It returns at once; if the queue is full the
// event is dropped with an error, so a Slack outage can't slow the engine.
func (s *Slack) Notify(_ context.Context, e rules.Event) error {
	if !s.wanted(e) {
		return nil
	}
	select {
	case s.queue <- e:
		return nil
	default:
		return errors.New("slack queue full, alert dropped")
	}
}

// wanted applies the severity filter and the cooldown.
func (s *Slack) wanted(e rules.Event) bool {
	f := e.Finding
	if f.Severity.Rank() < s.cfg.MinSeverity.Rank() {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	switch e.Type {
	case rules.EventOpened:
		if last, ok := s.lastSent[f.Key]; ok && s.now().Sub(last) < s.cfg.Cooldown {
			s.muted[f.Key] = true
			s.log.Info("slack alert skipped, same problem alerted recently", "finding", f.Key)
			return false
		}
		s.lastSent[f.Key] = s.now()
		delete(s.muted, f.Key)
	case rules.EventEscalate:
		s.lastSent[f.Key] = s.now()
		delete(s.muted, f.Key) // it got worse: speak up again
	case rules.EventResolved:
		if s.muted[f.Key] {
			delete(s.muted, f.Key)
			return false
		}
	}
	return true
}

// Start runs the sender until ctx is cancelled. Run it in a goroutine.
func (s *Slack) Start(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case e := <-s.queue:
			if err := s.Send(ctx, e); err != nil {
				s.log.Warn("slack alert failed", "event", e.Type, "finding", e.Finding.Key, "error", err)
			} else {
				s.log.Info("slack alert sent", "event", e.Type, "finding", e.Finding.Key)
			}
			if s.sleep(ctx, minGap) != nil {
				return
			}
		}
	}
}

// Send posts one event now, retrying on rate limits and server errors.
func (s *Slack) Send(ctx context.Context, e rules.Event) error {
	body, err := json.Marshal(s.Message(e))
	if err != nil {
		return err
	}
	var last error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		wait, err := s.post(ctx, body)
		if err == nil {
			return nil
		}
		last = err
		var perm permanentError
		if errors.As(err, &perm) || attempt == maxAttempts {
			break
		}
		if wait == 0 {
			wait = time.Duration(attempt) * time.Second
		}
		if err := s.sleep(ctx, wait); err != nil {
			return err
		}
	}
	return last
}

type permanentError struct{ msg string }

func (e permanentError) Error() string { return e.msg }

// post sends the body once. It returns how long Slack asked us to wait, if
// it did.
func (s *Slack) post(ctx context.Context, body []byte) (time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.cfg.WebhookURL, bytes.NewReader(body))
	if err != nil {
		return 0, permanentError{"could not build the request"}
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		// url.Error repeats the URL, which is a secret. Keep only the cause.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return 0, fmt.Errorf("posting to slack: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
	reason := strings.TrimSpace(string(msg))

	switch {
	case resp.StatusCode == http.StatusOK:
		return 0, nil
	case resp.StatusCode == http.StatusTooManyRequests:
		wait := maxWait
		if secs, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && secs >= 0 {
			wait = min(time.Duration(secs)*time.Second, maxWait)
		}
		return wait, fmt.Errorf("slack rate limit (429)")
	case resp.StatusCode >= 500:
		return 0, fmt.Errorf("slack server error %d: %s", resp.StatusCode, reason)
	default:
		// 400 invalid_payload, 403 action_prohibited, 404 no_service, 410 channel_is_archived:
		// retrying won't help.
		return 0, permanentError{fmt.Sprintf("slack rejected the message (%d): %s", resp.StatusCode, reason)}
	}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
