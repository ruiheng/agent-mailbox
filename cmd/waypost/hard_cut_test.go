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
	var subscribersPage struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal([]byte(listSubscribers.stdout), &subscribersPage); err != nil {
		t.Fatalf("json.Unmarshal(subscribers) error = %v", err)
	}
	subscribers := subscribersPage.Items
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

	receivedAfterFailure := runCLI(t, "", "--state-dir", stateDir, "recv", "--for", "workflow/lifecycle", "--json")
	if receivedAfterFailure.exitCode != 0 {
		t.Fatalf("third recv exit code = %d, stderr = %q", receivedAfterFailure.exitCode, receivedAfterFailure.stderr)
	}
	message = decodeReceivedMessage(t, receivedAfterFailure.stdout)
	deadLetter := runCLI(t, "", "--state-dir", stateDir,
		"dead-letter", "--delivery", message.DeliveryID, "--lease-token", message.LeaseToken, "--reason", "unsupported request", "--json")
	if deadLetter.exitCode != 0 {
		t.Fatalf("dead-letter exit code = %d, stderr = %q", deadLetter.exitCode, deadLetter.stderr)
	}
	if err := json.Unmarshal([]byte(deadLetter.stdout), &transition); err != nil {
		t.Fatalf("json.Unmarshal(dead-letter) error = %v", err)
	}
	if transition["delivery_id"] != message.DeliveryID || transition["state"] != "dead_letter" || transition["attempt_count"] != float64(1) {
		t.Fatalf("dead-letter payload = %v", transition)
	}
}

func TestCLIHistoryFiltersPersonalAndGroupMessagesBySender(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "waypost-state")

	for _, sender := range []string{"agent/alice", "agent/bob"} {
		send := runCLI(t, sender+" body\n", "--state-dir", stateDir,
			"send", "--to", "workflow/inbox", "--from", sender,
			"--subject", sender, "--body-file", "-")
		if send.exitCode != 0 {
			t.Fatalf("personal send from %s exit code = %d, stderr = %q", sender, send.exitCode, send.stderr)
		}
	}

	list := runCLI(t, "", "--state-dir", stateDir,
		"list", "--for", "workflow/inbox", "--from", "agent/alice", "--json")
	if list.exitCode != 0 {
		t.Fatalf("personal list by sender exit code = %d, stderr = %q", list.exitCode, list.stderr)
	}
	var listedPage struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal([]byte(list.stdout), &listedPage); err != nil {
		t.Fatalf("json.Unmarshal(personal list) error = %v; stdout = %q", err, list.stdout)
	}
	listed := listedPage.Items
	if len(listed) != 1 || listed[0]["sender_address"] != "agent/alice" || listed[0]["subject"] != "agent/alice" {
		t.Fatalf("personal list by sender = %v", listed)
	}
	invalidSender := runCLI(t, "", "--state-dir", stateDir,
		"list", "--for", "workflow/inbox", "--from", "group/not-a-sender", "--json")
	if invalidSender.exitCode != 1 || invalidSender.stdout != "" || !strings.Contains(invalidSender.stderr, "reserved group/ prefix") {
		t.Fatalf("personal list invalid sender result = %+v", invalidSender)
	}

	read := runCLI(t, "", "--state-dir", stateDir,
		"read", "--latest", "--for", "workflow/inbox", "--from", "agent/bob", "--limit", "1", "--json")
	if read.exitCode != 0 {
		t.Fatalf("personal read by sender exit code = %d, stderr = %q", read.exitCode, read.stderr)
	}
	var readResult struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal([]byte(read.stdout), &readResult); err != nil {
		t.Fatalf("json.Unmarshal(personal read) error = %v; stdout = %q", err, read.stdout)
	}
	if len(readResult.Items) != 1 || readResult.Items[0]["sender_address"] != "agent/bob" || readResult.Items[0]["body"] != "agent/bob body\n" {
		t.Fatalf("personal read by sender = %v", readResult.Items)
	}
	readByIDWithFrom := runCLI(t, "", "--state-dir", stateDir,
		"read", "--delivery", readResult.Items[0]["delivery_id"].(string), "--from", "agent/bob", "--json")
	if readByIDWithFrom.exitCode != 1 || readByIDWithFrom.stdout != "" || !strings.Contains(readByIDWithFrom.stderr, "--from requires --latest") {
		t.Fatalf("read by ID with --from result = %+v", readByIDWithFrom)
	}

	source := runCLI(t, "source body\n", "--state-dir", stateDir,
		"send", "--to", "workflow/source", "--from", "agent/origin", "--body-file", "-", "--json")
	if source.exitCode != 0 {
		t.Fatalf("source send exit code = %d, stderr = %q", source.exitCode, source.stderr)
	}
	var sourceResult map[string]any
	if err := json.Unmarshal([]byte(source.stdout), &sourceResult); err != nil {
		t.Fatalf("json.Unmarshal(source send) error = %v; stdout = %q", err, source.stdout)
	}
	forward := runCLI(t, "", "--state-dir", stateDir,
		"forward", "--delivery", sourceResult["delivery_id"].(string),
		"--to", "workflow/forwarded", "--from", "agent/forwarder", "--json")
	if forward.exitCode != 0 {
		t.Fatalf("forward exit code = %d, stderr = %q", forward.exitCode, forward.stderr)
	}

	forwarderList := runCLI(t, "", "--state-dir", stateDir,
		"list", "--for", "workflow/forwarded", "--from", "agent/forwarder", "--json")
	if forwarderList.exitCode != 0 {
		t.Fatalf("forwarder list exit code = %d, stderr = %q", forwarderList.exitCode, forwarderList.stderr)
	}
	listedPage.Items = nil
	if err := json.Unmarshal([]byte(forwarderList.stdout), &listedPage); err != nil {
		t.Fatalf("json.Unmarshal(forwarder list) error = %v; stdout = %q", err, forwarderList.stdout)
	}
	listed = listedPage.Items
	if len(listed) != 1 || listed[0]["sender_address"] != "agent/forwarder" || listed[0]["forwarded_from_address"] != "agent/origin" {
		t.Fatalf("forwarder list = %v, want current and original sender separated", listed)
	}
	originList := runCLI(t, "", "--state-dir", stateDir,
		"list", "--for", "workflow/forwarded", "--from", "agent/origin", "--json")
	if originList.exitCode != 0 || originList.stdout != "{\n  \"items\": []\n}\n" {
		t.Fatalf("origin list result = %+v, want no match for forwarded delivery", originList)
	}

	for _, args := range [][]string{
		{"group", "create", "--group", "group/review", "--json"},
		{"group", "add-member", "--group", "group/review", "--person", "reader", "--json"},
	} {
		result := runCLI(t, "", append([]string{"--state-dir", stateDir}, args...)...)
		if result.exitCode != 0 {
			t.Fatalf("group setup %v exit code = %d, stderr = %q", args, result.exitCode, result.stderr)
		}
	}
	for _, sender := range []string{"agent/alice", "agent/bob"} {
		send := runCLI(t, sender+" group body\n", "--state-dir", stateDir,
			"send", "--to", "group/review", "--group", "--from", sender,
			"--subject", sender, "--body-file", "-")
		if send.exitCode != 0 {
			t.Fatalf("group send from %s exit code = %d, stderr = %q", sender, send.exitCode, send.stderr)
		}
	}

	groupList := runCLI(t, "", "--state-dir", stateDir,
		"list", "--for", "group/review", "--as", "reader", "--from", "agent/bob", "--json")
	if groupList.exitCode != 0 {
		t.Fatalf("group list by sender exit code = %d, stderr = %q", groupList.exitCode, groupList.stderr)
	}
	listedPage.Items = nil
	if err := json.Unmarshal([]byte(groupList.stdout), &listedPage); err != nil {
		t.Fatalf("json.Unmarshal(group list) error = %v; stdout = %q", err, groupList.stdout)
	}
	listed = listedPage.Items
	if len(listed) != 1 || listed[0]["sender_address"] != "agent/bob" || listed[0]["subject"] != "agent/bob" {
		t.Fatalf("group list by sender = %v", listed)
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
	wantOverview := `Waypost state is isolated by state directory; clients using different state directories do not share a mailbox.

Personal deliveries have four states:
- queued: waiting to be claimed; claimable when visible_at is reached.
- leased: claimed by one receiver; an expired lease may be reclaimed with a new lease token.
- acked: completed successfully and retained for history.
- dead_letter: reached the failure-attempt limit or was explicitly dead-lettered, and is no longer claimable.

Receiving a personal delivery returns its delivery ID and lease token. While it is leased:
- renew extends the lease without changing its state or token.
- ack moves it to acked.
- release moves it to queued immediately without recording a failure.
- defer moves it to queued with a future visible_at.
- fail increments attempt_count, then moves it to queued or dead_letter.
- dead-letter moves it directly to dead_letter without incrementing attempt_count.

Group messages track unread/read state per person and do not use personal delivery states or leases.

Persistence and recipient notification are separate outcomes.

Use waypost COMMAND --help for command syntax. Use waypost doc --list for focused topics.
`
	if overview.stdout != wantOverview {
		t.Fatalf("doc overview = %q, want %q", overview.stdout, wantOverview)
	}

	list := runCLI(t, "", "doc", "--list")
	if list.exitCode != 0 {
		t.Fatalf("doc --list exit code = %d, stderr = %q", list.exitCode, list.stderr)
	}
	for _, topic := range []string{"addresses", "dead-letter", "mcp-cli-boundary", "recovery", "history", "groups", "diagnostics"} {
		if !strings.Contains(list.stdout, topic+"\n") {
			t.Fatalf("doc --list = %q, missing %q", list.stdout, topic)
		}
	}
	for _, topic := range []string{"addresses", "dead-letter", "mcp-cli-boundary", "recovery", "history", "groups", "diagnostics"} {
		t.Run(topic, func(t *testing.T) {
			prompt := runCLI(t, "", "doc", topic)
			if prompt.exitCode != 0 {
				t.Fatalf("doc topic exit code = %d, stderr = %q", prompt.exitCode, prompt.stderr)
			}
			if len(strings.Fields(prompt.stdout)) > 100 {
				t.Fatalf("%s prompt has %d words, want at most 100", topic, len(strings.Fields(prompt.stdout)))
			}
			for _, forbidden := range []string{"# ", "##", "--json", "--yaml", "Run WAYPOST", "Use MCP for", "Agent Deck", "planner", "reviewer", "coder", "git branch"} {
				if strings.Contains(prompt.stdout, forbidden) {
					t.Fatalf("%s prompt contains forbidden %q: %q", topic, forbidden, prompt.stdout)
				}
			}
			requiredByTopic := map[string][]string{
				"addresses":        {"Waypost does not assign a current address", "use its actual session identity", "Obtain the ID from the launcher or tool", "--from when sending and --for when receiving", "group/... is reserved"},
				"dead-letter":      {"explicit terminal decision", "does not increment attempt_count", "must not be retried", "remains readable"},
				"mcp-cli-boundary": {"MCP is optional", "process-local bindings", "same state directory"},
				"recovery":         {"does not complete a delivery or immediately invalidate its token", "reclaiming replaces the token", "Undefer only"},
				"history":          {"Message IDs and delivery IDs identify different records", "does not claim a personal delivery", "forwarded_from_address"},
				"groups":           {"Active members at send time", "oldest unread message", "does not create a lease"},
				"diagnostics":      {"endpoint, group, or unbound", "Unbound is a valid inspection result", "separate from a live MCP binding"},
			}
			for _, required := range requiredByTopic[topic] {
				if !strings.Contains(prompt.stdout, required) {
					t.Fatalf("%s prompt = %q, missing %q", topic, prompt.stdout, required)
				}
			}
		})
	}

	addressPrompt := runCLI(t, "", "doc", "addresses")
	for _, forbidden := range []string{"waypost_status", "default_sender", "bound_addresses", "waypost_bind"} {
		if strings.Contains(addressPrompt.stdout, forbidden) {
			t.Fatalf("addresses prompt contains MCP-specific field or tool %q: %q", forbidden, addressPrompt.stdout)
		}
	}

	aliases := map[string]string{
		"ack":           "mcp-cli-boundary",
		"bind":          "mcp-cli-boundary",
		"defer":         "mcp-cli-boundary",
		"forward":       "mcp-cli-boundary",
		"read":          "history",
		"list":          "history",
		"send":          "mcp-cli-boundary",
		"recv":          "mcp-cli-boundary",
		"receive":       "mcp-cli-boundary",
		"receiver":      "mcp-cli-boundary",
		"claim-history": "mcp-cli-boundary",
		"address":       "addresses",
		"group":         "groups",
		"fail":          "recovery",
		"release":       "mcp-cli-boundary",
		"status":        "mcp-cli-boundary",
		"undefer":       "recovery",
		"wait":          "mcp-cli-boundary",
	}
	for alias, canonical := range aliases {
		t.Run("alias/"+alias, func(t *testing.T) {
			prompt := runCLI(t, "", "doc", alias)
			canonicalPrompt := runCLI(t, "", "doc", canonical)
			if prompt.exitCode != 0 || prompt.stderr != "" || prompt.stdout != canonicalPrompt.stdout {
				t.Fatalf("doc alias %q result = %+v, want canonical topic %q output %q", alias, prompt, canonical, canonicalPrompt.stdout)
			}
		})
	}

	multiple := runCLI(t, "", "doc", "recovery", "diagnostics")
	if multiple.exitCode != 0 || multiple.stderr != "" {
		t.Fatalf("doc multiple topics result = %+v, want combined prompt on stdout", multiple)
	}
	for _, required := range []string{
		"waypost: recovery\n  A delivery ID alone does not prove lease ownership. Lease-bound transitions require the current lease token.\n",
		"\n\nwaypost: diagnostics\n  An address has a durable kind in one state directory: endpoint, group, or unbound.",
	} {
		if !strings.Contains(multiple.stdout, required) {
			t.Fatalf("doc multiple topics = %q, missing %q", multiple.stdout, required)
		}
	}
	if strings.Index(multiple.stdout, "waypost: recovery") > strings.Index(multiple.stdout, "waypost: diagnostics") {
		t.Fatalf("doc multiple topics = %q, topics are not in requested order", multiple.stdout)
	}

	aliasedMultiple := runCLI(t, "", "doc", "read", "list", "address")
	if aliasedMultiple.exitCode != 0 || aliasedMultiple.stderr != "" {
		t.Fatalf("doc aliased multiple topics result = %+v, want combined prompt on stdout", aliasedMultiple)
	}
	if strings.Count(aliasedMultiple.stdout, "waypost: history") != 1 ||
		strings.Count(aliasedMultiple.stdout, "waypost: addresses") != 1 {
		t.Fatalf("doc aliased multiple topics = %q, want canonical topics once each", aliasedMultiple.stdout)
	}

	unknownMultiple := runCLI(t, "", "doc", "recovery", "missing", "diagnostics")
	if unknownMultiple.exitCode != 1 || unknownMultiple.stdout != "" || !strings.Contains(unknownMultiple.stderr, `unknown doc topic "missing"`) {
		t.Fatalf("doc multiple topics with unknown topic result = %+v, want atomic failure", unknownMultiple)
	}
	for _, topic := range []string{"addresses", "diagnostics", "groups", "history", "mcp-cli-boundary", "recovery"} {
		if !strings.Contains(unknownMultiple.stderr, topic) {
			t.Fatalf("doc unknown topic error = %q, missing available topic %q", unknownMultiple.stderr, topic)
		}
	}
}

func TestCLIJSONNotFoundErrorsUseStructuralIdentity(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "waypost-state")
	tests := []struct {
		name string
		args []string
	}{
		{
			name: "forward message",
			args: []string{"forward", "--message", "msg_missing", "--to", "workflow/target", "--json"},
		},
		{
			name: "forward delivery",
			args: []string{"forward", "--delivery", "dlv_missing", "--to", "workflow/target", "--json"},
		},
		{
			name: "read message",
			args: []string{"read", "--message", "msg_missing", "--json"},
		},
		{
			name: "read delivery",
			args: []string{"read", "--delivery", "dlv_missing", "--json"},
		},
		{
			name: "undefer delivery",
			args: []string{"undefer", "--delivery", "dlv_missing", "--json"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			args := append([]string{"--state-dir", stateDir}, test.args...)
			result := runCLI(t, "", args...)
			if result.exitCode != 1 || result.stdout != "" {
				t.Fatalf("result = %+v, want JSON failure on stderr only", result)
			}
			var failure map[string]any
			if err := json.Unmarshal([]byte(result.stderr), &failure); err != nil {
				t.Fatalf("json.Unmarshal(stderr) error = %v; stderr = %q", err, result.stderr)
			}
			if failure["error_code"] != "not_found" || failure["retryable"] != false {
				t.Fatalf("error payload = %v", failure)
			}
		})
	}
}

func TestCLIJSONUnclassifiedTextRemainsInternal(t *testing.T) {
	for _, message := range []string{
		`reload existing endpoint address "agent/example": not found after conflict`,
		`delivery changed while updating`,
		`invalid persisted migration stage`,
		`parse stored state: empty document`,
	} {
		t.Run(message, func(t *testing.T) {
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			exitCode := runCommand(context.Background(), []string{"forward", "--json"}, nil, &stdout, &stderr, func(context.Context, []string) error {
				return errors.New(message)
			})
			if exitCode != 1 || stdout.Len() != 0 {
				t.Fatalf("exit=%d stdout=%q, want exit 1 and empty stdout", exitCode, stdout.String())
			}
			var failure map[string]any
			if err := json.Unmarshal(stderr.Bytes(), &failure); err != nil {
				t.Fatalf("json.Unmarshal(stderr) error = %v; stderr = %q", err, stderr.String())
			}
			if failure["error_code"] != "internal" || failure["retryable"] != false {
				t.Fatalf("error payload = %v", failure)
			}
		})
	}
}

func TestCLIJSONCallerValidationErrorsUseStructuralClassification(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "waypost-state")
	tests := []struct {
		name        string
		args        []string
		wantMessage string
	}{
		{
			name:        "global flag",
			args:        []string{"--bogus", "forward", "--json"},
			wantMessage: "flag provided but not defined: -bogus",
		},
		{
			name: "list group state",
			args: []string{"--state-dir", stateDir,
				"list", "--for", "group/x", "--as", "p", "--state", "queued", "--json"},
			wantMessage: "--state is not supported with --as",
		},
		{
			name: "wait group address count",
			args: []string{"--state-dir", stateDir,
				"wait", "--for", "group/x", "--for", "group/y", "--as", "p", "--json"},
			wantMessage: "--as requires exactly one --for address",
		},
		{
			name:        "group web flag",
			args:        []string{"group", "web", "--bogus", "--json"},
			wantMessage: "flag provided but not defined: -bogus",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := runCLI(t, "", test.args...)
			if result.exitCode != 1 || result.stdout != "" {
				t.Fatalf("result = %+v, want JSON failure on stderr only", result)
			}
			var failure map[string]any
			if err := json.Unmarshal([]byte(result.stderr), &failure); err != nil {
				t.Fatalf("json.Unmarshal(stderr) error = %v; stderr = %q", err, result.stderr)
			}
			if failure["status"] != "error" || failure["error_code"] != "invalid_argument" || failure["retryable"] != false {
				t.Fatalf("error payload = %v", failure)
			}
			if failure["message"] != test.wantMessage {
				t.Fatalf("error message = %v, want %q", failure["message"], test.wantMessage)
			}
		})
	}
}

func TestCLIJSONRuntimeErrorsUseStructuralClassification(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "waypost-state")

	invalid := runCLI(t, "", "--state-dir", stateDir,
		"group", "create", "--group", "workflow/not-a-group", "--json")
	if invalid.exitCode != 1 || invalid.stdout != "" {
		t.Fatalf("invalid group result = %+v, want JSON failure on stderr only", invalid)
	}
	var failure map[string]any
	if err := json.Unmarshal([]byte(invalid.stderr), &failure); err != nil {
		t.Fatalf("json.Unmarshal(invalid group stderr) error = %v; stderr = %q", err, invalid.stderr)
	}
	if failure["error_code"] != "invalid_argument" || failure["retryable"] != false {
		t.Fatalf("invalid group error payload = %v", failure)
	}

	sent := runCLI(t, "body", "--state-dir", stateDir,
		"send", "--to", "workflow/visible", "--body-file", "-", "--json")
	if sent.exitCode != 0 || sent.stderr != "" {
		t.Fatalf("send result = %+v", sent)
	}
	var receipt map[string]any
	if err := json.Unmarshal([]byte(sent.stdout), &receipt); err != nil {
		t.Fatalf("json.Unmarshal(send stdout) error = %v; stdout = %q", err, sent.stdout)
	}
	deliveryID, _ := receipt["delivery_id"].(string)
	if deliveryID == "" {
		t.Fatalf("send receipt = %v, want delivery_id", receipt)
	}

	invalidState := runCLI(t, "", "--state-dir", stateDir,
		"undefer", "--delivery", deliveryID, "--json")
	if invalidState.exitCode != 1 || invalidState.stdout != "" {
		t.Fatalf("undefer result = %+v, want JSON failure on stderr only", invalidState)
	}
	if err := json.Unmarshal([]byte(invalidState.stderr), &failure); err != nil {
		t.Fatalf("json.Unmarshal(undefer stderr) error = %v; stderr = %q", err, invalidState.stderr)
	}
	if failure["error_code"] != "invalid_state" || failure["retryable"] != false {
		t.Fatalf("undefer error payload = %v", failure)
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
