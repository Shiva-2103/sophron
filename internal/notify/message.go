package notify

import (
	"fmt"
	"strings"
	"time"

	"github.com/Shiva-2103/sophron/internal/rules"
)

// EventTest marks a test message sent by --send-test-alert.
const EventTest = "test"

// Slack limits (https://api.slack.com/reference/block-kit/blocks).
const (
	headerLimit  = 150
	sectionLimit = 3000
	fieldLimit   = 2000
	maxEvidence  = 10
)

// Colors of the bar on the left of the message.
const (
	colorCritical = "#E01E5A"
	colorWarning  = "#F2A93B"
	colorInfo     = "#36C5F0"
	colorResolved = "#2EB67D"
	colorTest     = "#7C5CFF"
)

// Message is a Slack webhook payload.
//
// Layout: a bold header line on top (what and how bad), then a coloured
// card with the facts in two columns, what's wrong, the evidence, the fix
// and a footer. Like a good alert email: the subject tells you whether to
// open it, the body tells you what to do.
type Message struct {
	Text        string       `json:"text"` // shown in notifications and old clients
	Blocks      []Block      `json:"blocks"`
	Attachments []Attachment `json:"attachments"`
}

// Attachment holds the coloured card.
type Attachment struct {
	Color  string  `json:"color"`
	Blocks []Block `json:"blocks"`
}

// Block is one Block Kit block.
type Block struct {
	Type     string `json:"type"`
	Text     *Text  `json:"text,omitempty"`
	Fields   []Text `json:"fields,omitempty"`
	Elements []any  `json:"elements,omitempty"` // Text, or buttons in an actions block
}

// Text is a Block Kit text object.
type Text struct {
	Type  string `json:"type"` // plain_text or mrkdwn
	Text  string `json:"text"`
	Emoji bool   `json:"emoji,omitempty"`
}

// Message builds the Slack payload for an event.
func (s *Slack) Message(e rules.Event) Message {
	f := e.Finding
	sev := f.Severity
	label, color, status := strings.ToUpper(string(sev)), severityColor(sev), "Open"

	switch e.Type {
	case rules.EventResolved:
		label, color = "RESOLVED", colorResolved
	case rules.EventEscalate:
		label, status = "ESCALATED TO "+strings.ToUpper(string(sev)), "Open, got worse"
	case EventTest:
		label, color, status = "TEST", colorTest, "Test, no action needed"
	case rules.EventActionChanged:
		label, status = "NEXT STEP", "Open, the last fix wasn't enough"
	}

	title := f.Title
	if title == "" {
		title = f.Target()
	}
	headline := fmt.Sprintf("%s %s · %s", icon(e.Type, sev), label, title)
	fallback := fmt.Sprintf("[%s] %s | %s | %s", label, s.cfg.Cluster, f.Target(), title)

	var card []Block
	if e.Type == rules.EventResolved {
		card = s.resolvedCard(f)
	} else {
		card = s.problemCard(f, status, e.Type == EventTest)
	}

	return Message{
		Text:        truncate(fallback, sectionLimit),
		Blocks:      []Block{{Type: "header", Text: &Text{Type: "plain_text", Text: truncate(headline, headerLimit), Emoji: true}}},
		Attachments: []Attachment{{Color: color, Blocks: card}},
	}
}

func (s *Slack) problemCard(f rules.Finding, status string, test bool) []Block {
	seen := f.FirstSeen
	if seen.IsZero() {
		seen = s.now()
	}
	blocks := []Block{fields(
		"Status", status,
		"Severity", titleCase(string(f.Severity)),
		"Cluster", s.cfg.Cluster,
		"Workload", workload(f),
		"Pod", code(f.Pod),
		"Detected", s.when(seen),
	)}

	if f.Summary != "" {
		blocks = append(blocks, section("*What's wrong*\n"+esc(f.Summary)))
	}
	if len(f.Evidence) > 0 {
		var b strings.Builder
		b.WriteString("*Evidence*")
		for i, ev := range f.Evidence {
			if i == maxEvidence {
				fmt.Fprintf(&b, "\n• _…and %d more_", len(f.Evidence)-maxEvidence)
				break
			}
			b.WriteString("\n• " + esc(ev))
		}
		blocks = append(blocks, section(b.String()))
	}
	if f.Fix.Summary != "" || len(f.Fix.Steps) > 0 {
		var b strings.Builder
		b.WriteString("*Suggested fix*")
		if f.Fix.Summary != "" {
			b.WriteString("\n" + esc(f.Fix.Summary))
		}
		for i, st := range f.Fix.Steps {
			fmt.Fprintf(&b, "\n%d. %s", i+1, esc(st))
		}
		blocks = append(blocks, section(b.String()))
	}
	if a := f.Fix.Action; a != nil && !test {
		where := "Approve or deny it in the Sophron dashboard, under Approvals."
		if s.cfg.Dashboard != "" {
			where = fmt.Sprintf("<%s/#/approvals|Approve or deny it in Sophron>", strings.TrimRight(s.cfg.Dashboard, "/"))
		}
		if req, ok := s.pendingFix(f.ID); ok && s.cfg.Interactive {
			blocks = append(blocks, section("*Sophron can do this for you*\n"+esc(a.Summary)), approveButtons(req.ID, a.Summary))
		} else {
			blocks = append(blocks, section("*Sophron can do this for you*\n"+esc(a.Summary)+"\n"+where))
		}
	}
	if snip := strings.TrimSpace(f.Fix.Snippet); snip != "" {
		// Leave room for the fences.
		blocks = append(blocks, section("```"+truncate(esc(snip), sectionLimit-10)+"```"))
	}

	foot := s.footer(f)
	if test {
		foot = "This is a sample finding sent by `--send-test-alert`. Real alerts look exactly like this."
	}
	return append(blocks, Block{Type: "divider"}, contextBlock(foot))
}

func (s *Slack) resolvedCard(f rules.Finding) []Block {
	end := f.ResolvedAt
	if end.IsZero() {
		end = s.now()
	}
	open := "unknown"
	if !f.FirstSeen.IsZero() {
		open = humanDuration(end.Sub(f.FirstSeen))
	}
	blocks := []Block{fields(
		"Status", "Resolved",
		"Was", titleCase(string(f.Severity)),
		"Cluster", s.cfg.Cluster,
		"Workload", workload(f),
		"Open for", open,
		"Resolved", s.when(end),
	)}
	if r := f.Resolution; r != nil {
		var b strings.Builder
		b.WriteString("*How it was resolved*\n" + esc(r.Summary))
		for _, c := range r.Changes {
			b.WriteString("\n• " + esc(c))
		}
		blocks = append(blocks, section(b.String()))

		changed := "No change recorded"
		switch {
		case r.ChangedBy != "":
			changed = code(r.ChangedBy) + "\n" + s.when(r.ChangedAt)
		case strings.HasPrefix(r.Summary, "Cleared"):
			changed = "Nothing: it cleared on its own"
		}
		approved := "Not needed: Sophron changed nothing"
		if r.ApprovedBy != "" {
			approved = esc(r.ApprovedBy)
		}
		kv := []string{"Changed with", changed, "Approved by", approved}
		if r.DownSeconds > 0 {
			cost := "not priced yet"
			if r.DownCost != nil {
				cost = money(*r.DownCost)
			}
			kv = append(kv, "Not serving for", humanDuration(time.Duration(r.DownSeconds*float64(time.Second))), "Cost while down", cost)
		}
		blocks = append(blocks, fields(kv...))
	}
	return append(blocks, contextBlock(s.footer(f)))
}

func (s *Slack) footer(f rules.Finding) string {
	id := ""
	if f.ID > 0 {
		id = fmt.Sprintf(" · finding #%d", f.ID)
	}
	return fmt.Sprintf("Automated message from *Sophron* · detector `%s`%s · nothing changes until someone approves", esc(f.Detector), id)
}

// when formats a time like the alert emails do:
// "15 Nov 2026, 01:20 PM IST (07:50 UTC)".
func (s *Slack) when(t time.Time) string {
	local := t.In(s.cfg.Location).Format("02 Jan 2006, 03:04 PM MST")
	if s.cfg.Location == time.UTC {
		return local
	}
	return local + " (" + t.UTC().Format("15:04") + " UTC)"
}

func workload(f rules.Finding) string {
	if f.WorkloadName == "" {
		return code(f.Namespace + "/" + f.Pod)
	}
	return code(strings.ToLower(f.WorkloadKind)+"/"+f.WorkloadName) + "\nnamespace " + code(f.Namespace)
}

func fields(kv ...string) Block {
	b := Block{Type: "section"}
	for i := 0; i+1 < len(kv); i += 2 {
		v := kv[i+1]
		if v == "" {
			v = "–"
		}
		b.Fields = append(b.Fields, Text{Type: "mrkdwn", Text: truncate("*"+kv[i]+"*\n"+v, fieldLimit)})
	}
	return b
}

func section(md string) Block {
	return Block{Type: "section", Text: &Text{Type: "mrkdwn", Text: truncate(md, sectionLimit)}}
}

func contextBlock(md string) Block {
	return Block{Type: "context", Elements: []any{Text{Type: "mrkdwn", Text: truncate(md, fieldLimit)}}}
}

func code(s string) string {
	if s == "" {
		return ""
	}
	return "`" + esc(s) + "`"
}

// esc escapes the three characters Slack's mrkdwn treats as control
// characters, so pod names and messages can't turn into links or mentions.
func esc(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

// truncate cuts s to at most n characters (runes), ending with "…".
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func severityColor(s rules.Severity) string {
	switch s {
	case rules.Critical:
		return colorCritical
	case rules.Warning:
		return colorWarning
	}
	return colorInfo
}

func icon(event string, s rules.Severity) string {
	switch event {
	case rules.EventResolved:
		return "✅"
	case rules.EventEscalate:
		return "⏫"
	case rules.EventActionChanged:
		return "🔁"
	case EventTest:
		return "🧪"
	}
	switch s {
	case rules.Critical:
		return "🔴"
	case rules.Warning:
		return "🟠"
	}
	return "🔵"
}

func titleCase(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// money prints USD with cents, or more digits for tiny amounts.
func money(v float64) string {
	if v > 0 && v < 0.01 {
		return fmt.Sprintf("$%.4f", v)
	}
	return fmt.Sprintf("$%.2f", v)
}

func humanDuration(d time.Duration) string {
	d = d.Round(time.Second)
	h, m, sec := int(d.Hours()), int(d.Minutes())%60, int(d.Seconds())%60
	switch {
	case h > 0:
		return fmt.Sprintf("%dh %dm", h, m)
	case m > 0:
		return fmt.Sprintf("%dm %ds", m, sec)
	default:
		return fmt.Sprintf("%ds", sec)
	}
}

// SampleFinding is what --send-test-alert sends: a realistic OOM finding,
// so you can see a real alert's layout in your channel.
func SampleFinding(now time.Time) rules.Finding {
	return rules.Finding{
		Key: "test/llm/llm-server", Detector: "oom-killed", Severity: rules.Critical,
		Namespace: "llm", Pod: "llm-server-7d9f8b6c5-x2k4p", WorkloadKind: "Deployment", WorkloadName: "llm-server",
		Title:   "llm/llm-server is being killed for using too much memory",
		Summary: "The model server container used more than its 512Mi memory limit and the kernel killed it (OOMKilled). It restarted 2 times in the last 15 minutes and is not serving requests.",
		Evidence: []string{
			"last exit: OOMKilled (exit code 137), 40s ago",
			"memory limit 512Mi, peak use 498 MiB",
			"2 restarts in 15m, pod not Ready",
			"llama.cpp: --ctx-size 32768 --parallel 4 (KV cache grows with both)",
		},
		Fix: rules.Fix{
			Summary: "Give the container more memory, or make the model need less.",
			Steps: []string{
				"Raise the memory limit to 768Mi (max of limit × 1.5 and peak × 1.3)",
				"Or lower `--ctx-size` / `--parallel`: the KV cache needs memory for every slot",
				"Watch it: `kubectl -n llm get pods -w`",
			},
			Snippet: "kubectl -n llm set resources deployment/llm-server -c llama-server --limits=memory=768Mi",
		},
		Status: rules.StatusOpen, FirstSeen: now, LastSeen: now,
	}
}

// Button action IDs, matched when Slack sends the click back.
const (
	ActionApprove = "sophron_approve"
	ActionDeny    = "sophron_deny"
)

func approveButtons(id int64, summary string) Block {
	value := fmt.Sprint(id)
	pt := func(t string) Text { return Text{Type: "plain_text", Text: t} }
	return Block{Type: "actions", Elements: []any{
		map[string]any{"type": "button", "action_id": ActionApprove, "value": value, "style": "primary", "text": pt("Approve fix"),
			"confirm": map[string]any{"title": pt("Apply this fix?"), "text": Text{Type: "mrkdwn", Text: truncate(esc(summary), 300)},
				"confirm": pt("Approve"), "deny": pt("Cancel")}},
		map[string]any{"type": "button", "action_id": ActionDeny, "value": value, "style": "danger", "text": pt("Deny")},
	}}
}
