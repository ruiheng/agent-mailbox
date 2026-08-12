package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ruiheng/waypost/internal/waypost"
)

func TestCLIReceiveUsesSparseRemainingStateWithoutHasMore(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "waypost-state")
	for _, address := range []string{"workflow/one", "workflow/two", "workflow/three"} {
		result := runCLI(t, "body\n", "--state-dir", stateDir,
			"send", "--to", address, "--from", "agent/sender", "--body-file", "-")
		if result.exitCode != 0 {
			t.Fatalf("send %s exit code = %d, stderr = %q", address, result.exitCode, result.stderr)
		}
	}

	recv := runCLI(t, "", "--state-dir", stateDir,
		"recv", "--for", "workflow/one", "--for", "workflow/two", "--for", "workflow/three", "--max", "2", "--json")
	if recv.exitCode != 0 {
		t.Fatalf("recv exit code = %d, stderr = %q", recv.exitCode, recv.stderr)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(recv.stdout), &payload); err != nil {
		t.Fatalf("json.Unmarshal(recv) error = %v; stdout = %q", err, recv.stdout)
	}
	if payload["status"] != "received" {
		t.Fatalf("recv status = %v, want received", payload["status"])
	}
	deliveries := payload["deliveries"].([]any)
	if len(deliveries) != 2 {
		t.Fatalf("recv deliveries = %d, want 2", len(deliveries))
	}
	remaining := payload["remaining_by_state"].(map[string]any)
	if got := remaining["queued"]; got != float64(1) {
		t.Fatalf("remaining_by_state[queued] = %v, want 1", got)
	}
	if _, ok := payload["has_more"]; ok {
		t.Fatalf("recv payload exposes removed has_more field: %v", payload)
	}
}

func TestCLIOwnedLifecycleAndSubscriberCommandsEmitJSON(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "waypost-state")

	create := runCLI(t, "", "--state-dir", stateDir,
		"group", "create", "--group", "group/review", "--json")
	if create.exitCode != 0 {
		t.Fatalf("group create exit code = %d, stderr = %q", create.exitCode, create.stderr)
	}
	addSubscriber := runCLI(t, "", "--state-dir", stateDir,
		"group", "add-subscriber", "--group", "group/review", "--notify-address", "agent-deck/moderator", "--person", "moderator", "--json")
	if addSubscriber.exitCode != 0 {
		t.Fatalf("group add-subscriber exit code = %d, stderr = %q", addSubscriber.exitCode, addSubscriber.stderr)
	}
	var subscriber map[string]any
	if err := json.Unmarshal([]byte(addSubscriber.stdout), &subscriber); err != nil {
		t.Fatalf("json.Unmarshal(add-subscriber) error = %v", err)
	}
	if subscriber["notify_address"] != "agent-deck/moderator" || subscriber["active"] != true {
		t.Fatalf("add-subscriber payload = %v", subscriber)
	}
	listSubscribers := runCLI(t, "", "--state-dir", stateDir,
		"group", "subscribers", "--group", "group/review", "--json")
	if listSubscribers.exitCode != 0 {
		t.Fatalf("group subscribers exit code = %d, stderr = %q", listSubscribers.exitCode, listSubscribers.stderr)
	}
	var subscribers []map[string]any
	if err := json.Unmarshal([]byte(listSubscribers.stdout), &subscribers); err != nil {
		t.Fatalf("json.Unmarshal(subscribers) error = %v", err)
	}
	if len(subscribers) != 1 || subscribers[0]["person"] != "moderator" {
		t.Fatalf("subscribers = %v, want active moderator", subscribers)
	}
	removeSubscriber := runCLI(t, "", "--state-dir", stateDir,
		"group", "remove-subscriber", "--group", "group/review", "--notify-address", "agent-deck/moderator", "--json")
	if removeSubscriber.exitCode != 0 {
		t.Fatalf("group remove-subscriber exit code = %d, stderr = %q", removeSubscriber.exitCode, removeSubscriber.stderr)
	}
	if err := json.Unmarshal([]byte(removeSubscriber.stdout), &subscriber); err != nil {
		t.Fatalf("json.Unmarshal(remove-subscriber) error = %v", err)
	}
	if subscriber["active"] != false {
		t.Fatalf("remove-subscriber payload = %v, want inactive", subscriber)
	}

	send := runCLI(t, "body\n", "--state-dir", stateDir,
		"send", "--to", "workflow/lifecycle", "--from", "agent/sender", "--body-file", "-")
	if send.exitCode != 0 {
		t.Fatalf("send exit code = %d, stderr = %q", send.exitCode, send.stderr)
	}
	recv := runCLI(t, "", "--state-dir", stateDir, "recv", "--for", "workflow/lifecycle", "--json")
	if recv.exitCode != 0 {
		t.Fatalf("recv exit code = %d, stderr = %q", recv.exitCode, recv.stderr)
	}
	message := decodeReceivedMessage(t, recv.stdout)
	until := time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano)
	deferResult := runCLI(t, "", "--state-dir", stateDir,
		"defer", "--delivery", message.DeliveryID, "--lease-token", message.LeaseToken, "--until", until)
	if deferResult.exitCode != 0 {
		t.Fatalf("defer exit code = %d, stderr = %q", deferResult.exitCode, deferResult.stderr)
	}
	undefer := runCLI(t, "", "--state-dir", stateDir, "undefer", "--delivery", message.DeliveryID, "--json")
	if undefer.exitCode != 0 {
		t.Fatalf("undefer exit code = %d, stderr = %q", undefer.exitCode, undefer.stderr)
	}
	var transition map[string]any
	if err := json.Unmarshal([]byte(undefer.stdout), &transition); err != nil {
		t.Fatalf("json.Unmarshal(undefer) error = %v", err)
	}
	if transition["delivery_id"] != message.DeliveryID || transition["state"] != "queued" || transition["visible_at"] == "" {
		t.Fatalf("undefer payload = %v", transition)
	}

	receivedAgain := runCLI(t, "", "--state-dir", stateDir, "recv", "--for", "workflow/lifecycle", "--json")
	if receivedAgain.exitCode != 0 {
		t.Fatalf("second recv exit code = %d, stderr = %q", receivedAgain.exitCode, receivedAgain.stderr)
	}
	message = decodeReceivedMessage(t, receivedAgain.stdout)
	fail := runCLI(t, "", "--state-dir", stateDir,
		"fail", "--delivery", message.DeliveryID, "--lease-token", message.LeaseToken, "--reason", "test failure", "--json")
	if fail.exitCode != 0 {
		t.Fatalf("fail exit code = %d, stderr = %q", fail.exitCode, fail.stderr)
	}
	if err := json.Unmarshal([]byte(fail.stdout), &transition); err != nil {
		t.Fatalf("json.Unmarshal(fail) error = %v", err)
	}
	if transition["delivery_id"] != message.DeliveryID || transition["state"] != "queued" || transition["attempt_count"] != float64(1) {
		t.Fatalf("fail payload = %v", transition)
	}
}

func TestCLIJSONErrorsAndEmbeddedDocs(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "waypost-state")
	forward := runCLI(t, "", "--state-dir", stateDir,
		"forward", "--message", "msg_missing", "--to", "workflow/target", "--json")
	if forward.exitCode != 1 || forward.stdout != "" {
		t.Fatalf("forward result = %+v, want JSON failure on stderr only", forward)
	}
	var failure map[string]any
	if err := json.Unmarshal([]byte(forward.stderr), &failure); err != nil {
		t.Fatalf("json.Unmarshal(forward stderr) error = %v; stderr = %q", err, forward.stderr)
	}
	if failure["status"] != "error" || failure["error_code"] != "not_found" || failure["retryable"] != false {
		t.Fatalf("forward error payload = %v", failure)
	}

	invalid := runCLI(t, "", "--state-dir", stateDir,
		"group", "create", "--unexpected", "--json")
	if invalid.exitCode != 1 || invalid.stdout != "" {
		t.Fatalf("invalid group command result = %+v, want JSON failure on stderr only", invalid)
	}
	if err := json.Unmarshal([]byte(invalid.stderr), &failure); err != nil {
		t.Fatalf("json.Unmarshal(invalid group stderr) error = %v; stderr = %q", err, invalid.stderr)
	}
	if failure["error_code"] != "invalid_argument" || failure["retryable"] != false {
		t.Fatalf("invalid group command error payload = %v", failure)
	}

	wait := runCLI(t, "", "--state-dir", stateDir,
		"wait", "--for", "workflow/empty", "--timeout", "50ms", "--json")
	if wait.exitCode != 2 || wait.stdout != "" || wait.stderr != "" {
		t.Fatalf("wait no-message result = %+v, want silent exit 2", wait)
	}

	overview := runCLI(t, "", "doc")
	if overview.exitCode != 0 || overview.stderr != "" {
		t.Fatalf("doc overview result = %+v, want prompt on stdout", overview)
	}
	if len(strings.Fields(overview.stdout)) > 300 {
		t.Fatalf("doc overview has %d words, want at most 300", len(strings.Fields(overview.stdout)))
	}
	for _, required := range []string{
		"# Waypost workflow",
		"waypost_status",
		"waypost_recv",
		"settle its lease exactly once",
		"release for immediate retry without recording failure",
		"CLI fail for a processing failure that increments attempts and may dead-letter",
		`MCP waypost_recv no message: successful result with status: "no_message"`,
		`CLI recv no message: exit 2 with status: "no_message" JSON on stdout`,
		"CLI wait no message: exit 2 with no output",
		"CLI --json",
		"WAYPOST doc --list",
		"error_code",
	} {
		if !strings.Contains(overview.stdout, required) {
			t.Fatalf("doc overview = %q, missing %q", overview.stdout, required)
		}
	}
	if strings.Contains(overview.stdout, "Usage:") {
		t.Fatalf("doc overview regressed to command help: %q", overview.stdout)
	}
	for _, forbidden := range []string{"Agent Deck", "planner", "reviewer", "coder", "YAML", "git branch"} {
		if strings.Contains(overview.stdout, forbidden) {
			t.Fatalf("doc overview contains forbidden %q: %q", forbidden, overview.stdout)
		}
	}

	list := runCLI(t, "", "doc", "--list")
	if list.exitCode != 0 {
		t.Fatalf("doc --list exit code = %d, stderr = %q", list.exitCode, list.stderr)
	}
	for _, topic := range []string{"mcp-cli-boundary", "recovery", "history", "groups", "diagnostics"} {
		if !strings.Contains(list.stdout, topic+"\n") {
			t.Fatalf("doc --list = %q, missing %q", list.stdout, topic)
		}
	}
	for _, topic := range []string{"mcp-cli-boundary", "recovery", "history", "groups", "diagnostics"} {
		t.Run(topic, func(t *testing.T) {
			prompt := runCLI(t, "", "doc", topic)
			if prompt.exitCode != 0 {
				t.Fatalf("doc topic exit code = %d, stderr = %q", prompt.exitCode, prompt.stderr)
			}
			if len(strings.Fields(prompt.stdout)) > 300 {
				t.Fatalf("%s prompt has %d words, want at most 300", topic, len(strings.Fields(prompt.stdout)))
			}
			for _, forbidden := range []string{"Agent Deck", "planner", "reviewer", "coder", "YAML", "git branch"} {
				if strings.Contains(prompt.stdout, forbidden) {
					t.Fatalf("%s prompt contains forbidden %q: %q", topic, forbidden, prompt.stdout)
				}
			}
			if topic == "mcp-cli-boundary" && !strings.Contains(prompt.stdout, "durable-only") {
				t.Fatalf("mcp-cli-boundary prompt = %q, want durable-only forward guidance", prompt.stdout)
			}
			if topic == "mcp-cli-boundary" {
				for _, operation := range []string{
					"waypost_status",
					"waypost_bind",
					"waypost_debug",
					"waypost_send",
					"waypost_recv",
					"waypost_claim_history",
					"waypost_ack",
					"waypost_release",
					"waypost_defer",
				} {
					if !strings.Contains(prompt.stdout, operation) {
						t.Fatalf("mcp-cli-boundary prompt = %q, missing retained MCP operation %q", prompt.stdout, operation)
					}
				}
			}
		})
	}

	multiple := runCLI(t, "", "doc", "recovery", "diagnostics")
	if multiple.exitCode != 0 || multiple.stderr != "" {
		t.Fatalf("doc multiple topics result = %+v, want combined prompt on stdout", multiple)
	}
	for _, required := range []string{
		"waypost: recovery\n  # Recover persisted input\n",
		"\n\nwaypost: diagnostics\n  # Diagnose an address\n",
		"  ## Required context\n",
		"  ## Stop\n",
	} {
		if !strings.Contains(multiple.stdout, required) {
			t.Fatalf("doc multiple topics = %q, missing %q", multiple.stdout, required)
		}
	}
	if strings.Index(multiple.stdout, "waypost: recovery") > strings.Index(multiple.stdout, "waypost: diagnostics") {
		t.Fatalf("doc multiple topics = %q, topics are not in requested order", multiple.stdout)
	}

	unknownMultiple := runCLI(t, "", "doc", "recovery", "missing", "diagnostics")
	if unknownMultiple.exitCode != 1 || unknownMultiple.stdout != "" || !strings.Contains(unknownMultiple.stderr, `unknown doc topic "missing"`) {
		t.Fatalf("doc multiple topics with unknown topic result = %+v, want atomic failure", unknownMultiple)
	}
}

func TestCLIReceiveRecoveryErrorWritesStructuredJSON(t *testing.T) {
	recovery := &waypost.ReceiveRecoveryRequiredError{
		Cause: errors.New("remaining-state query failed"),
		Claims: []waypost.ReceivedMessage{
			{
				DeliveryID:       "dlv_recovery_one",
				LeaseToken:       "lease_recovery_one",
				RecipientAddress: "workflow/one",
				LeaseExpiresAt:   "2026-07-25T10:00:00Z",
			},
			{
				DeliveryID:       "dlv_recovery_two",
				LeaseToken:       "lease_recovery_two",
				RecipientAddress: "workflow/two",
				LeaseExpiresAt:   "2026-07-25T10:01:00Z",
			},
		},
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := runCommand(context.Background(), []string{"recv", "--json"}, nil, &stdout, &stderr, func(context.Context, []string) error {
		return recovery
	})
	if exitCode != 1 || stdout.Len() != 0 {
		t.Fatalf("receive recovery command exit=%d stdout=%q, want exit 1 and empty stdout", exitCode, stdout.String())
	}

	var document struct {
		Status    string `json:"status"`
		ErrorCode string `json:"error_code"`
		Message   string `json:"message"`
		Retryable bool   `json:"retryable"`
		Details   struct {
			RemainingByStateStatus string `json:"remaining_by_state_status"`
			Claims                 []struct {
				DeliveryID       string `json:"delivery_id"`
				LeaseToken       string `json:"lease_token"`
				RecipientAddress string `json:"recipient_address"`
				LeaseExpiresAt   string `json:"lease_expires_at"`
			} `json:"claims"`
		} `json:"details"`
	}
	if err := json.Unmarshal(stderr.Bytes(), &document); err != nil {
		t.Fatalf("json.Unmarshal(recovery stderr) error = %v; stderr = %q", err, stderr.String())
	}
	if document.Status != "error" || document.ErrorCode != "receive_recovery_required" || document.Message != recovery.Error() || document.Retryable {
		t.Fatalf("recovery error document = %+v", document)
	}
	if document.Details.RemainingByStateStatus != "unavailable" || len(document.Details.Claims) != len(recovery.Claims) {
		t.Fatalf("recovery error details = %+v", document.Details)
	}
	for index, want := range recovery.Claims {
		got := document.Details.Claims[index]
		if got.DeliveryID != want.DeliveryID || got.LeaseToken != want.LeaseToken || got.RecipientAddress != want.RecipientAddress || got.LeaseExpiresAt != want.LeaseExpiresAt {
			t.Fatalf("recovery claim[%d] = %+v, want %+v", index, got, want)
		}
	}
}
