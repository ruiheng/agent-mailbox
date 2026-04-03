package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/ruiheng/agent-mailbox/internal/mailbox"
)

type fakeMailboxService struct {
	t *testing.T

	sendFunc                func(context.Context, mailbox.SendParams) (mailbox.SendResult, error)
	listFunc                func(context.Context, mailbox.ListParams) ([]mailbox.ListedDelivery, error)
	listGroupMessagesFunc   func(context.Context, mailbox.GroupListParams) ([]mailbox.GroupListedMessage, error)
	listStaleAddressesFunc  func(context.Context, mailbox.StaleAddressesParams) ([]mailbox.StaleAddress, error)
	receiveBatchFunc        func(context.Context, mailbox.ReceiveBatchParams) (mailbox.ReceiveResult, error)
	receiveBatchWithTTLFunc func(context.Context, mailbox.ReceiveBatchParams, time.Duration) (mailbox.ReceiveResult, error)
	waitFunc                func(context.Context, mailbox.WaitParams) (mailbox.ListedDelivery, error)
	readMessagesFunc        func(context.Context, []string) ([]mailbox.ReadMessage, error)
	readLatestFunc          func(context.Context, []string, string, int) ([]mailbox.ReadDelivery, bool, error)
	readDeliveriesFunc      func(context.Context, []string) ([]mailbox.ReadDelivery, error)
	ackFunc                 func(context.Context, string, string) (mailbox.DeliveryTransitionResult, error)
	renewFunc               func(context.Context, string, string, time.Duration) (mailbox.LeaseRenewResult, error)
	releaseFunc             func(context.Context, string, string) (mailbox.DeliveryTransitionResult, error)
	deferFunc               func(context.Context, string, string, time.Time) (mailbox.DeliveryTransitionResult, error)
	failFunc                func(context.Context, string, string, string) (mailbox.DeliveryTransitionResult, error)
}

func (f *fakeMailboxService) Send(ctx context.Context, params mailbox.SendParams) (mailbox.SendResult, error) {
	if f.sendFunc == nil {
		f.t.Fatalf("unexpected Send call: %+v", params)
	}
	return f.sendFunc(ctx, params)
}

func (f *fakeMailboxService) List(ctx context.Context, params mailbox.ListParams) ([]mailbox.ListedDelivery, error) {
	if f.listFunc == nil {
		f.t.Fatalf("unexpected List call: %+v", params)
	}
	return f.listFunc(ctx, params)
}

func (f *fakeMailboxService) ListGroupMessages(ctx context.Context, params mailbox.GroupListParams) ([]mailbox.GroupListedMessage, error) {
	if f.listGroupMessagesFunc == nil {
		f.t.Fatalf("unexpected ListGroupMessages call: %+v", params)
	}
	return f.listGroupMessagesFunc(ctx, params)
}

func (f *fakeMailboxService) ListStaleAddresses(ctx context.Context, params mailbox.StaleAddressesParams) ([]mailbox.StaleAddress, error) {
	if f.listStaleAddressesFunc == nil {
		f.t.Fatalf("unexpected ListStaleAddresses call: %+v", params)
	}
	return f.listStaleAddressesFunc(ctx, params)
}

func (f *fakeMailboxService) ReceiveBatch(ctx context.Context, params mailbox.ReceiveBatchParams) (mailbox.ReceiveResult, error) {
	if f.receiveBatchFunc == nil {
		f.t.Fatalf("unexpected ReceiveBatch call: %+v", params)
	}
	return f.receiveBatchFunc(ctx, params)
}

func (f *fakeMailboxService) ReceiveBatchWithLeaseTTL(ctx context.Context, params mailbox.ReceiveBatchParams, ttl time.Duration) (mailbox.ReceiveResult, error) {
	if f.receiveBatchWithTTLFunc != nil {
		return f.receiveBatchWithTTLFunc(ctx, params, ttl)
	}
	if f.receiveBatchFunc == nil {
		f.t.Fatalf("unexpected ReceiveBatchWithLeaseTTL call: %+v ttl=%s", params, ttl)
	}
	return f.receiveBatchFunc(ctx, params)
}

func (f *fakeMailboxService) Wait(ctx context.Context, params mailbox.WaitParams) (mailbox.ListedDelivery, error) {
	if f.waitFunc == nil {
		f.t.Fatalf("unexpected Wait call: %+v", params)
	}
	return f.waitFunc(ctx, params)
}

func (f *fakeMailboxService) ReadMessages(ctx context.Context, messageIDs []string) ([]mailbox.ReadMessage, error) {
	if f.readMessagesFunc == nil {
		f.t.Fatalf("unexpected ReadMessages call: %v", messageIDs)
	}
	return f.readMessagesFunc(ctx, messageIDs)
}

func (f *fakeMailboxService) ReadLatestDeliveries(ctx context.Context, addresses []string, state string, limit int) ([]mailbox.ReadDelivery, bool, error) {
	if f.readLatestFunc == nil {
		f.t.Fatalf("unexpected ReadLatestDeliveries call: addresses=%v state=%q limit=%d", addresses, state, limit)
	}
	return f.readLatestFunc(ctx, addresses, state, limit)
}

func (f *fakeMailboxService) ReadDeliveries(ctx context.Context, deliveryIDs []string) ([]mailbox.ReadDelivery, error) {
	if f.readDeliveriesFunc == nil {
		f.t.Fatalf("unexpected ReadDeliveries call: %v", deliveryIDs)
	}
	return f.readDeliveriesFunc(ctx, deliveryIDs)
}

func (f *fakeMailboxService) Ack(ctx context.Context, deliveryID, leaseToken string) (mailbox.DeliveryTransitionResult, error) {
	if f.ackFunc == nil {
		f.t.Fatalf("unexpected Ack call: delivery=%q lease=%q", deliveryID, leaseToken)
	}
	return f.ackFunc(ctx, deliveryID, leaseToken)
}

func (f *fakeMailboxService) Renew(ctx context.Context, deliveryID, leaseToken string, extendBy time.Duration) (mailbox.LeaseRenewResult, error) {
	if f.renewFunc == nil {
		f.t.Fatalf("unexpected Renew call: delivery=%q lease=%q extendBy=%s", deliveryID, leaseToken, extendBy)
	}
	return f.renewFunc(ctx, deliveryID, leaseToken, extendBy)
}

func (f *fakeMailboxService) Release(ctx context.Context, deliveryID, leaseToken string) (mailbox.DeliveryTransitionResult, error) {
	if f.releaseFunc == nil {
		f.t.Fatalf("unexpected Release call: delivery=%q lease=%q", deliveryID, leaseToken)
	}
	return f.releaseFunc(ctx, deliveryID, leaseToken)
}

func (f *fakeMailboxService) Defer(ctx context.Context, deliveryID, leaseToken string, until time.Time) (mailbox.DeliveryTransitionResult, error) {
	if f.deferFunc == nil {
		f.t.Fatalf("unexpected Defer call: delivery=%q lease=%q until=%s", deliveryID, leaseToken, until)
	}
	return f.deferFunc(ctx, deliveryID, leaseToken, until)
}

func (f *fakeMailboxService) Fail(ctx context.Context, deliveryID, leaseToken, reason string) (mailbox.DeliveryTransitionResult, error) {
	if f.failFunc == nil {
		f.t.Fatalf("unexpected Fail call: delivery=%q lease=%q reason=%q", deliveryID, leaseToken, reason)
	}
	return f.failFunc(ctx, deliveryID, leaseToken, reason)
}

type fakeMailboxServiceFactory struct {
	service mailboxService
}

func (f fakeMailboxServiceFactory) Open(context.Context) (mailboxService, func() error, error) {
	return f.service, func() error { return nil }, nil
}

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

func TestResolveNotifyMessageUsesFixedWakeText(t *testing.T) {
	if got := resolveNotifyMessage(nil, defaultNotifyMessage); got != defaultNotifyMessage {
		t.Fatalf("resolveNotifyMessage(nil) = %q, want %q", got, defaultNotifyMessage)
	}

	custom := "Check the delegated task immediately."
	if got := resolveNotifyMessage(&custom, defaultNotifyMessage); got != defaultNotifyMessage {
		t.Fatalf("resolveNotifyMessage(custom) = %q, want fixed default", got)
	}

	disabled := ""
	if got := resolveNotifyMessage(&disabled, defaultNotifyMessage); got != "" {
		t.Fatalf("resolveNotifyMessage(empty) = %q, want empty", got)
	}
}

func TestMailboxSendNotifiesWorkerTarget(t *testing.T) {
	mailboxService := &fakeMailboxService{t: t}
	mailboxService.sendFunc = func(_ context.Context, params mailbox.SendParams) (mailbox.SendResult, error) {
		if params.ToAddress != "agent-deck/target" || params.FromAddress != "agent-deck/self" || params.Subject != "delegate" {
			t.Fatalf("send params = %+v", params)
		}
		if string(params.Body) != "body" {
			t.Fatalf("send body = %q, want body", string(params.Body))
		}
		return mailbox.SendResult{DeliveryID: "dlv_1"}, nil
	}

	commandRunner := &fakeRunner{t: t, handler: func(args []string, input string) (RunResult, error) {
		switch {
		case len(args) == 6 && args[0] == "agent-deck" && args[1] == "session" && args[2] == "send":
			if args[4] != "target" {
				t.Fatalf("notify target = %q, want target", args[4])
			}
			if args[5] != defaultNotifyMessage {
				t.Fatalf("notify message = %q, want fixed default", args[5])
			}
			return RunResult{ExitCode: 0}, nil
		default:
			t.Fatalf("unexpected command args: %v", args)
			return RunResult{}, nil
		}
	}}

	service := newService(Options{
		MailboxServiceFactory: fakeMailboxServiceFactory{service: mailboxService},
		CommandRunner:         commandRunner,
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
	mailboxService := &fakeMailboxService{t: t}
	mailboxService.sendFunc = func(_ context.Context, params mailbox.SendParams) (mailbox.SendResult, error) {
		return mailbox.SendResult{DeliveryID: "dlv_disabled"}, nil
	}

	service := newService(Options{
		MailboxServiceFactory: fakeMailboxServiceFactory{service: mailboxService},
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

func TestMailboxSendIgnoresCustomNotifyMessage(t *testing.T) {
	const customNotify = "Check the delegated task immediately."

	mailboxService := &fakeMailboxService{t: t}
	mailboxService.sendFunc = func(_ context.Context, params mailbox.SendParams) (mailbox.SendResult, error) {
		return mailbox.SendResult{DeliveryID: "dlv_custom"}, nil
	}

	commandRunner := &fakeRunner{t: t, handler: func(args []string, input string) (RunResult, error) {
		switch {
		case len(args) == 6 && args[0] == "agent-deck" && args[1] == "session" && args[2] == "send":
			if args[4] != "target" {
				t.Fatalf("notify target = %q, want target", args[4])
			}
			if args[5] != defaultNotifyMessage {
				t.Fatalf("notify message = %q, want fixed default", args[5])
			}
			return RunResult{ExitCode: 0}, nil
		default:
			t.Fatalf("unexpected command args: %v", args)
			return RunResult{}, nil
		}
	}}

	service := newService(Options{
		MailboxServiceFactory: fakeMailboxServiceFactory{service: mailboxService},
		CommandRunner:         commandRunner,
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
	mailboxService := &fakeMailboxService{t: t}
	mailboxService.sendFunc = func(_ context.Context, params mailbox.SendParams) (mailbox.SendResult, error) {
		if params.ContentType != "" || params.SchemaVersion != "" {
			t.Fatalf("send params unexpectedly set defaults: %+v", params)
		}
		return mailbox.SendResult{DeliveryID: "dlv_2"}, nil
	}

	service := newService(Options{
		MailboxServiceFactory: fakeMailboxServiceFactory{service: mailboxService},
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
	mailboxService := &fakeMailboxService{t: t}
	mailboxService.sendFunc = func(_ context.Context, params mailbox.SendParams) (mailbox.SendResult, error) {
		return mailbox.SendResult{DeliveryID: "dlv_3"}, nil
	}
	commandRunner := &fakeRunner{t: t, handler: func(args []string, input string) (RunResult, error) {
		switch {
		case len(args) == 6 && args[0] == "agent-deck" && args[1] == "session" && args[2] == "send":
			return RunResult{ExitCode: 1, Stderr: "wakeup failed"}, nil
		default:
			t.Fatalf("unexpected command args: %v", args)
			return RunResult{}, nil
		}
	}}

	service := newService(Options{
		MailboxServiceFactory: fakeMailboxServiceFactory{service: mailboxService},
		CommandRunner:         commandRunner,
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
	var staleCalls []mailbox.StaleAddressesParams
	mailboxService := &fakeMailboxService{t: t}
	mailboxService.listStaleAddressesFunc = func(_ context.Context, params mailbox.StaleAddressesParams) ([]mailbox.StaleAddress, error) {
		staleCalls = append(staleCalls, params)
		return []mailbox.StaleAddress{}, nil
	}

	service := newService(Options{
		MailboxServiceFactory: fakeMailboxServiceFactory{service: mailboxService},
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
	for _, params := range staleCalls {
		if params.OlderThan != 10*time.Minute && params.OlderThan != 15*time.Minute {
			t.Fatalf("older_than = %s, want 10m or 15m", params.OlderThan)
		}
		if len(params.Addresses) != 2 || params.Addresses[0] != "agent-deck/a" || params.Addresses[1] != "agent-deck/b" {
			t.Fatalf("stale addresses = %v, want sorted deduped values", params.Addresses)
		}
	}
}

func TestMailboxReminderUnsubscribeIsIdempotent(t *testing.T) {
	mailboxService := &fakeMailboxService{t: t}
	mailboxService.listStaleAddressesFunc = func(_ context.Context, params mailbox.StaleAddressesParams) ([]mailbox.StaleAddress, error) {
		return []mailbox.StaleAddress{}, nil
	}

	service := newService(Options{
		MailboxServiceFactory: fakeMailboxServiceFactory{service: mailboxService},
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
	mailboxService := &fakeMailboxService{t: t}
	mailboxService.listStaleAddressesFunc = func(_ context.Context, params mailbox.StaleAddressesParams) ([]mailbox.StaleAddress, error) {
		callCount++
		if params.OlderThan != 10*time.Minute || len(params.GroupViews) != 1 {
			t.Fatalf("stale params = %+v, want one normalized group view at 10m", params)
		}
		groupView := params.GroupViews[0]
		switch {
		case groupView.Address == "group/a" && groupView.Person == "alice":
			return []mailbox.StaleAddress{}, nil
		case groupView.Address == "group/b" && groupView.Person == "bob":
			return []mailbox.StaleAddress{}, nil
		default:
			t.Fatalf("group view = %+v, want normalized group query", groupView)
			return nil, nil
		}
	}

	service := newService(Options{
		MailboxServiceFactory: fakeMailboxServiceFactory{service: mailboxService},
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

func TestMailboxReminderSubscribeStartsActiveLoop(t *testing.T) {
	mailboxService := &fakeMailboxService{t: t}
	mailboxService.listStaleAddressesFunc = func(_ context.Context, params mailbox.StaleAddressesParams) ([]mailbox.StaleAddress, error) {
		if params.OlderThan != 10*time.Minute || len(params.Addresses) != 1 || params.Addresses[0] != "agent-deck/worker" {
			t.Fatalf("stale params = %+v, want worker selector at 10m", params)
		}
		return []mailbox.StaleAddress{{
			Address:          "agent-deck/worker",
			OldestEligibleAt: "2026-04-03T00:40:00Z",
			ClaimableCount:   1,
		}}, nil
	}

	notified := make(chan struct{}, 1)
	commandRunner := &fakeRunner{t: t, handler: func(args []string, input string) (RunResult, error) {
		switch {
		case strings.Join(args, "\x00") == strings.Join([]string{"agent-deck", "session", "show", "worker", "--json"}, "\x00"):
			return RunResult{ExitCode: 0, Stdout: `{"id":"worker","title":"coder-123","status":"waiting"}`}, nil
		case len(args) == 6 && args[0] == "agent-deck" && args[1] == "session" && args[2] == "send":
			select {
			case notified <- struct{}{}:
			default:
			}
			return RunResult{ExitCode: 0}, nil
		default:
			t.Fatalf("unexpected command args: %v", args)
			return RunResult{}, nil
		}
	}}

	service := newService(Options{
		MailboxServiceFactory: fakeMailboxServiceFactory{service: mailboxService},
		CommandRunner:         commandRunner,
		Now:                   time.Now,
		ReminderPollInterval:  10 * time.Millisecond,
		ReminderConfirmDelay:  time.Millisecond,
	})
	service.state.autoBindAttempted = true

	callTool(t, service.Server(), "mailbox_reminder_subscribe", map[string]any{
		"addresses":   []string{"agent-deck/worker"},
		"route":       "agent-deck/worker",
		"older_than":  "10m",
		"active_push": true,
	})

	select {
	case <-notified:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for active reminder notification")
	}
}

func TestProcessActiveReminderSubscriptionsConfirmsThenCooldowns(t *testing.T) {
	current := time.Date(2026, 4, 3, 1, 0, 0, 0, time.UTC)
	sendCount := 0

	mailboxService := &fakeMailboxService{t: t}
	mailboxService.listStaleAddressesFunc = func(_ context.Context, params mailbox.StaleAddressesParams) ([]mailbox.StaleAddress, error) {
		if params.OlderThan != 10*time.Minute || len(params.Addresses) != 1 || params.Addresses[0] != "agent-deck/worker" {
			t.Fatalf("stale params = %+v, want worker selector at 10m", params)
		}
		return []mailbox.StaleAddress{{
			Address:          "agent-deck/worker",
			OldestEligibleAt: "2026-04-03T00:40:00Z",
			ClaimableCount:   1,
		}}, nil
	}

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
		MailboxServiceFactory:     fakeMailboxServiceFactory{service: mailboxService},
		CommandRunner:             commandRunner,
		Now:                       func() time.Time { return current },
		ReminderConfirmDelay:      2 * time.Second,
		DisableActiveReminderLoop: true,
	})
	subscription, err := buildReminderSubscription([]string{"agent-deck/worker"}, nil, "agent-deck/worker", "10m", true)
	if err != nil {
		t.Fatalf("buildReminderSubscription() error = %v", err)
	}
	service.reminders.setSubscription(subscription)

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

	mailboxService := &fakeMailboxService{t: t}
	mailboxService.listStaleAddressesFunc = func(_ context.Context, params mailbox.StaleAddressesParams) ([]mailbox.StaleAddress, error) {
		return []mailbox.StaleAddress{{
			Address:          "agent-deck/worker",
			OldestEligibleAt: "2026-04-03T01:40:00Z",
			ClaimableCount:   1,
		}}, nil
	}

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
		MailboxServiceFactory:     fakeMailboxServiceFactory{service: mailboxService},
		CommandRunner:             commandRunner,
		Now:                       func() time.Time { return current },
		ReminderConfirmDelay:      2 * time.Second,
		DisableActiveReminderLoop: true,
	})
	subscription, err := buildReminderSubscription([]string{"agent-deck/worker"}, nil, "agent-deck/worker", "10m", true)
	if err != nil {
		t.Fatalf("buildReminderSubscription() error = %v", err)
	}
	service.reminders.setSubscription(subscription)

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

	mailboxService := &fakeMailboxService{t: t}
	mailboxService.listStaleAddressesFunc = func(_ context.Context, params mailbox.StaleAddressesParams) ([]mailbox.StaleAddress, error) {
		switch {
		case params.OlderThan == 10*time.Minute && len(params.Addresses) == 1 && params.Addresses[0] == "agent-deck/personal":
			personalCalls++
			return []mailbox.StaleAddress{{
				Address:          "agent-deck/personal",
				OldestEligibleAt: "2026-04-03T02:40:00Z",
				ClaimableCount:   1,
			}}, nil
		case params.OlderThan == 10*time.Minute && len(params.GroupViews) == 1 && params.GroupViews[0].Address == "group/ops" && params.GroupViews[0].Person == "alice":
			groupCalls++
			return []mailbox.StaleAddress{{
				Address:          "group/ops",
				Person:           "alice",
				OldestEligibleAt: "2026-04-03T02:41:00Z",
				ClaimableCount:   2,
			}}, nil
		default:
			t.Fatalf("unexpected stale params: %+v", params)
			return nil, nil
		}
	}

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
		MailboxServiceFactory:     fakeMailboxServiceFactory{service: mailboxService},
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
	service.reminders.setSubscription(subscription)

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

func TestProcessActiveReminderSubscriptionsRetriesAfterFailedNotifyWithoutCooldown(t *testing.T) {
	current := time.Date(2026, 4, 3, 4, 0, 0, 0, time.UTC)
	sendCount := 0

	mailboxService := &fakeMailboxService{t: t}
	mailboxService.listStaleAddressesFunc = func(_ context.Context, params mailbox.StaleAddressesParams) ([]mailbox.StaleAddress, error) {
		if params.OlderThan != 10*time.Minute || len(params.Addresses) != 1 || params.Addresses[0] != "agent-deck/worker" {
			t.Fatalf("stale params = %+v, want worker selector at 10m", params)
		}
		return []mailbox.StaleAddress{{
			Address:          "agent-deck/worker",
			OldestEligibleAt: "2026-04-03T03:40:00Z",
			ClaimableCount:   1,
		}}, nil
	}

	commandRunner := &fakeRunner{t: t, handler: func(args []string, input string) (RunResult, error) {
		switch {
		case strings.Join(args, "\x00") == strings.Join([]string{"agent-deck", "session", "show", "worker", "--json"}, "\x00"):
			return RunResult{ExitCode: 0, Stdout: `{"id":"worker","title":"coder-123","status":"waiting"}`}, nil
		case len(args) == 6 && args[0] == "agent-deck" && args[1] == "session" && args[2] == "send":
			sendCount++
			if sendCount == 1 {
				return RunResult{ExitCode: 1, Stderr: "wakeup failed"}, nil
			}
			return RunResult{ExitCode: 0}, nil
		default:
			t.Fatalf("unexpected command args: %v", args)
			return RunResult{}, nil
		}
	}}

	service := newService(Options{
		MailboxServiceFactory:     fakeMailboxServiceFactory{service: mailboxService},
		CommandRunner:             commandRunner,
		Now:                       func() time.Time { return current },
		ReminderConfirmDelay:      2 * time.Second,
		DisableActiveReminderLoop: true,
	})
	subscription, err := buildReminderSubscription([]string{"agent-deck/worker"}, nil, "agent-deck/worker", "10m", true)
	if err != nil {
		t.Fatalf("buildReminderSubscription() error = %v", err)
	}
	service.reminders.setSubscription(subscription)

	if err := service.processActiveReminderSubscriptions(context.Background()); err != nil {
		t.Fatalf("processActiveReminderSubscriptions(first) error = %v", err)
	}

	current = current.Add(3 * time.Second)
	if err := service.processActiveReminderSubscriptions(context.Background()); err != nil {
		t.Fatalf("processActiveReminderSubscriptions(failed notify) error = %v", err)
	}
	if sendCount != 1 {
		t.Fatalf("sendCount after failed notify = %d, want 1", sendCount)
	}
	runtime, ok := service.reminders.subscriptionRuntime(subscription.Key)
	if !ok {
		t.Fatalf("subscription runtime missing for key %q", subscription.Key)
	}
	if runtime.LastNotifiedAt != "" {
		t.Fatalf("LastNotifiedAt after failed notify = %q, want empty", runtime.LastNotifiedAt)
	}
	if runtime.PendingSince == "" {
		t.Fatal("PendingSince after failed notify = empty, want preserved pending retry state")
	}

	current = current.Add(time.Second)
	if err := service.processActiveReminderSubscriptions(context.Background()); err != nil {
		t.Fatalf("processActiveReminderSubscriptions(retry) error = %v", err)
	}
	if sendCount != 2 {
		t.Fatalf("sendCount after retry = %d, want 2", sendCount)
	}
	runtime, ok = service.reminders.subscriptionRuntime(subscription.Key)
	if !ok {
		t.Fatalf("subscription runtime missing for key %q", subscription.Key)
	}
	if runtime.LastNotifiedAt == "" {
		t.Fatal("LastNotifiedAt after successful retry = empty, want cooldown stamp")
	}
	if runtime.PendingSince != "" {
		t.Fatalf("PendingSince after successful retry = %q, want empty", runtime.PendingSince)
	}
}

func TestMailboxStatusIncludesPassiveReminderHints(t *testing.T) {
	mailboxService := &fakeMailboxService{t: t}
	mailboxService.listStaleAddressesFunc = func(_ context.Context, params mailbox.StaleAddressesParams) ([]mailbox.StaleAddress, error) {
		if params.OlderThan != 10*time.Minute || len(params.Addresses) != 1 || params.Addresses[0] != "agent-deck/worker" {
			t.Fatalf("stale params = %+v, want worker selector at 10m", params)
		}
		return []mailbox.StaleAddress{{
			Address:          "agent-deck/worker",
			OldestEligibleAt: "2026-04-01T00:00:00Z",
			ClaimableCount:   2,
		}}, nil
	}

	service := newService(Options{
		MailboxServiceFactory: fakeMailboxServiceFactory{service: mailboxService},
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
	service.reminders.setSubscription(subscription)

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
	mailboxService := &fakeMailboxService{t: t}
	mailboxService.sendFunc = func(_ context.Context, params mailbox.SendParams) (mailbox.SendResult, error) {
		return mailbox.SendResult{DeliveryID: "dlv_side_effect"}, nil
	}
	mailboxService.listStaleAddressesFunc = func(_ context.Context, params mailbox.StaleAddressesParams) ([]mailbox.StaleAddress, error) {
		return nil, context.DeadlineExceeded
	}

	service := newService(Options{
		MailboxServiceFactory: fakeMailboxServiceFactory{service: mailboxService},
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
	service.reminders.setSubscription(subscription)

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
	mailboxService := &fakeMailboxService{t: t}
	mailboxService.listStaleAddressesFunc = func(_ context.Context, params mailbox.StaleAddressesParams) ([]mailbox.StaleAddress, error) {
		return []mailbox.StaleAddress{}, nil
	}

	service := newService(Options{
		MailboxServiceFactory: fakeMailboxServiceFactory{service: mailboxService},
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
		case strings.Join(args, "\x00") == strings.Join([]string{"agent-deck", "session", "start", "--json", "session-1"}, "\x00"):
			return RunResult{ExitCode: 0}, nil
		case strings.Join(args, "\x00") == strings.Join([]string{"agent-deck", "session", "show", "session-1", "--json"}, "\x00"):
			return RunResult{ExitCode: 0, Stdout: `{"id":"session-1","title":"coder-123","status":"waiting"}`}, nil
		default:
			t.Fatalf("unexpected command args: %v", args)
			return RunResult{}, nil
		}
	}}

	service := newService(Options{
		MailboxServiceFactory: fakeMailboxServiceFactory{service: &fakeMailboxService{t: t}},
		CommandRunner:         commandRunner,
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

func TestAgentDeckEnsureSessionStartsInactiveTargetWithExplicitListenerMessage(t *testing.T) {
	commandRunner := &fakeRunner{t: t, handler: func(args []string, input string) (RunResult, error) {
		switch {
		case strings.Join(args, "\x00") == strings.Join([]string{"agent-deck", "session", "show", "coder-ref", "--json"}, "\x00"):
			return RunResult{ExitCode: 0, Stdout: `{"id":"session-1","title":"coder-123","status":"stopped"}`}, nil
		case strings.Join(args, "\x00") == strings.Join([]string{"agent-deck", "session", "start", "--json", "-m", "listen now", "session-1"}, "\x00"):
			return RunResult{ExitCode: 0}, nil
		case strings.Join(args, "\x00") == strings.Join([]string{"agent-deck", "session", "show", "session-1", "--json"}, "\x00"):
			return RunResult{ExitCode: 0, Stdout: `{"id":"session-1","title":"coder-123","status":"waiting"}`}, nil
		default:
			t.Fatalf("unexpected command args: %v", args)
			return RunResult{}, nil
		}
	}}

	service := newService(Options{
		MailboxServiceFactory: fakeMailboxServiceFactory{service: &fakeMailboxService{t: t}},
		CommandRunner:         commandRunner,
	})
	service.state.autoBindAttempted = true

	output := callTool(t, service.Server(), "agent_deck_ensure_session", map[string]any{
		"session_ref":      "coder-ref",
		"listener_message": "listen now",
	})

	if got := output["listener_status"]; got != "started_waiting" {
		t.Fatalf("listener_status = %v, want started_waiting", got)
	}
}

func TestAgentDeckEnsureSessionCreatesTargetWithoutDefaultListenerMessage(t *testing.T) {
	commandRunner := &fakeRunner{t: t, handler: func(args []string, input string) (RunResult, error) {
		switch {
		case strings.Join(args, "\x00") == strings.Join([]string{"agent-deck", "launch", "--json", "--title", "coder-ref", "--parent", "planner-1", "--cmd", "codex --model gpt-5.4 --ask-for-approval on-request", "/tmp"}, "\x00"):
			return RunResult{ExitCode: 0, Stdout: `{"id":"session-2","title":"coder-ref","status":"waiting"}`}, nil
		default:
			t.Fatalf("unexpected command args: %v", args)
			return RunResult{}, nil
		}
	}}

	service := newService(Options{
		MailboxServiceFactory: fakeMailboxServiceFactory{service: &fakeMailboxService{t: t}},
		CommandRunner:         commandRunner,
	})
	service.state.autoBindAttempted = true

	output := callTool(t, service.Server(), "agent_deck_ensure_session", map[string]any{
		"ensure_title":      "coder-ref",
		"ensure_cmd":        "codex --model gpt-5.4 --ask-for-approval on-request",
		"parent_session_id": "planner-1",
		"workdir":           "/tmp",
	})

	if got := output["created_target"]; got != true {
		t.Fatalf("created_target = %v, want true", got)
	}
	if got := output["listener_status"]; got != "started_waiting" {
		t.Fatalf("listener_status = %v, want started_waiting", got)
	}
}

func TestMailboxServiceUsesConfiguredStateDir(t *testing.T) {
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

func TestMailboxLifecycleToolsUseDirectMailboxService(t *testing.T) {
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

	send := callTool(t, service.Server(), "mailbox_send", map[string]any{
		"to_address": "agent-deck/self",
		"subject":    "delegate",
		"body":       "body",
	})
	deliveryID := send["delivery_id"].(string)
	if deliveryID == "" {
		t.Fatal("delivery_id = empty, want non-empty")
	}

	wait := callTool(t, service.Server(), "mailbox_wait", map[string]any{
		"addresses": []string{"agent-deck/self"},
	})
	if got := wait["status"]; got != "message_available" {
		t.Fatalf("wait status = %v, want message_available", got)
	}
	waitDelivery := wait["delivery"].(map[string]any)
	if waitDelivery["delivery_id"] != deliveryID {
		t.Fatalf("wait delivery_id = %v, want %s", waitDelivery["delivery_id"], deliveryID)
	}
	if _, ok := waitDelivery["body"]; ok {
		t.Fatalf("wait delivery unexpectedly contains body: %v", waitDelivery)
	}

	recv := callTool(t, service.Server(), "mailbox_recv", map[string]any{
		"addresses": []string{"agent-deck/self"},
	})
	if got := recv["status"]; got != "received" {
		t.Fatalf("recv status = %v, want received", got)
	}
	received := recv["delivery"].(map[string]any)
	messages := received["messages"].([]any)
	if len(messages) != 1 {
		t.Fatalf("recv messages = %d, want 1", len(messages))
	}
	message := messages[0].(map[string]any)
	if message["delivery_id"] != deliveryID {
		t.Fatalf("recv delivery_id = %v, want %s", message["delivery_id"], deliveryID)
	}
	if message["body"] != "body" {
		t.Fatalf("recv body = %v, want body", message["body"])
	}

	ack := callTool(t, service.Server(), "mailbox_ack", map[string]any{
		"delivery_id": deliveryID,
		"lease_token": message["lease_token"],
	})
	if got := ack["status"]; got != "acked" {
		t.Fatalf("ack status = %v, want acked", got)
	}

	list := callTool(t, service.Server(), "mailbox_list", map[string]any{
		"address": "agent-deck/self",
		"state":   "acked",
	})
	deliveries := list["deliveries"].([]any)
	if len(deliveries) != 1 {
		t.Fatalf("list deliveries = %d, want 1", len(deliveries))
	}
	listed := deliveries[0].(map[string]any)
	if listed["delivery_id"] != deliveryID {
		t.Fatalf("listed delivery_id = %v, want %s", listed["delivery_id"], deliveryID)
	}
	if listed["state"] != "acked" {
		t.Fatalf("listed state = %v, want acked", listed["state"])
	}

	read := callTool(t, service.Server(), "mailbox_read", map[string]any{
		"addresses": []string{"agent-deck/self"},
		"latest":    true,
		"state":     "acked",
		"limit":     1,
	})
	if got := read["mode"]; got != "latest" {
		t.Fatalf("read mode = %v, want latest", got)
	}
	items := read["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("read items = %d, want 1", len(items))
	}
	readDelivery := items[0].(map[string]any)
	if readDelivery["delivery_id"] != deliveryID {
		t.Fatalf("read delivery_id = %v, want %s", readDelivery["delivery_id"], deliveryID)
	}
	if readDelivery["body"] != "body" {
		t.Fatalf("read body = %v, want body", readDelivery["body"])
	}
}

func TestMailboxRecvStartsLeaseRenewLoopWithShortTTL(t *testing.T) {
	current := time.Date(2026, 4, 3, 6, 0, 0, 0, time.UTC)
	renewed := make(chan struct{}, 1)

	mailboxService := &fakeMailboxService{t: t}
	mailboxService.receiveBatchWithTTLFunc = func(_ context.Context, params mailbox.ReceiveBatchParams, ttl time.Duration) (mailbox.ReceiveResult, error) {
		if ttl != defaultMCPLeaseTTL {
			t.Fatalf("recv lease ttl = %s, want %s", ttl, defaultMCPLeaseTTL)
		}
		if params.Max != 1 || len(params.Addresses) != 1 || params.Addresses[0] != "agent-deck/self" {
			t.Fatalf("recv params = %+v, want one bound address", params)
		}
		return mailbox.ReceiveResult{
			Messages: []mailbox.ReceivedMessage{{
				DeliveryID:       "dlv_lease",
				LeaseToken:       "lease_1",
				LeaseExpiresAt:   current.Add(defaultMCPLeaseTTL).Format(time.RFC3339Nano),
				RecipientAddress: "agent-deck/self",
				Subject:          "delegate",
				Body:             "body",
			}},
		}, nil
	}
	mailboxService.renewFunc = func(_ context.Context, deliveryID, leaseToken string, extendBy time.Duration) (mailbox.LeaseRenewResult, error) {
		if deliveryID != "dlv_lease" || leaseToken != "lease_1" {
			t.Fatalf("renew args = delivery=%q lease=%q", deliveryID, leaseToken)
		}
		if extendBy != defaultMCPLeaseTTL {
			t.Fatalf("renew extendBy = %s, want %s", extendBy, defaultMCPLeaseTTL)
		}
		select {
		case renewed <- struct{}{}:
		default:
		}
		return mailbox.LeaseRenewResult{
			DeliveryID:     deliveryID,
			LeaseToken:     leaseToken,
			LeaseExpiresAt: time.Now().UTC().Add(defaultMCPLeaseTTL).Format(time.RFC3339Nano),
		}, nil
	}

	service := newService(Options{
		MailboxServiceFactory: fakeMailboxServiceFactory{service: mailboxService},
		CommandRunner: &fakeRunner{t: t, handler: func(args []string, input string) (RunResult, error) {
			t.Fatalf("unexpected command call: %v", args)
			return RunResult{}, nil
		}},
		LeaseRenewInterval: 10 * time.Millisecond,
	})
	service.state.boundAddresses = []string{"agent-deck/self"}
	service.state.defaultSender = "agent-deck/self"
	service.state.autoBindAttempted = true

	callTool(t, service.Server(), "mailbox_recv", map[string]any{
		"addresses": []string{"agent-deck/self"},
	})

	select {
	case <-renewed:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for lease renew")
	}
}

func TestProcessLeaseRenewalsRetriesTransientFailureWithinLeaseWindow(t *testing.T) {
	current := time.Date(2026, 4, 3, 6, 15, 0, 0, time.UTC)
	renewCalls := 0
	ackCalled := false

	mailboxService := &fakeMailboxService{t: t}
	mailboxService.receiveBatchWithTTLFunc = func(_ context.Context, params mailbox.ReceiveBatchParams, ttl time.Duration) (mailbox.ReceiveResult, error) {
		return mailbox.ReceiveResult{
			Messages: []mailbox.ReceivedMessage{{
				DeliveryID:       "dlv_retry",
				LeaseToken:       "lease_retry",
				LeaseExpiresAt:   current.Add(defaultMCPLeaseTTL).Format(time.RFC3339Nano),
				RecipientAddress: "agent-deck/self",
				Subject:          "delegate",
				Body:             "body",
			}},
		}, nil
	}
	mailboxService.renewFunc = func(_ context.Context, deliveryID, leaseToken string, extendBy time.Duration) (mailbox.LeaseRenewResult, error) {
		renewCalls++
		if renewCalls == 1 {
			return mailbox.LeaseRenewResult{}, context.DeadlineExceeded
		}
		return mailbox.LeaseRenewResult{
			DeliveryID:     deliveryID,
			LeaseToken:     leaseToken,
			LeaseExpiresAt: current.Add(defaultMCPLeaseTTL).Format(time.RFC3339Nano),
		}, nil
	}
	mailboxService.ackFunc = func(_ context.Context, deliveryID, leaseToken string) (mailbox.DeliveryTransitionResult, error) {
		ackCalled = true
		return mailbox.DeliveryTransitionResult{DeliveryID: deliveryID, State: "acked"}, nil
	}

	service := newService(Options{
		MailboxServiceFactory: fakeMailboxServiceFactory{service: mailboxService},
		CommandRunner: &fakeRunner{t: t, handler: func(args []string, input string) (RunResult, error) {
			t.Fatalf("unexpected command call: %v", args)
			return RunResult{}, nil
		}},
		Now:                   func() time.Time { return current },
		DisableLeaseRenewLoop: true,
	})
	service.state.boundAddresses = []string{"agent-deck/self"}
	service.state.defaultSender = "agent-deck/self"
	service.state.autoBindAttempted = true

	callTool(t, service.Server(), "mailbox_recv", map[string]any{
		"addresses": []string{"agent-deck/self"},
	})

	if err := service.processLeaseRenewals(context.Background()); err != nil {
		t.Fatalf("processLeaseRenewals() error = %v, want nil after transient retry", err)
	}
	if renewCalls != 2 {
		t.Fatalf("renewCalls = %d, want 2", renewCalls)
	}
	if failure := service.activeLeases.lastRenewalError(); failure != nil {
		t.Fatalf("lastRenewalError() = %v, want nil after successful retry", failure)
	}

	output := callTool(t, service.Server(), "mailbox_ack", map[string]any{
		"delivery_id": "dlv_retry",
		"lease_token": "lease_retry",
	})
	if got := output["status"]; got != "acked" {
		t.Fatalf("mailbox_ack status = %v, want acked", got)
	}
	if !ackCalled {
		t.Fatal("Ack was not forwarded after transient renew retry")
	}
}

func TestProcessLeaseRenewalsAllowsTerminalMutationBeforeExpiryAfterTransientFailure(t *testing.T) {
	current := time.Date(2026, 4, 3, 6, 30, 0, 0, time.UTC)
	mailboxService := &fakeMailboxService{t: t}
	mailboxService.receiveBatchWithTTLFunc = func(_ context.Context, params mailbox.ReceiveBatchParams, ttl time.Duration) (mailbox.ReceiveResult, error) {
		return mailbox.ReceiveResult{
			Messages: []mailbox.ReceivedMessage{{
				DeliveryID:       "dlv_failure",
				LeaseToken:       "lease_failure",
				LeaseExpiresAt:   current.Add(defaultMCPLeaseTTL).Format(time.RFC3339Nano),
				RecipientAddress: "agent-deck/self",
				Subject:          "delegate",
				Body:             "body",
			}},
		}, nil
	}
	mailboxService.renewFunc = func(_ context.Context, deliveryID, leaseToken string, extendBy time.Duration) (mailbox.LeaseRenewResult, error) {
		return mailbox.LeaseRenewResult{}, context.DeadlineExceeded
	}
	ackCalled := false
	mailboxService.ackFunc = func(_ context.Context, deliveryID, leaseToken string) (mailbox.DeliveryTransitionResult, error) {
		ackCalled = true
		return mailbox.DeliveryTransitionResult{DeliveryID: deliveryID, State: "acked"}, nil
	}

	service := newService(Options{
		MailboxServiceFactory: fakeMailboxServiceFactory{service: mailboxService},
		CommandRunner: &fakeRunner{t: t, handler: func(args []string, input string) (RunResult, error) {
			t.Fatalf("unexpected command call: %v", args)
			return RunResult{}, nil
		}},
		Now:                   func() time.Time { return current },
		DisableLeaseRenewLoop: true,
	})
	service.state.boundAddresses = []string{"agent-deck/self"}
	service.state.defaultSender = "agent-deck/self"
	service.state.autoBindAttempted = true

	callTool(t, service.Server(), "mailbox_recv", map[string]any{
		"addresses": []string{"agent-deck/self"},
	})

	err := service.processLeaseRenewals(context.Background())
	if err == nil || !isLeaseRenewalFailure(err) {
		t.Fatalf("processLeaseRenewals() error = %v, want lease renewal failure", err)
	}
	if !service.activeLeases.hasTrackedLeases() {
		t.Fatal("active lease tracking removed after transient renewal failure")
	}

	output := callTool(t, service.Server(), "mailbox_ack", map[string]any{
		"delivery_id": "dlv_failure",
		"lease_token": "lease_failure",
	})
	if got := output["status"]; got != "acked" {
		t.Fatalf("mailbox_ack status = %v, want acked", got)
	}
	if !ackCalled {
		t.Fatal("Ack was not forwarded before lease expiry")
	}
}

func TestProcessLeaseRenewalsBlocksTerminalMutationAfterExpiryFollowingTransientFailure(t *testing.T) {
	current := time.Date(2026, 4, 3, 6, 45, 0, 0, time.UTC)

	mailboxService := &fakeMailboxService{t: t}
	mailboxService.receiveBatchWithTTLFunc = func(_ context.Context, params mailbox.ReceiveBatchParams, ttl time.Duration) (mailbox.ReceiveResult, error) {
		return mailbox.ReceiveResult{
			Messages: []mailbox.ReceivedMessage{{
				DeliveryID:       "dlv_expired_failure",
				LeaseToken:       "lease_expired_failure",
				LeaseExpiresAt:   current.Add(defaultMCPLeaseTTL).Format(time.RFC3339Nano),
				RecipientAddress: "agent-deck/self",
				Subject:          "delegate",
				Body:             "body",
			}},
		}, nil
	}
	mailboxService.renewFunc = func(_ context.Context, deliveryID, leaseToken string, extendBy time.Duration) (mailbox.LeaseRenewResult, error) {
		return mailbox.LeaseRenewResult{}, context.DeadlineExceeded
	}
	mailboxService.ackFunc = func(_ context.Context, deliveryID, leaseToken string) (mailbox.DeliveryTransitionResult, error) {
		t.Fatalf("Ack should not be called after lease expiry")
		return mailbox.DeliveryTransitionResult{}, nil
	}

	service := newService(Options{
		MailboxServiceFactory: fakeMailboxServiceFactory{service: mailboxService},
		CommandRunner: &fakeRunner{t: t, handler: func(args []string, input string) (RunResult, error) {
			t.Fatalf("unexpected command call: %v", args)
			return RunResult{}, nil
		}},
		Now:                   func() time.Time { return current },
		DisableLeaseRenewLoop: true,
	})
	service.state.boundAddresses = []string{"agent-deck/self"}
	service.state.defaultSender = "agent-deck/self"
	service.state.autoBindAttempted = true

	callTool(t, service.Server(), "mailbox_recv", map[string]any{
		"addresses": []string{"agent-deck/self"},
	})

	err := service.processLeaseRenewals(context.Background())
	if err == nil || !isLeaseRenewalFailure(err) {
		t.Fatalf("processLeaseRenewals() error = %v, want lease renewal failure", err)
	}

	current = current.Add(defaultMCPLeaseTTL + time.Second)
	toolErr := callToolExpectError(t, service.Server(), "mailbox_ack", map[string]any{
		"delivery_id": "dlv_expired_failure",
		"lease_token": "lease_expired_failure",
	})
	if !strings.Contains(toolErr.Error(), "lease ownership is no longer guaranteed") {
		t.Fatalf("mailbox_ack error = %v, want lease renewal failure text", toolErr)
	}
}

func TestMailboxAckStopsTrackingActiveLease(t *testing.T) {
	mailboxService := &fakeMailboxService{t: t}
	mailboxService.receiveBatchWithTTLFunc = func(_ context.Context, params mailbox.ReceiveBatchParams, ttl time.Duration) (mailbox.ReceiveResult, error) {
		return mailbox.ReceiveResult{
			Messages: []mailbox.ReceivedMessage{{
				DeliveryID:       "dlv_acked",
				LeaseToken:       "lease_acked",
				LeaseExpiresAt:   time.Now().UTC().Add(defaultMCPLeaseTTL).Format(time.RFC3339Nano),
				RecipientAddress: "agent-deck/self",
				Subject:          "delegate",
				Body:             "body",
			}},
		}, nil
	}
	mailboxService.ackFunc = func(_ context.Context, deliveryID, leaseToken string) (mailbox.DeliveryTransitionResult, error) {
		if deliveryID != "dlv_acked" || leaseToken != "lease_acked" {
			t.Fatalf("ack args = delivery=%q lease=%q", deliveryID, leaseToken)
		}
		return mailbox.DeliveryTransitionResult{DeliveryID: deliveryID, State: "acked"}, nil
	}

	service := newService(Options{
		MailboxServiceFactory: fakeMailboxServiceFactory{service: mailboxService},
		CommandRunner: &fakeRunner{t: t, handler: func(args []string, input string) (RunResult, error) {
			t.Fatalf("unexpected command call: %v", args)
			return RunResult{}, nil
		}},
		DisableLeaseRenewLoop: true,
	})
	service.state.boundAddresses = []string{"agent-deck/self"}
	service.state.defaultSender = "agent-deck/self"
	service.state.autoBindAttempted = true

	recv := callTool(t, service.Server(), "mailbox_recv", map[string]any{
		"addresses": []string{"agent-deck/self"},
	})
	delivery := recv["delivery"].(map[string]any)
	message := delivery["messages"].([]any)[0].(map[string]any)

	callTool(t, service.Server(), "mailbox_ack", map[string]any{
		"delivery_id": "dlv_acked",
		"lease_token": message["lease_token"],
	})

	if err := service.processLeaseRenewals(context.Background()); err != nil {
		t.Fatalf("processLeaseRenewals() error = %v", err)
	}
	if service.activeLeases.hasTrackedLeases() {
		t.Fatal("active leases still tracked after ack")
	}
}

func TestMailboxReleaseDeferAndFailUseDirectMailboxService(t *testing.T) {
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

	firstSend := callTool(t, service.Server(), "mailbox_send", map[string]any{
		"to_address": "agent-deck/self",
		"subject":    "release-defer",
		"body":       "body",
	})
	firstRecv := callTool(t, service.Server(), "mailbox_recv", map[string]any{
		"addresses": []string{"agent-deck/self"},
	})
	firstMessage := firstRecv["delivery"].(map[string]any)["messages"].([]any)[0].(map[string]any)

	release := callTool(t, service.Server(), "mailbox_release", map[string]any{
		"delivery_id": firstSend["delivery_id"],
		"lease_token": firstMessage["lease_token"],
	})
	if got := release["status"]; got != "released" {
		t.Fatalf("release status = %v, want released", got)
	}

	secondRecv := callTool(t, service.Server(), "mailbox_recv", map[string]any{
		"addresses": []string{"agent-deck/self"},
	})
	secondMessage := secondRecv["delivery"].(map[string]any)["messages"].([]any)[0].(map[string]any)
	until := time.Now().UTC().Add(10 * time.Minute).Format(time.RFC3339Nano)
	deferResult := callTool(t, service.Server(), "mailbox_defer", map[string]any{
		"delivery_id": firstSend["delivery_id"],
		"lease_token": secondMessage["lease_token"],
		"until":       until,
	})
	if got := deferResult["status"]; got != "deferred" {
		t.Fatalf("defer status = %v, want deferred", got)
	}

	wait := callTool(t, service.Server(), "mailbox_wait", map[string]any{
		"addresses": []string{"agent-deck/self"},
		"timeout":   "10ms",
	})
	if got := wait["status"]; got != "no_message" {
		t.Fatalf("wait status after defer = %v, want no_message", got)
	}

	secondSend := callTool(t, service.Server(), "mailbox_send", map[string]any{
		"to_address": "agent-deck/self",
		"subject":    "fail",
		"body":       "body-2",
	})
	failRecv := callTool(t, service.Server(), "mailbox_recv", map[string]any{
		"addresses": []string{"agent-deck/self"},
	})
	failMessage := failRecv["delivery"].(map[string]any)["messages"].([]any)[0].(map[string]any)
	failResult := callTool(t, service.Server(), "mailbox_fail", map[string]any{
		"delivery_id": secondSend["delivery_id"],
		"lease_token": failMessage["lease_token"],
		"reason":      "boom",
	})
	if got := failResult["status"]; got != "failed" {
		t.Fatalf("fail status = %v, want failed", got)
	}
	if got := failResult["reason"]; got != "boom" {
		t.Fatalf("fail reason = %v, want boom", got)
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

func callToolExpectError(t *testing.T, server *mcp.Server, name string, args map[string]any) error {
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
		return err
	}
	if result.IsError {
		encoded, marshalErr := json.Marshal(result.Content)
		if marshalErr != nil {
			return fmt.Errorf("marshal tool error content: %w", marshalErr)
		}
		return fmt.Errorf("%s", encoded)
	}
	if err == nil {
		t.Fatalf("call tool %s unexpectedly succeeded", name)
	}
	return nil
}
