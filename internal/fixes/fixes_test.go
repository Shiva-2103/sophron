package fixes_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/Shiva-2103/sophron/internal/fixes"
	"github.com/Shiva-2103/sophron/internal/rules"
	"github.com/Shiva-2103/sophron/internal/store"
)

type fakeApplier struct {
	applied []rules.Action
	err     error
}

func (f *fakeApplier) Apply(_ context.Context, a rules.Action) (string, error) {
	f.applied = append(f.applied, a)
	return "done", f.err
}

func newManager(t *testing.T, p fixes.Policy) (*fixes.Manager, *store.Store, *fakeApplier) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ap := &fakeApplier{}
	return &fixes.Manager{Store: db, Policy: p, Applier: ap}, db, ap
}

func oomFinding(id int64) rules.Finding {
	return rules.Finding{ID: id, Key: "oom-killed/llm/llm-server", Title: "llm/llm-server is being killed", Severity: rules.Critical,
		Fix: rules.Fix{Action: &rules.Action{Kind: rules.ActionSetMemoryLimit, Namespace: "llm", WorkloadKind: "Deployment",
			WorkloadName: "llm-server", Container: "llama-server", Value: "768Mi", Summary: "Raise the memory limit to 768Mi"}}}
}

func TestApproveApplyVerify(t *testing.T) {
	ctx := context.Background()
	m, db, ap := newManager(t, fixes.Policy{Default: fixes.ModeApprove, Namespaces: []string{"llm"}})
	f := oomFinding(7)
	if err := m.Notify(ctx, rules.Event{Type: rules.EventOpened, Finding: f}); err != nil {
		t.Fatal(err)
	}
	pending, _ := db.Fixes(ctx, fixes.StatusPending, 10)
	if len(pending) != 1 || len(ap.applied) != 0 {
		t.Fatalf("want one pending request and nothing applied, got %+v / %d", pending, len(ap.applied))
	}
	r, err := m.Approve(ctx, pending[0].ID, "shiva", "looks right")
	if err != nil || r.Status != fixes.StatusApplied || len(ap.applied) != 1 {
		t.Fatalf("approve: %+v %v", r, err)
	}
	if _, err := m.Approve(ctx, r.ID, "someone-else", ""); !errors.Is(err, fixes.ErrNotPending) {
		t.Errorf("a second approval must be refused, got %v", err)
	}
	if by, _, ok := m.ApprovedFor(ctx, 7); !ok || by != "shiva" {
		t.Errorf("ApprovedFor = %q %v", by, ok)
	}
	if err := m.Notify(ctx, rules.Event{Type: rules.EventResolved, Finding: f}); err != nil {
		t.Fatal(err)
	}
	got, _ := db.Fix(ctx, r.ID)
	if got.Status != fixes.StatusVerified || got.VerifiedAt.IsZero() {
		t.Errorf("want verified, got %+v", got)
	}
	audit, _ := db.Audit(ctx, 10)
	var events []string
	for i := len(audit) - 1; i >= 0; i-- {
		events = append(events, audit[i].Event)
	}
	if strings.Join(events, ",") != "fix.requested,fix.approved,fix.applied,fix.verified" {
		t.Errorf("audit trail: %v", events)
	}
	if bad, err := db.VerifyAudit(ctx); err != nil || bad != 0 {
		t.Errorf("audit chain broken at %d (%v)", bad, err)
	}
}

func TestPolicyModes(t *testing.T) {
	ctx := context.Background()
	// auto: applied without a person.
	m, db, ap := newManager(t, fixes.Policy{Default: fixes.ModeApprove, Namespaces: []string{"llm"},
		Actions: map[string]string{rules.ActionSetMemoryLimit: fixes.ModeAuto}})
	_ = m.Notify(ctx, rules.Event{Type: rules.EventOpened, Finding: oomFinding(1)})
	if all, _ := db.Fixes(ctx, "", 10); len(all) != 1 || all[0].Status != fixes.StatusApplied || all[0].DecidedBy != "policy" || len(ap.applied) != 1 {
		t.Errorf("auto: %+v", all)
	}
	// never, and namespaces not allowed: blocked, nothing applied.
	for _, p := range []fixes.Policy{
		{Default: fixes.ModeNever, Namespaces: []string{"llm"}},
		{Default: fixes.ModeApprove, Namespaces: []string{"other"}},
	} {
		m, db, ap := newManager(t, p)
		_ = m.Notify(ctx, rules.Event{Type: rules.EventOpened, Finding: oomFinding(1)})
		all, _ := db.Fixes(ctx, "", 10)
		if len(all) != 1 || all[0].Status != fixes.StatusBlocked || len(ap.applied) != 0 {
			t.Errorf("policy %+v: %+v", p, all)
		}
		if _, err := m.Approve(ctx, all[0].ID, "shiva", ""); err == nil || len(ap.applied) != 0 {
			t.Error("a blocked fix must never be applied")
		}
	}
}

func TestDenyAndObsolete(t *testing.T) {
	ctx := context.Background()
	m, db, ap := newManager(t, fixes.Policy{Default: fixes.ModeApprove, Namespaces: []string{"*"}})
	_ = m.Notify(ctx, rules.Event{Type: rules.EventOpened, Finding: oomFinding(1)})
	_ = m.Notify(ctx, rules.Event{Type: rules.EventOpened, Finding: oomFinding(2)})
	all, _ := db.Fixes(ctx, fixes.StatusPending, 10)
	if r, err := m.Deny(ctx, all[1].ID, "shiva", "not now"); err != nil || r.Status != fixes.StatusDenied {
		t.Fatalf("deny: %+v %v", r, err)
	}
	_ = m.Notify(ctx, rules.Event{Type: rules.EventResolved, Finding: oomFinding(2)})
	r, _ := db.Fix(ctx, all[0].ID)
	if r.Status != fixes.StatusObsolete || len(ap.applied) != 0 {
		t.Errorf("want obsolete when the problem goes away first, got %+v", r)
	}
}

func TestApplyFailureIsRecorded(t *testing.T) {
	ctx := context.Background()
	m, db, ap := newManager(t, fixes.Policy{Default: fixes.ModeApprove, Namespaces: []string{"llm"}})
	ap.err = errors.New("forbidden")
	_ = m.Notify(ctx, rules.Event{Type: rules.EventOpened, Finding: oomFinding(1)})
	all, _ := db.Fixes(ctx, fixes.StatusPending, 10)
	if _, err := m.Approve(ctx, all[0].ID, "shiva", ""); err == nil {
		t.Fatal("want the apply error")
	}
	r, _ := db.Fix(ctx, all[0].ID)
	if r.Status != fixes.StatusFailed || !strings.Contains(r.Result, "forbidden") {
		t.Errorf("failure not recorded: %+v", r)
	}
}

func deployment(revision string, mem string) *appsv1.Deployment {
	one := int32(1)
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "llm-server", Namespace: "llm", UID: "d1",
			Annotations: map[string]string{"deployment.kubernetes.io/revision": revision}},
		Spec: appsv1.DeploymentSpec{Replicas: &one, Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "llm"}},
			Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "llm"}},
				Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "llama-server", Image: "new",
					Resources: corev1.ResourceRequirements{
						Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse(mem)},
						Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse(mem)}}}}}}},
	}
}

func TestKubeApplier(t *testing.T) {
	ctx := context.Background()
	d := deployment("2", "512Mi")
	ctrl := true
	oldRS := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "llm-server-old", Namespace: "llm", Labels: map[string]string{"app": "llm"},
		Annotations:     map[string]string{"deployment.kubernetes.io/revision": "1"},
		OwnerReferences: []metav1.OwnerReference{{Kind: "Deployment", Name: "llm-server", UID: "d1", Controller: &ctrl}}},
		Spec: appsv1.ReplicaSetSpec{Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "llm", "pod-template-hash": "abc"}},
			Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "llama-server", Image: "old"}}}}}}
	client := fake.NewClientset(d, oldRS)
	k := fixes.KubeApplier{Client: client}
	a := rules.Action{Kind: rules.ActionSetMemoryLimit, Namespace: "llm", WorkloadKind: "Deployment", WorkloadName: "llm-server", Container: "llama-server", Value: "768Mi"}
	if res, err := k.Apply(ctx, a); err != nil || !strings.Contains(res, "512Mi to 768Mi") {
		t.Fatalf("set memory: %q %v", res, err)
	}
	got, _ := client.AppsV1().Deployments("llm").Get(ctx, "llm-server", metav1.GetOptions{})
	c := got.Spec.Template.Spec.Containers[0]
	if c.Resources.Limits.Memory().String() != "768Mi" || c.Resources.Requests.Memory().String() != "512Mi" {
		t.Errorf("limits %v requests %v", c.Resources.Limits.Memory(), c.Resources.Requests.Memory())
	}
	if _, err := k.Apply(ctx, a); err == nil {
		t.Error("applying the same limit twice should say it's already done")
	}

	a.Kind = rules.ActionRolloutUndo
	if res, err := k.Apply(ctx, a); err != nil || !strings.Contains(res, "revision 1") {
		t.Fatalf("undo: %q %v", res, err)
	}
	got, _ = client.AppsV1().Deployments("llm").Get(ctx, "llm-server", metav1.GetOptions{})
	if got.Spec.Template.Spec.Containers[0].Image != "old" || got.Spec.Template.Labels["pod-template-hash"] != "" {
		t.Errorf("undo did not restore the old template: %+v", got.Spec.Template)
	}

	a.Kind = rules.ActionRestart
	if _, err := k.Apply(ctx, a); err != nil {
		t.Fatalf("restart: %v", err)
	}
	got, _ = client.AppsV1().Deployments("llm").Get(ctx, "llm-server", metav1.GetOptions{})
	if got.Spec.Template.Annotations["kubectl.kubernetes.io/restartedAt"] == "" {
		t.Error("restart annotation missing")
	}
}

func TestFollowUpWhenActionChanges(t *testing.T) {
	ctx := context.Background()
	m, db, _ := newManager(t, fixes.Policy{Default: fixes.ModeApprove, Namespaces: []string{"llm"}})
	f := oomFinding(3)
	_ = m.Notify(ctx, rules.Event{Type: rules.EventOpened, Finding: f})
	// Still pending: a changed action doesn't stack a second request.
	f.Fix.Action.Value = "1152Mi"
	_ = m.Notify(ctx, rules.Event{Type: rules.EventActionChanged, Finding: f})
	if all, _ := db.Fixes(ctx, "", 10); len(all) != 1 {
		t.Fatalf("want 1 request while one is pending, got %d", len(all))
	}
	first, _ := db.Fixes(ctx, fixes.StatusPending, 1)
	if _, err := m.Approve(ctx, first[0].ID, "shiva", ""); err != nil {
		t.Fatal(err)
	}
	// Applied, but the problem is still there and needs a bigger step.
	_ = m.Notify(ctx, rules.Event{Type: rules.EventActionChanged, Finding: f})
	pending, _ := db.Fixes(ctx, fixes.StatusPending, 10)
	if len(pending) != 1 || pending[0].Action.Value != "1152Mi" {
		t.Errorf("want a follow-up for 1152Mi, got %+v", pending)
	}
	if r, _ := db.Fix(ctx, first[0].ID); r.Status != fixes.StatusSuperseded {
		t.Errorf("the step that didn't solve it should be superseded, got %s", r.Status)
	}
	// The follow-up works: only it is verified.
	_, _ = m.Approve(ctx, pending[0].ID, "shiva", "")
	_ = m.Notify(ctx, rules.Event{Type: rules.EventResolved, Finding: f})
	if v, _ := db.Fixes(ctx, fixes.StatusVerified, 10); len(v) != 1 || v[0].ID != pending[0].ID {
		t.Errorf("want only the last step verified, got %+v", v)
	}
}
