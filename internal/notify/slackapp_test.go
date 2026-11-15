package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/slack-go/slack"

	"github.com/Shiva-2103/sophron/internal/fixes"
	"github.com/Shiva-2103/sophron/internal/rules"
)

type onePending struct{ id int64 }

func (p onePending) PendingFix(context.Context, int64) (fixes.Request, bool) {
	return fixes.Request{ID: p.id, Status: fixes.StatusPending}, true
}

type fakeDecider struct {
	mu       sync.Mutex
	approved []string
}

func (d *fakeDecider) Approve(_ context.Context, id int64, user, _ string) (fixes.Request, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.approved = append(d.approved, user)
	return fixes.Request{ID: id, Status: fixes.StatusApplied, Result: "memory limit changed"}, nil
}
func (d *fakeDecider) Deny(_ context.Context, id int64, _, _ string) (fixes.Request, error) {
	return fixes.Request{ID: id, Status: fixes.StatusDenied}, nil
}

func TestButtonsOnlyWhenInteractive(t *testing.T) {
	s, _ := newTestSlack(t, "https://hooks.slack.com/services/T/B/x")
	f := SampleFinding(t0)
	f.ID = 9
	f.Fix.Action = &rules.Action{Kind: rules.ActionSetMemoryLimit, Summary: "Raise the memory limit to 768Mi"}
	s.UseFixes(onePending{id: 4})
	if strings.Contains(dump(t, s.Message(opened(f))), ActionApprove) {
		t.Error("no buttons without an app token and approvers")
	}
	s.cfg.Interactive = true
	out := dump(t, s.Message(opened(f)))
	if !strings.Contains(out, `"action_id":"sophron_approve"`) || !strings.Contains(out, `"value":"4"`) || !strings.Contains(out, `"style":"danger"`) {
		t.Errorf("want approve and deny buttons for fix 4: %s", out)
	}
	if !strings.Contains(out, `"title":{"type":"plain_text","text":"Apply this fix?"}`) {
		t.Error("approve needs a confirmation dialog")
	}
}

func TestClickNeedsAnApprover(t *testing.T) {
	var mu sync.Mutex
	var replies []string
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		mu.Lock()
		replies = append(replies, m["response_type"].(string)+"|"+m["text"].(string))
		mu.Unlock()
	}))
	defer srv.Close()
	s, _ := newTestSlack(t, "https://hooks.slack.com/services/T/B/x")
	s.cfg.Approvers = []string{"U_SHIVA"}
	d := &fakeDecider{}
	click := func(user string) slack.InteractionCallback {
		cb := slack.InteractionCallback{ResponseURL: srv.URL}
		cb.User.ID, cb.User.Name = user, strings.ToLower(user)
		cb.ActionCallback.BlockActions = []*slack.BlockAction{{ActionID: ActionApprove, Value: "4"}}
		return cb
	}
	s.handleClick(context.Background(), click("U_STRANGER"), d)
	s.handleClick(context.Background(), click("U_SHIVA"), d)
	if len(d.approved) != 1 || d.approved[0] != "slack:u_shiva" {
		t.Fatalf("only the approver may approve: %v", d.approved)
	}
	if len(replies) != 2 || !strings.HasPrefix(replies[0], "ephemeral|Sorry") || !strings.HasPrefix(replies[1], "in_channel|✅ Fix #4 approved by u_shiva") {
		t.Errorf("replies: %v", replies)
	}
}
