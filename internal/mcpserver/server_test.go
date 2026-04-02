package mcpserver

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"

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
