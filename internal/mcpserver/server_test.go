package mcpserver

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/ruiheng/agent-mailbox/internal/mailbox"
)

type fakeRunner struct {
	t       *testing.T
	handler func(args []string, input string) (RunResult, error)

	mu    sync.Mutex
	calls []runnerCall
}

type runnerCall struct {
	Args  []string
	Input string
}

func (r *fakeRunner) Run(_ context.Context, args []string, input string) (RunResult, error) {
	r.mu.Lock()
	r.calls = append(r.calls, runnerCall{Args: append([]string(nil), args...), Input: input})
	r.mu.Unlock()
	return r.handler(args, input)
}

func (r *fakeRunner) Calls() []runnerCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]runnerCall(nil), r.calls...)
}

func TestValidateSendReceipt(t *testing.T) {
	receipt, err := validateSendReceipt(parseSendTokens("delivery_id=dlv_1"), "delivery_id=dlv_1")
	if err != nil {
		t.Fatalf("validateSendReceipt returned error: %v", err)
	}
	if receipt.DeliveryID != "dlv_1" {
		t.Fatalf("delivery id = %q, want dlv_1", receipt.DeliveryID)
	}

	if _, err := validateSendReceipt(parseSendTokens("message_id=msg_1 blob_id=blob_1"), "message_id=msg_1 blob_id=blob_1"); err == nil {
		t.Fatal("validateSendReceipt should reject incomplete receipts")
	}
}

func TestEnsureReceiverWorkflowHint(t *testing.T) {
	hint := ensureReceiverWorkflowHint("Handle the request.", defaultListenerMessage, "coder-123")
	if !strings.Contains(hint, "check-agent-mail") {
		t.Fatalf("hint %q does not mention check-agent-mail", hint)
	}
	if !strings.Contains(hint, "mailbox_read") || !strings.Contains(hint, "acked") {
		t.Fatalf("hint %q does not include mailbox recovery guidance", hint)
	}

	plannerHint := ensureReceiverWorkflowHint("Handle the request.", defaultListenerMessage, "planner")
	if strings.Contains(plannerHint, "mailbox_read") {
		t.Fatalf("planner hint %q unexpectedly includes recovery guidance", plannerHint)
	}
}

func TestMailboxSendNotifiesWorkerTarget(t *testing.T) {
	mailboxRunner := &fakeRunner{t: t, handler: func(args []string, input string) (RunResult, error) {
		wantArgs := []string{
			"send",
			"--to", "agent-deck/target",
			"--from", "agent-deck/self",
			"--subject", "delegate",
			"--body-file", "-",
		}
		if strings.Join(args, "\x00") != strings.Join(wantArgs, "\x00") {
			t.Fatalf("mailbox args = %v, want %v", args, wantArgs)
		}
		if input != "body" {
			t.Fatalf("mailbox input = %q, want body", input)
		}
		return RunResult{ExitCode: 0, Stdout: "delivery_id=dlv_1\n"}, nil
	}}

	commandRunner := &fakeRunner{t: t, handler: func(args []string, input string) (RunResult, error) {
		switch {
		case strings.Join(args, "\x00") == strings.Join([]string{"agent-deck", "session", "show", "target", "--json"}, "\x00"):
			return RunResult{ExitCode: 0, Stdout: `{"id":"target","title":"coder-123","status":"waiting"}`}, nil
		case len(args) == 6 && args[0] == "agent-deck" && args[1] == "session" && args[2] == "send":
			if args[4] != "target" {
				t.Fatalf("notify target = %q, want target", args[4])
			}
			if !strings.Contains(args[5], "check-agent-mail") {
				t.Fatalf("notify message %q missing check-agent-mail", args[5])
			}
			if !strings.Contains(args[5], "mailbox_read") {
				t.Fatalf("notify message %q missing mailbox recovery hint", args[5])
			}
			return RunResult{ExitCode: 0}, nil
		default:
			t.Fatalf("unexpected command args: %v", args)
			return RunResult{}, nil
		}
	}}

	service := newService(Options{
		MailboxRunner: mailboxRunner,
		CommandRunner: commandRunner,
	})
	service.state.boundAddresses = []string{"agent-deck/self"}
	service.state.defaultSender = "agent-deck/self"
	service.state.autoBindAttempted = true

	output := callTool(t, service.Server(), "mailbox_send", map[string]any{
		"to_address": "agent-deck/target",
		"subject":    "delegate",
		"body":       "body",
	})

	if got := output["delivery_id"]; got != "dlv_1" {
		t.Fatalf("delivery_id = %v, want dlv_1", got)
	}
	if got := output["notify_status"]; got != "sent" {
		t.Fatalf("notify_status = %v, want sent", got)
	}
	if got := output["notify_scheme"]; got != "agent-deck" {
		t.Fatalf("notify_scheme = %v, want agent-deck", got)
	}
	if got := output["notify_error"]; got != nil {
		t.Fatalf("notify_error = %v, want nil", got)
	}
}

func TestMailboxSendAllowsAgentDeckNotifyDisable(t *testing.T) {
	mailboxRunner := &fakeRunner{t: t, handler: func(args []string, input string) (RunResult, error) {
		return RunResult{ExitCode: 0, Stdout: "delivery_id=dlv_disabled\n"}, nil
	}}

	service := newService(Options{
		MailboxRunner: mailboxRunner,
		CommandRunner: &fakeRunner{t: t, handler: func(args []string, input string) (RunResult, error) {
			t.Fatalf("unexpected command call: %v", args)
			return RunResult{}, nil
		}},
	})
	service.state.boundAddresses = []string{"agent-deck/self"}
	service.state.defaultSender = "agent-deck/self"
	service.state.autoBindAttempted = true

	output := callTool(t, service.Server(), "mailbox_send", map[string]any{
		"to_address":     "agent-deck/target",
		"subject":        "delegate",
		"body":           "body",
		"notify_message": "",
	})

	if got := output["delivery_id"]; got != "dlv_disabled" {
		t.Fatalf("delivery_id = %v, want dlv_disabled", got)
	}
	if got := output["notify_status"]; got != "skipped_disabled" {
		t.Fatalf("notify_status = %v, want skipped_disabled", got)
	}
	if got := output["notify_scheme"]; got != "agent-deck" {
		t.Fatalf("notify_scheme = %v, want agent-deck", got)
	}
	if got := output["notify_error"]; got != nil {
		t.Fatalf("notify_error = %v, want nil", got)
	}
}

func TestMailboxSendPreservesCustomNotifyMessage(t *testing.T) {
	const customNotify = "Check the delegated task immediately."

	mailboxRunner := &fakeRunner{t: t, handler: func(args []string, input string) (RunResult, error) {
		return RunResult{ExitCode: 0, Stdout: "delivery_id=dlv_custom\n"}, nil
	}}

	commandRunner := &fakeRunner{t: t, handler: func(args []string, input string) (RunResult, error) {
		switch {
		case strings.Join(args, "\x00") == strings.Join([]string{"agent-deck", "session", "show", "target", "--json"}, "\x00"):
			return RunResult{ExitCode: 0, Stdout: `{"id":"target","title":"coder-123","status":"waiting"}`}, nil
		case len(args) == 6 && args[0] == "agent-deck" && args[1] == "session" && args[2] == "send":
			if args[4] != "target" {
				t.Fatalf("notify target = %q, want target", args[4])
			}
			if !strings.Contains(args[5], customNotify) {
				t.Fatalf("notify message %q missing custom override", args[5])
			}
			if strings.Contains(args[5], defaultNotifyMessage) {
				t.Fatalf("notify message %q unexpectedly replaced custom override", args[5])
			}
			if !strings.Contains(args[5], "mailbox_read") {
				t.Fatalf("notify message %q missing mailbox recovery hint", args[5])
			}
			return RunResult{ExitCode: 0}, nil
		default:
			t.Fatalf("unexpected command args: %v", args)
			return RunResult{}, nil
		}
	}}

	service := newService(Options{
		MailboxRunner: mailboxRunner,
		CommandRunner: commandRunner,
	})
	service.state.boundAddresses = []string{"agent-deck/self"}
	service.state.defaultSender = "agent-deck/self"
	service.state.autoBindAttempted = true

	output := callTool(t, service.Server(), "mailbox_send", map[string]any{
		"to_address":     "agent-deck/target",
		"subject":        "delegate",
		"body":           "body",
		"notify_message": customNotify,
	})

	if got := output["delivery_id"]; got != "dlv_custom" {
		t.Fatalf("delivery_id = %v, want dlv_custom", got)
	}
	if got := output["notify_status"]; got != "sent" {
		t.Fatalf("notify_status = %v, want sent", got)
	}
	if got := output["notify_scheme"]; got != "agent-deck" {
		t.Fatalf("notify_scheme = %v, want agent-deck", got)
	}
	if got := output["notify_error"]; got != nil {
		t.Fatalf("notify_error = %v, want nil", got)
	}
}

func TestMailboxSendPreservesMailboxDefaultsWhenMetadataOmitted(t *testing.T) {
	mailboxRunner := &fakeRunner{t: t, handler: func(args []string, input string) (RunResult, error) {
		for _, token := range args {
			if token == "--content-type" || token == "--schema-version" {
				t.Fatalf("unexpected explicit metadata flag in args: %v", args)
			}
		}
		return RunResult{ExitCode: 0, Stdout: "delivery_id=dlv_2\n"}, nil
	}}

	service := newService(Options{
		MailboxRunner: mailboxRunner,
		CommandRunner: &fakeRunner{t: t, handler: func(args []string, input string) (RunResult, error) {
			t.Fatalf("unexpected command call: %v", args)
			return RunResult{}, nil
		}},
	})
	service.state.boundAddresses = []string{"agent-deck/self"}
	service.state.defaultSender = "agent-deck/self"
	service.state.autoBindAttempted = true

	output := callTool(t, service.Server(), "mailbox_send", map[string]any{
		"to_address":   "agent-deck/self",
		"subject":      "delegate",
		"body":         "body",
		"from_address": "agent-deck/self",
	})

	if got := output["delivery_id"]; got != "dlv_2" {
		t.Fatalf("delivery_id = %v, want dlv_2", got)
	}
	if got := output["notify_status"]; got != "skipped_local" {
		t.Fatalf("notify_status = %v, want skipped_local", got)
	}
}

func TestMailboxSendReturnsReceiptWhenNotifyFails(t *testing.T) {
	mailboxRunner := &fakeRunner{t: t, handler: func(args []string, input string) (RunResult, error) {
		return RunResult{ExitCode: 0, Stdout: "delivery_id=dlv_3\n"}, nil
	}}
	commandRunner := &fakeRunner{t: t, handler: func(args []string, input string) (RunResult, error) {
		switch {
		case strings.Join(args, "\x00") == strings.Join([]string{"agent-deck", "session", "show", "target", "--json"}, "\x00"):
			return RunResult{ExitCode: 0, Stdout: `{"id":"target","title":"coder-123","status":"waiting"}`}, nil
		case len(args) == 6 && args[0] == "agent-deck" && args[1] == "session" && args[2] == "send":
			return RunResult{ExitCode: 1, Stderr: "wakeup failed"}, nil
		default:
			t.Fatalf("unexpected command args: %v", args)
			return RunResult{}, nil
		}
	}}

	service := newService(Options{
		MailboxRunner: mailboxRunner,
		CommandRunner: commandRunner,
	})
	service.state.boundAddresses = []string{"agent-deck/self"}
	service.state.defaultSender = "agent-deck/self"
	service.state.autoBindAttempted = true

	output := callTool(t, service.Server(), "mailbox_send", map[string]any{
		"to_address": "agent-deck/target",
		"subject":    "delegate",
		"body":       "body",
	})

	if got := output["status"]; got != "sent" {
		t.Fatalf("status = %v, want sent", got)
	}
	if got := output["delivery_id"]; got != "dlv_3" {
		t.Fatalf("delivery_id = %v, want dlv_3", got)
	}
	if got := output["notify_status"]; got != "failed" {
		t.Fatalf("notify_status = %v, want failed", got)
	}
	if got := output["notify_scheme"]; got != "agent-deck" {
		t.Fatalf("notify_scheme = %v, want agent-deck", got)
	}
	if got := output["notify_error"]; got == nil || !strings.Contains(got.(string), "wakeup failed") {
		t.Fatalf("notify_error = %v, want wakeup failure detail", got)
	}
}

func TestMailboxReminderSubscribeUpsertsBySelectorAndRoute(t *testing.T) {
	var staleCalls [][]string
	mailboxRunner := &fakeRunner{t: t, handler: func(args []string, input string) (RunResult, error) {
		if len(args) == 0 || args[0] != "stale" {
			t.Fatalf("unexpected mailbox args: %v", args)
		}
		staleCalls = append(staleCalls, append([]string(nil), args...))
		return RunResult{ExitCode: 0, Stdout: "[]"}, nil
	}}

	service := newService(Options{
		MailboxRunner: mailboxRunner,
		CommandRunner: &fakeRunner{t: t, handler: func(args []string, input string) (RunResult, error) {
			t.Fatalf("unexpected command call: %v", args)
			return RunResult{}, nil
		}},
	})
	service.state.autoBindAttempted = true

	first := callTool(t, service.Server(), "mailbox_reminder_subscribe", map[string]any{
		"addresses":  []string{"agent-deck/b", "agent-deck/a", "agent-deck/a"},
		"route":      "agent-deck/worker",
		"older_than": "10m",
	})
	if got := first["updated"]; got != false {
		t.Fatalf("updated = %v, want false", got)
	}
	firstSub := first["subscription"].(map[string]any)
	firstSelector := firstSub["selector"].(map[string]any)
	if got := firstSelector["addresses"].([]any); len(got) != 2 || got[0] != "agent-deck/a" || got[1] != "agent-deck/b" {
		t.Fatalf("selector addresses = %v, want sorted deduped values", got)
	}

	second := callTool(t, service.Server(), "mailbox_reminder_subscribe", map[string]any{
		"addresses":  []string{"agent-deck/a", "agent-deck/b"},
		"route":      "agent-deck/worker",
		"older_than": "15m",
	})
	if got := second["updated"]; got != true {
		t.Fatalf("updated = %v, want true", got)
	}

	status := callTool(t, service.Server(), "mailbox_reminder_status", nil)
	subs := status["subscriptions"].([]any)
	if len(subs) != 1 {
		t.Fatalf("subscriptions = %d, want 1", len(subs))
	}
	entry := subs[0].(map[string]any)
	subscription := entry["subscription"].(map[string]any)
	policy := subscription["policy"].(map[string]any)
	if got := policy["older_than"]; got != "15m" {
		t.Fatalf("older_than = %v, want 15m", got)
	}

	if len(staleCalls) != 3 {
		t.Fatalf("stale call count = %d, want 3", len(staleCalls))
	}
	for _, args := range staleCalls {
		if strings.Join(args, "\x00") != strings.Join([]string{
			"stale",
			"--older-than", args[2],
			"--for", "agent-deck/a",
			"--for", "agent-deck/b",
			"--json",
		}, "\x00") {
			t.Fatalf("stale args = %v, want canonical sorted addresses", args)
		}
	}
}

func TestMailboxReminderUnsubscribeIsIdempotent(t *testing.T) {
	mailboxRunner := &fakeRunner{t: t, handler: func(args []string, input string) (RunResult, error) {
		if len(args) == 0 || args[0] != "stale" {
			t.Fatalf("unexpected mailbox args: %v", args)
		}
		return RunResult{ExitCode: 0, Stdout: "[]"}, nil
	}}

	service := newService(Options{
		MailboxRunner: mailboxRunner,
		CommandRunner: &fakeRunner{t: t, handler: func(args []string, input string) (RunResult, error) {
			t.Fatalf("unexpected command call: %v", args)
			return RunResult{}, nil
		}},
	})
	service.state.autoBindAttempted = true

	callTool(t, service.Server(), "mailbox_reminder_subscribe", map[string]any{
		"addresses":  []string{"agent-deck/a"},
		"route":      "agent-deck/worker",
		"older_than": "10m",
	})

	first := callTool(t, service.Server(), "mailbox_reminder_unsubscribe", map[string]any{
		"addresses": []string{"agent-deck/a"},
		"route":     "agent-deck/worker",
	})
	if got := first["removed"]; got != true {
		t.Fatalf("removed = %v, want true", got)
	}

	second := callTool(t, service.Server(), "mailbox_reminder_unsubscribe", map[string]any{
		"addresses": []string{"agent-deck/a"},
		"route":     "agent-deck/worker",
	})
	if got := second["removed"]; got != false {
		t.Fatalf("removed = %v, want false", got)
	}
}

func TestMailboxReminderSubscribeSupportsGroupViewsAndActivePush(t *testing.T) {
	callCount := 0
	mailboxRunner := &fakeRunner{t: t, handler: func(args []string, input string) (RunResult, error) {
		callCount++
		switch strings.Join(args, "\x00") {
		case strings.Join([]string{
			"stale",
			"--older-than", "10m",
			"--for", "group/a",
			"--as", "alice",
			"--json",
		}, "\x00"):
			return RunResult{ExitCode: 0, Stdout: "[]"}, nil
		case strings.Join([]string{
			"stale",
			"--older-than", "10m",
			"--for", "group/b",
			"--as", "bob",
			"--json",
		}, "\x00"):
			return RunResult{ExitCode: 0, Stdout: "[]"}, nil
		default:
			t.Fatalf("mailbox args = %v, want normalized group stale query", args)
			return RunResult{}, nil
		}
	}}

	service := newService(Options{
		MailboxRunner: mailboxRunner,
		CommandRunner: &fakeRunner{t: t, handler: func(args []string, input string) (RunResult, error) {
			t.Fatalf("unexpected command call: %v", args)
			return RunResult{}, nil
		}},
		DisableActiveReminderLoop: true,
	})
	service.state.autoBindAttempted = true

	output := callTool(t, service.Server(), "mailbox_reminder_subscribe", map[string]any{
		"group_views": []map[string]any{
			{"group_address": "group/b", "as_person": "bob"},
			{"group_address": "group/a", "as_person": "alice"},
			{"group_address": "group/a", "as_person": "alice"},
		},
		"route":       "agent-deck/worker",
		"older_than":  "10m",
		"active_push": true,
	})

	subscription := output["subscription"].(map[string]any)
	selector := subscription["selector"].(map[string]any)
	groupViews := selector["group_views"].([]any)
	if len(groupViews) != 2 {
		t.Fatalf("len(group_views) = %d, want 2", len(groupViews))
	}
	first := groupViews[0].(map[string]any)
	second := groupViews[1].(map[string]any)
	if first["group_address"] != "group/a" || first["as_person"] != "alice" {
		t.Fatalf("first group view = %v, want group/a alice", first)
	}
	if second["group_address"] != "group/b" || second["as_person"] != "bob" {
		t.Fatalf("second group view = %v, want group/b bob", second)
	}
	policy := subscription["policy"].(map[string]any)
	if got := policy["active_push"]; got != true {
		t.Fatalf("active_push = %v, want true", got)
	}
	if callCount != 2 {
		t.Fatalf("stale call count = %d, want 2", callCount)
	}
}

func TestProcessActiveReminderSubscriptionsConfirmsThenCooldowns(t *testing.T) {
	current := time.Date(2026, 4, 3, 1, 0, 0, 0, time.UTC)
	sendCount := 0

	mailboxRunner := &fakeRunner{t: t, handler: func(args []string, input string) (RunResult, error) {
		wantArgs := []string{
			"stale",
			"--older-than", "10m",
			"--for", "agent-deck/worker",
			"--json",
		}
		if strings.Join(args, "\x00") != strings.Join(wantArgs, "\x00") {
			t.Fatalf("mailbox args = %v, want %v", args, wantArgs)
		}
		return RunResult{ExitCode: 0, Stdout: `[{"address":"agent-deck/worker","oldest_eligible_at":"2026-04-03T00:40:00Z","claimable_count":1}]`}, nil
	}}

	commandRunner := &fakeRunner{t: t, handler: func(args []string, input string) (RunResult, error) {
		switch {
		case strings.Join(args, "\x00") == strings.Join([]string{"agent-deck", "session", "show", "worker", "--json"}, "\x00"):
			return RunResult{ExitCode: 0, Stdout: `{"id":"worker","title":"coder-123","status":"waiting"}`}, nil
		case len(args) == 6 && args[0] == "agent-deck" && args[1] == "session" && args[2] == "send":
			sendCount++
			return RunResult{ExitCode: 0}, nil
		default:
			t.Fatalf("unexpected command args: %v", args)
			return RunResult{}, nil
		}
	}}

	service := newService(Options{
		MailboxRunner:             mailboxRunner,
		CommandRunner:             commandRunner,
		Now:                       func() time.Time { return current },
		ReminderConfirmDelay:      2 * time.Second,
		DisableActiveReminderLoop: true,
	})
	subscription, err := buildReminderSubscription([]string{"agent-deck/worker"}, nil, "agent-deck/worker", "10m", true)
	if err != nil {
		t.Fatalf("buildReminderSubscription() error = %v", err)
	}
	service.state.reminderSubscriptions[subscription.Key] = subscription

	if err := service.processActiveReminderSubscriptions(context.Background()); err != nil {
		t.Fatalf("processActiveReminderSubscriptions(first) error = %v", err)
	}
	if sendCount != 0 {
		t.Fatalf("sendCount after first poll = %d, want 0", sendCount)
	}

	current = current.Add(time.Second)
	if err := service.processActiveReminderSubscriptions(context.Background()); err != nil {
		t.Fatalf("processActiveReminderSubscriptions(second) error = %v", err)
	}
	if sendCount != 0 {
		t.Fatalf("sendCount after second poll = %d, want 0", sendCount)
	}

	current = current.Add(2 * time.Second)
	if err := service.processActiveReminderSubscriptions(context.Background()); err != nil {
		t.Fatalf("processActiveReminderSubscriptions(third) error = %v", err)
	}
	if sendCount != 1 {
		t.Fatalf("sendCount after third poll = %d, want 1", sendCount)
	}

	current = current.Add(time.Second)
	if err := service.processActiveReminderSubscriptions(context.Background()); err != nil {
		t.Fatalf("processActiveReminderSubscriptions(cooldown) error = %v", err)
	}
	if sendCount != 1 {
		t.Fatalf("sendCount during cooldown = %d, want 1", sendCount)
	}

	current = current.Add(10 * time.Minute)
	if err := service.processActiveReminderSubscriptions(context.Background()); err != nil {
		t.Fatalf("processActiveReminderSubscriptions(post-cooldown pending) error = %v", err)
	}
	if sendCount != 1 {
		t.Fatalf("sendCount after cooldown reset = %d, want 1", sendCount)
	}

	current = current.Add(3 * time.Second)
	if err := service.processActiveReminderSubscriptions(context.Background()); err != nil {
		t.Fatalf("processActiveReminderSubscriptions(post-cooldown notify) error = %v", err)
	}
	if sendCount != 2 {
		t.Fatalf("sendCount after post-cooldown notify = %d, want 2", sendCount)
	}
}

func TestProcessActiveReminderSubscriptionsSuppressesRecentLocalActivity(t *testing.T) {
	current := time.Date(2026, 4, 3, 2, 0, 0, 0, time.UTC)
	showStatuses := []string{"running", "waiting", "waiting"}
	sendCount := 0
	showIndex := 0

	mailboxRunner := &fakeRunner{t: t, handler: func(args []string, input string) (RunResult, error) {
		return RunResult{ExitCode: 0, Stdout: `[{"address":"agent-deck/worker","oldest_eligible_at":"2026-04-03T01:40:00Z","claimable_count":1}]`}, nil
	}}

	commandRunner := &fakeRunner{t: t, handler: func(args []string, input string) (RunResult, error) {
		switch {
		case strings.Join(args, "\x00") == strings.Join([]string{"agent-deck", "session", "show", "worker", "--json"}, "\x00"):
			status := showStatuses[showIndex]
			if showIndex < len(showStatuses)-1 {
				showIndex++
			}
			return RunResult{ExitCode: 0, Stdout: `{"id":"worker","title":"coder-123","status":"` + status + `"}`}, nil
		case len(args) == 6 && args[0] == "agent-deck" && args[1] == "session" && args[2] == "send":
			sendCount++
			return RunResult{ExitCode: 0}, nil
		default:
			t.Fatalf("unexpected command args: %v", args)
			return RunResult{}, nil
		}
	}}

	service := newService(Options{
		MailboxRunner:             mailboxRunner,
		CommandRunner:             commandRunner,
		Now:                       func() time.Time { return current },
		ReminderConfirmDelay:      2 * time.Second,
		DisableActiveReminderLoop: true,
	})
	subscription, err := buildReminderSubscription([]string{"agent-deck/worker"}, nil, "agent-deck/worker", "10m", true)
	if err != nil {
		t.Fatalf("buildReminderSubscription() error = %v", err)
	}
	service.state.reminderSubscriptions[subscription.Key] = subscription

	if err := service.processActiveReminderSubscriptions(context.Background()); err != nil {
		t.Fatalf("processActiveReminderSubscriptions(first) error = %v", err)
	}
	if sendCount != 0 {
		t.Fatalf("sendCount after suppressed poll = %d, want 0", sendCount)
	}

	current = current.Add(3 * time.Second)
	if err := service.processActiveReminderSubscriptions(context.Background()); err != nil {
		t.Fatalf("processActiveReminderSubscriptions(second) error = %v", err)
	}
	if sendCount != 0 {
		t.Fatalf("sendCount after pending reset = %d, want 0", sendCount)
	}

	current = current.Add(3 * time.Second)
	if err := service.processActiveReminderSubscriptions(context.Background()); err != nil {
		t.Fatalf("processActiveReminderSubscriptions(third) error = %v", err)
	}
	if sendCount != 1 {
		t.Fatalf("sendCount after wakeable re-confirm = %d, want 1", sendCount)
	}
}

func TestProcessActiveReminderSubscriptionsCombinesPersonalAndGroupSelectors(t *testing.T) {
	current := time.Date(2026, 4, 3, 3, 0, 0, 0, time.UTC)
	personalCalls := 0
	groupCalls := 0
	sendCount := 0

	mailboxRunner := &fakeRunner{t: t, handler: func(args []string, input string) (RunResult, error) {
		switch strings.Join(args, "\x00") {
		case strings.Join([]string{"stale", "--older-than", "10m", "--for", "agent-deck/personal", "--json"}, "\x00"):
			personalCalls++
			return RunResult{ExitCode: 0, Stdout: `[{"address":"agent-deck/personal","oldest_eligible_at":"2026-04-03T02:40:00Z","claimable_count":1}]`}, nil
		case strings.Join([]string{"stale", "--older-than", "10m", "--for", "group/ops", "--as", "alice", "--json"}, "\x00"):
			groupCalls++
			return RunResult{ExitCode: 0, Stdout: `[{"address":"group/ops","person":"alice","oldest_eligible_at":"2026-04-03T02:41:00Z","claimable_count":2}]`}, nil
		default:
			t.Fatalf("unexpected mailbox args: %v", args)
			return RunResult{}, nil
		}
	}}

	commandRunner := &fakeRunner{t: t, handler: func(args []string, input string) (RunResult, error) {
		switch {
		case strings.Join(args, "\x00") == strings.Join([]string{"agent-deck", "session", "show", "worker", "--json"}, "\x00"):
			return RunResult{ExitCode: 0, Stdout: `{"id":"worker","title":"coder-123","status":"waiting"}`}, nil
		case len(args) == 6 && args[0] == "agent-deck" && args[1] == "session" && args[2] == "send":
			sendCount++
			return RunResult{ExitCode: 0}, nil
		default:
			t.Fatalf("unexpected command args: %v", args)
			return RunResult{}, nil
		}
	}}

	service := newService(Options{
		MailboxRunner:             mailboxRunner,
		CommandRunner:             commandRunner,
		Now:                       func() time.Time { return current },
		ReminderConfirmDelay:      2 * time.Second,
		DisableActiveReminderLoop: true,
	})
	subscription, err := buildReminderSubscription(
		[]string{"agent-deck/personal"},
		[]reminderGroupViewInput{{GroupAddress: "group/ops", AsPerson: "alice"}},
		"agent-deck/worker",
		"10m",
		true,
	)
	if err != nil {
		t.Fatalf("buildReminderSubscription() error = %v", err)
	}
	service.state.reminderSubscriptions[subscription.Key] = subscription

	if err := service.processActiveReminderSubscriptions(context.Background()); err != nil {
		t.Fatalf("processActiveReminderSubscriptions(first) error = %v", err)
	}
	current = current.Add(3 * time.Second)
	if err := service.processActiveReminderSubscriptions(context.Background()); err != nil {
		t.Fatalf("processActiveReminderSubscriptions(second) error = %v", err)
	}

	if personalCalls != 2 {
		t.Fatalf("personal stale calls = %d, want 2", personalCalls)
	}
	if groupCalls != 2 {
		t.Fatalf("group stale calls = %d, want 2", groupCalls)
	}
	if sendCount != 1 {
		t.Fatalf("sendCount = %d, want 1", sendCount)
	}
}

func TestMailboxStatusIncludesPassiveReminderHints(t *testing.T) {
	mailboxRunner := &fakeRunner{t: t, handler: func(args []string, input string) (RunResult, error) {
		wantArgs := []string{
			"stale",
			"--older-than", "10m",
			"--for", "agent-deck/worker",
			"--json",
		}
		if strings.Join(args, "\x00") != strings.Join(wantArgs, "\x00") {
			t.Fatalf("mailbox args = %v, want %v", args, wantArgs)
		}
		return RunResult{ExitCode: 0, Stdout: `[{"address":"agent-deck/worker","oldest_eligible_at":"2026-04-01T00:00:00Z","claimable_count":2}]`}, nil
	}}

	service := newService(Options{
		MailboxRunner: mailboxRunner,
		CommandRunner: &fakeRunner{t: t, handler: func(args []string, input string) (RunResult, error) {
			switch {
			case strings.Join(args, "\x00") == strings.Join([]string{"agent-deck", "session", "show", "planner", "--json"}, "\x00"):
				return RunResult{ExitCode: 0, Stdout: `{"id":"planner","title":"planner","status":"waiting"}`}, nil
			default:
				t.Fatalf("unexpected command call: %v", args)
				return RunResult{}, nil
			}
		}},
	})
	service.state.boundAddresses = []string{"agent-deck/self"}
	service.state.defaultSender = "agent-deck/self"
	service.state.autoBindAttempted = true

	initial := callTool(t, service.Server(), "mailbox_status", nil)
	if got := initial["reminders"]; got != nil {
		t.Fatalf("reminders = %v, want nil before subscription", got)
	}

	subscription, err := buildReminderSubscription([]string{"agent-deck/worker"}, nil, "agent-deck/self", "10m", false)
	if err != nil {
		t.Fatalf("buildReminderSubscription() error = %v", err)
	}
	service.state.reminderSubscriptions[subscription.Key] = subscription

	status := callTool(t, service.Server(), "mailbox_status", nil)
	reminders := status["reminders"].(map[string]any)
	if got := reminders["configured_count"]; got != float64(1) {
		t.Fatalf("configured_count = %v, want 1", got)
	}
	if got := reminders["stale_count"]; got != float64(1) {
		t.Fatalf("stale_count = %v, want 1", got)
	}
	staleSubs := reminders["subscriptions"].([]any)
	if len(staleSubs) != 1 {
		t.Fatalf("subscriptions = %d, want 1", len(staleSubs))
	}
	staleSub := staleSubs[0].(map[string]any)
	if got := staleSub["route"]; got != "agent-deck/self" {
		t.Fatalf("route = %v, want agent-deck/self", got)
	}
	if got := staleSub["stale_address_count"]; got != float64(1) {
		t.Fatalf("stale_address_count = %v, want 1", got)
	}
	if got := staleSub["claimable_count"]; got != float64(2) {
		t.Fatalf("claimable_count = %v, want 2", got)
	}

	resolve := callTool(t, service.Server(), "agent_deck_resolve_session", map[string]any{
		"session": "planner",
	})
	if got := resolve["reminders"]; got != nil {
		t.Fatalf("agent_deck_resolve_session reminders = %v, want nil", got)
	}
}

func TestMailboxSendOmitsPassiveRemindersWhenStaleCheckFails(t *testing.T) {
	mailboxRunner := &fakeRunner{t: t, handler: func(args []string, input string) (RunResult, error) {
		switch {
		case len(args) > 0 && args[0] == "send":
			return RunResult{ExitCode: 0, Stdout: "delivery_id=dlv_side_effect\n"}, nil
		case len(args) > 0 && args[0] == "stale":
			return RunResult{ExitCode: 1, Stderr: "stale unavailable"}, nil
		default:
			t.Fatalf("unexpected mailbox args: %v", args)
			return RunResult{}, nil
		}
	}}

	service := newService(Options{
		MailboxRunner: mailboxRunner,
		CommandRunner: &fakeRunner{t: t, handler: func(args []string, input string) (RunResult, error) {
			t.Fatalf("unexpected command call: %v", args)
			return RunResult{}, nil
		}},
	})
	service.state.boundAddresses = []string{"agent-deck/self"}
	service.state.defaultSender = "agent-deck/self"
	service.state.autoBindAttempted = true

	subscription, err := buildReminderSubscription([]string{"agent-deck/worker"}, nil, "agent-deck/self", "10m", false)
	if err != nil {
		t.Fatalf("buildReminderSubscription() error = %v", err)
	}
	service.state.reminderSubscriptions[subscription.Key] = subscription

	output := callTool(t, service.Server(), "mailbox_send", map[string]any{
		"to_address":   "agent-deck/self",
		"from_address": "agent-deck/self",
		"subject":      "delegate",
		"body":         "body",
	})
	if got := output["status"]; got != "sent" {
		t.Fatalf("status = %v, want sent", got)
	}
	if got := output["delivery_id"]; got != "dlv_side_effect" {
		t.Fatalf("delivery_id = %v, want dlv_side_effect", got)
	}
	if got := output["notify_status"]; got != "skipped_local" {
		t.Fatalf("notify_status = %v, want skipped_local", got)
	}
	if got := output["reminders"]; got != nil {
		t.Fatalf("reminders = %v, want nil when passive stale check fails", got)
	}
}

func TestMailboxBindAndReminderToolsDoNotExposePassiveReminderHints(t *testing.T) {
	mailboxRunner := &fakeRunner{t: t, handler: func(args []string, input string) (RunResult, error) {
		if len(args) == 0 || args[0] != "stale" {
			t.Fatalf("unexpected mailbox args: %v", args)
		}
		return RunResult{ExitCode: 0, Stdout: "[]"}, nil
	}}

	service := newService(Options{
		MailboxRunner: mailboxRunner,
		CommandRunner: &fakeRunner{t: t, handler: func(args []string, input string) (RunResult, error) {
			t.Fatalf("unexpected command call: %v", args)
			return RunResult{}, nil
		}},
	})
	service.state.autoBindAttempted = true

	bind := callTool(t, service.Server(), "mailbox_bind", map[string]any{
		"addresses": []string{"agent-deck/self"},
	})
	if got := bind["reminders"]; got != nil {
		t.Fatalf("mailbox_bind reminders = %v, want nil", got)
	}

	subscribe := callTool(t, service.Server(), "mailbox_reminder_subscribe", map[string]any{
		"addresses":  []string{"agent-deck/self"},
		"route":      "agent-deck/self",
		"older_than": "10m",
	})
	if got := subscribe["reminders"]; got != nil {
		t.Fatalf("mailbox_reminder_subscribe reminders = %v, want nil", got)
	}

	reminderStatus := callTool(t, service.Server(), "mailbox_reminder_status", nil)
	if got := reminderStatus["reminders"]; got != nil {
		t.Fatalf("mailbox_reminder_status reminders = %v, want nil", got)
	}
}

func TestAgentDeckEnsureSessionStartsInactiveTarget(t *testing.T) {
	commandRunner := &fakeRunner{t: t, handler: func(args []string, input string) (RunResult, error) {
		switch {
		case strings.Join(args, "\x00") == strings.Join([]string{"agent-deck", "session", "show", "coder-ref", "--json"}, "\x00"):
			return RunResult{ExitCode: 0, Stdout: `{"id":"session-1","title":"coder-123","status":"stopped"}`}, nil
		case strings.Join(args, "\x00") == strings.Join([]string{"agent-deck", "session", "start", "--json", "-m", ensureReceiverWorkflowHint(defaultListenerMessage, defaultListenerMessage, "coder-123"), "session-1"}, "\x00"):
			return RunResult{ExitCode: 0}, nil
		case strings.Join(args, "\x00") == strings.Join([]string{"agent-deck", "session", "show", "session-1", "--json"}, "\x00"):
			return RunResult{ExitCode: 0, Stdout: `{"id":"session-1","title":"coder-123","status":"waiting"}`}, nil
		default:
			t.Fatalf("unexpected command args: %v", args)
			return RunResult{}, nil
		}
	}}

	service := newService(Options{
		MailboxRunner: &fakeRunner{t: t, handler: func(args []string, input string) (RunResult, error) {
			t.Fatalf("unexpected mailbox call: %v", args)
			return RunResult{}, nil
		}},
		CommandRunner: commandRunner,
	})
	service.state.autoBindAttempted = true

	output := callTool(t, service.Server(), "agent_deck_ensure_session", map[string]any{
		"session_ref": "coder-ref",
	})

	if got := output["status"]; got != "ready" {
		t.Fatalf("status = %v, want ready", got)
	}
	if got := output["created_target"]; got != false {
		t.Fatalf("created_target = %v, want false", got)
	}
	if got := output["started_session"]; got != true {
		t.Fatalf("started_session = %v, want true", got)
	}
	if got := output["notify_needed"]; got != false {
		t.Fatalf("notify_needed = %v, want false", got)
	}
	if got := output["listener_status"]; got != "started_waiting" {
		t.Fatalf("listener_status = %v, want started_waiting", got)
	}
}

func TestMailboxRunnerUsesConfiguredStateDir(t *testing.T) {
	t.Parallel()

	stateDir := filepath.Join(t.TempDir(), "mailbox-state")
	service := newService(Options{
		StateDir: stateDir,
		CommandRunner: &fakeRunner{t: t, handler: func(args []string, input string) (RunResult, error) {
			t.Fatalf("unexpected command call: %v", args)
			return RunResult{}, nil
		}},
	})
	service.state.boundAddresses = []string{"agent-deck/self"}
	service.state.defaultSender = "agent-deck/self"
	service.state.autoBindAttempted = true

	output := callTool(t, service.Server(), "mailbox_send", map[string]any{
		"to_address": "agent-deck/self",
		"subject":    "delegate",
		"body":       "body",
	})
	if got := output["delivery_id"]; got == nil || got == "" {
		t.Fatalf("delivery_id = %v, want non-empty", got)
	}

	runtime, err := mailbox.OpenRuntime(context.Background(), stateDir)
	if err != nil {
		t.Fatalf("OpenRuntime() error = %v", err)
	}
	defer runtime.Close()

	deliveries, err := runtime.Store().List(context.Background(), mailbox.ListParams{
		Address: "agent-deck/self",
		State:   "queued",
	})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(deliveries) != 1 {
		t.Fatalf("queued deliveries = %d, want 1", len(deliveries))
	}
	if deliveries[0].Subject != "delegate" {
		t.Fatalf("queued subject = %q, want delegate", deliveries[0].Subject)
	}
}

func callTool(t *testing.T, server *mcp.Server, name string, args map[string]any) map[string]any {
	t.Helper()

	ctx := context.Background()
	clientTransport, serverTransport := mcp.NewInMemoryTransports()

	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "v0.0.1"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() {
		_ = clientSession.Close()
		_ = serverSession.Wait()
	})

	result, err := clientSession.CallTool(ctx, &mcp.CallToolParams{
		Name:      name,
		Arguments: args,
	})
	if err != nil {
		t.Fatalf("call tool %s: %v", name, err)
	}
	if result.IsError {
		t.Fatalf("tool %s returned error result: %#v", name, result.Content)
	}

	var output map[string]any
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatalf("marshal structured content: %v", err)
	}
	if err := json.Unmarshal(encoded, &output); err != nil {
		t.Fatalf("unmarshal structured content: %v", err)
	}
	return output
}
