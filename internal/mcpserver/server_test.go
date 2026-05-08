package mcpserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/ruiheng/agent-mailbox/internal/mailbox"
)

type fakeMailboxService struct {
	t *testing.T

	sendFunc                  func(context.Context, mailbox.SendParams) (mailbox.SendResult, error)
	listFunc                  func(context.Context, mailbox.ListParams) ([]mailbox.ListedDelivery, error)
	listGroupMessagesFunc     func(context.Context, mailbox.GroupListParams) ([]mailbox.GroupListedMessage, error)
	waitGroupMessageFunc      func(context.Context, mailbox.GroupWaitParams) (mailbox.GroupListedMessage, error)
	receiveGroupMessageFunc   func(context.Context, mailbox.GroupReceiveParams) (mailbox.GroupReceivedMessage, error)
	createGroupFunc           func(context.Context, string) (mailbox.GroupRecord, error)
	addGroupMemberFunc        func(context.Context, string, string) (mailbox.GroupMembershipRecord, error)
	removeGroupMemberFunc     func(context.Context, string, string) (mailbox.GroupMembershipRecord, error)
	listGroupMembersFunc      func(context.Context, string) ([]mailbox.GroupMembershipRecord, error)
	addGroupSubscriberFunc    func(context.Context, string, string, string) (mailbox.GroupNotificationSubscriberRecord, error)
	removeGroupSubscriberFunc func(context.Context, string, string) (mailbox.GroupNotificationSubscriberRecord, error)
	listGroupSubscribersFunc  func(context.Context, string) ([]mailbox.GroupNotificationSubscriberRecord, error)
	inspectAddressFunc        func(context.Context, string) (mailbox.AddressInspection, error)
	listClaimableFunc         func(context.Context, []string) ([]mailbox.ClaimableAddress, error)
	listStaleAddressesFunc    func(context.Context, mailbox.StaleAddressesParams) ([]mailbox.StaleAddress, error)
	receiveBatchFunc          func(context.Context, mailbox.ReceiveBatchParams) (mailbox.ReceiveResult, error)
	receiveBatchWithTTLFunc   func(context.Context, mailbox.ReceiveBatchParams, time.Duration) (mailbox.ReceiveResult, error)
	waitFunc                  func(context.Context, mailbox.WaitParams) (mailbox.ListedDelivery, error)
	hasVisibleDeliveryFunc    func(context.Context, mailbox.WaitParams) (bool, error)
	readMessagesFunc          func(context.Context, []string) ([]mailbox.ReadMessage, error)
	readLatestFunc            func(context.Context, []string, string, int) ([]mailbox.ReadDelivery, bool, error)
	readDeliveriesFunc        func(context.Context, []string) ([]mailbox.ReadDelivery, error)
	ackFunc                   func(context.Context, string, string) (mailbox.DeliveryTransitionResult, error)
	renewFunc                 func(context.Context, string, string, time.Duration) (mailbox.LeaseRenewResult, error)
	releaseFunc               func(context.Context, string, string) (mailbox.DeliveryTransitionResult, error)
	deferFunc                 func(context.Context, string, string, time.Time) (mailbox.DeliveryTransitionResult, error)
	failFunc                  func(context.Context, string, string, string) (mailbox.DeliveryTransitionResult, error)
}

func TestAgentDeckCreateSessionSchemaRequiresWorkdir(t *testing.T) {
	schema, err := jsonschema.For[agentDeckCreateSessionInput](nil)
	if err != nil {
		t.Fatalf("jsonschema.For() error = %v", err)
	}
	if !slices.Contains(schema.Required, "workdir") {
		t.Fatalf("required fields = %v, want workdir", schema.Required)
	}
}

func TestAgentDeckRequireSessionSchemaRequiresWorkdir(t *testing.T) {
	schema, err := jsonschema.For[agentDeckRequireSessionInput](nil)
	if err != nil {
		t.Fatalf("jsonschema.For() error = %v", err)
	}
	if !slices.Contains(schema.Required, "workdir") {
		t.Fatalf("required fields = %v, want workdir", schema.Required)
	}
}

func TestAgentDeckRequireSessionSchemaOmitsCreateOnlyFields(t *testing.T) {
	schema, err := jsonschema.For[agentDeckRequireSessionInput](nil)
	if err != nil {
		t.Fatalf("jsonschema.For() error = %v", err)
	}

	for _, field := range []string{
		"ensure_title",
		"ensure_cmd",
		"parent_session_id",
		"group_path",
		"group_parent_session_id",
		"child_group_name",
		"no_parent_link",
		"startup_instruction",
	} {
		if _, ok := schema.Properties[field]; ok {
			t.Fatalf("schema.Properties[%q] unexpectedly present", field)
		}
	}
}

func TestMailboxSendSchemaExposesGroup(t *testing.T) {
	schema, err := jsonschema.For[mailboxSendInput](nil)
	if err != nil {
		t.Fatalf("jsonschema.For() error = %v", err)
	}
	if _, ok := schema.Properties["group"]; !ok {
		t.Fatalf("schema.Properties missing group: %v", schema.Properties)
	}
}

func TestMailboxRecvSchemaExposesTimeout(t *testing.T) {
	schema, err := jsonschema.For[mailboxRecvInput](nil)
	if err != nil {
		t.Fatalf("jsonschema.For() error = %v", err)
	}
	if _, ok := schema.Properties["timeout"]; !ok {
		t.Fatalf("schema.Properties missing timeout: %v", schema.Properties)
	}
}

func (f *fakeMailboxService) Send(ctx context.Context, params mailbox.SendParams) (mailbox.SendResult, error) {
	if f.sendFunc == nil {
		f.t.Fatalf("unexpected Send call: %+v", params)
	}
	return f.sendFunc(ctx, params)
}

func (f *fakeMailboxService) List(ctx context.Context, params mailbox.ListParams) ([]mailbox.ListedDelivery, error) {
	if f.listFunc == nil {
		return []mailbox.ListedDelivery{}, nil
	}
	return f.listFunc(ctx, params)
}

func (f *fakeMailboxService) ListGroupMessages(ctx context.Context, params mailbox.GroupListParams) ([]mailbox.GroupListedMessage, error) {
	if f.listGroupMessagesFunc == nil {
		f.t.Fatalf("unexpected ListGroupMessages call: %+v", params)
	}
	return f.listGroupMessagesFunc(ctx, params)
}

func (f *fakeMailboxService) WaitGroupMessage(ctx context.Context, params mailbox.GroupWaitParams) (mailbox.GroupListedMessage, error) {
	if f.waitGroupMessageFunc == nil {
		f.t.Fatalf("unexpected WaitGroupMessage call: %+v", params)
	}
	return f.waitGroupMessageFunc(ctx, params)
}

func (f *fakeMailboxService) ReceiveGroupMessage(ctx context.Context, params mailbox.GroupReceiveParams) (mailbox.GroupReceivedMessage, error) {
	if f.receiveGroupMessageFunc == nil {
		f.t.Fatalf("unexpected ReceiveGroupMessage call: %+v", params)
	}
	return f.receiveGroupMessageFunc(ctx, params)
}

func (f *fakeMailboxService) CreateGroup(ctx context.Context, groupAddress string) (mailbox.GroupRecord, error) {
	if f.createGroupFunc == nil {
		f.t.Fatalf("unexpected CreateGroup call: %q", groupAddress)
	}
	return f.createGroupFunc(ctx, groupAddress)
}

func (f *fakeMailboxService) AddGroupMember(ctx context.Context, groupAddress, person string) (mailbox.GroupMembershipRecord, error) {
	if f.addGroupMemberFunc == nil {
		f.t.Fatalf("unexpected AddGroupMember call: group=%q person=%q", groupAddress, person)
	}
	return f.addGroupMemberFunc(ctx, groupAddress, person)
}

func (f *fakeMailboxService) RemoveGroupMember(ctx context.Context, groupAddress, person string) (mailbox.GroupMembershipRecord, error) {
	if f.removeGroupMemberFunc == nil {
		f.t.Fatalf("unexpected RemoveGroupMember call: group=%q person=%q", groupAddress, person)
	}
	return f.removeGroupMemberFunc(ctx, groupAddress, person)
}

func (f *fakeMailboxService) ListGroupMembers(ctx context.Context, groupAddress string) ([]mailbox.GroupMembershipRecord, error) {
	if f.listGroupMembersFunc == nil {
		f.t.Fatalf("unexpected ListGroupMembers call: %q", groupAddress)
	}
	return f.listGroupMembersFunc(ctx, groupAddress)
}

func (f *fakeMailboxService) AddGroupNotificationSubscriber(ctx context.Context, groupAddress, notifyAddress, person string) (mailbox.GroupNotificationSubscriberRecord, error) {
	if f.addGroupSubscriberFunc == nil {
		f.t.Fatalf("unexpected AddGroupNotificationSubscriber call: group=%q notify=%q person=%q", groupAddress, notifyAddress, person)
	}
	return f.addGroupSubscriberFunc(ctx, groupAddress, notifyAddress, person)
}

func (f *fakeMailboxService) RemoveGroupNotificationSubscriber(ctx context.Context, groupAddress, notifyAddress string) (mailbox.GroupNotificationSubscriberRecord, error) {
	if f.removeGroupSubscriberFunc == nil {
		f.t.Fatalf("unexpected RemoveGroupNotificationSubscriber call: group=%q notify=%q", groupAddress, notifyAddress)
	}
	return f.removeGroupSubscriberFunc(ctx, groupAddress, notifyAddress)
}

func (f *fakeMailboxService) ListGroupNotificationSubscribers(ctx context.Context, groupAddress string) ([]mailbox.GroupNotificationSubscriberRecord, error) {
	if f.listGroupSubscribersFunc == nil {
		return nil, nil
	}
	return f.listGroupSubscribersFunc(ctx, groupAddress)
}

func (f *fakeMailboxService) InspectAddress(ctx context.Context, address string) (mailbox.AddressInspection, error) {
	if f.inspectAddressFunc == nil {
		f.t.Fatalf("unexpected InspectAddress call: %q", address)
	}
	return f.inspectAddressFunc(ctx, address)
}

func (f *fakeMailboxService) ListClaimableAddresses(ctx context.Context, addresses []string) ([]mailbox.ClaimableAddress, error) {
	if f.listClaimableFunc == nil {
		return []mailbox.ClaimableAddress{}, nil
	}
	return f.listClaimableFunc(ctx, addresses)
}

func (f *fakeMailboxService) ListStaleAddresses(ctx context.Context, params mailbox.StaleAddressesParams) ([]mailbox.StaleAddress, error) {
	if f.listStaleAddressesFunc == nil {
		return nil, nil
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

func (f *fakeMailboxService) HasVisibleDelivery(ctx context.Context, params mailbox.WaitParams) (bool, error) {
	if f.hasVisibleDeliveryFunc == nil {
		return false, nil
	}
	return f.hasVisibleDeliveryFunc(ctx, params)
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
		deliveries := make([]mailbox.ReadDelivery, 0, len(deliveryIDs))
		for _, deliveryID := range deliveryIDs {
			deliveries = append(deliveries, mailbox.ReadDelivery{
				DeliveryID: deliveryID,
				State:      "queued",
			})
		}
		return deliveries, nil
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

	type result struct {
		runResult RunResult
		err       error
		completed bool
	}
	resultCh := make(chan result, 1)
	go func() {
		out := result{}
		defer func() {
			resultCh <- out
		}()
		out.runResult, out.err = r.handler(args, input)
		out.completed = true
	}()
	out := <-resultCh
	if !out.completed {
		return RunResult{}, fmt.Errorf("fake command handler stopped before returning for args: %v", args)
	}
	return out.runResult, out.err
}

func (r *fakeRunner) Calls() []runnerCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]runnerCall(nil), r.calls...)
}

func canonicalTestWorkdir(t *testing.T, path string) string {
	t.Helper()
	workdir, err := canonicalizeExistingPath(path)
	if err != nil {
		t.Fatalf("canonicalize test workdir %q: %v", path, err)
	}
	return workdir
}

func TestResolveWakeNotifyMessageUsesFixedWakeText(t *testing.T) {
	if got := resolveWakeNotifyMessage(nil, defaultNotifyMessage); got != defaultNotifyMessage {
		t.Fatalf("resolveWakeNotifyMessage(nil) = %q, want %q", got, defaultNotifyMessage)
	}

	disabled := true
	if got := resolveWakeNotifyMessage(&disabled, defaultNotifyMessage); got != "" {
		t.Fatalf("resolveWakeNotifyMessage(true) = %q, want empty", got)
	}

	enabled := false
	if got := resolveWakeNotifyMessage(&enabled, defaultNotifyMessage); got != defaultNotifyMessage {
		t.Fatalf("resolveWakeNotifyMessage(false) = %q, want %q", got, defaultNotifyMessage)
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
		case strings.Join(args, "\x00") == strings.Join([]string{"agent-deck", "session", "show", "target", "--json"}, "\x00"):
			return RunResult{ExitCode: 0, Stdout: `{"id":"target","title":"coder-123","status":"waiting"}`}, nil
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
		NotifyDelay:           -1,
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

func TestMailboxSendSkipsNotifyWhenDeliveryAlreadyClaimed(t *testing.T) {
	mailboxService := &fakeMailboxService{t: t}
	mailboxService.sendFunc = func(_ context.Context, params mailbox.SendParams) (mailbox.SendResult, error) {
		return mailbox.SendResult{DeliveryID: "dlv_claimed"}, nil
	}
	mailboxService.readDeliveriesFunc = func(_ context.Context, deliveryIDs []string) ([]mailbox.ReadDelivery, error) {
		if !reflect.DeepEqual(deliveryIDs, []string{"dlv_claimed"}) {
			t.Fatalf("ReadDeliveries ids = %v, want [dlv_claimed]", deliveryIDs)
		}
		return []mailbox.ReadDelivery{{
			DeliveryID: "dlv_claimed",
			State:      "leased",
		}}, nil
	}

	service := newService(Options{
		MailboxServiceFactory: fakeMailboxServiceFactory{service: mailboxService},
		CommandRunner: &fakeRunner{t: t, handler: func(args []string, input string) (RunResult, error) {
			t.Fatalf("unexpected command call: %v", args)
			return RunResult{}, nil
		}},
		NotifyDelay: -1,
	})
	service.state.boundAddresses = []string{"agent-deck/self"}
	service.state.defaultSender = "agent-deck/self"
	service.state.autoBindAttempted = true

	output := callTool(t, service.Server(), "mailbox_send", map[string]any{
		"to_address": "agent-deck/target",
		"subject":    "delegate",
		"body":       "body",
	})

	if got := output["delivery_id"]; got != "dlv_claimed" {
		t.Fatalf("delivery_id = %v, want dlv_claimed", got)
	}
	if got := output["notify_status"]; got != "skipped_already_claimed" {
		t.Fatalf("notify_status = %v, want skipped_already_claimed", got)
	}
	if got := output["notify_scheme"]; got != "mailbox" {
		t.Fatalf("notify_scheme = %v, want mailbox", got)
	}
	if got := output["notify_error"]; got != nil {
		t.Fatalf("notify_error = %v, want nil", got)
	}
}

func TestMailboxSendNotifyIgnoresRequestCancellationAfterSend(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mailboxService := &fakeMailboxService{t: t}
	mailboxService.sendFunc = func(_ context.Context, params mailbox.SendParams) (mailbox.SendResult, error) {
		cancel()
		return mailbox.SendResult{DeliveryID: "dlv_cancelled"}, nil
	}
	mailboxService.readDeliveriesFunc = func(ctx context.Context, deliveryIDs []string) ([]mailbox.ReadDelivery, error) {
		if err := ctx.Err(); err != nil {
			t.Fatalf("ReadDeliveries context error = %v, want nil", err)
		}
		if !reflect.DeepEqual(deliveryIDs, []string{"dlv_cancelled"}) {
			t.Fatalf("ReadDeliveries ids = %v, want [dlv_cancelled]", deliveryIDs)
		}
		return []mailbox.ReadDelivery{{
			DeliveryID: "dlv_cancelled",
			State:      "queued",
		}}, nil
	}

	commandRunner := &fakeRunner{t: t, handler: func(args []string, input string) (RunResult, error) {
		switch {
		case strings.Join(args, "\x00") == strings.Join([]string{"agent-deck", "session", "show", "target", "--json"}, "\x00"):
			return RunResult{ExitCode: 0, Stdout: `{"id":"target","title":"coder-123","status":"waiting"}`}, nil
		case len(args) == 6 && args[0] == "agent-deck" && args[1] == "session" && args[2] == "send":
			return RunResult{ExitCode: 0}, nil
		default:
			t.Fatalf("unexpected command args: %v", args)
			return RunResult{}, nil
		}
	}}

	service := newService(Options{
		MailboxServiceFactory: fakeMailboxServiceFactory{service: mailboxService},
		CommandRunner:         commandRunner,
		NotifyDelay:           -1,
	})
	service.state.boundAddresses = []string{"agent-deck/self"}
	service.state.defaultSender = "agent-deck/self"
	service.state.autoBindAttempted = true

	output, err := service.sendMailboxMessage(ctx, mailboxSendInput{
		ToAddress: "agent-deck/target",
		Subject:   "delegate",
		Body:      "body",
	})
	if err != nil {
		t.Fatalf("sendMailboxMessage error = %v", err)
	}
	if got := output["notify_status"]; got != "sent" {
		t.Fatalf("notify_status = %v, want sent", got)
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
			switch {
			case strings.Join(args, "\x00") == strings.Join([]string{"agent-deck", "session", "show", "target", "--json"}, "\x00"):
				return RunResult{ExitCode: 0, Stdout: `{"id":"target","title":"coder-123","status":"waiting"}`}, nil
			default:
				t.Fatalf("unexpected command call: %v", args)
				return RunResult{}, nil
			}
		}},
	})
	service.state.boundAddresses = []string{"agent-deck/self"}
	service.state.defaultSender = "agent-deck/self"
	service.state.autoBindAttempted = true

	output := callTool(t, service.Server(), "mailbox_send", map[string]any{
		"to_address":             "agent-deck/target",
		"subject":                "delegate",
		"body":                   "body",
		"disable_notify_message": true,
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

func TestMailboxSendUsesExplicitFromAddressWithoutBoundState(t *testing.T) {
	mailboxService := &fakeMailboxService{t: t}
	mailboxService.sendFunc = func(_ context.Context, params mailbox.SendParams) (mailbox.SendResult, error) {
		if params.FromAddress != "agent/sender" || params.ToAddress != "workflow/target" {
			t.Fatalf("send params = %+v", params)
		}
		return mailbox.SendResult{DeliveryID: "dlv_explicit"}, nil
	}

	service := newService(Options{
		MailboxServiceFactory: fakeMailboxServiceFactory{service: mailboxService},
		CommandRunner: &fakeRunner{t: t, handler: func(args []string, input string) (RunResult, error) {
			return RunResult{}, errors.New("auto-bind should not run for explicit sender")
		}},
		DisableWakeScheduler: true,
	})

	output := callTool(t, service.Server(), "mailbox_send", map[string]any{
		"to_address":   "workflow/target",
		"from_address": "agent/sender",
		"subject":      "delegate",
		"body":         "body",
	})
	if got := output["delivery_id"]; got != "dlv_explicit" {
		t.Fatalf("delivery_id = %v, want dlv_explicit", got)
	}
	if got := output["from_address"]; got != "agent/sender" {
		t.Fatalf("from_address = %v, want agent/sender", got)
	}
}

func TestMailboxSendGroupModeUsesGroupSendParams(t *testing.T) {
	mailboxService := &fakeMailboxService{t: t}
	mailboxService.sendFunc = func(_ context.Context, params mailbox.SendParams) (mailbox.SendResult, error) {
		if !params.Group {
			t.Fatal("send group = false, want true")
		}
		if params.ToAddress != "group/review" || params.FromAddress != "agent/sender" {
			t.Fatalf("send params = %+v", params)
		}
		return mailbox.SendResult{
			Mode:             mailbox.SendModeGroup,
			MessageID:        "msg_group",
			GroupID:          "grp_1",
			GroupAddress:     "group/review",
			EligibleCount:    2,
			MessageCreatedAt: "2026-04-18T00:00:00Z",
		}, nil
	}

	service := newService(Options{
		MailboxServiceFactory: fakeMailboxServiceFactory{service: mailboxService},
		CommandRunner: &fakeRunner{t: t, handler: func(args []string, input string) (RunResult, error) {
			return RunResult{}, errors.New("auto-bind should not run for explicit sender")
		}},
		DisableWakeScheduler: true,
	})

	output := callTool(t, service.Server(), "mailbox_send", map[string]any{
		"to_address":   "group/review",
		"from_address": "agent/sender",
		"subject":      "group update",
		"body":         "body",
		"group":        true,
	})

	if got := output["mode"]; got != mailbox.SendModeGroup {
		t.Fatalf("mode = %v, want %q", got, mailbox.SendModeGroup)
	}
	if got := output["message_id"]; got != "msg_group" {
		t.Fatalf("message_id = %v, want msg_group", got)
	}
	if got := output["group_id"]; got != "grp_1" {
		t.Fatalf("group_id = %v, want grp_1", got)
	}
	if got := output["group_address"]; got != "group/review" {
		t.Fatalf("group_address = %v, want group/review", got)
	}
	if got := output["eligible_count"]; got != float64(2) {
		t.Fatalf("eligible_count = %v, want 2", got)
	}
	if got := output["message_created_at"]; got != "2026-04-18T00:00:00Z" {
		t.Fatalf("message_created_at = %v, want timestamp", got)
	}
	if got := output["delivery_id"]; got != nil {
		t.Fatalf("delivery_id = %v, want nil", got)
	}
}

func TestMailboxSendGroupModeNotifiesSubscriber(t *testing.T) {
	mailboxService := &fakeMailboxService{t: t}
	mailboxService.sendFunc = func(_ context.Context, params mailbox.SendParams) (mailbox.SendResult, error) {
		if !params.Group {
			t.Fatal("send group = false, want true")
		}
		return mailbox.SendResult{
			Mode:             mailbox.SendModeGroup,
			MessageID:        "msg_group",
			GroupID:          "grp_1",
			GroupAddress:     "group/review",
			EligibleCount:    1,
			MessageCreatedAt: "2026-04-18T00:00:00Z",
		}, nil
	}
	mailboxService.listGroupSubscribersFunc = func(_ context.Context, groupAddress string) ([]mailbox.GroupNotificationSubscriberRecord, error) {
		if groupAddress != "group/review" {
			t.Fatalf("ListGroupNotificationSubscribers address = %q, want group/review", groupAddress)
		}
		return []mailbox.GroupNotificationSubscriberRecord{{
			SubscriberID:  "gns_1",
			GroupID:       "grp_1",
			GroupAddress:  "group/review",
			NotifyAddress: "agent-deck/moderator",
			Person:        "moderator",
			CreatedAt:     "2026-04-18T00:00:00Z",
			Active:        true,
		}, {
			SubscriberID:  "gns_2",
			GroupID:       "grp_1",
			GroupAddress:  "group/review",
			NotifyAddress: "agent-deck/observer",
			Person:        "observer",
			CreatedAt:     "2026-04-18T00:01:00Z",
			Active:        true,
		}}, nil
	}

	commandRunner := &fakeRunner{t: t, handler: func(args []string, input string) (RunResult, error) {
		switch {
		case strings.Join(args, "\x00") == strings.Join([]string{"agent-deck", "session", "show", "moderator", "--json"}, "\x00"):
			return RunResult{ExitCode: 0, Stdout: `{"id":"moderator","title":"moderator","status":"waiting"}`}, nil
		case strings.Join(args, "\x00") == strings.Join([]string{"agent-deck", "session", "show", "observer", "--json"}, "\x00"):
			return RunResult{ExitCode: 0, Stdout: `{"id":"observer","title":"observer","status":"waiting"}`}, nil
		case len(args) == 6 && args[0] == "agent-deck" && args[1] == "session" && args[2] == "send":
			if args[4] != "moderator" && args[4] != "observer" {
				t.Fatalf("notify target = %q, want moderator or observer", args[4])
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
		DisableWakeScheduler:  true,
	})

	output := callTool(t, service.Server(), "mailbox_send", map[string]any{
		"to_address":   "group/review",
		"from_address": "agent-deck/expert",
		"subject":      "expert post",
		"body":         "body",
		"group":        true,
	})

	if got := output["message_id"]; got != "msg_group" {
		t.Fatalf("message_id = %v, want msg_group", got)
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

	sentTargets := map[string]int{}
	for _, call := range commandRunner.Calls() {
		if len(call.Args) == 6 && call.Args[0] == "agent-deck" && call.Args[1] == "session" && call.Args[2] == "send" {
			sentTargets[call.Args[4]]++
		}
	}
	if sentTargets["moderator"] != 1 || sentTargets["observer"] != 1 {
		t.Fatalf("sent targets = %v, want moderator and observer once", sentTargets)
	}
}

func TestMailboxSendGroupModeSkipsSenderSubscriber(t *testing.T) {
	mailboxService := &fakeMailboxService{t: t}
	mailboxService.sendFunc = func(_ context.Context, params mailbox.SendParams) (mailbox.SendResult, error) {
		return mailbox.SendResult{
			Mode:             mailbox.SendModeGroup,
			MessageID:        "msg_group",
			GroupID:          "grp_1",
			GroupAddress:     "group/review",
			MessageCreatedAt: "2026-04-18T00:00:00Z",
		}, nil
	}
	mailboxService.listGroupSubscribersFunc = func(_ context.Context, groupAddress string) ([]mailbox.GroupNotificationSubscriberRecord, error) {
		return []mailbox.GroupNotificationSubscriberRecord{{
			SubscriberID:  "gns_1",
			GroupID:       "grp_1",
			GroupAddress:  groupAddress,
			NotifyAddress: "agent-deck/moderator",
			Active:        true,
		}}, nil
	}

	service := newService(Options{
		MailboxServiceFactory: fakeMailboxServiceFactory{service: mailboxService},
		CommandRunner: &fakeRunner{t: t, handler: func(args []string, input string) (RunResult, error) {
			t.Fatalf("unexpected command call: %v", args)
			return RunResult{}, nil
		}},
		DisableWakeScheduler: true,
	})

	output := callTool(t, service.Server(), "mailbox_send", map[string]any{
		"to_address":   "group/review",
		"from_address": "agent-deck/moderator",
		"subject":      "moderator post",
		"body":         "body",
		"group":        true,
	})
	if got := output["message_id"]; got != "msg_group" {
		t.Fatalf("message_id = %v, want msg_group", got)
	}
	if got := output["notify_status"]; got != "skipped_sender" {
		t.Fatalf("notify_status = %v, want skipped_sender", got)
	}
}

func TestMailboxSendGroupModeSkipsResolvedDefaultSenderSubscriber(t *testing.T) {
	mailboxService := &fakeMailboxService{t: t}
	mailboxService.sendFunc = func(_ context.Context, params mailbox.SendParams) (mailbox.SendResult, error) {
		if params.FromAddress != "agent-deck/moderator" {
			t.Fatalf("send from_address = %q, want resolved default sender", params.FromAddress)
		}
		return mailbox.SendResult{
			Mode:             mailbox.SendModeGroup,
			MessageID:        "msg_group",
			GroupID:          "grp_1",
			GroupAddress:     "group/review",
			MessageCreatedAt: "2026-04-18T00:00:00Z",
		}, nil
	}
	mailboxService.listGroupSubscribersFunc = func(_ context.Context, groupAddress string) ([]mailbox.GroupNotificationSubscriberRecord, error) {
		return []mailbox.GroupNotificationSubscriberRecord{{
			SubscriberID:  "gns_1",
			GroupID:       "grp_1",
			GroupAddress:  groupAddress,
			NotifyAddress: "agent-deck/moderator",
			Active:        true,
		}}, nil
	}

	service := newService(Options{
		MailboxServiceFactory: fakeMailboxServiceFactory{service: mailboxService},
		CommandRunner: &fakeRunner{t: t, handler: func(args []string, input string) (RunResult, error) {
			t.Fatalf("unexpected command call: %v", args)
			return RunResult{}, nil
		}},
		DisableWakeScheduler: true,
	})
	service.state.boundAddresses = []string{"agent-deck/moderator"}
	service.state.defaultSender = "agent-deck/moderator"
	service.state.autoBindAttempted = true

	output := callTool(t, service.Server(), "mailbox_send", map[string]any{
		"to_address": "group/review",
		"subject":    "moderator post",
		"body":       "body",
		"group":      true,
	})
	if got := output["from_address"]; got != "agent-deck/moderator" {
		t.Fatalf("from_address = %v, want agent-deck/moderator", got)
	}
	if got := output["notify_status"]; got != "skipped_sender" {
		t.Fatalf("notify_status = %v, want skipped_sender", got)
	}
}

func TestMailboxSendGroupModeKeepsReceiptWhenSubscriberNotifyFails(t *testing.T) {
	mailboxService := &fakeMailboxService{t: t}
	mailboxService.sendFunc = func(_ context.Context, params mailbox.SendParams) (mailbox.SendResult, error) {
		return mailbox.SendResult{
			Mode:             mailbox.SendModeGroup,
			MessageID:        "msg_group",
			GroupID:          "grp_1",
			GroupAddress:     "group/review",
			MessageCreatedAt: "2026-04-18T00:00:00Z",
		}, nil
	}
	mailboxService.listGroupSubscribersFunc = func(_ context.Context, groupAddress string) ([]mailbox.GroupNotificationSubscriberRecord, error) {
		return []mailbox.GroupNotificationSubscriberRecord{{
			SubscriberID:  "gns_1",
			GroupID:       "grp_1",
			GroupAddress:  groupAddress,
			NotifyAddress: "agent-deck/moderator",
			Active:        true,
		}}, nil
	}

	commandRunner := &fakeRunner{t: t, handler: func(args []string, input string) (RunResult, error) {
		switch {
		case strings.Join(args, "\x00") == strings.Join([]string{"agent-deck", "session", "show", "moderator", "--json"}, "\x00"):
			return RunResult{ExitCode: 0, Stdout: `{"id":"moderator","title":"moderator","status":"waiting"}`}, nil
		case len(args) == 6 && args[0] == "agent-deck" && args[1] == "session" && args[2] == "send":
			return RunResult{ExitCode: 1, Stderr: "notify failed"}, nil
		default:
			t.Fatalf("unexpected command args: %v", args)
			return RunResult{}, nil
		}
	}}

	service := newService(Options{
		MailboxServiceFactory: fakeMailboxServiceFactory{service: mailboxService},
		CommandRunner:         commandRunner,
		DisableWakeScheduler:  true,
	})

	output := callTool(t, service.Server(), "mailbox_send", map[string]any{
		"to_address":   "group/review",
		"from_address": "agent-deck/expert",
		"subject":      "expert post",
		"body":         "body",
		"group":        true,
	})

	if got := output["status"]; got != "sent" {
		t.Fatalf("status = %v, want sent", got)
	}
	if got := output["message_id"]; got != "msg_group" {
		t.Fatalf("message_id = %v, want msg_group", got)
	}
	if got := output["notify_status"]; got != "failed" {
		t.Fatalf("notify_status = %v, want failed", got)
	}
	if got := output["notify_error"]; got == nil || !strings.Contains(got.(string), "notify failed") {
		t.Fatalf("notify_error = %v, want notify failure detail", got)
	}
}

func TestMailboxForwardByMessageIDPreservesPayloadAndPrefixesSubject(t *testing.T) {
	sourceSenderAddress := "agent/source"
	mailboxService := &fakeMailboxService{t: t}
	mailboxService.readMessagesFunc = func(_ context.Context, messageIDs []string) ([]mailbox.ReadMessage, error) {
		if diff := slices.Compare(messageIDs, []string{"msg_1"}); diff != 0 {
			t.Fatalf("ReadMessages ids = %v, want [msg_1]", messageIDs)
		}
		return []mailbox.ReadMessage{{
			MessageID:     "msg_1",
			SenderAddress: &sourceSenderAddress,
			Subject:       "Original subject",
			ContentType:   "text/markdown",
			SchemaVersion: "v2",
			Body:          "forward me",
		}}, nil
	}
	mailboxService.sendFunc = func(_ context.Context, params mailbox.SendParams) (mailbox.SendResult, error) {
		if params.ToAddress != "workflow/target" {
			t.Fatalf("send to_address = %q, want workflow/target", params.ToAddress)
		}
		if params.FromAddress != "agent/sender" {
			t.Fatalf("send from_address = %q, want agent/sender", params.FromAddress)
		}
		if params.Subject != "Fwd: Original subject" {
			t.Fatalf("send subject = %q, want forwarded subject", params.Subject)
		}
		if params.ContentType != "text/markdown" {
			t.Fatalf("send content_type = %q, want text/markdown", params.ContentType)
		}
		if params.SchemaVersion != "v2" {
			t.Fatalf("send schema_version = %q, want v2", params.SchemaVersion)
		}
		if params.ForwardedMessageID != "msg_1" {
			t.Fatalf("send forwarded_message_id = %q, want msg_1", params.ForwardedMessageID)
		}
		if params.ForwardedFromAddress != "agent/source" {
			t.Fatalf("send forwarded_from_address = %q, want agent/source", params.ForwardedFromAddress)
		}
		if string(params.Body) != "forward me" {
			t.Fatalf("send body = %q, want forward me", string(params.Body))
		}
		return mailbox.SendResult{DeliveryID: "dlv_forwarded"}, nil
	}

	service := newService(Options{
		MailboxServiceFactory: fakeMailboxServiceFactory{service: mailboxService},
		CommandRunner: &fakeRunner{t: t, handler: func(args []string, input string) (RunResult, error) {
			return RunResult{}, errors.New("auto-bind should not run for explicit sender")
		}},
		DisableWakeScheduler: true,
	})

	output := callTool(t, service.Server(), "mailbox_forward", map[string]any{
		"message_id":   "msg_1",
		"to_address":   "workflow/target",
		"from_address": "agent/sender",
	})

	if got := output["status"]; got != "forwarded" {
		t.Fatalf("status = %v, want forwarded", got)
	}
	if got := output["delivery_id"]; got != "dlv_forwarded" {
		t.Fatalf("delivery_id = %v, want dlv_forwarded", got)
	}
	if got := output["source_message_id"]; got != "msg_1" {
		t.Fatalf("source_message_id = %v, want msg_1", got)
	}
}

func TestMailboxForwardByDeliveryIDAllowsSubjectOverride(t *testing.T) {
	sourceSenderAddress := "agent/source"
	mailboxService := &fakeMailboxService{t: t}
	mailboxService.readDeliveriesFunc = func(_ context.Context, deliveryIDs []string) ([]mailbox.ReadDelivery, error) {
		if diff := slices.Compare(deliveryIDs, []string{"dlv_1"}); diff != 0 {
			t.Fatalf("ReadDeliveries ids = %v, want [dlv_1]", deliveryIDs)
		}
		return []mailbox.ReadDelivery{{
			DeliveryID:    "dlv_1",
			MessageID:     "msg_1",
			SenderAddress: &sourceSenderAddress,
			Subject:       "Original subject",
			ContentType:   "text/plain",
			SchemaVersion: "v1",
			Body:          "forward me",
		}}, nil
	}
	mailboxService.sendFunc = func(_ context.Context, params mailbox.SendParams) (mailbox.SendResult, error) {
		if params.Subject != "Custom forward subject" {
			t.Fatalf("send subject = %q, want Custom forward subject", params.Subject)
		}
		if params.ForwardedMessageID != "msg_1" {
			t.Fatalf("send forwarded_message_id = %q, want msg_1", params.ForwardedMessageID)
		}
		if params.ForwardedFromAddress != "agent/source" {
			t.Fatalf("send forwarded_from_address = %q, want agent/source", params.ForwardedFromAddress)
		}
		return mailbox.SendResult{DeliveryID: "dlv_forwarded"}, nil
	}

	service := newService(Options{
		MailboxServiceFactory: fakeMailboxServiceFactory{service: mailboxService},
		CommandRunner: &fakeRunner{t: t, handler: func(args []string, input string) (RunResult, error) {
			return RunResult{}, errors.New("auto-bind should not run for explicit sender")
		}},
		DisableWakeScheduler: true,
	})

	output := callTool(t, service.Server(), "mailbox_forward", map[string]any{
		"delivery_id":  "dlv_1",
		"to_address":   "workflow/target",
		"from_address": "agent/sender",
		"subject":      "Custom forward subject",
	})

	if got := output["status"]; got != "forwarded" {
		t.Fatalf("status = %v, want forwarded", got)
	}
	if got := output["source_delivery_id"]; got != "dlv_1" {
		t.Fatalf("source_delivery_id = %v, want dlv_1", got)
	}
}

func TestMailboxForwardToGroupInboxPreservesGroupMode(t *testing.T) {
	sourceSenderAddress := "agent/source"
	mailboxService := &fakeMailboxService{t: t}
	mailboxService.readMessagesFunc = func(_ context.Context, messageIDs []string) ([]mailbox.ReadMessage, error) {
		if diff := slices.Compare(messageIDs, []string{"msg_1"}); diff != 0 {
			t.Fatalf("ReadMessages ids = %v, want [msg_1]", messageIDs)
		}
		return []mailbox.ReadMessage{{
			MessageID:     "msg_1",
			SenderAddress: &sourceSenderAddress,
			Subject:       "Original subject",
			ContentType:   "text/plain",
			Body:          "forward me",
		}}, nil
	}
	mailboxService.sendFunc = func(_ context.Context, params mailbox.SendParams) (mailbox.SendResult, error) {
		if !params.Group {
			t.Fatal("send group = false, want true")
		}
		if params.ToAddress != "group/review" {
			t.Fatalf("send to_address = %q, want group/review", params.ToAddress)
		}
		if params.ForwardedMessageID != "msg_1" {
			t.Fatalf("send forwarded_message_id = %q, want msg_1", params.ForwardedMessageID)
		}
		if params.ForwardedFromAddress != "agent/source" {
			t.Fatalf("send forwarded_from_address = %q, want agent/source", params.ForwardedFromAddress)
		}
		return mailbox.SendResult{
			Mode:             mailbox.SendModeGroup,
			MessageID:        "msg_forwarded",
			GroupID:          "grp_1",
			GroupAddress:     "group/review",
			EligibleCount:    1,
			MessageCreatedAt: "2026-04-18T00:00:00Z",
		}, nil
	}

	service := newService(Options{
		MailboxServiceFactory: fakeMailboxServiceFactory{service: mailboxService},
		CommandRunner: &fakeRunner{t: t, handler: func(args []string, input string) (RunResult, error) {
			return RunResult{}, errors.New("auto-bind should not run for explicit sender")
		}},
		DisableWakeScheduler: true,
	})

	output := callTool(t, service.Server(), "mailbox_forward", map[string]any{
		"message_id":   "msg_1",
		"to_address":   "group/review",
		"from_address": "agent/sender",
		"group":        true,
	})

	if got := output["status"]; got != "forwarded" {
		t.Fatalf("status = %v, want forwarded", got)
	}
	if got := output["mode"]; got != mailbox.SendModeGroup {
		t.Fatalf("mode = %v, want %q", got, mailbox.SendModeGroup)
	}
	if got := output["message_id"]; got != "msg_forwarded" {
		t.Fatalf("message_id = %v, want msg_forwarded", got)
	}
	if got := output["group_address"]; got != "group/review" {
		t.Fatalf("group_address = %v, want group/review", got)
	}
	if got := output["delivery_id"]; got != nil {
		t.Fatalf("delivery_id = %v, want nil for group send", got)
	}
	if got := output["source_message_id"]; got != "msg_1" {
		t.Fatalf("source_message_id = %v, want msg_1", got)
	}
}

func TestMailboxForwardRequiresExactlyOneSourceID(t *testing.T) {
	service := newService(Options{
		MailboxServiceFactory: fakeMailboxServiceFactory{service: &fakeMailboxService{t: t}},
		CommandRunner: &fakeRunner{t: t, handler: func(args []string, input string) (RunResult, error) {
			t.Fatalf("unexpected command call: %v", args)
			return RunResult{}, nil
		}},
		DisableWakeScheduler: true,
	})
	service.state.autoBindAttempted = true

	err := callToolExpectError(t, service.Server(), "mailbox_forward", map[string]any{
		"message_id":  "msg_1",
		"delivery_id": "dlv_1",
		"to_address":  "workflow/target",
	})
	if err == nil || !strings.Contains(err.Error(), "requires exactly one of message_id or delivery_id") {
		t.Fatalf("mailbox_forward error = %v, want source id validation", err)
	}
}

func TestMailboxSendUsesFixedWakeTextWhenDisableFlagUnset(t *testing.T) {
	mailboxService := &fakeMailboxService{t: t}
	mailboxService.sendFunc = func(_ context.Context, params mailbox.SendParams) (mailbox.SendResult, error) {
		return mailbox.SendResult{DeliveryID: "dlv_custom"}, nil
	}

	commandRunner := &fakeRunner{t: t, handler: func(args []string, input string) (RunResult, error) {
		switch {
		case strings.Join(args, "\x00") == strings.Join([]string{"agent-deck", "session", "show", "target", "--json"}, "\x00"):
			return RunResult{ExitCode: 0, Stdout: `{"id":"target","title":"coder-123","status":"waiting"}`}, nil
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
		"to_address":             "agent-deck/target",
		"subject":                "delegate",
		"body":                   "body",
		"disable_notify_message": false,
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
		DisableWakeScheduler: true,
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

func TestToolResultsIncludeMailHintWhenBoundAddressesHaveMail(t *testing.T) {
	mailboxService := &fakeMailboxService{t: t}
	mailboxService.hasVisibleDeliveryFunc = func(_ context.Context, params mailbox.WaitParams) (bool, error) {
		if len(params.Addresses) != 1 || params.Addresses[0] != "agent-deck/self" {
			t.Fatalf("hasVisibleDelivery params = %+v, want bound self address", params)
		}
		return true, nil
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

	status := callTool(t, service.Server(), "mailbox_status", nil)
	if got := status["mail_hint"]; got != defaultMailHint {
		t.Fatalf("mailbox_status mail_hint = %v, want %q", got, defaultMailHint)
	}

	resolve := callTool(t, service.Server(), "agent_deck_resolve_session", map[string]any{
		"session": "planner",
	})
	if got := resolve["mail_hint"]; got != defaultMailHint {
		t.Fatalf("agent_deck_resolve_session mail_hint = %v, want %q", got, defaultMailHint)
	}
}

func TestMailboxSendOmitsMailHintWhenAvailabilityCheckFails(t *testing.T) {
	mailboxService := &fakeMailboxService{t: t}
	mailboxService.sendFunc = func(_ context.Context, params mailbox.SendParams) (mailbox.SendResult, error) {
		return mailbox.SendResult{DeliveryID: "dlv_side_effect"}, nil
	}
	mailboxService.hasVisibleDeliveryFunc = func(_ context.Context, params mailbox.WaitParams) (bool, error) {
		return false, context.DeadlineExceeded
	}

	service := newService(Options{
		MailboxServiceFactory: fakeMailboxServiceFactory{service: mailboxService},
		CommandRunner: &fakeRunner{t: t, handler: func(args []string, input string) (RunResult, error) {
			t.Fatalf("unexpected command call: %v", args)
			return RunResult{}, nil
		}},
		DisableWakeScheduler: true,
	})
	service.state.boundAddresses = []string{"agent-deck/self"}
	service.state.defaultSender = "agent-deck/self"
	service.state.autoBindAttempted = true

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
	if got := output["mail_hint"]; got != nil {
		t.Fatalf("mail_hint = %v, want nil when availability check fails", got)
	}
}

func TestMailboxBindIncludesMailHint(t *testing.T) {
	mailboxService := &fakeMailboxService{t: t}
	mailboxService.hasVisibleDeliveryFunc = func(_ context.Context, params mailbox.WaitParams) (bool, error) {
		if len(params.Addresses) != 1 || params.Addresses[0] != "agent-deck/self" {
			t.Fatalf("hasVisibleDelivery params = %+v, want bound self address", params)
		}
		return true, nil
	}

	service := newService(Options{
		MailboxServiceFactory: fakeMailboxServiceFactory{service: mailboxService},
		CommandRunner: &fakeRunner{t: t, handler: func(args []string, input string) (RunResult, error) {
			t.Fatalf("unexpected command call: %v", args)
			return RunResult{}, nil
		}},
		DisableWakeScheduler: true,
	})
	service.state.autoBindAttempted = true

	bind := callTool(t, service.Server(), "mailbox_bind", map[string]any{
		"addresses": []string{"agent-deck/self"},
	})
	if got := bind["mail_hint"]; got != defaultMailHint {
		t.Fatalf("mailbox_bind mail_hint = %v, want %q", got, defaultMailHint)
	}
}

func TestMailboxBindRejectsInvalidAddress(t *testing.T) {
	service := newService(Options{
		MailboxServiceFactory: fakeMailboxServiceFactory{service: &fakeMailboxService{t: t}},
		CommandRunner: &fakeRunner{t: t, handler: func(args []string, input string) (RunResult, error) {
			t.Fatalf("unexpected command call: %v", args)
			return RunResult{}, nil
		}},
	})
	service.state.autoBindAttempted = true

	err := callToolExpectError(t, service.Server(), "mailbox_bind", map[string]any{
		"addresses": []string{"agent-deck"},
	})
	if err == nil || !strings.Contains(err.Error(), `invalid address`) || !strings.Contains(err.Error(), `agent-deck`) {
		t.Fatalf("mailbox_bind error = %v, want invalid address", err)
	}
	if got := service.state.boundAddresses; len(got) != 0 {
		t.Fatalf("boundAddresses = %v, want unchanged empty state", got)
	}
}

func TestMailboxBindAcceptsGenericAddressCharacters(t *testing.T) {
	service := newService(Options{
		MailboxServiceFactory: fakeMailboxServiceFactory{service: &fakeMailboxService{t: t}},
		CommandRunner: &fakeRunner{t: t, handler: func(args []string, input string) (RunResult, error) {
			t.Fatalf("unexpected command call: %v", args)
			return RunResult{}, nil
		}},
	})
	service.state.autoBindAttempted = true

	output := callTool(t, service.Server(), "mailbox_bind", map[string]any{
		"addresses": []string{"workflow/收件箱+tag@example.com"},
	})
	if got := output["bound_addresses"]; !reflect.DeepEqual(got, []any{"workflow/收件箱+tag@example.com"}) {
		t.Fatalf("bound_addresses = %v, want generic address preserved", got)
	}
}

func TestMailboxSendRejectsInvalidOverrideSender(t *testing.T) {
	service := newService(Options{
		MailboxServiceFactory: fakeMailboxServiceFactory{service: &fakeMailboxService{t: t}},
		CommandRunner: &fakeRunner{t: t, handler: func(args []string, input string) (RunResult, error) {
			t.Fatalf("unexpected command call: %v", args)
			return RunResult{}, nil
		}},
	})
	service.state.autoBindAttempted = true

	err := callToolExpectError(t, service.Server(), "mailbox_send", map[string]any{
		"to_address":   "agent-deck/target",
		"from_address": "agent-deck",
		"subject":      "delegate",
		"body":         "body",
	})
	if err == nil || !strings.Contains(err.Error(), `invalid address`) || !strings.Contains(err.Error(), `agent-deck`) {
		t.Fatalf("mailbox_send error = %v, want invalid address", err)
	}
}

func TestMailboxSendRejectsInvalidRecipientAddress(t *testing.T) {
	service := newService(Options{
		MailboxServiceFactory: fakeMailboxServiceFactory{service: &fakeMailboxService{t: t}},
		CommandRunner: &fakeRunner{t: t, handler: func(args []string, input string) (RunResult, error) {
			t.Fatalf("unexpected command call: %v", args)
			return RunResult{}, nil
		}},
	})
	service.state.autoBindAttempted = true

	err := callToolExpectError(t, service.Server(), "mailbox_send", map[string]any{
		"to_address": "agent-deck",
		"subject":    "delegate",
		"body":       "body",
	})
	if err == nil || !strings.Contains(err.Error(), `invalid address`) || !strings.Contains(err.Error(), `agent-deck`) {
		t.Fatalf("mailbox_send error = %v, want invalid recipient address", err)
	}
}

func TestMailboxRecvRejectsInvalidExplicitAddress(t *testing.T) {
	service := newService(Options{
		MailboxServiceFactory: fakeMailboxServiceFactory{service: &fakeMailboxService{t: t}},
		CommandRunner: &fakeRunner{t: t, handler: func(args []string, input string) (RunResult, error) {
			t.Fatalf("unexpected command call: %v", args)
			return RunResult{}, nil
		}},
	})
	service.state.autoBindAttempted = true

	err := callToolExpectError(t, service.Server(), "mailbox_recv", map[string]any{
		"addresses": []string{"agent-deck"},
	})
	if err == nil || !strings.Contains(err.Error(), `invalid address`) || !strings.Contains(err.Error(), `agent-deck`) {
		t.Fatalf("mailbox_recv error = %v, want invalid address", err)
	}
}

func TestServiceServerReturnsStableInstance(t *testing.T) {
	service := newService(Options{
		MailboxServiceFactory: fakeMailboxServiceFactory{service: &fakeMailboxService{t: t}},
		CommandRunner: &fakeRunner{t: t, handler: func(args []string, input string) (RunResult, error) {
			t.Fatalf("unexpected command call: %v", args)
			return RunResult{}, nil
		}},
	})

	first := service.Server()
	second := service.Server()
	if first != second {
		t.Fatal("Service.Server() returned different server instances")
	}
}

func TestMailboxOverviewResourceCapabilitiesAndNotifications(t *testing.T) {
	updateCh := make(chan struct{}, 1)
	mailboxService := &fakeMailboxService{t: t}
	mailboxService.listClaimableFunc = func(_ context.Context, addresses []string) ([]mailbox.ClaimableAddress, error) {
		if len(addresses) != 1 || addresses[0] != "agent-deck/self" {
			t.Fatalf("claimable addresses = %v, want [agent-deck/self]", addresses)
		}
		return []mailbox.ClaimableAddress{{
			Address:          "agent-deck/self",
			OldestEligibleAt: "2026-04-03T00:40:00Z",
			ClaimableCount:   1,
		}}, nil
	}

	service := newService(Options{
		MailboxServiceFactory: fakeMailboxServiceFactory{service: mailboxService},
		CommandRunner: &fakeRunner{t: t, handler: func(args []string, input string) (RunResult, error) {
			t.Fatalf("unexpected command call: %v", args)
			return RunResult{}, nil
		}},
		DisableWakeScheduler: true,
	})

	clientSession, cleanup := connectTestClientSession(t, service.Server(), updateCh)
	defer cleanup()

	caps := clientSession.InitializeResult().Capabilities
	if caps == nil || caps.Resources == nil {
		t.Fatal("resources capability missing")
	}
	if !caps.Resources.ListChanged {
		t.Fatal("resources.listChanged = false, want true")
	}
	if !caps.Resources.Subscribe {
		t.Fatal("resources.subscribe = false, want true")
	}

	if err := clientSession.Subscribe(context.Background(), &mcp.SubscribeParams{URI: mailboxOverviewURI}); err != nil {
		t.Fatalf("Subscribe() error = %v", err)
	}

	callTool(t, service.Server(), "mailbox_bind", map[string]any{
		"addresses": []string{"agent-deck/self"},
	})

	select {
	case <-updateCh:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for mailbox overview update")
	}

	resources, err := clientSession.ListResources(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListResources() error = %v", err)
	}
	if len(resources.Resources) != 1 || resources.Resources[0].URI != mailboxOverviewURI {
		t.Fatalf("resources = %#v, want mailbox overview resource", resources.Resources)
	}

	read, err := clientSession.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: mailboxOverviewURI})
	if err != nil {
		t.Fatalf("ReadResource() error = %v", err)
	}
	if len(read.Contents) != 1 {
		t.Fatalf("len(ReadResource().Contents) = %d, want 1", len(read.Contents))
	}

	var overview map[string]any
	if err := json.Unmarshal([]byte(read.Contents[0].Text), &overview); err != nil {
		t.Fatalf("unmarshal overview: %v", err)
	}
	if got := overview["default_sender"]; got != "agent-deck/self" {
		t.Fatalf("default_sender = %v, want agent-deck/self", got)
	}
	if got := overview["has_claimable_delivery"]; got != true {
		t.Fatalf("has_claimable_delivery = %v, want true", got)
	}
	if got := overview["claimable_delivery_count"]; got != float64(1) {
		t.Fatalf("claimable_delivery_count = %v, want 1", got)
	}
	if got := overview["oldest_claimable_at"]; got != "2026-04-03T00:40:00Z" {
		t.Fatalf("oldest_claimable_at = %v, want oldest claimable timestamp", got)
	}
}

func TestProcessWakeSchedulerUsesLocalHintThenAgentDeckWake(t *testing.T) {
	current := time.Date(2026, 4, 3, 6, 0, 0, 0, time.UTC)
	mailboxService := &fakeMailboxService{t: t}
	mailboxService.listFunc = func(_ context.Context, params mailbox.ListParams) ([]mailbox.ListedDelivery, error) {
		t.Fatalf("wake scheduler should not use List for claimable state: %+v", params)
		return nil, nil
	}
	mailboxService.listClaimableFunc = func(_ context.Context, addresses []string) ([]mailbox.ClaimableAddress, error) {
		if len(addresses) != 2 {
			t.Fatalf("claimable addresses = %v, want two bound addresses", addresses)
		}
		return []mailbox.ClaimableAddress{{
			Address:          "codex/self",
			OldestEligibleAt: current.Add(-4 * time.Minute).Format(time.RFC3339Nano),
			ClaimableCount:   1,
		}}, nil
	}

	commandRunner := &fakeRunner{t: t, handler: func(args []string, input string) (RunResult, error) {
		switch {
		case strings.Join(args, "\x00") == strings.Join([]string{"agent-deck", "session", "show", "worker", "--json"}, "\x00"):
			return RunResult{ExitCode: 0, Stdout: `{"id":"worker","title":"coder-123","status":"waiting"}`}, nil
		case len(args) == 6 && args[0] == "agent-deck" && args[1] == "session" && args[2] == "send":
			if args[4] != "worker" {
				t.Fatalf("notify target = %q, want worker", args[4])
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
		Now:                   func() time.Time { return current },
		DisableWakeScheduler:  true,
	})
	service.state.boundAddresses = []string{"agent-deck/worker", "codex/self"}
	service.state.defaultSender = "agent-deck/worker"
	service.state.autoBindAttempted = true
	service.state.detectedAgentDeckSession = "worker"
	service.state.detectedAgentSession = "self"
	server := service.Server()
	clientSession, cleanup := connectTestClientSession(t, server, nil)
	defer cleanup()
	if err := clientSession.Subscribe(context.Background(), &mcp.SubscribeParams{URI: mailboxOverviewURI}); err != nil {
		t.Fatalf("Subscribe() error = %v", err)
	}

	if err := service.processWakeScheduler(context.Background()); err != nil {
		t.Fatalf("processWakeScheduler(first) error = %v", err)
	}
	if len(commandRunner.Calls()) != 0 {
		t.Fatalf("command calls after local hint = %v, want none", commandRunner.Calls())
	}

	runtime := service.wakeSchedulerState.runtimeForScope("local/agent-deck/worker", current.Add(-4*time.Minute).Format(time.RFC3339Nano))
	if runtime.LastWakeByChannel[WakeHintMCPResourceUpdated] == "" {
		t.Fatal("mcp_resource_updated was not recorded")
	}
	if runtime.LastWakeByChannel[WakeChannelAgentDeck] != "" {
		t.Fatal("agent_deck wake recorded too early")
	}

	current = current.Add(defaultWakeInterChannelGap)
	if err := service.processWakeScheduler(context.Background()); err != nil {
		t.Fatalf("processWakeScheduler(second) error = %v", err)
	}

	calls := commandRunner.Calls()
	if len(calls) != 2 {
		t.Fatalf("command calls = %v, want probe + send", calls)
	}
	if got := calls[1].Args; len(got) != 6 || got[0] != "agent-deck" || got[2] != "send" {
		t.Fatalf("second command = %v, want agent-deck send", got)
	}
}

func TestProcessWakeSchedulerIgnoresDisconnectedOverviewSubscriber(t *testing.T) {
	current := time.Date(2026, 4, 3, 6, 0, 0, 0, time.UTC)
	mailboxService := &fakeMailboxService{t: t}
	mailboxService.listFunc = func(_ context.Context, params mailbox.ListParams) ([]mailbox.ListedDelivery, error) {
		t.Fatalf("wake scheduler should not use List for claimable state: %+v", params)
		return nil, nil
	}
	mailboxService.listClaimableFunc = func(_ context.Context, addresses []string) ([]mailbox.ClaimableAddress, error) {
		if len(addresses) != 2 {
			t.Fatalf("claimable addresses = %v, want two bound addresses", addresses)
		}
		return []mailbox.ClaimableAddress{{
			Address:          "codex/self",
			OldestEligibleAt: current.Add(-4 * time.Minute).Format(time.RFC3339Nano),
			ClaimableCount:   1,
		}}, nil
	}

	commandRunner := &fakeRunner{t: t, handler: func(args []string, input string) (RunResult, error) {
		switch {
		case strings.Join(args, "\x00") == strings.Join([]string{"agent-deck", "session", "show", "worker", "--json"}, "\x00"):
			return RunResult{ExitCode: 0, Stdout: `{"id":"worker","title":"coder-123","status":"waiting"}`}, nil
		case len(args) == 6 && args[0] == "agent-deck" && args[1] == "session" && args[2] == "send":
			return RunResult{ExitCode: 0}, nil
		default:
			t.Fatalf("unexpected command args: %v", args)
			return RunResult{}, nil
		}
	}}

	service := newService(Options{
		MailboxServiceFactory: fakeMailboxServiceFactory{service: mailboxService},
		CommandRunner:         commandRunner,
		Now:                   func() time.Time { return current },
		DisableWakeScheduler:  true,
	})
	service.state.boundAddresses = []string{"agent-deck/worker", "codex/self"}
	service.state.defaultSender = "agent-deck/worker"
	service.state.autoBindAttempted = true
	service.state.detectedAgentDeckSession = "worker"
	service.state.detectedAgentSession = "self"

	server := service.Server()
	clientSession, cleanup := connectTestClientSession(t, server, nil)
	if err := clientSession.Subscribe(context.Background(), &mcp.SubscribeParams{URI: mailboxOverviewURI}); err != nil {
		t.Fatalf("Subscribe() error = %v", err)
	}
	cleanup()

	if err := service.processWakeScheduler(context.Background()); err != nil {
		t.Fatalf("processWakeScheduler() error = %v", err)
	}

	calls := commandRunner.Calls()
	if len(calls) != 2 {
		t.Fatalf("command calls = %v, want probe + send after subscriber disconnect", calls)
	}

	runtime := service.wakeSchedulerState.runtimeForScope("local/agent-deck/worker", current.Add(-4*time.Minute).Format(time.RFC3339Nano))
	if runtime.LastWakeByChannel[WakeHintMCPResourceUpdated] != "" {
		t.Fatal("mcp_resource_updated remained deliverable after disconnect")
	}
	if runtime.LastWakeByChannel[WakeChannelAgentDeck] == "" {
		t.Fatal("agent_deck wake was not recorded after disconnected subscriber cleanup")
	}
}

func TestProcessWakeSchedulerExhaustsWakeableAgentDeckTargets(t *testing.T) {
	current := time.Date(2026, 4, 3, 6, 0, 0, 0, time.UTC)
	mailboxService := &fakeMailboxService{t: t}
	mailboxService.listFunc = func(_ context.Context, params mailbox.ListParams) ([]mailbox.ListedDelivery, error) {
		t.Fatalf("wake scheduler should not use List for claimable state: %+v", params)
		return nil, nil
	}
	mailboxService.listClaimableFunc = func(_ context.Context, addresses []string) ([]mailbox.ClaimableAddress, error) {
		if len(addresses) != 3 {
			t.Fatalf("claimable addresses = %v, want three bound addresses", addresses)
		}
		return []mailbox.ClaimableAddress{{
			Address:          "codex/self",
			OldestEligibleAt: current.Add(-4 * time.Minute).Format(time.RFC3339Nano),
			ClaimableCount:   1,
		}}, nil
	}

	commandRunner := &fakeRunner{t: t, handler: func(args []string, input string) (RunResult, error) {
		switch strings.Join(args, "\x00") {
		case strings.Join([]string{"agent-deck", "session", "show", "worker-a", "--json"}, "\x00"):
			return RunResult{ExitCode: 0, Stdout: `{"id":"worker-a","title":"coder-a","status":"waiting"}`}, nil
		case strings.Join([]string{"agent-deck", "session", "show", "worker-b", "--json"}, "\x00"):
			return RunResult{ExitCode: 0, Stdout: `{"id":"worker-b","title":"coder-b","status":"waiting"}`}, nil
		case strings.Join([]string{"agent-deck", "session", "send", "--no-wait", "worker-a", defaultNotifyMessage}, "\x00"):
			return RunResult{ExitCode: 1, Stderr: "first wake failed"}, nil
		case strings.Join([]string{"agent-deck", "session", "send", "--no-wait", "worker-b", defaultNotifyMessage}, "\x00"):
			return RunResult{ExitCode: 0}, nil
		default:
			t.Fatalf("unexpected command args: %v", args)
			return RunResult{}, nil
		}
	}}

	service := newService(Options{
		MailboxServiceFactory: fakeMailboxServiceFactory{service: mailboxService},
		CommandRunner:         commandRunner,
		Now:                   func() time.Time { return current },
		DisableWakeScheduler:  true,
	})
	service.state.boundAddresses = []string{"agent-deck/worker-a", "agent-deck/worker-b", "codex/self"}
	service.state.defaultSender = "agent-deck/worker-a"
	service.state.autoBindAttempted = true
	service.state.detectedAgentDeckSession = "worker-a"
	service.state.detectedAgentSession = "self"

	if err := service.processWakeScheduler(context.Background()); err != nil {
		t.Fatalf("processWakeScheduler() error = %v", err)
	}

	calls := commandRunner.Calls()
	if len(calls) != 4 {
		t.Fatalf("command calls = %v, want probe/send for both targets", calls)
	}
	if got := calls[2].Args[3]; got != "worker-b" {
		t.Fatalf("third command target = %q, want worker-b probe", got)
	}
	if got := calls[3].Args[4]; got != "worker-b" {
		t.Fatalf("fourth command target = %q, want worker-b send", got)
	}

	runtime := service.wakeSchedulerState.runtimeForScope("local/agent-deck/worker-a", current.Add(-4*time.Minute).Format(time.RFC3339Nano))
	if runtime.LastWakeByChannel[WakeChannelAgentDeck] == "" {
		t.Fatal("agent_deck wake was not recorded after second target succeeded")
	}
}

func TestProcessWakeSchedulerFallsThroughWhenMailboxOverviewUpdateFails(t *testing.T) {
	current := time.Date(2026, 4, 3, 6, 0, 0, 0, time.UTC)
	mailboxService := &fakeMailboxService{t: t}
	mailboxService.listClaimableFunc = func(_ context.Context, addresses []string) ([]mailbox.ClaimableAddress, error) {
		return []mailbox.ClaimableAddress{{
			Address:          "codex/self",
			OldestEligibleAt: current.Add(-4 * time.Minute).Format(time.RFC3339Nano),
			ClaimableCount:   1,
		}}, nil
	}

	commandRunner := &fakeRunner{t: t, handler: func(args []string, input string) (RunResult, error) {
		switch {
		case strings.Join(args, "\x00") == strings.Join([]string{"agent-deck", "session", "show", "worker", "--json"}, "\x00"):
			return RunResult{ExitCode: 0, Stdout: `{"id":"worker","title":"coder-123","status":"waiting"}`}, nil
		case len(args) == 6 && args[0] == "agent-deck" && args[1] == "session" && args[2] == "send":
			return RunResult{ExitCode: 0}, nil
		default:
			t.Fatalf("unexpected command args: %v", args)
			return RunResult{}, nil
		}
	}}

	service := newService(Options{
		MailboxServiceFactory: fakeMailboxServiceFactory{service: mailboxService},
		CommandRunner:         commandRunner,
		Now:                   func() time.Time { return current },
		DisableWakeScheduler:  true,
	})
	service.state.boundAddresses = []string{"agent-deck/worker", "codex/self"}
	service.state.defaultSender = "agent-deck/worker"
	service.state.autoBindAttempted = true
	service.state.detectedAgentDeckSession = "worker"
	service.state.detectedAgentSession = "self"
	service.mailboxOverviewEmitter = func(context.Context) notificationOutcome {
		return notificationOutcome{
			Status: "failed",
			Scheme: string(WakeHintMCPResourceUpdated),
			Err:    fmt.Errorf("resource update failed"),
		}
	}

	server := service.Server()
	clientSession, cleanup := connectTestClientSession(t, server, nil)
	defer cleanup()
	if err := clientSession.Subscribe(context.Background(), &mcp.SubscribeParams{URI: mailboxOverviewURI}); err != nil {
		t.Fatalf("Subscribe() error = %v", err)
	}

	if err := service.processWakeScheduler(context.Background()); err != nil {
		t.Fatalf("processWakeScheduler() error = %v", err)
	}

	calls := commandRunner.Calls()
	if len(calls) != 2 {
		t.Fatalf("command calls = %v, want probe + send after local hint failure", calls)
	}

	runtime := service.wakeSchedulerState.runtimeForScope("local/agent-deck/worker", current.Add(-4*time.Minute).Format(time.RFC3339Nano))
	if runtime.LastWakeByChannel[WakeHintMCPResourceUpdated] != "" {
		t.Fatal("mcp_resource_updated should not be recorded after failed local hint")
	}
	if runtime.LastWakeByChannel[WakeChannelAgentDeck] == "" {
		t.Fatal("agent_deck wake should be recorded after local hint failure")
	}
}

func TestAgentDeckRequireSessionReturnsActiveTargetWithoutStart(t *testing.T) {
	commandRunner := &fakeRunner{t: t, handler: func(args []string, input string) (RunResult, error) {
		switch {
		case strings.Join(args, "\x00") == strings.Join([]string{"agent-deck", "session", "show", "coder-ref", "--json"}, "\x00"):
			return RunResult{ExitCode: 0, Stdout: `{"id":"session-1","title":"coder-123","status":"waiting","path":"/tmp"}`}, nil
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

	output := callTool(t, service.Server(), "agent_deck_require_session", map[string]any{
		"session_ref": "coder-ref",
		"workdir":     "/tmp",
	})

	if got := output["status"]; got != "ready" {
		t.Fatalf("status = %v, want ready", got)
	}
	if got := output["created_target"]; got != false {
		t.Fatalf("created_target = %v, want false", got)
	}
	if got := output["started_session"]; got != false {
		t.Fatalf("started_session = %v, want false", got)
	}
	if got := output["notify_needed"]; got != true {
		t.Fatalf("notify_needed = %v, want true", got)
	}
	if got := output["startup_instruction_status"]; got != "not_needed_existing_session" {
		t.Fatalf("startup_instruction_status = %v, want not_needed_existing_session", got)
	}
}

func TestAgentDeckRequireSessionStartsInactiveTarget(t *testing.T) {
	commandRunner := &fakeRunner{t: t, handler: func(args []string, input string) (RunResult, error) {
		switch {
		case strings.Join(args, "\x00") == strings.Join([]string{"agent-deck", "session", "show", "coder-ref", "--json"}, "\x00"):
			return RunResult{ExitCode: 0, Stdout: `{"id":"session-1","title":"coder-123","status":"stopped","path":"/tmp"}`}, nil
		case strings.Join(args, "\x00") == strings.Join([]string{"agent-deck", "session", "start", "--json", "session-1"}, "\x00"):
			return RunResult{ExitCode: 0}, nil
		case strings.Join(args, "\x00") == strings.Join([]string{"agent-deck", "session", "show", "session-1", "--json"}, "\x00"):
			return RunResult{ExitCode: 0, Stdout: `{"id":"session-1","title":"coder-123","status":"waiting","path":"/tmp"}`}, nil
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

	output := callTool(t, service.Server(), "agent_deck_require_session", map[string]any{
		"session_ref": "coder-ref",
		"workdir":     "/tmp",
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
	if got := output["startup_instruction_status"]; got != "started" {
		t.Fatalf("startup_instruction_status = %v, want started", got)
	}
}

func TestAgentDeckRequireSessionRejectsStartupInstruction(t *testing.T) {
	service := newService(Options{
		MailboxServiceFactory: fakeMailboxServiceFactory{service: &fakeMailboxService{t: t}},
		CommandRunner: &fakeRunner{t: t, handler: func(args []string, input string) (RunResult, error) {
			t.Fatalf("unexpected command args: %v", args)
			return RunResult{}, nil
		}},
	})
	service.state.autoBindAttempted = true

	err := callToolExpectError(t, service.Server(), "agent_deck_require_session", map[string]any{
		"session_ref":         "coder-ref",
		"startup_instruction": "listen now",
		"workdir":             "/tmp",
	})
	if err == nil || !strings.Contains(err.Error(), "unexpected additional properties") {
		t.Fatalf("agent_deck_require_session error = %v, want schema-level startup_instruction validation", err)
	}
}

func TestAgentDeckRequireSessionRequiresExplicitWorkdir(t *testing.T) {
	service := newService(Options{
		MailboxServiceFactory: fakeMailboxServiceFactory{service: &fakeMailboxService{t: t}},
		CommandRunner: &fakeRunner{t: t, handler: func(args []string, input string) (RunResult, error) {
			t.Fatalf("unexpected command args: %v", args)
			return RunResult{}, nil
		}},
	})
	service.state.autoBindAttempted = true

	err := callToolExpectError(t, service.Server(), "agent_deck_require_session", map[string]any{
		"session_ref": "coder-ref",
	})
	if err == nil || !strings.Contains(err.Error(), "workdir") {
		t.Fatalf("agent_deck_require_session error = %v, want workdir validation", err)
	}
}

func TestAgentDeckRequireSessionRejectsMissingTarget(t *testing.T) {
	commandRunner := &fakeRunner{t: t, handler: func(args []string, input string) (RunResult, error) {
		switch {
		case strings.Join(args, "\x00") == strings.Join([]string{"agent-deck", "session", "show", "coder-ref", "--json"}, "\x00"):
			return RunResult{ExitCode: 1, Stderr: "not found"}, nil
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

	err := callToolExpectError(t, service.Server(), "agent_deck_require_session", map[string]any{
		"session_ref": "coder-ref",
		"workdir":     "/tmp",
	})
	if err == nil || !strings.Contains(err.Error(), "target session not found") {
		t.Fatalf("agent_deck_require_session error = %v, want missing target validation", err)
	}
}

func TestAgentDeckRequireSessionRejectsExistingSessionWithoutPath(t *testing.T) {
	commandRunner := &fakeRunner{t: t, handler: func(args []string, input string) (RunResult, error) {
		switch {
		case strings.Join(args, "\x00") == strings.Join([]string{"agent-deck", "session", "show", "coder-ref", "--json"}, "\x00"):
			return RunResult{ExitCode: 0, Stdout: `{"id":"session-1","title":"coder-123","status":"stopped"}`}, nil
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

	err := callToolExpectError(t, service.Server(), "agent_deck_require_session", map[string]any{
		"session_ref": "coder-ref",
		"workdir":     "/tmp",
	})
	if err == nil || !strings.Contains(err.Error(), "existing session path unavailable") {
		t.Fatalf("agent_deck_require_session error = %v, want existing session path unavailable", err)
	}
}

func TestAgentDeckRequireSessionRejectsExtraFields(t *testing.T) {
	service := newService(Options{
		MailboxServiceFactory: fakeMailboxServiceFactory{service: &fakeMailboxService{t: t}},
		CommandRunner: &fakeRunner{t: t, handler: func(args []string, input string) (RunResult, error) {
			t.Fatalf("unexpected command args: %v", args)
			return RunResult{}, nil
		}},
	})
	service.state.autoBindAttempted = true

	err := callToolExpectError(t, service.Server(), "agent_deck_require_session", map[string]any{
		"session_ref":       "coder-ref",
		"workdir":           "/tmp",
		"ensure_title":      "coder-ref",
		"parent_session_id": "planner-1",
	})
	if err == nil || !strings.Contains(err.Error(), "unexpected additional properties") {
		t.Fatalf("agent_deck_require_session error = %v, want schema-level extra field validation", err)
	}
}

func TestAgentDeckCreateSessionCreatesTargetWithoutDefaultStartupInstruction(t *testing.T) {
	workdir := canonicalTestWorkdir(t, "/tmp")
	commandRunner := &fakeRunner{t: t, handler: func(args []string, input string) (RunResult, error) {
		switch {
		case strings.Join(args, "\x00") == strings.Join([]string{"agent-deck", "session", "show", "coder-ref", "--json"}, "\x00"):
			return RunResult{ExitCode: 1, Stderr: "not found"}, nil
		case strings.Join(args, "\x00") == strings.Join([]string{"agent-deck", "session", "show", "planner-1", "--json"}, "\x00"):
			return RunResult{ExitCode: 0, Stdout: `{"id":"planner-1","title":"planner","status":"waiting","path":"/tmp"}`}, nil
		case strings.Join(args, "\x00") == strings.Join([]string{"agent-deck", "launch", "--json", "--title", "coder-ref", "--cmd", "codex --model gpt-5.4 --ask-for-approval on-request", "--parent", "planner-1", workdir}, "\x00"):
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

	output := callTool(t, service.Server(), "agent_deck_create_session", map[string]any{
		"ensure_title":      "coder-ref",
		"ensure_cmd":        "codex --model gpt-5.4 --ask-for-approval on-request",
		"parent_session_id": "planner-1",
		"workdir":           "/tmp",
	})

	if got := output["created_target"]; got != true {
		t.Fatalf("created_target = %v, want true", got)
	}
	if got := output["status"]; got != "created" {
		t.Fatalf("status = %v, want created", got)
	}
	if got := output["startup_instruction_status"]; got != "started" {
		t.Fatalf("startup_instruction_status = %v, want started", got)
	}
}

func TestAgentDeckCreateSessionRejectsExistingTarget(t *testing.T) {
	commandRunner := &fakeRunner{t: t, handler: func(args []string, input string) (RunResult, error) {
		switch {
		case strings.Join(args, "\x00") == strings.Join([]string{"agent-deck", "session", "show", "coder-ref", "--json"}, "\x00"):
			return RunResult{ExitCode: 0, Stdout: `{"id":"session-9","title":"coder-ref","status":"waiting","path":"/tmp"}`}, nil
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

	err := callToolExpectError(t, service.Server(), "agent_deck_create_session", map[string]any{
		"ensure_title":   "coder-ref",
		"ensure_cmd":     "codex --model gpt-5.4 --ask-for-approval on-request",
		"no_parent_link": true,
		"workdir":        "/tmp",
	})
	if err == nil || !strings.Contains(err.Error(), "target session already exists") {
		t.Fatalf("agent_deck_create_session error = %v, want existing target validation", err)
	}
}

func TestAgentDeckCreateSessionRejectsExistingTargetWithMismatchedWorkdir(t *testing.T) {
	commandRunner := &fakeRunner{t: t, handler: func(args []string, input string) (RunResult, error) {
		switch {
		case strings.Join(args, "\x00") == strings.Join([]string{"agent-deck", "session", "show", "coder-ref", "--json"}, "\x00"):
			return RunResult{ExitCode: 0, Stdout: `{"id":"session-9","title":"coder-ref","status":"waiting","path":"/var/tmp"}`}, nil
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

	err := callToolExpectError(t, service.Server(), "agent_deck_create_session", map[string]any{
		"ensure_title":   "coder-ref",
		"ensure_cmd":     "codex --model gpt-5.4 --ask-for-approval on-request",
		"no_parent_link": true,
		"workdir":        "/tmp",
	})
	if err == nil || !strings.Contains(err.Error(), "session path mismatch") {
		t.Fatalf("agent_deck_create_session error = %v, want workdir mismatch validation", err)
	}
}

func TestAgentDeckCreateSessionAllowsDetachedCreateWithoutGroup(t *testing.T) {
	workdir := canonicalTestWorkdir(t, "/tmp")
	commandRunner := &fakeRunner{t: t, handler: func(args []string, input string) (RunResult, error) {
		switch {
		case strings.Join(args, "\x00") == strings.Join([]string{"agent-deck", "session", "show", "coder-ref", "--json"}, "\x00"):
			return RunResult{ExitCode: 1, Stderr: "not found"}, nil
		case strings.Join(args, "\x00") == strings.Join([]string{"agent-deck", "launch", "--json", "--title", "coder-ref", "--cmd", "codex --model gpt-5.4 --ask-for-approval on-request", "--no-parent", workdir}, "\x00"):
			return RunResult{ExitCode: 0, Stdout: `{"id":"session-2","title":"coder-ref","status":"waiting","path":"/tmp"}`}, nil
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

	output := callTool(t, service.Server(), "agent_deck_create_session", map[string]any{
		"ensure_title":   "coder-ref",
		"ensure_cmd":     "codex --model gpt-5.4 --ask-for-approval on-request",
		"no_parent_link": true,
		"workdir":        "/tmp",
	})
	if got := output["created_target"]; got != true {
		t.Fatalf("created_target = %v, want true", got)
	}
	if got := output["started_session"]; got != true {
		t.Fatalf("started_session = %v, want true", got)
	}
	if got := output["path"]; got != "/tmp" {
		t.Fatalf("path = %v, want /tmp", got)
	}
}

func TestAgentDeckCreateSessionDerivesChildGroupFromChildParentSession(t *testing.T) {
	workdir := canonicalTestWorkdir(t, "/tmp")
	commandRunner := &fakeRunner{t: t, handler: func(args []string, input string) (RunResult, error) {
		switch {
		case strings.Join(args, "\x00") == strings.Join([]string{"agent-deck", "session", "show", "coder-ref", "--json"}, "\x00"):
			return RunResult{ExitCode: 1, Stderr: "not found"}, nil
		case strings.Join(args, "\x00") == strings.Join([]string{"agent-deck", "session", "show", "child-planner", "--json"}, "\x00"):
			return RunResult{ExitCode: 0, Stdout: `{"id":"child-planner","title":"planner-child","status":"waiting","path":"/tmp","group":"planning/active","parent_session_id":"root-planner"}`}, nil
		case strings.Join(args, "\x00") == strings.Join([]string{"agent-deck", "group", "list", "--json"}, "\x00"):
			return RunResult{ExitCode: 0, Stdout: `{"groups":[{"path":"planning"},{"path":"planning/active"}]}`}, nil
		case strings.Join(args, "\x00") == strings.Join([]string{"agent-deck", "group", "create", "planner-child", "--parent", "planning/active"}, "\x00"):
			return RunResult{ExitCode: 0}, nil
		case strings.Join(args, "\x00") == strings.Join([]string{"agent-deck", "launch", "--json", "--title", "coder-ref", "--cmd", "codex --model gpt-5.4 --ask-for-approval on-request", "--group", "planning/active/planner-child", "--no-parent", workdir}, "\x00"):
			return RunResult{ExitCode: 0, Stdout: `{"id":"session-2","title":"coder-ref","status":"waiting","group":"planning/active/planner-child","path":"/tmp"}`}, nil
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

	output := callTool(t, service.Server(), "agent_deck_create_session", map[string]any{
		"ensure_title":      "coder-ref",
		"ensure_cmd":        "codex --model gpt-5.4 --ask-for-approval on-request",
		"parent_session_id": "child-planner",
		"workdir":           "/tmp",
	})
	if got := output["created_target"]; got != true {
		t.Fatalf("created_target = %v, want true", got)
	}
	if got := output["group"]; got != "planning/active/planner-child" {
		t.Fatalf("group = %v, want planning/active/planner-child", got)
	}
}

func TestAgentDeckCreateSessionDerivesTopLevelGroupFromChildParentWithoutGroup(t *testing.T) {
	workdir := canonicalTestWorkdir(t, "/tmp")
	commandRunner := &fakeRunner{t: t, handler: func(args []string, input string) (RunResult, error) {
		switch {
		case strings.Join(args, "\x00") == strings.Join([]string{"agent-deck", "session", "show", "coder-ref", "--json"}, "\x00"):
			return RunResult{ExitCode: 1, Stderr: "not found"}, nil
		case strings.Join(args, "\x00") == strings.Join([]string{"agent-deck", "session", "show", "child-planner", "--json"}, "\x00"):
			return RunResult{ExitCode: 0, Stdout: `{"id":"child-planner","title":"planner-child","status":"waiting","path":"/tmp","group":"","parent_session_id":"root-planner"}`}, nil
		case strings.Join(args, "\x00") == strings.Join([]string{"agent-deck", "group", "list", "--json"}, "\x00"):
			return RunResult{ExitCode: 0, Stdout: `{"groups":[]}`}, nil
		case strings.Join(args, "\x00") == strings.Join([]string{"agent-deck", "group", "create", "planner-child"}, "\x00"):
			return RunResult{ExitCode: 0}, nil
		case strings.Join(args, "\x00") == strings.Join([]string{"agent-deck", "launch", "--json", "--title", "coder-ref", "--cmd", "codex --model gpt-5.4 --ask-for-approval on-request", "--group", "planner-child", "--no-parent", workdir}, "\x00"):
			return RunResult{ExitCode: 0, Stdout: `{"id":"session-2","title":"coder-ref","status":"waiting","group":"planner-child","path":"/tmp"}`}, nil
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

	output := callTool(t, service.Server(), "agent_deck_create_session", map[string]any{
		"ensure_title":      "coder-ref",
		"ensure_cmd":        "codex --model gpt-5.4 --ask-for-approval on-request",
		"parent_session_id": "child-planner",
		"workdir":           "/tmp",
	})
	if got := output["created_target"]; got != true {
		t.Fatalf("created_target = %v, want true", got)
	}
	if got := output["group"]; got != "planner-child" {
		t.Fatalf("group = %v, want planner-child", got)
	}
}

func TestAgentDeckCreateSessionDropsChildParentLinkForExplicitGroupPath(t *testing.T) {
	workdir := canonicalTestWorkdir(t, "/tmp")
	commandRunner := &fakeRunner{t: t, handler: func(args []string, input string) (RunResult, error) {
		switch {
		case strings.Join(args, "\x00") == strings.Join([]string{"agent-deck", "session", "show", "coder-ref", "--json"}, "\x00"):
			return RunResult{ExitCode: 1, Stderr: "not found"}, nil
		case strings.Join(args, "\x00") == strings.Join([]string{"agent-deck", "session", "show", "child-planner", "--json"}, "\x00"):
			return RunResult{ExitCode: 0, Stdout: `{"id":"child-planner","title":"planner-child","status":"waiting","path":"/tmp","group":"planning/active","parent_session_id":"root-planner"}`}, nil
		case strings.Join(args, "\x00") == strings.Join([]string{"agent-deck", "group", "list", "--json"}, "\x00"):
			return RunResult{ExitCode: 0, Stdout: `{"groups":[{"path":"planning"},{"path":"planning/active"},{"path":"reviews"},{"path":"reviews/ready"}]}`}, nil
		case strings.Join(args, "\x00") == strings.Join([]string{"agent-deck", "launch", "--json", "--title", "coder-ref", "--cmd", "codex --model gpt-5.4 --ask-for-approval on-request", "--group", "reviews/ready", "--no-parent", workdir}, "\x00"):
			return RunResult{ExitCode: 0, Stdout: `{"id":"session-2","title":"coder-ref","status":"waiting","group":"reviews/ready","path":"/tmp"}`}, nil
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

	output := callTool(t, service.Server(), "agent_deck_create_session", map[string]any{
		"ensure_title":      "coder-ref",
		"ensure_cmd":        "codex --model gpt-5.4 --ask-for-approval on-request",
		"parent_session_id": "child-planner",
		"group_path":        "reviews/ready",
		"workdir":           "/tmp",
	})
	if got := output["created_target"]; got != true {
		t.Fatalf("created_target = %v, want true", got)
	}
	if got := output["group"]; got != "reviews/ready" {
		t.Fatalf("group = %v, want reviews/ready", got)
	}
}

func TestAgentDeckCreateSessionDerivesGroupFromGroupParentSession(t *testing.T) {
	workdir := canonicalTestWorkdir(t, "/tmp")
	commandRunner := &fakeRunner{t: t, handler: func(args []string, input string) (RunResult, error) {
		switch {
		case strings.Join(args, "\x00") == strings.Join([]string{"agent-deck", "session", "show", "coder-ref", "--json"}, "\x00"):
			return RunResult{ExitCode: 1, Stderr: "not found"}, nil
		case strings.Join(args, "\x00") == strings.Join([]string{"agent-deck", "session", "show", "planner-1", "--json"}, "\x00"):
			return RunResult{ExitCode: 0, Stdout: `{"id":"planner-1","title":"planner","status":"waiting","group":"planning","path":"/tmp"}`}, nil
		case strings.Join(args, "\x00") == strings.Join([]string{"agent-deck", "group", "list", "--json"}, "\x00"):
			return RunResult{ExitCode: 0, Stdout: `{"groups":[{"path":"planning"}]}`}, nil
		case strings.Join(args, "\x00") == strings.Join([]string{"agent-deck", "group", "create", "coder-review", "--parent", "planning"}, "\x00"):
			return RunResult{ExitCode: 0}, nil
		case strings.Join(args, "\x00") == strings.Join([]string{"agent-deck", "launch", "--json", "--title", "coder-ref", "--cmd", "codex --model gpt-5.4 --ask-for-approval on-request", "--group", "planning/coder-review", workdir}, "\x00"):
			return RunResult{ExitCode: 0, Stdout: `{"id":"session-2","title":"coder-ref","status":"waiting","group":"planning/coder-review","path":"/tmp"}`}, nil
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

	output := callTool(t, service.Server(), "agent_deck_create_session", map[string]any{
		"ensure_title":            "coder-ref",
		"ensure_cmd":              "codex --model gpt-5.4 --ask-for-approval on-request",
		"group_parent_session_id": "planner-1",
		"child_group_name":        "Coder Review",
		"workdir":                 "/tmp",
	})
	if got := output["group"]; got != "planning/coder-review" {
		t.Fatalf("group = %v, want planning/coder-review", got)
	}
}

func TestAgentDeckCreateSessionDoesNotCreateGroupWhenCreateValidationFails(t *testing.T) {
	commandRunner := &fakeRunner{t: t, handler: func(args []string, input string) (RunResult, error) {
		t.Fatalf("unexpected command args: %v", args)
		return RunResult{}, nil
	}}

	service := newService(Options{
		MailboxServiceFactory: fakeMailboxServiceFactory{service: &fakeMailboxService{t: t}},
		CommandRunner:         commandRunner,
	})
	service.state.autoBindAttempted = true

	err := callToolExpectError(t, service.Server(), "agent_deck_create_session", map[string]any{
		"ensure_title": "coder-ref",
		"group_path":   "reviews/new-group",
		"workdir":      "/tmp",
	})
	if err == nil || !strings.Contains(err.Error(), "ensure_cmd is required") {
		t.Fatalf("agent_deck_create_session error = %v, want ensure_cmd is required", err)
	}
}

func TestAgentDeckRequireSessionAcceptsSymlinkedEquivalentWorkdir(t *testing.T) {
	baseDir := t.TempDir()
	realDir := filepath.Join(baseDir, "real")
	if err := os.Mkdir(realDir, 0o755); err != nil {
		t.Fatalf("Mkdir(realDir) error = %v", err)
	}
	symlinkDir := filepath.Join(baseDir, "linked")
	if err := os.Symlink(realDir, symlinkDir); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}

	commandRunner := &fakeRunner{t: t, handler: func(args []string, input string) (RunResult, error) {
		switch {
		case strings.Join(args, "\x00") == strings.Join([]string{"agent-deck", "session", "show", "coder-ref", "--json"}, "\x00"):
			return RunResult{ExitCode: 0, Stdout: fmt.Sprintf(`{"id":"session-1","title":"coder-123","status":"stopped","path":%q}`, realDir)}, nil
		case strings.Join(args, "\x00") == strings.Join([]string{"agent-deck", "session", "start", "--json", "session-1"}, "\x00"):
			return RunResult{ExitCode: 0}, nil
		case strings.Join(args, "\x00") == strings.Join([]string{"agent-deck", "session", "show", "session-1", "--json"}, "\x00"):
			return RunResult{ExitCode: 0, Stdout: fmt.Sprintf(`{"id":"session-1","title":"coder-123","status":"waiting","path":%q}`, realDir)}, nil
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

	output := callTool(t, service.Server(), "agent_deck_require_session", map[string]any{
		"session_ref": "coder-ref",
		"workdir":     symlinkDir,
	})
	if got := output["status"]; got != "ready" {
		t.Fatalf("status = %v, want ready", got)
	}
	if got := output["started_session"]; got != true {
		t.Fatalf("started_session = %v, want true", got)
	}
}

func TestAgentDeckCreateSessionCreatesTargetWithGroupPathAndNoParentLink(t *testing.T) {
	workdir := canonicalTestWorkdir(t, "/tmp")
	commandRunner := &fakeRunner{t: t, handler: func(args []string, input string) (RunResult, error) {
		switch {
		case strings.Join(args, "\x00") == strings.Join([]string{"agent-deck", "session", "show", "coder-ref", "--json"}, "\x00"):
			return RunResult{ExitCode: 1, Stderr: "not found"}, nil
		case strings.Join(args, "\x00") == strings.Join([]string{"agent-deck", "group", "list", "--json"}, "\x00"):
			return RunResult{ExitCode: 0, Stdout: `{"groups":[]}`}, nil
		case strings.Join(args, "\x00") == strings.Join([]string{"agent-deck", "group", "create", "reviews"}, "\x00"):
			return RunResult{ExitCode: 0}, nil
		case strings.Join(args, "\x00") == strings.Join([]string{"agent-deck", "launch", "--json", "--title", "coder-ref", "--cmd", "codex --model gpt-5.4 --ask-for-approval on-request", "--group", "reviews", "--no-parent", workdir}, "\x00"):
			return RunResult{ExitCode: 0, Stdout: `{"id":"session-2","title":"coder-ref","status":"waiting","group":"reviews","path":"/tmp"}`}, nil
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

	output := callTool(t, service.Server(), "agent_deck_create_session", map[string]any{
		"ensure_title":   "coder-ref",
		"ensure_cmd":     "codex --model gpt-5.4 --ask-for-approval on-request",
		"group_path":     "reviews",
		"no_parent_link": true,
		"workdir":        "/tmp",
	})

	if got := output["created_target"]; got != true {
		t.Fatalf("created_target = %v, want true", got)
	}
	if got := output["group"]; got != "reviews" {
		t.Fatalf("group = %v, want reviews", got)
	}
	if got := output["path"]; got != "/tmp" {
		t.Fatalf("path = %v, want /tmp", got)
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

func TestMailboxRecvWithTimeoutClaimsMessageSentLater(t *testing.T) {
	t.Parallel()

	stateDir := filepath.Join(t.TempDir(), "mailbox-state")
	service := newService(Options{
		StateDir: stateDir,
		CommandRunner: &fakeRunner{t: t, handler: func(args []string, input string) (RunResult, error) {
			t.Fatalf("unexpected command call: %v", args)
			return RunResult{}, nil
		}},
		DisableWakeScheduler:  true,
		DisableLeaseRenewLoop: true,
	})
	service.state.boundAddresses = []string{"agent-deck/self"}
	service.state.defaultSender = "agent-deck/self"
	service.state.autoBindAttempted = true

	type recvResult struct {
		output map[string]any
		err    error
	}
	resultCh := make(chan recvResult, 1)
	go func() {
		_, output, err := service.mailboxRecv(context.Background(), nil, mailboxRecvInput{
			Addresses: []string{"agent-deck/self"},
			Timeout:   "500ms",
		})
		resultCh <- recvResult{output: output, err: err}
	}()

	time.Sleep(75 * time.Millisecond)
	send := callTool(t, service.Server(), "mailbox_send", map[string]any{
		"to_address": "agent-deck/self",
		"subject":    "blocking recv",
		"body":       "body",
	})
	deliveryID := send["delivery_id"].(string)

	result := <-resultCh
	if result.err != nil {
		t.Fatalf("mailboxRecv error = %v", result.err)
	}
	if got := result.output["status"]; got != "received" {
		t.Fatalf("recv status = %v, want received", got)
	}
	encodedDelivery, err := json.Marshal(result.output["delivery"])
	if err != nil {
		t.Fatalf("marshal recv delivery: %v", err)
	}
	var received map[string]any
	if err := json.Unmarshal(encodedDelivery, &received); err != nil {
		t.Fatalf("unmarshal recv delivery: %v", err)
	}
	messages := received["messages"].([]any)
	if len(messages) != 1 {
		t.Fatalf("recv messages = %d, want 1", len(messages))
	}
	message := messages[0].(map[string]any)
	if got := message["delivery_id"]; got != deliveryID {
		t.Fatalf("recv delivery_id = %v, want %s", got, deliveryID)
	}
	if got := message["body"]; got != "body" {
		t.Fatalf("recv body = %v, want body", got)
	}
	if !service.activeLeases.hasTrackedLeases() {
		t.Fatal("recv with timeout did not track active lease")
	}
}

func TestMailboxRecvWithTimeoutClaimsWithParentContext(t *testing.T) {
	t.Parallel()

	mailboxService := &fakeMailboxService{t: t}
	mailboxService.hasVisibleDeliveryFunc = func(ctx context.Context, params mailbox.WaitParams) (bool, error) {
		if _, hasDeadline := ctx.Deadline(); !hasDeadline {
			return false, nil
		}
		if !reflect.DeepEqual(params.Addresses, []string{"agent-deck/self"}) {
			t.Fatalf("HasVisibleDelivery addresses = %v, want [agent-deck/self]", params.Addresses)
		}
		return true, nil
	}
	mailboxService.receiveBatchWithTTLFunc = func(ctx context.Context, params mailbox.ReceiveBatchParams, ttl time.Duration) (mailbox.ReceiveResult, error) {
		if _, hasDeadline := ctx.Deadline(); hasDeadline {
			t.Fatal("ReceiveBatchWithLeaseTTL got timeout context, want parent call context")
		}
		if ttl <= 0 {
			t.Fatalf("ReceiveBatchWithLeaseTTL ttl = %s, want positive", ttl)
		}
		return mailbox.ReceiveResult{
			Messages: []mailbox.ReceivedMessage{{
				DeliveryID:       "dlv_parent_ctx",
				RecipientAddress: "agent-deck/self",
				LeaseToken:       "lease_parent_ctx",
				Subject:          "parent ctx",
				Body:             "body",
			}},
		}, nil
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

	output := callTool(t, service.Server(), "mailbox_recv", map[string]any{
		"addresses": []string{"agent-deck/self"},
		"timeout":   "30s",
	})
	if got := output["status"]; got != "received" {
		t.Fatalf("recv status = %v, want received", got)
	}
	if !service.activeLeases.hasTrackedLeases() {
		t.Fatal("recv with timeout did not track active lease")
	}
}

func TestMailboxRecvWithTimeoutReturnsNoMessage(t *testing.T) {
	t.Parallel()

	callCount := 0
	mailboxService := &fakeMailboxService{t: t}
	mailboxService.hasVisibleDeliveryFunc = func(ctx context.Context, params mailbox.WaitParams) (bool, error) {
		callCount++
		if !reflect.DeepEqual(params.Addresses, []string{"agent-deck/self"}) {
			t.Fatalf("HasVisibleDelivery addresses = %v, want [agent-deck/self]", params.Addresses)
		}
		return false, nil
	}
	mailboxService.receiveBatchFunc = func(_ context.Context, params mailbox.ReceiveBatchParams) (mailbox.ReceiveResult, error) {
		t.Fatalf("ReceiveBatch called without visible delivery: %+v", params)
		return mailbox.ReceiveResult{}, nil
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

	output := callTool(t, service.Server(), "mailbox_recv", map[string]any{
		"addresses": []string{"agent-deck/self"},
		"timeout":   "25ms",
	})
	if got := output["status"]; got != "no_message" {
		t.Fatalf("recv status = %v, want no_message", got)
	}
	if callCount == 0 {
		t.Fatal("ReceiveBatch was not called")
	}
	if service.activeLeases.hasTrackedLeases() {
		t.Fatal("no-message recv tracked active lease")
	}
}

func TestMailboxRecvWithTimeoutBoundsAvailabilityCheck(t *testing.T) {
	t.Parallel()

	mailboxService := &fakeMailboxService{t: t}
	mailboxService.hasVisibleDeliveryFunc = func(ctx context.Context, params mailbox.WaitParams) (bool, error) {
		if _, hasDeadline := ctx.Deadline(); !hasDeadline {
			return false, nil
		}
		<-ctx.Done()
		return false, ctx.Err()
	}
	mailboxService.receiveBatchFunc = func(_ context.Context, params mailbox.ReceiveBatchParams) (mailbox.ReceiveResult, error) {
		t.Fatalf("ReceiveBatch called after availability timeout: %+v", params)
		return mailbox.ReceiveResult{}, nil
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

	startedAt := time.Now()
	output := callTool(t, service.Server(), "mailbox_recv", map[string]any{
		"addresses": []string{"agent-deck/self"},
		"timeout":   "25ms",
	})
	if got := output["status"]; got != "no_message" {
		t.Fatalf("recv status = %v, want no_message", got)
	}
	if elapsed := time.Since(startedAt); elapsed > 500*time.Millisecond {
		t.Fatalf("recv timeout elapsed = %s, want bounded by availability timeout", elapsed)
	}
}

func TestMailboxRecvWithoutTimeoutRemainsImmediate(t *testing.T) {
	t.Parallel()

	callCount := 0
	mailboxService := &fakeMailboxService{t: t}
	mailboxService.receiveBatchFunc = func(_ context.Context, params mailbox.ReceiveBatchParams) (mailbox.ReceiveResult, error) {
		callCount++
		return mailbox.ReceiveResult{}, mailbox.ErrNoMessage
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

	output := callTool(t, service.Server(), "mailbox_recv", map[string]any{
		"addresses": []string{"agent-deck/self"},
	})
	if got := output["status"]; got != "no_message" {
		t.Fatalf("recv status = %v, want no_message", got)
	}
	if callCount != 1 {
		t.Fatalf("ReceiveBatch calls = %d, want 1", callCount)
	}
}

func TestMailboxGroupMCPRuntimeFlow(t *testing.T) {
	t.Parallel()

	stateDir := filepath.Join(t.TempDir(), "mailbox-state")
	service := newService(Options{
		StateDir: stateDir,
		CommandRunner: &fakeRunner{t: t, handler: func(args []string, input string) (RunResult, error) {
			t.Fatalf("unexpected command call: %v", args)
			return RunResult{}, nil
		}},
		DisableWakeScheduler:  true,
		DisableLeaseRenewLoop: true,
	})
	service.state.boundAddresses = []string{"agent/sender"}
	service.state.defaultSender = "agent/sender"
	service.state.autoBindAttempted = true

	created := callTool(t, service.Server(), "mailbox_group_create", map[string]any{
		"group_address": "group/review",
	})
	group := created["group"].(map[string]any)
	if group["address"] != "group/review" {
		t.Fatalf("created group address = %v, want group/review", group["address"])
	}

	callTool(t, service.Server(), "mailbox_group_add_member", map[string]any{
		"group_address": "group/review",
		"person":        "alice",
	})
	callTool(t, service.Server(), "mailbox_group_add_member", map[string]any{
		"group_address": "group/review",
		"person":        "bob",
	})

	send := callTool(t, service.Server(), "mailbox_send", map[string]any{
		"to_address": "group/review",
		"subject":    "group update",
		"body":       "group body",
		"group":      true,
	})
	if got := send["mode"]; got != mailbox.SendModeGroup {
		t.Fatalf("send mode = %v, want group", got)
	}
	if got := send["delivery_id"]; got != nil {
		t.Fatalf("group send delivery_id = %v, want nil", got)
	}

	wait := callTool(t, service.Server(), "mailbox_wait", map[string]any{
		"addresses": []string{"group/review"},
		"as_person": "alice",
	})
	if got := wait["status"]; got != "message_available" {
		t.Fatalf("wait status = %v, want message_available", got)
	}
	waitMessage := wait["message"].(map[string]any)
	if waitMessage["read"] != false {
		t.Fatalf("wait read = %v, want false", waitMessage["read"])
	}
	if _, ok := waitMessage["delivery_id"]; ok {
		t.Fatalf("group wait exposed delivery_id: %v", waitMessage)
	}

	recv := callTool(t, service.Server(), "mailbox_recv", map[string]any{
		"addresses": []string{"group/review"},
		"as_person": "alice",
	})
	if got := recv["status"]; got != "received" {
		t.Fatalf("recv status = %v, want received", got)
	}
	recvMessage := recv["message"].(map[string]any)
	if recvMessage["body"] != "group body" {
		t.Fatalf("recv body = %v, want group body", recvMessage["body"])
	}
	if _, ok := recvMessage["lease_token"]; ok {
		t.Fatalf("group recv exposed lease_token: %v", recvMessage)
	}
	if service.activeLeases.hasTrackedLeases() {
		t.Fatal("group recv tracked active lease")
	}

	list := callTool(t, service.Server(), "mailbox_list", map[string]any{
		"address":   "group/review",
		"as_person": "alice",
	})
	deliveries := list["deliveries"].([]any)
	if len(deliveries) != 1 {
		t.Fatalf("group list deliveries = %d, want 1", len(deliveries))
	}
	listed := deliveries[0].(map[string]any)
	if listed["read"] != true {
		t.Fatalf("listed read = %v, want true after recv", listed["read"])
	}

	members := callTool(t, service.Server(), "mailbox_group_members", map[string]any{
		"group_address": "group/review",
	})
	if got := len(members["memberships"].([]any)); got != 2 {
		t.Fatalf("group members = %d, want 2", got)
	}

	inspect := callTool(t, service.Server(), "mailbox_address_inspect", map[string]any{
		"address": "group/review",
	})
	if got := inspect["inspection"].(map[string]any)["kind"]; got != mailbox.AddressKindGroup {
		t.Fatalf("inspect kind = %v, want group", got)
	}
}

func TestMailboxGroupSendRuntimeKeepsMessageWhenSubscriberNotifyFails(t *testing.T) {
	t.Parallel()

	stateDir := filepath.Join(t.TempDir(), "mailbox-state")
	service := newService(Options{
		StateDir: stateDir,
		CommandRunner: &fakeRunner{t: t, handler: func(args []string, input string) (RunResult, error) {
			switch {
			case strings.Join(args, "\x00") == strings.Join([]string{"agent-deck", "session", "show", "moderator", "--json"}, "\x00"):
				return RunResult{ExitCode: 0, Stdout: `{"id":"moderator","title":"moderator","status":"waiting"}`}, nil
			case len(args) == 6 && args[0] == "agent-deck" && args[1] == "session" && args[2] == "send":
				return RunResult{ExitCode: 1, Stderr: "notify failed"}, nil
			default:
				t.Fatalf("unexpected command call: %v", args)
				return RunResult{}, nil
			}
		}},
		DisableWakeScheduler:  true,
		DisableLeaseRenewLoop: true,
	})
	service.state.autoBindAttempted = true

	callTool(t, service.Server(), "mailbox_group_create", map[string]any{
		"group_address": "group/review",
	})
	callTool(t, service.Server(), "mailbox_group_add_member", map[string]any{
		"group_address": "group/review",
		"person":        "alice",
	})
	callTool(t, service.Server(), "mailbox_group_add_subscriber", map[string]any{
		"group_address":  "group/review",
		"notify_address": "agent-deck/moderator",
		"person":         "moderator",
	})

	send := callTool(t, service.Server(), "mailbox_send", map[string]any{
		"to_address":   "group/review",
		"from_address": "agent-deck/expert",
		"subject":      "expert post",
		"body":         "group body",
		"group":        true,
	})
	if got := send["status"]; got != "sent" {
		t.Fatalf("send status = %v, want sent", got)
	}
	if got := send["notify_status"]; got != "failed" {
		t.Fatalf("notify_status = %v, want failed", got)
	}

	recv := callTool(t, service.Server(), "mailbox_recv", map[string]any{
		"addresses": []string{"group/review"},
		"as_person": "alice",
	})
	message := recv["message"].(map[string]any)
	if message["message_id"] != send["message_id"] {
		t.Fatalf("recv message_id = %v, want %v", message["message_id"], send["message_id"])
	}
	if message["body"] != "group body" {
		t.Fatalf("recv body = %v, want group body", message["body"])
	}
}

func TestMailboxRecvExposesForwardedFromAddressInCompactPayload(t *testing.T) {
	forwardedFromAddress := "agent/source"
	mailboxService := &fakeMailboxService{t: t}
	mailboxService.receiveBatchWithTTLFunc = func(_ context.Context, params mailbox.ReceiveBatchParams, ttl time.Duration) (mailbox.ReceiveResult, error) {
		return mailbox.ReceiveResult{
			Messages: []mailbox.ReceivedMessage{{
				DeliveryID:           "dlv_forwarded",
				MessageID:            "msg_forwarded",
				ForwardedFromAddress: &forwardedFromAddress,
				LeaseToken:           "lease_forwarded",
				LeaseExpiresAt:       time.Now().UTC().Add(defaultMCPLeaseTTL).Format(time.RFC3339Nano),
				RecipientAddress:     "agent-deck/self",
				Subject:              "delegate",
				Body:                 "body",
			}},
		}, nil
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
	if got := message["forwarded_from_address"]; got != "agent/source" {
		t.Fatalf("forwarded_from_address = %v, want agent/source", got)
	}
	assertMCPMapOmitsForwardedMessageID(t, message)
}

func TestMailboxWaitExposesForwardedFromAddressInCompactPayload(t *testing.T) {
	forwardedFromAddress := "agent/source"
	mailboxService := &fakeMailboxService{t: t}
	mailboxService.waitFunc = func(_ context.Context, params mailbox.WaitParams) (mailbox.ListedDelivery, error) {
		return mailbox.ListedDelivery{
			DeliveryID:           "dlv_forwarded",
			MessageID:            "msg_forwarded",
			ForwardedFromAddress: &forwardedFromAddress,
			RecipientAddress:     "agent-deck/self",
			Subject:              "delegate",
			ContentType:          "text/plain",
		}, nil
	}

	service := newService(Options{
		MailboxServiceFactory: fakeMailboxServiceFactory{service: mailboxService},
		CommandRunner: &fakeRunner{t: t, handler: func(args []string, input string) (RunResult, error) {
			t.Fatalf("unexpected command call: %v", args)
			return RunResult{}, nil
		}},
		DisableWakeScheduler: true,
	})
	service.state.boundAddresses = []string{"agent-deck/self"}
	service.state.defaultSender = "agent-deck/self"
	service.state.autoBindAttempted = true

	wait := callTool(t, service.Server(), "mailbox_wait", map[string]any{
		"addresses": []string{"agent-deck/self"},
	})
	delivery := wait["delivery"].(map[string]any)
	if got := delivery["forwarded_from_address"]; got != "agent/source" {
		t.Fatalf("forwarded_from_address = %v, want agent/source", got)
	}
	assertMCPMapOmitsForwardedMessageID(t, delivery)
}

func TestMailboxListAsPersonExposesForwardedFromAddress(t *testing.T) {
	forwardedFromAddress := "agent/source"
	mailboxService := &fakeMailboxService{t: t}
	mailboxService.listGroupMessagesFunc = func(_ context.Context, params mailbox.GroupListParams) ([]mailbox.GroupListedMessage, error) {
		if params.Address != "group/review" {
			t.Fatalf("ListGroupMessages address = %q, want group/review", params.Address)
		}
		if params.Person != "alice" {
			t.Fatalf("ListGroupMessages person = %q, want alice", params.Person)
		}
		return []mailbox.GroupListedMessage{{
			MessageID:            "msg_forwarded",
			ForwardedFromAddress: &forwardedFromAddress,
			GroupID:              "grp_1",
			GroupAddress:         "group/review",
			Person:               "alice",
			MessageCreatedAt:     "2026-04-18T00:00:00Z",
			Subject:              "review",
			ContentType:          "text/plain",
			Read:                 false,
			ReadCount:            0,
			EligibleCount:        1,
		}}, nil
	}

	service := newService(Options{
		MailboxServiceFactory: fakeMailboxServiceFactory{service: mailboxService},
		CommandRunner: &fakeRunner{t: t, handler: func(args []string, input string) (RunResult, error) {
			t.Fatalf("unexpected command call: %v", args)
			return RunResult{}, nil
		}},
		DisableWakeScheduler: true,
	})
	service.state.autoBindAttempted = true

	list := callTool(t, service.Server(), "mailbox_list", map[string]any{
		"address":   "group/review",
		"as_person": "alice",
	})
	deliveries := list["deliveries"].([]any)
	if len(deliveries) != 1 {
		t.Fatalf("len(deliveries) = %d, want 1", len(deliveries))
	}
	delivery := deliveries[0].(map[string]any)
	if got := delivery["forwarded_from_address"]; got != "agent/source" {
		t.Fatalf("forwarded_from_address = %v, want agent/source", got)
	}
	assertMCPMapOmitsForwardedMessageID(t, delivery)
}

func TestMailboxWaitAsPersonUsesGroupWaitWithoutDeliveryLease(t *testing.T) {
	mailboxService := &fakeMailboxService{t: t}
	mailboxService.waitGroupMessageFunc = func(_ context.Context, params mailbox.GroupWaitParams) (mailbox.GroupListedMessage, error) {
		if params.Address != "group/review" || params.Person != "alice" || params.Timeout != 25*time.Millisecond {
			t.Fatalf("WaitGroupMessage params = %+v", params)
		}
		return mailbox.GroupListedMessage{
			MessageID:        "msg_group",
			GroupID:          "grp_1",
			GroupAddress:     "group/review",
			Person:           "alice",
			MessageCreatedAt: "2026-04-18T00:00:00Z",
			Subject:          "review",
			ContentType:      "text/plain",
			Read:             false,
			ReadCount:        0,
			EligibleCount:    1,
		}, nil
	}

	service := newService(Options{
		MailboxServiceFactory: fakeMailboxServiceFactory{service: mailboxService},
		CommandRunner: &fakeRunner{t: t, handler: func(args []string, input string) (RunResult, error) {
			t.Fatalf("unexpected command call: %v", args)
			return RunResult{}, nil
		}},
		DisableWakeScheduler: true,
	})
	service.state.autoBindAttempted = true

	wait := callTool(t, service.Server(), "mailbox_wait", map[string]any{
		"addresses": []string{"group/review"},
		"as_person": "alice",
		"timeout":   "25ms",
	})
	if got := wait["status"]; got != "message_available" {
		t.Fatalf("status = %v, want message_available", got)
	}
	message := wait["message"].(map[string]any)
	if got := message["message_id"]; got != "msg_group" {
		t.Fatalf("message_id = %v, want msg_group", got)
	}
	if _, ok := message["delivery_id"]; ok {
		t.Fatalf("group wait exposed delivery_id: %v", message)
	}
	if _, ok := message["lease_token"]; ok {
		t.Fatalf("group wait exposed lease_token: %v", message)
	}
}

func TestMailboxRecvAsPersonUsesGroupRecvWithoutTrackingLease(t *testing.T) {
	mailboxService := &fakeMailboxService{t: t}
	mailboxService.receiveGroupMessageFunc = func(_ context.Context, params mailbox.GroupReceiveParams) (mailbox.GroupReceivedMessage, error) {
		if params.Address != "group/review" || params.Person != "alice" {
			t.Fatalf("ReceiveGroupMessage params = %+v", params)
		}
		return mailbox.GroupReceivedMessage{
			MessageID:        "msg_group",
			GroupID:          "grp_1",
			GroupAddress:     "group/review",
			Person:           "alice",
			MessageCreatedAt: "2026-04-18T00:00:00Z",
			Subject:          "review",
			ContentType:      "text/plain",
			Body:             "body",
			ReadCount:        1,
			EligibleCount:    1,
			FirstReadAt:      "2026-04-18T00:01:00Z",
		}, nil
	}

	service := newService(Options{
		MailboxServiceFactory: fakeMailboxServiceFactory{service: mailboxService},
		CommandRunner: &fakeRunner{t: t, handler: func(args []string, input string) (RunResult, error) {
			t.Fatalf("unexpected command call: %v", args)
			return RunResult{}, nil
		}},
		DisableLeaseRenewLoop: true,
		DisableWakeScheduler:  true,
	})
	service.state.autoBindAttempted = true

	recv := callTool(t, service.Server(), "mailbox_recv", map[string]any{
		"addresses": []string{"group/review"},
		"as_person": "alice",
	})
	if got := recv["status"]; got != "received" {
		t.Fatalf("status = %v, want received", got)
	}
	message := recv["message"].(map[string]any)
	if got := message["body"]; got != "body" {
		t.Fatalf("body = %v, want body", got)
	}
	if _, ok := message["delivery_id"]; ok {
		t.Fatalf("group recv exposed delivery_id: %v", message)
	}
	if _, ok := message["lease_token"]; ok {
		t.Fatalf("group recv exposed lease_token: %v", message)
	}
	if service.activeLeases.hasTrackedLeases() {
		t.Fatal("group recv tracked a personal delivery lease")
	}
}

func TestMailboxRecvAsPersonWithTimeoutUsesGroupWaitThenRecv(t *testing.T) {
	mailboxService := &fakeMailboxService{t: t}
	waitCalled := false
	recvCalled := false
	mailboxService.waitGroupMessageFunc = func(ctx context.Context, params mailbox.GroupWaitParams) (mailbox.GroupListedMessage, error) {
		waitCalled = true
		if _, hasDeadline := ctx.Deadline(); !hasDeadline {
			t.Fatal("WaitGroupMessage got parent context, want timeout context")
		}
		if params.Address != "group/review" || params.Person != "alice" || params.Timeout != 0 {
			t.Fatalf("WaitGroupMessage params = %+v", params)
		}
		return mailbox.GroupListedMessage{
			MessageID:        "msg_group",
			GroupID:          "grp_1",
			GroupAddress:     "group/review",
			Person:           "alice",
			MessageCreatedAt: "2026-04-18T00:00:00Z",
			Subject:          "review",
			ContentType:      "text/plain",
			Read:             false,
			ReadCount:        0,
			EligibleCount:    1,
		}, nil
	}
	mailboxService.receiveGroupMessageFunc = func(ctx context.Context, params mailbox.GroupReceiveParams) (mailbox.GroupReceivedMessage, error) {
		recvCalled = true
		if _, hasDeadline := ctx.Deadline(); hasDeadline {
			t.Fatal("ReceiveGroupMessage got timeout context, want parent context")
		}
		if params.Address != "group/review" || params.Person != "alice" {
			t.Fatalf("ReceiveGroupMessage params = %+v", params)
		}
		return mailbox.GroupReceivedMessage{
			MessageID:        "msg_group",
			GroupID:          "grp_1",
			GroupAddress:     "group/review",
			Person:           "alice",
			MessageCreatedAt: "2026-04-18T00:00:00Z",
			Subject:          "review",
			ContentType:      "text/plain",
			Body:             "body",
			ReadCount:        1,
			EligibleCount:    1,
			FirstReadAt:      "2026-04-18T00:01:00Z",
		}, nil
	}

	service := newService(Options{
		MailboxServiceFactory: fakeMailboxServiceFactory{service: mailboxService},
		CommandRunner: &fakeRunner{t: t, handler: func(args []string, input string) (RunResult, error) {
			t.Fatalf("unexpected command call: %v", args)
			return RunResult{}, nil
		}},
		DisableLeaseRenewLoop: true,
		DisableWakeScheduler:  true,
	})
	service.state.autoBindAttempted = true

	recv := callTool(t, service.Server(), "mailbox_recv", map[string]any{
		"addresses": []string{"group/review"},
		"as_person": "alice",
		"timeout":   "30s",
	})
	if got := recv["status"]; got != "received" {
		t.Fatalf("status = %v, want received", got)
	}
	message := recv["message"].(map[string]any)
	if got := message["body"]; got != "body" {
		t.Fatalf("body = %v, want body", got)
	}
	if !waitCalled {
		t.Fatal("WaitGroupMessage was not called")
	}
	if !recvCalled {
		t.Fatal("ReceiveGroupMessage was not called")
	}
	if service.activeLeases.hasTrackedLeases() {
		t.Fatal("group recv tracked a personal delivery lease")
	}
}

func TestMailboxGroupReadRequiresSingleGroupAddress(t *testing.T) {
	service := newService(Options{
		MailboxServiceFactory: fakeMailboxServiceFactory{service: &fakeMailboxService{t: t}},
		CommandRunner: &fakeRunner{t: t, handler: func(args []string, input string) (RunResult, error) {
			t.Fatalf("unexpected command call: %v", args)
			return RunResult{}, nil
		}},
		DisableWakeScheduler: true,
	})
	service.state.autoBindAttempted = true

	err := callToolExpectError(t, service.Server(), "mailbox_recv", map[string]any{
		"addresses": []string{"group/one", "group/two"},
		"as_person": "alice",
	})
	if err == nil || !strings.Contains(err.Error(), "requires exactly one group address") {
		t.Fatalf("mailbox_recv error = %v, want single group address validation", err)
	}

	err = callToolExpectError(t, service.Server(), "mailbox_wait", map[string]any{
		"addresses": []string{"agent/alice"},
		"as_person": "alice",
	})
	if err == nil || !strings.Contains(err.Error(), "requires a group address") {
		t.Fatalf("mailbox_wait error = %v, want group address validation", err)
	}
}

func TestMailboxGroupControlToolsUseMailboxService(t *testing.T) {
	mailboxService := &fakeMailboxService{t: t}
	mailboxService.createGroupFunc = func(_ context.Context, groupAddress string) (mailbox.GroupRecord, error) {
		if groupAddress != "group/review" {
			t.Fatalf("CreateGroup address = %q, want group/review", groupAddress)
		}
		return mailbox.GroupRecord{GroupID: "grp_1", Address: groupAddress, CreatedAt: "2026-04-18T00:00:00Z"}, nil
	}
	mailboxService.addGroupMemberFunc = func(_ context.Context, groupAddress, person string) (mailbox.GroupMembershipRecord, error) {
		if groupAddress != "group/review" || person != "alice" {
			t.Fatalf("AddGroupMember args = group=%q person=%q", groupAddress, person)
		}
		return mailbox.GroupMembershipRecord{
			MembershipID: "gm_1",
			GroupID:      "grp_1",
			GroupAddress: groupAddress,
			PersonID:     "person_1",
			Person:       person,
			JoinedAt:     "2026-04-18T00:01:00Z",
			Active:       true,
		}, nil
	}
	mailboxService.listGroupMembersFunc = func(_ context.Context, groupAddress string) ([]mailbox.GroupMembershipRecord, error) {
		if groupAddress != "group/review" {
			t.Fatalf("ListGroupMembers address = %q, want group/review", groupAddress)
		}
		return []mailbox.GroupMembershipRecord{{
			MembershipID: "gm_1",
			GroupID:      "grp_1",
			GroupAddress: groupAddress,
			PersonID:     "person_1",
			Person:       "alice",
			JoinedAt:     "2026-04-18T00:01:00Z",
			Active:       true,
		}}, nil
	}
	mailboxService.removeGroupMemberFunc = func(_ context.Context, groupAddress, person string) (mailbox.GroupMembershipRecord, error) {
		if groupAddress != "group/review" || person != "alice" {
			t.Fatalf("RemoveGroupMember args = group=%q person=%q", groupAddress, person)
		}
		leftAt := "2026-04-18T00:02:00Z"
		return mailbox.GroupMembershipRecord{
			MembershipID: "gm_1",
			GroupID:      "grp_1",
			GroupAddress: groupAddress,
			PersonID:     "person_1",
			Person:       person,
			JoinedAt:     "2026-04-18T00:01:00Z",
			LeftAt:       &leftAt,
			Active:       false,
		}, nil
	}
	mailboxService.addGroupSubscriberFunc = func(_ context.Context, groupAddress, notifyAddress, person string) (mailbox.GroupNotificationSubscriberRecord, error) {
		if groupAddress != "group/review" || notifyAddress != "agent-deck/moderator" || person != "moderator" {
			t.Fatalf("AddGroupNotificationSubscriber args = group=%q notify=%q person=%q", groupAddress, notifyAddress, person)
		}
		return mailbox.GroupNotificationSubscriberRecord{
			SubscriberID:  "gns_1",
			GroupID:       "grp_1",
			GroupAddress:  groupAddress,
			NotifyAddress: notifyAddress,
			Person:        person,
			CreatedAt:     "2026-04-18T00:01:30Z",
			Active:        true,
		}, nil
	}
	mailboxService.listGroupSubscribersFunc = func(_ context.Context, groupAddress string) ([]mailbox.GroupNotificationSubscriberRecord, error) {
		if groupAddress != "group/review" {
			t.Fatalf("ListGroupNotificationSubscribers address = %q, want group/review", groupAddress)
		}
		return []mailbox.GroupNotificationSubscriberRecord{{
			SubscriberID:  "gns_1",
			GroupID:       "grp_1",
			GroupAddress:  groupAddress,
			NotifyAddress: "agent-deck/moderator",
			Person:        "moderator",
			CreatedAt:     "2026-04-18T00:01:30Z",
			Active:        true,
		}}, nil
	}
	mailboxService.removeGroupSubscriberFunc = func(_ context.Context, groupAddress, notifyAddress string) (mailbox.GroupNotificationSubscriberRecord, error) {
		if groupAddress != "group/review" || notifyAddress != "agent-deck/moderator" {
			t.Fatalf("RemoveGroupNotificationSubscriber args = group=%q notify=%q", groupAddress, notifyAddress)
		}
		removedAt := "2026-04-18T00:02:30Z"
		return mailbox.GroupNotificationSubscriberRecord{
			SubscriberID:  "gns_1",
			GroupID:       "grp_1",
			GroupAddress:  groupAddress,
			NotifyAddress: notifyAddress,
			Person:        "moderator",
			CreatedAt:     "2026-04-18T00:01:30Z",
			RemovedAt:     &removedAt,
			Active:        false,
		}, nil
	}
	mailboxService.inspectAddressFunc = func(_ context.Context, address string) (mailbox.AddressInspection, error) {
		if address != "group/review" {
			t.Fatalf("InspectAddress address = %q, want group/review", address)
		}
		groupID := "grp_1"
		return mailbox.AddressInspection{Address: address, Kind: mailbox.AddressKindGroup, GroupID: &groupID}, nil
	}

	service := newService(Options{
		MailboxServiceFactory: fakeMailboxServiceFactory{service: mailboxService},
		CommandRunner: &fakeRunner{t: t, handler: func(args []string, input string) (RunResult, error) {
			t.Fatalf("unexpected command call: %v", args)
			return RunResult{}, nil
		}},
		DisableWakeScheduler: true,
	})
	service.state.autoBindAttempted = true

	created := callTool(t, service.Server(), "mailbox_group_create", map[string]any{
		"group_address": "group/review",
	})
	if got := created["status"]; got != "created" {
		t.Fatalf("create status = %v, want created", got)
	}
	if got := created["group"].(map[string]any)["group_id"]; got != "grp_1" {
		t.Fatalf("created group_id = %v, want grp_1", got)
	}

	added := callTool(t, service.Server(), "mailbox_group_add_member", map[string]any{
		"group_address": "group/review",
		"person":        "alice",
	})
	if got := added["status"]; got != "added" {
		t.Fatalf("add status = %v, want added", got)
	}
	if got := added["membership"].(map[string]any)["person"]; got != "alice" {
		t.Fatalf("added person = %v, want alice", got)
	}

	members := callTool(t, service.Server(), "mailbox_group_members", map[string]any{
		"group_address": "group/review",
	})
	memberships := members["memberships"].([]any)
	if len(memberships) != 1 {
		t.Fatalf("memberships = %d, want 1", len(memberships))
	}

	removed := callTool(t, service.Server(), "mailbox_group_remove_member", map[string]any{
		"group_address": "group/review",
		"person":        "alice",
	})
	if got := removed["status"]; got != "removed" {
		t.Fatalf("remove status = %v, want removed", got)
	}
	if got := removed["membership"].(map[string]any)["active"]; got != false {
		t.Fatalf("removed active = %v, want false", got)
	}

	addedSubscriber := callTool(t, service.Server(), "mailbox_group_add_subscriber", map[string]any{
		"group_address":  "group/review",
		"notify_address": "agent-deck/moderator",
		"person":         "moderator",
	})
	if got := addedSubscriber["status"]; got != "added" {
		t.Fatalf("add subscriber status = %v, want added", got)
	}
	if got := addedSubscriber["subscriber"].(map[string]any)["notify_address"]; got != "agent-deck/moderator" {
		t.Fatalf("subscriber notify_address = %v, want agent-deck/moderator", got)
	}

	subscribers := callTool(t, service.Server(), "mailbox_group_subscribers", map[string]any{
		"group_address": "group/review",
	})
	subscriptions := subscribers["subscribers"].([]any)
	if len(subscriptions) != 1 {
		t.Fatalf("subscribers = %d, want 1", len(subscriptions))
	}

	removedSubscriber := callTool(t, service.Server(), "mailbox_group_remove_subscriber", map[string]any{
		"group_address":  "group/review",
		"notify_address": "agent-deck/moderator",
	})
	if got := removedSubscriber["status"]; got != "removed" {
		t.Fatalf("remove subscriber status = %v, want removed", got)
	}
	if got := removedSubscriber["subscriber"].(map[string]any)["active"]; got != false {
		t.Fatalf("removed subscriber active = %v, want false", got)
	}

	inspected := callTool(t, service.Server(), "mailbox_address_inspect", map[string]any{
		"address": "group/review",
	})
	inspection := inspected["inspection"].(map[string]any)
	if got := inspection["kind"]; got != mailbox.AddressKindGroup {
		t.Fatalf("kind = %v, want group", got)
	}
	if got := inspection["group_id"]; got != "grp_1" {
		t.Fatalf("group_id = %v, want grp_1", got)
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

func TestProcessLeaseRenewalsAllowsTerminalMutationAfterExpiryFollowingTransientFailure(t *testing.T) {
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

	current = current.Add(defaultMCPLeaseTTL + time.Second)
	output := callTool(t, service.Server(), "mailbox_ack", map[string]any{
		"delivery_id": "dlv_expired_failure",
		"lease_token": "lease_expired_failure",
	})
	if got := output["status"]; got != "acked" {
		t.Fatalf("mailbox_ack status = %v, want acked", got)
	}
	if !ackCalled {
		t.Fatal("Ack was not forwarded after transient renew failure and local expiry")
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

func TestAutoBindFindsAgentDeckSessionFromCodexStateDB(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_SESSION_ID", "codex-session-123")
	t.Setenv("AGENTDECK_INSTANCE_ID", "")
	t.Setenv("AGENTDECK_PROFILE", "bad")
	writeBrokenAgentDeckStateDB(t, home, "bad")
	writeAgentDeckStateDB(t, home, "work", "deck-session-1", "/tmp/project", "codex-session-123")

	runner := &fakeRunner{t: t}
	runner.handler = func(args []string, _ string) (RunResult, error) {
		switch strings.Join(args, "\x00") {
		case strings.Join([]string{"agent-deck", "session", "current", "--json"}, "\x00"):
			return RunResult{ExitCode: 1, Stderr: "not in an agent-deck pane"}, nil
		case strings.Join([]string{"agent-deck", "session", "show", "deck-session-1", "--json"}, "\x00"):
			return RunResult{ExitCode: 1, Stderr: "not found"}, nil
		default:
			t.Fatalf("unexpected command: %v", args)
			return RunResult{}, nil
		}
	}

	service := newService(Options{
		MailboxServiceFactory: fakeMailboxServiceFactory{service: &fakeMailboxService{t: t}},
		CommandRunner:         runner,
		DisableWakeScheduler:  true,
		DisableLeaseRenewLoop: true,
	})
	status := callTool(t, service.Server(), "mailbox_status", nil)

	if got := status["default_sender"]; got != "agent-deck/deck-session-1" {
		t.Fatalf("default_sender = %v, want agent-deck/deck-session-1", got)
	}
	wantAddresses := []any{"agent-deck/deck-session-1", "codex/codex-session-123"}
	if !reflect.DeepEqual(status["bound_addresses"], wantAddresses) {
		t.Fatalf("bound_addresses = %v, want %v", status["bound_addresses"], wantAddresses)
	}
}

func TestAutoBindSkipsBadAgentDeckDBAndFallsBackToCodexOnly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_SESSION_ID", "codex-session-123")
	t.Setenv("AGENTDECK_INSTANCE_ID", "")
	t.Setenv("AGENTDECK_PROFILE", "bad")
	writeBrokenAgentDeckStateDB(t, home, "bad")

	runner := &fakeRunner{t: t}
	runner.handler = func(args []string, _ string) (RunResult, error) {
		switch strings.Join(args, "\x00") {
		case strings.Join([]string{"agent-deck", "session", "current", "--json"}, "\x00"):
			return RunResult{ExitCode: 1, Stderr: "not in an agent-deck pane"}, nil
		default:
			t.Fatalf("unexpected command: %v", args)
			return RunResult{}, nil
		}
	}

	service := newService(Options{
		MailboxServiceFactory: fakeMailboxServiceFactory{service: &fakeMailboxService{t: t}},
		CommandRunner:         runner,
		DisableWakeScheduler:  true,
		DisableLeaseRenewLoop: true,
	})
	status := callTool(t, service.Server(), "mailbox_status", nil)

	if got := status["default_sender"]; got != "codex/codex-session-123" {
		t.Fatalf("default_sender = %v, want codex/codex-session-123", got)
	}
	wantAddresses := []any{"codex/codex-session-123"}
	if !reflect.DeepEqual(status["bound_addresses"], wantAddresses) {
		t.Fatalf("bound_addresses = %v, want %v", status["bound_addresses"], wantAddresses)
	}
}

func TestAutoBindRetriesAgentDeckAfterCodexOnlyFallback(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_SESSION_ID", "codex-session-123")
	t.Setenv("AGENTDECK_INSTANCE_ID", "")
	t.Setenv("AGENTDECK_PROFILE", "")

	var currentCalls int
	runner := &fakeRunner{t: t}
	runner.handler = func(args []string, _ string) (RunResult, error) {
		switch strings.Join(args, "\x00") {
		case strings.Join([]string{"agent-deck", "session", "current", "--json"}, "\x00"):
			currentCalls++
			if currentCalls == 1 {
				return RunResult{ExitCode: 1, Stderr: "not in an agent-deck pane"}, nil
			}
			return RunResult{ExitCode: 0, Stdout: `{"id":"deck-session-1"}`}, nil
		case strings.Join([]string{"agent-deck", "session", "show", "deck-session-1", "--json"}, "\x00"):
			return RunResult{ExitCode: 1, Stderr: "not found"}, nil
		default:
			t.Fatalf("unexpected command: %v", args)
			return RunResult{}, nil
		}
	}

	mailboxService := &fakeMailboxService{t: t}
	mailboxService.receiveBatchFunc = func(_ context.Context, params mailbox.ReceiveBatchParams) (mailbox.ReceiveResult, error) {
		want := []string{"agent-deck/deck-session-1", "codex/codex-session-123"}
		if !reflect.DeepEqual(params.Addresses, want) {
			t.Fatalf("receive addresses = %v, want %v", params.Addresses, want)
		}
		return mailbox.ReceiveResult{}, mailbox.ErrNoMessage
	}

	service := newService(Options{
		MailboxServiceFactory: fakeMailboxServiceFactory{service: mailboxService},
		CommandRunner:         runner,
		DisableWakeScheduler:  true,
		DisableLeaseRenewLoop: true,
	})

	status := callTool(t, service.Server(), "mailbox_status", nil)
	if got := status["default_sender"]; got != "codex/codex-session-123" {
		t.Fatalf("initial default_sender = %v, want codex/codex-session-123", got)
	}

	recv := callTool(t, service.Server(), "mailbox_recv", nil)
	if got := recv["warnings"]; got != nil {
		t.Fatalf("recv warnings = %v, want nil after agent-deck retry succeeds", got)
	}
	status = callTool(t, service.Server(), "mailbox_status", nil)
	if got := status["default_sender"]; got != "agent-deck/deck-session-1" {
		t.Fatalf("upgraded default_sender = %v, want agent-deck/deck-session-1", got)
	}
}

func TestMailboxBindDisablesAgentDeckRetryUpgrade(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_SESSION_ID", "codex-session-123")
	t.Setenv("AGENTDECK_INSTANCE_ID", "")
	t.Setenv("AGENTDECK_PROFILE", "")

	var currentCalls int
	runner := &fakeRunner{t: t}
	runner.handler = func(args []string, _ string) (RunResult, error) {
		switch strings.Join(args, "\x00") {
		case strings.Join([]string{"agent-deck", "session", "current", "--json"}, "\x00"):
			currentCalls++
			if currentCalls == 1 {
				return RunResult{ExitCode: 1, Stderr: "not in an agent-deck pane"}, nil
			}
			return RunResult{ExitCode: 0, Stdout: `{"id":"deck-session-1"}`}, nil
		case strings.Join([]string{"agent-deck", "session", "show", "deck-session-1", "--json"}, "\x00"):
			return RunResult{ExitCode: 1, Stderr: "not found"}, nil
		default:
			t.Fatalf("unexpected command: %v", args)
			return RunResult{}, nil
		}
	}

	service := newService(Options{
		MailboxServiceFactory: fakeMailboxServiceFactory{service: &fakeMailboxService{t: t}},
		CommandRunner:         runner,
		DisableWakeScheduler:  true,
		DisableLeaseRenewLoop: true,
	})

	status := callTool(t, service.Server(), "mailbox_status", nil)
	if got := status["default_sender"]; got != "codex/codex-session-123" {
		t.Fatalf("initial default_sender = %v, want codex/codex-session-123", got)
	}

	bind := callTool(t, service.Server(), "mailbox_bind", map[string]any{
		"addresses": []string{"codex/manual"},
	})
	if got := bind["default_sender"]; got != "codex/manual" {
		t.Fatalf("mailbox_bind default_sender = %v, want codex/manual", got)
	}

	status = callTool(t, service.Server(), "mailbox_status", nil)
	if got := status["default_sender"]; got != "codex/manual" {
		t.Fatalf("status default_sender = %v, want codex/manual", got)
	}
	wantAddresses := []any{"codex/manual"}
	if !reflect.DeepEqual(status["bound_addresses"], wantAddresses) {
		t.Fatalf("bound_addresses = %v, want %v", status["bound_addresses"], wantAddresses)
	}
	if currentCalls != 1 {
		t.Fatalf("agent-deck current calls = %d, want 1", currentCalls)
	}
}

func TestAgentDeckRetryRechecksFallbackStateBeforeUpgrade(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_SESSION_ID", "codex-session-123")
	t.Setenv("AGENTDECK_INSTANCE_ID", "")
	t.Setenv("AGENTDECK_PROFILE", "")

	runner := &fakeRunner{t: t}
	runner.handler = func(args []string, _ string) (RunResult, error) {
		switch strings.Join(args, "\x00") {
		case strings.Join([]string{"agent-deck", "session", "current", "--json"}, "\x00"):
			return RunResult{ExitCode: 0, Stdout: `{"id":"deck-session-1"}`}, nil
		case strings.Join([]string{"agent-deck", "session", "show", "deck-session-1", "--json"}, "\x00"):
			return RunResult{ExitCode: 1, Stderr: "not found"}, nil
		default:
			t.Fatalf("unexpected command: %v", args)
			return RunResult{}, nil
		}
	}

	service := newService(Options{
		MailboxServiceFactory: fakeMailboxServiceFactory{service: &fakeMailboxService{t: t}},
		CommandRunner:         runner,
		DisableWakeScheduler:  true,
		DisableLeaseRenewLoop: true,
	})

	staleFallback := stateSnapshot{
		BoundAddresses:           []string{"codex/codex-session-123"},
		DefaultSender:            "codex/codex-session-123",
		AutoBindAttempted:        true,
		AutoBoundCodexFallback:   true,
		DetectedAgentSession:     "codex-session-123",
		DetectedAgentDeckSession: "",
	}
	service.state.boundAddresses = []string{"codex/manual"}
	service.state.defaultSender = "codex/manual"
	service.state.autoBindAttempted = true
	service.state.autoBoundCodexFallback = false
	service.state.detectedAgentSession = "codex-session-123"

	if err := service.sessions.tryUpgradeAgentDeckBinding(context.Background(), staleFallback); err != nil {
		t.Fatalf("tryUpgradeAgentDeckBinding error = %v", err)
	}
	if !reflect.DeepEqual(service.state.boundAddresses, []string{"codex/manual"}) {
		t.Fatalf("boundAddresses = %v, want [codex/manual]", service.state.boundAddresses)
	}
	if got := service.state.defaultSender; got != "codex/manual" {
		t.Fatalf("defaultSender = %v, want codex/manual", got)
	}
	if got := service.state.detectedAgentDeckSession; got != "" {
		t.Fatalf("detectedAgentDeckSession = %v, want empty", got)
	}
}

func TestMailboxRecvWarnsWhenOnlyCodexSessionIsBound(t *testing.T) {
	mailboxService := &fakeMailboxService{t: t}
	mailboxService.receiveBatchFunc = func(_ context.Context, params mailbox.ReceiveBatchParams) (mailbox.ReceiveResult, error) {
		if !reflect.DeepEqual(params.Addresses, []string{"codex/self"}) {
			t.Fatalf("receive addresses = %v, want [codex/self]", params.Addresses)
		}
		return mailbox.ReceiveResult{}, mailbox.ErrNoMessage
	}

	service := newService(Options{
		MailboxServiceFactory: fakeMailboxServiceFactory{service: mailboxService},
		DisableWakeScheduler:  true,
		DisableLeaseRenewLoop: true,
	})
	service.state.boundAddresses = []string{"codex/self"}
	service.state.defaultSender = "codex/self"
	service.state.detectedAgentSession = "self"
	service.state.autoBindAttempted = true

	out := callTool(t, service.Server(), "mailbox_recv", nil)
	warnings, ok := out["warnings"].([]any)
	if !ok || len(warnings) != 1 {
		t.Fatalf("warnings = %#v, want one warning", out["warnings"])
	}
	warning := warnings[0].(string)
	if !strings.Contains(warning, "agent-deck session current --json") || !strings.Contains(warning, "mailbox_bind") {
		t.Fatalf("warning = %v, want manual agent-deck bind recovery hint", warning)
	}
}

func writeBrokenAgentDeckStateDB(t *testing.T, home, profile string) {
	t.Helper()
	dbPath := filepath.Join(home, ".agent-deck", "profiles", profile, "state.db")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0700); err != nil {
		t.Fatalf("mkdir broken state db dir: %v", err)
	}
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatalf("open broken state db: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE unrelated (id TEXT PRIMARY KEY)`); err != nil {
		t.Fatalf("create broken state db table: %v", err)
	}
}

func writeAgentDeckStateDB(t *testing.T, home, profile, id, projectPath, codexSessionID string) {
	t.Helper()
	dbPath := filepath.Join(home, ".agent-deck", "profiles", profile, "state.db")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0700); err != nil {
		t.Fatalf("mkdir state db dir: %v", err)
	}
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatalf("open state db: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`
		CREATE TABLE instances (
			id TEXT PRIMARY KEY,
			project_path TEXT NOT NULL,
			tool TEXT NOT NULL,
			command TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			last_accessed INTEGER NOT NULL,
			tool_data TEXT NOT NULL
		)
	`); err != nil {
		t.Fatalf("create instances: %v", err)
	}
	toolData, err := json.Marshal(map[string]string{"codex_session_id": codexSessionID})
	if err != nil {
		t.Fatalf("marshal tool data: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO instances (id, project_path, tool, command, created_at, last_accessed, tool_data)
		VALUES (?, ?, 'codex', 'codex', 1, 2, ?)
	`, id, projectPath, string(toolData)); err != nil {
		t.Fatalf("insert instance: %v", err)
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

func assertMCPMapOmitsForwardedMessageID(t *testing.T, payload map[string]any) {
	t.Helper()

	if _, ok := payload["forwarded_message_id"]; ok {
		t.Fatalf("payload unexpectedly exposes forwarded_message_id: %v", payload)
	}
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

func connectTestClientSession(t *testing.T, server *mcp.Server, updateCh chan struct{}) (*mcp.ClientSession, func()) {
	t.Helper()

	ctx := context.Background()
	clientTransport, serverTransport := mcp.NewInMemoryTransports()

	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "v0.0.1"}, &mcp.ClientOptions{
		ResourceUpdatedHandler: func(context.Context, *mcp.ResourceUpdatedNotificationRequest) {
			if updateCh == nil {
				return
			}
			select {
			case updateCh <- struct{}{}:
			default:
			}
		},
	})
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}

	cleanup := func() {
		_ = clientSession.Close()
		_ = serverSession.Wait()
	}
	return clientSession, cleanup
}
