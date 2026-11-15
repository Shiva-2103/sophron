// Package fixes turns a finding's suggested action into a fix request, asks
// the policy whether it may run, waits for a person to approve it, applies
// it, and checks afterwards that the problem really went away.
//
//	finding opened ──► policy ──► never:   blocked (shown, never runs)
//	                          ├─► approve: pending ──► approved ──► applied ──► verified
//	                          │                    └─► denied
//	                          └─► auto:    approved by policy ──► applied ──► verified
//
// Every step is written to the audit log.
package fixes

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"sync"
	"time"

	"sigs.k8s.io/yaml"

	"github.com/Shiva-2103/sophron/internal/rules"
)

// Request statuses.
const (
	StatusPending  = "pending"  // waiting for a person
	StatusApproved = "approved" // about to be applied
	StatusDenied   = "denied"
	StatusApplied  = "applied"  // change made, problem not gone yet
	StatusFailed   = "failed"   // the change could not be made
	StatusVerified = "verified" // change made and the problem went away
	StatusObsolete = "obsolete" // the problem went away before anyone acted
	StatusBlocked  = "blocked"  // policy says never
)

// Policy modes.
const (
	ModeAuto    = "auto"
	ModeApprove = "approve"
	ModeNever   = "never"
)

// Request is one proposed change and what happened to it.
type Request struct {
	ID          int64        `json:"id"`
	FindingID   int64        `json:"findingId"`
	FindingKey  string       `json:"findingKey"`
	Title       string       `json:"title"` // the finding's title
	Severity    string       `json:"severity"`
	Action      rules.Action `json:"action"`
	Mode        string       `json:"mode"`
	Status      string       `json:"status"`
	RequestedAt time.Time    `json:"requestedAt"`
	DecidedBy   string       `json:"decidedBy,omitempty"`
	DecidedAt   time.Time    `json:"decidedAt,omitzero"`
	Reason      string       `json:"reason,omitempty"`
	AppliedAt   time.Time    `json:"appliedAt,omitzero"`
	Result      string       `json:"result,omitempty"`
	VerifiedAt  time.Time    `json:"verifiedAt,omitzero"`
}

// Store keeps requests and the audit log.
type Store interface {
	InsertFix(ctx context.Context, r Request) (int64, error)
	UpdateFix(ctx context.Context, r Request) error
	Fix(ctx context.Context, id int64) (Request, error)
	Fixes(ctx context.Context, status string, limit int) ([]Request, error)
	FixesForFinding(ctx context.Context, findingID int64) ([]Request, error)
	AppendAudit(ctx context.Context, e AuditEntry) error
}

// AuditEntry is one line of the audit log.
type AuditEntry struct {
	ID     int64     `json:"id"`
	At     time.Time `json:"at"`
	Actor  string    `json:"actor"` // a person, "sophron" or "policy"
	Event  string    `json:"event"` // e.g. fix.approved
	Target string    `json:"target"`
	Detail string    `json:"detail,omitempty"`
	// SHA-256 of the previous entry's hash and this entry: editing or
	// deleting any line breaks the chain from there on.
	Hash string `json:"hash"`
}

// Applier makes the change in the cluster.
type Applier interface {
	Apply(ctx context.Context, a rules.Action) (string, error)
}

// Policy says which actions may run, and where.
type Policy struct {
	// Mode for actions not listed: auto, approve or never. Default approve.
	Default string `json:"default"`
	// Per action kind, e.g. rollout-restart: auto.
	Actions map[string]string `json:"actions,omitempty"`
	// Namespaces Sophron may change. Empty: none, so nothing is ever applied.
	Namespaces []string `json:"namespaces"`
	// Most replicas a scale-up may reach.
	MaxReplicas int32 `json:"maxReplicas,omitempty"`
}

// LoadPolicy reads the policy file. An empty path gives the safe default:
// every action needs approval and no namespace is allowed.
func LoadPolicy(path string) (Policy, error) {
	p := Policy{Default: ModeApprove}
	if path == "" {
		return p, nil
	}
	data, err := os.ReadFile(path) //nolint:gosec // path comes from the operator's flag
	if err != nil {
		return p, fmt.Errorf("read policy: %w", err)
	}
	if err := yaml.UnmarshalStrict(data, &p); err != nil {
		return p, fmt.Errorf("parse policy %s: %w", path, err)
	}
	if p.Default == "" {
		p.Default = ModeApprove
	}
	for k, m := range p.Actions {
		if m != ModeAuto && m != ModeApprove && m != ModeNever {
			return p, fmt.Errorf("policy: action %s has mode %q (use auto, approve or never)", k, m)
		}
	}
	if p.Default != ModeAuto && p.Default != ModeApprove && p.Default != ModeNever {
		return p, fmt.Errorf("policy: default mode %q (use auto, approve or never)", p.Default)
	}
	return p, nil
}

// Decide returns the mode for an action and, when blocked, why.
func (p Policy) Decide(a rules.Action) (string, string) {
	if !slices.Contains(p.Namespaces, a.Namespace) && !slices.Contains(p.Namespaces, "*") {
		return ModeNever, fmt.Sprintf("namespace %s is not in the policy's namespaces", a.Namespace)
	}
	mode := p.Default
	if m, ok := p.Actions[a.Kind]; ok {
		mode = m
	}
	if mode == ModeNever {
		return mode, fmt.Sprintf("policy says never for %s", a.Kind)
	}
	return mode, ""
}

// Manager runs the lifecycle. It is a rules.Notifier: findings opening and
// resolving drive it.
type Manager struct {
	Store   Store
	Policy  Policy
	Applier Applier
	Log     *slog.Logger
	Now     func() time.Time

	mu sync.Mutex // one decision at a time
}

func (m *Manager) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now().UTC()
}

// Notify reacts to rules engine events.
func (m *Manager) Notify(ctx context.Context, e rules.Event) error {
	f := e.Finding
	switch e.Type {
	case rules.EventOpened:
		if f.Fix.Action == nil {
			return nil
		}
		return m.propose(ctx, f)
	case rules.EventActionChanged:
		// A follow-up step, unless one is already waiting.
		reqs, err := m.Store.FixesForFinding(ctx, f.ID)
		if err != nil {
			return err
		}
		for _, r := range reqs {
			if r.Status == StatusPending || r.Status == StatusApproved {
				return nil
			}
		}
		return m.propose(ctx, f)
	case rules.EventResolved:
		return m.close(ctx, f)
	}
	return nil
}

func (m *Manager) propose(ctx context.Context, f rules.Finding) error {
	a := *f.Fix.Action
	mode, why := m.Policy.Decide(a)
	r := Request{FindingID: f.ID, FindingKey: f.Key, Title: f.Title, Severity: string(f.Severity),
		Action: a, Mode: mode, Status: StatusPending, RequestedAt: m.now()}
	if mode == ModeNever {
		r.Status, r.Reason = StatusBlocked, why
	}
	id, err := m.Store.InsertFix(ctx, r)
	if err != nil {
		return err
	}
	r.ID = id
	m.audit(ctx, "sophron", "fix.requested", r, fmt.Sprintf("%s (policy: %s)", a.Summary, mode))
	if mode == ModeNever {
		m.audit(ctx, "policy", "fix.blocked", r, why)
		return nil
	}
	if mode == ModeAuto {
		_, err := m.decide(ctx, id, "policy", true, "allowed automatically by policy")
		return err
	}
	return nil
}

// Approve applies a pending request on behalf of a person.
func (m *Manager) Approve(ctx context.Context, id int64, user, reason string) (Request, error) {
	return m.decide(ctx, id, user, true, reason)
}

// Deny closes a pending request.
func (m *Manager) Deny(ctx context.Context, id int64, user, reason string) (Request, error) {
	return m.decide(ctx, id, user, false, reason)
}

// ErrNotPending means someone else already decided, or the problem is gone.
var ErrNotPending = errors.New("this fix is no longer waiting for a decision")

func (m *Manager) decide(ctx context.Context, id int64, user string, approve bool, reason string) (Request, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, err := m.Store.Fix(ctx, id)
	if err != nil {
		return r, err
	}
	if r.Status != StatusPending {
		return r, ErrNotPending
	}
	// The policy may have changed since the request was made.
	if mode, why := m.Policy.Decide(r.Action); approve && mode == ModeNever {
		r.Status, r.Reason = StatusBlocked, why
		_ = m.Store.UpdateFix(ctx, r)
		m.audit(ctx, "policy", "fix.blocked", r, why)
		return r, fmt.Errorf("blocked: %s", why)
	}
	r.DecidedBy, r.DecidedAt, r.Reason = user, m.now(), reason
	if !approve {
		r.Status = StatusDenied
		if err := m.Store.UpdateFix(ctx, r); err != nil {
			return r, err
		}
		m.audit(ctx, user, "fix.denied", r, reason)
		return r, nil
	}
	r.Status = StatusApproved
	if err := m.Store.UpdateFix(ctx, r); err != nil {
		return r, err
	}
	m.audit(ctx, user, "fix.approved", r, reason)

	applyCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	result, err := m.Applier.Apply(applyCtx, r.Action)
	r.AppliedAt = m.now()
	if err != nil {
		r.Status, r.Result = StatusFailed, err.Error()
		_ = m.Store.UpdateFix(ctx, r)
		m.audit(ctx, "sophron", "fix.failed", r, err.Error())
		return r, fmt.Errorf("apply: %w", err)
	}
	r.Status, r.Result = StatusApplied, result
	if err := m.Store.UpdateFix(ctx, r); err != nil {
		return r, err
	}
	m.audit(ctx, "sophron", "fix.applied", r, result)
	if m.Log != nil {
		m.Log.Info("fix applied", "id", r.ID, "action", r.Action.Kind, "target", target(r.Action), "approvedBy", user)
	}
	return r, nil
}

// close settles a finding's requests when it resolves: an applied fix is
// verified; one nobody acted on is obsolete.
func (m *Manager) close(ctx context.Context, f rules.Finding) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	reqs, err := m.Store.FixesForFinding(ctx, f.ID)
	if err != nil {
		return err
	}
	for _, r := range reqs {
		switch r.Status {
		case StatusApplied:
			r.Status, r.VerifiedAt = StatusVerified, m.now()
			m.audit(ctx, "sophron", "fix.verified", r, "the problem went away after the fix")
		case StatusPending:
			r.Status = StatusObsolete
			m.audit(ctx, "sophron", "fix.obsolete", r, "the problem went away before anyone decided")
		default:
			continue
		}
		if err := m.Store.UpdateFix(ctx, r); err != nil {
			return err
		}
	}
	return nil
}

// ApprovedFor tells the rules engine who approved the fix for a finding, so
// the resolved alert can say so. It implements rules.Approvals.
func (m *Manager) ApprovedFor(ctx context.Context, findingID int64) (string, time.Time, bool) {
	reqs, err := m.Store.FixesForFinding(ctx, findingID)
	if err != nil {
		return "", time.Time{}, false
	}
	for _, r := range reqs {
		if r.Status == StatusApplied || r.Status == StatusVerified {
			return r.DecidedBy, r.AppliedAt, true
		}
	}
	return "", time.Time{}, false
}

func (m *Manager) audit(ctx context.Context, actor, event string, r Request, detail string) {
	e := AuditEntry{At: m.now(), Actor: actor, Event: event, Target: fmt.Sprintf("fix #%d · %s", r.ID, target(r.Action)), Detail: detail}
	if err := m.Store.AppendAudit(ctx, e); err != nil && m.Log != nil {
		m.Log.Error("audit write failed", "event", event, "error", err)
	}
}

func target(a rules.Action) string {
	return fmt.Sprintf("%s/%s/%s", a.Namespace, lowerKind(a.WorkloadKind), a.WorkloadName)
}

func lowerKind(k string) string {
	switch k {
	case "Deployment":
		return "deployment"
	case "StatefulSet":
		return "statefulset"
	}
	return k
}
