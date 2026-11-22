# Savings, approvals and the dashboard

---

## ***Overview***

Three pieces turn Sophron from "it tells you" into "it helps you act, safely":

| Piece | What it does |
|---|---|
| **Savings** | Looks at a week of history and says what to change to spend less, with the evidence and the command |
| **Fixes** | Turns a finding's suggested action into a fix request. The policy decides if it may run; an admin approves; Sophron applies it, then checks the problem is gone |
| **Dashboard** | Everything in a browser, in the Terracotta & Stone design, with sign-in and roles |

> The rule that holds it together: **nothing changes without the policy and a named person saying yes**, and every step is written to a tamper-evident audit log.

---

## ***What we built***

| Part | File | Job |
|---|---|---|
| Savings | `internal/cost/savings.go` | Right-size CPU and memory requests, idle models, idle nodes, cheaper node fit |
| Actions | `internal/rules/finding.go` | `set-memory-limit`, `rollout-undo`, `scale-up`, `rollout-restart`, attached by detectors |
| Fixes | `internal/fixes/fixes.go` | Policy, fix request lifecycle, follow-up steps, verification |
| Applier | `internal/fixes/apply.go` | Makes the change with client-go, as field manager `sophron` |
| Fix and audit storage | `internal/store/fixes.go` | `fix_requests` table; `audit_log` with a SHA-256 hash chain |
| Sign-in | `internal/auth/auth.go` | Admin and viewer passwords from a Secret, sessions, rate limit |
| API | `internal/api/actions.go` | Login, fixes, approve/deny, savings, trend, audit, setup; CSRF guard and security headers |
| Dashboard | `internal/web/static/` | Plain HTML/CSS/JS embedded in the binary: no Node, no build step |
| Doctor | `cmd/collector/doctor.go` | Setup checks with the exact fix for each |
| Demo | `hack/demo/main.go` | The real dashboard on a day of sample data: `make demo` |
| Helm chart | `charts/sophron/` | One install: RBAC, fixer Roles per allowed namespace, generated admin password |
| Lab loop | `lab/scripts/check-e2e.sh` | Break → alert → approve → fixed → receipt, for real |

---

## ***How a fix flows***

1. A detector finds a problem and attaches an **action**, e.g. OOM → "raise the memory limit of llama-server to 768Mi".
2. The fixes manager asks the **policy**:

| Mode | What happens |
|---|---|
| `never` (or namespace not listed) | Blocked: shown, never runs, even if someone approves |
| `approve` (default) | Pending: waits on the Approvals page |
| `auto` | Applied straight away, recorded as approved by "policy" |

3. An **admin approves** (viewers can't). The API checks the role, the policy again, and that the request is still pending.
4. The **applier** makes the change in Kubernetes as field manager `sophron`.
5. If the problem is **still there** and the detector now suggests a bigger step (768Mi → 1152Mi), a **follow-up request** opens. Each step needs its own approval.
6. When the finding **resolves**, the fix becomes **verified**, and the resolved Slack alert says "changed with sophron, approved by admin".

Every step is an audit entry: requested, approved, applied, failed, verified, obsolete, blocked, login.

---

## ***Approving from Slack***

Alerts with a fix carry **Approve fix** (with a confirm dialog) and **Deny** buttons.

| Piece | How |
|---|---|
| Transport | Slack Socket Mode: Sophron opens a WebSocket to Slack, so the cluster needs no public URL |
| Token | An app-level token (`xapp-…`, scope `connections:write`) in the `sophron-slack` Secret, mounted as a file |
| Who may approve | Only Slack member IDs in `SOPHRON_SLACK_APPROVERS`; anyone else gets a private "you're not an approver" |
| What's recorded | The audit log says `slack:<username>` approved it; the channel gets "✅ Fix #2 approved by …" |
| Next steps | If a fix isn't enough, a 🔁 NEXT STEP alert arrives with buttons for the bigger step |

---

## ***Security***

| Layer | How |
|---|---|
| Who can sign in | Admin and viewer passwords in the `sophron-admin` Secret; no password = read-only, nothing can be approved |
| Sessions | Random 256-bit token, HttpOnly, SameSite=Strict, Secure behind HTTPS, 12 h |
| Guessing | 5 failed logins per minute per IP, then refused |
| Cross-site requests | Every POST needs the `X-Sophron` header, which a form on another site can't send |
| Browser | Content-Security-Policy `default-src 'self'`, no third-party scripts, `X-Frame-Options: DENY` |
| Kubernetes | Read-only cluster-wide; write access only through a `sophron-fixer` Role in each namespace the policy lists |
| Audit | Each entry's hash covers the previous one; Setup recomputes the chain and flags any edit |

> Kubernetes still records *which tool* changed a workload (`sophron`). The audit log records *which person* approved it.

---

## ***Savings: how each is worked out***

| Saving | Rule | Saving = |
|---|---|---|
| Lower CPU request | p95 CPU use × 1.2 is under 80% of the request | (request − new) × $/core × 730 × pods |
| Raise CPU request | p95 use is over 1.2 × the request | 0, for reliability |
| Lower memory request | Highest memory use × 1.25 is under 80% of the request | (request − new) × $/GiB × 730 × pods |
| Idle model | Producing tokens under 10% of the time, 6 h+ of data | Up to its cost × idle share |
| Idle node | Over 50% of the node reserved by no pod | Idle cost × 730 |
| Cheaper node fit | A cheaper node type already in the cluster has room (10% margin) | Price difference × 730 |

Confidence is **high** with 7 days and 1,000+ samples, **medium** with a day, **low** otherwise.

---

## ***Dashboard audit (22 Nov)***

A pass over every page, in light and dark, at desktop and phone width, with a browser driving it.

| Area | Before | After |
|---|---|---|
| Phone | The sidebar was hidden and nothing replaced it: no way to change page | A scrollable nav bar under the header; tables scroll sideways; KPIs two per row |
| Models | Clicking a model opened Problems | Each model has its own page: KPIs, token/memory/CPU charts, pods, history, API comparison, its savings |
| Charts | The first and current hour looked like a drop in spend (they're only partly covered); no axis labels | Spend and tokens are shown per hour of coverage; time axis, top value, legend and hover values |
| Freshness | "Updated 10:31" even if the collector had stopped | "Live" or "Data from 4 min ago"; a banner when no data for 3 min |
| Approve | One click changed the cluster | First click arms the button, the second applies it (like Slack's confirm); a typed reason survives the 15 s refresh |
| Tabs | Problems → Resolved and the Activity filters didn't switch | Fixed: the "don't redraw while typing" guard now only applies to text fields |
| Robustness | A finding without a fix, a fix without an action, or a node priced at $0 could break a page; cost off showed "Can't reach the API" | Guarded; cost off says so and how to turn it on |
| Content | — | Overview: spend split (models, other, idle), "Could save" total, recent activity. Problems sorted by severity, with the waiting fix. Approvals: why a fix was superseded or obsolete. Copy buttons on snippets. Tab title shows open problems |
| Savings | Suggested lowering memory on a model that was being OOM-killed; "1 GPUs"; "(168h0m0s)" | No memory cut after an OOM kill (usage is capped by the limit, so the peak understates the need); "1 GPU"; "in the last 7 days" |

---

## ***Commands***

| Command | Does |
|---|---|
| `make admin-password` | Creates the admin and viewer logins (once) and prints them |
| `make dashboard-allow-me` | Opens port 8081 in the security group for your IP only |
| `make dashboard` | Serves the dashboard at http://<public-ip>:8081, restarting the port-forward if the collector restarts |
| `make dashboard-private` | Serves it on the machine only; prints the SSM port-forward for your laptop |
| `make demo` | The dashboard on sample data, no cluster (http://localhost:8090, admin / demo-password-1) |
| `make doctor` | Setup checks |
| `make savings` / `make fixes` | Savings suggestions / fix requests |
| `make e2e-check` | The whole loop in the lab, about 5–15 minutes |

---

## ***Interview questions you can now answer***

- How do you let an automated system change production safely?
- What's the difference between authentication, authorization and a policy?
- How do you make an audit log tamper-evident?
- How do you protect a cookie-based dashboard from CSRF?
- How do you right-size a pod's requests, and why p95 for CPU but the peak for memory?
- Why does `kubectl rollout undo` copy a ReplicaSet's template back into the Deployment?

---

> **Issues Faced & Resolutions**
>
> | Issue | Resolution |
> |---|---|
> | The OOM chaos needs about 2 GiB, but the first suggested step is 768Mi, so one fix wouldn't solve it | Follow-up requests: when the action changes while the finding is open, a new request opens; each step needs approval |
> | Slack would have sent a message for every internal event type | Slack only sends opened, escalated and resolved |
> | The lab scripts broke once the API required sign-in | `lab/scripts/auth.sh` reads the admin password from the Secret into memory and passes it to curl |
> | Snapshots didn't know which container to change | Snapshots now carry the container that last failed, else the first one |
> | A failed login (401) bounced back to the login form and lost the error | The API client only redirects on 401 for calls other than login |
> | In the first lab run, the two memory steps that weren't enough were also marked "verified" | Steps a follow-up replaces become **superseded**; only the step that ends the problem is verified |
> | The receipt said "1.2Gi" for 1280Mi | Quantities that aren't whole GiB print in Mi |
> | Anyone in the Slack channel could press Approve | Only Slack member IDs on the approvers list can; others get a private refusal |
> | Slack said "not configured to handle interactive responses" | Creating the app token isn't enough: Socket Mode and Interactivity must both be switched on in the app settings |
> | `kubectl port-forward` died every time the collector restarted, so the dashboard went away | `make dashboard` runs it in a restart loop, on 0.0.0.0:8081 |
> | Clicking a tab on Problems or Activity did nothing | Rendering skipped the page while anything in it had focus, including the tab just clicked; now only text fields block a redraw |
> | Savings suggested less memory for a workload being OOM-killed | Memory cuts are skipped when any snapshot in the window ended in OOMKilled |
> | Couldn't download Helm in the build sandbox | CI lints and renders the chart, then validates it with kubeconform |
{.is-danger}

> **Useful Links**
>
> - [Kubernetes RBAC](https://kubernetes.io/docs/reference/access-authn-authz/rbac/)
> - [SelfSubjectAccessReview](https://kubernetes.io/docs/reference/access-authn-authz/authorization/#checking-api-access)
> - [OWASP CSRF prevention](https://cheatsheetseries.owasp.org/cheatsheets/Cross-Site_Request_Forgery_Prevention_Cheat_Sheet.html)
> - [Content Security Policy](https://developer.mozilla.org/en-US/docs/Web/HTTP/CSP)
> - [AWS SSM port forwarding](https://docs.aws.amazon.com/systems-manager/latest/userguide/session-manager-working-with-sessions-start.html#sessions-remote-port-forwarding)
{.is-info}

-----
