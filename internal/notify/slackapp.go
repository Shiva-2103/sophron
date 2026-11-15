package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/slack-go/slack"
	"github.com/slack-go/slack/socketmode"

	"github.com/Shiva-2103/sophron/internal/fixes"
)

// Decider approves and denies fixes (the fixes manager).
type Decider interface {
	Approve(ctx context.Context, id int64, user, reason string) (fixes.Request, error)
	Deny(ctx context.Context, id int64, user, reason string) (fixes.Request, error)
}

// PendingFinder finds the fix waiting for a finding (the store).
type PendingFinder interface {
	PendingFix(ctx context.Context, findingID int64) (fixes.Request, bool)
}

// UseFixes turns on Approve/Deny buttons: pending lets alerts show the
// buttons; Interactive must also be set (an app token and approvers).
func (s *Slack) UseFixes(p PendingFinder) { s.pending = p }

func (s *Slack) pendingFix(findingID int64) (fixes.Request, bool) {
	if s.pending == nil || findingID == 0 {
		return fixes.Request{}, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return s.pending.PendingFix(ctx, findingID)
}

// RunApprovals listens for button clicks over Slack Socket Mode: Sophron
// opens the connection to Slack, so the cluster needs no public address.
// Only Slack users in SlackConfig.Approvers may approve or deny.
func (s *Slack) RunApprovals(ctx context.Context, appToken string, d Decider) {
	api := slack.New("", slack.OptionAppLevelToken(appToken))
	client := socketmode.New(api)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case evt, ok := <-client.Events:
				if !ok {
					return
				}
				switch evt.Type {
				case socketmode.EventTypeConnected:
					s.log.Info("slack approvals connected (socket mode)")
				case socketmode.EventTypeConnectionError:
					s.log.Warn("slack socket mode connection error; retrying")
				case socketmode.EventTypeInteractive:
					cb, ok := evt.Data.(slack.InteractionCallback)
					if evt.Request != nil {
						client.Ack(*evt.Request) // Slack wants an answer within 3 s
					}
					if ok && cb.Type == slack.InteractionTypeBlockActions {
						go s.handleClick(ctx, cb, d)
					}
				}
			}
		}
	}()
	for ctx.Err() == nil {
		err := client.RunContext(ctx) // returns only when the connection ends
		if ctx.Err() != nil {
			return
		}
		s.log.Warn("slack socket mode stopped; reconnecting in 10s", "error", err)
		select {
		case <-ctx.Done():
		case <-time.After(10 * time.Second):
		}
	}
}

func (s *Slack) handleClick(ctx context.Context, cb slack.InteractionCallback, d Decider) {
	for _, a := range cb.ActionCallback.BlockActions {
		if a.ActionID != ActionApprove && a.ActionID != ActionDeny {
			continue
		}
		who := cb.User.Name
		if who == "" {
			who = cb.User.ID
		}
		if !slices.Contains(s.cfg.Approvers, cb.User.ID) {
			s.respond(ctx, cb.ResponseURL, true, fmt.Sprintf("Sorry %s, you're not on Sophron's Slack approvers list. Ask an admin, or use the dashboard.", who))
			s.log.Warn("slack approval refused: not an approver", "user", cb.User.ID)
			continue
		}
		id, err := strconv.ParseInt(a.Value, 10, 64)
		if err != nil {
			continue
		}
		user := "slack:" + who
		var r fixes.Request
		if a.ActionID == ActionApprove {
			r, err = d.Approve(ctx, id, user, "approved in Slack")
		} else {
			r, err = d.Deny(ctx, id, user, "denied in Slack")
		}
		switch {
		case err != nil:
			s.respond(ctx, cb.ResponseURL, true, fmt.Sprintf("Fix #%d: %s", id, err.Error()))
		case r.Status == fixes.StatusDenied:
			s.respond(ctx, cb.ResponseURL, false, fmt.Sprintf("🚫 Fix #%d denied by %s. Nothing was changed.", id, who))
		default:
			s.respond(ctx, cb.ResponseURL, false, fmt.Sprintf("✅ Fix #%d approved by %s and %s: %s", id, who, r.Status, r.Result))
		}
	}
}

// respond answers through the click's response_url: ephemeral (only the
// clicker sees it) or a new message in the channel.
func (s *Slack) respond(ctx context.Context, url string, ephemeral bool, text string) {
	if url == "" {
		return
	}
	body := map[string]any{"text": esc(text), "replace_original": false, "response_type": "in_channel"}
	if ephemeral {
		body["response_type"] = "ephemeral"
	}
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		s.log.Warn("slack response failed", "error", "posting to response_url failed")
		return
	}
	_ = resp.Body.Close()
}
