package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ruiheng/agent-mailbox/internal/mailbox"
)

func TestCLISendRecvAckFlow(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "mailbox-state")

	send := runCLI(t, "hello reviewer\n", "--state-dir", stateDir,
		"send",
		"--to", "workflow/reviewer/task-123",
		"--from", "agent/sender",
		"--subject", "review request",
		"--body-file", "-",
	)
	if send.exitCode != 0 {
		t.Fatalf("send exit code = %d, stderr = %q", send.exitCode, send.stderr)
	}
	if !strings.Contains(send.stdout, "delivery_id=") {
		t.Fatalf("send stdout = %q, want delivery_id", send.stdout)
	}
	if strings.Contains(send.stdout, "message_id=") || strings.Contains(send.stdout, "blob_id=") {
		t.Fatalf("send stdout = %q, want compact default output", send.stdout)
	}

	recv := runCLI(t, "", "--state-dir", stateDir,
		"recv",
		"--for", "workflow/reviewer/task-123",
		"--json",
	)
	if recv.exitCode != 0 {
		t.Fatalf("recv exit code = %d, stderr = %q", recv.exitCode, recv.stderr)
	}

	message := decodeReceivedMessage(t, recv.stdout)
	if message.Subject != "review request" {
		t.Fatalf("recv subject = %q, want review request", message.Subject)
	}
	if message.Body != "hello reviewer\n" {
		t.Fatalf("recv body = %q, want %q", message.Body, "hello reviewer\n")
	}
	if message.LeaseToken == "" {
		t.Fatal("recv lease token = empty, want non-empty")
	}

	ack := runCLI(t, "", "--state-dir", stateDir,
		"ack",
		"--delivery", message.DeliveryID,
		"--lease-token", message.LeaseToken,
	)
	if ack.exitCode != 0 {
		t.Fatalf("ack exit code = %d, stderr = %q", ack.exitCode, ack.stderr)
	}
	if !strings.Contains(ack.stdout, "state=acked") {
		t.Fatalf("ack stdout = %q, want state=acked", ack.stdout)
	}

	list := runCLI(t, "", "--state-dir", stateDir,
		"list",
		"--for", "workflow/reviewer/task-123",
		"--state", "acked",
		"--json",
	)
	if list.exitCode != 0 {
		t.Fatalf("list acked exit code = %d, stderr = %q", list.exitCode, list.stderr)
	}

	var deliveries []map[string]any
	if err := json.Unmarshal([]byte(list.stdout), &deliveries); err != nil {
		t.Fatalf("json.Unmarshal(list acked stdout) error = %v; stdout = %q", err, list.stdout)
	}
	if len(deliveries) != 1 {
		t.Fatalf("len(list acked deliveries) = %d, want 1", len(deliveries))
	}
	if deliveries[0]["state"] != "acked" {
		t.Fatalf("list acked state = %v, want acked", deliveries[0]["state"])
	}
	if deliveries[0]["acked_at"] == "" {
		t.Fatalf("list acked payload = %v, want acked_at", deliveries[0])
	}

	read := runCLI(t, "", "--state-dir", stateDir,
		"read",
		"--delivery", message.DeliveryID,
		"--json",
	)
	if read.exitCode != 0 {
		t.Fatalf("read acked exit code = %d, stderr = %q", read.exitCode, read.stderr)
	}

	var stored readResult
	if err := json.Unmarshal([]byte(read.stdout), &stored); err != nil {
		t.Fatalf("json.Unmarshal(read acked stdout) error = %v; stdout = %q", err, read.stdout)
	}
	if stored.HasMore {
		t.Fatal("read acked has_more = true, want false")
	}
	if len(stored.Items) != 1 {
		t.Fatalf("len(read acked items) = %d, want 1", len(stored.Items))
	}
	if stored.Items[0]["state"] != "acked" {
		t.Fatalf("read acked state = %v, want acked", stored.Items[0]["state"])
	}
	if stored.Items[0]["body"] != "hello reviewer\n" {
		t.Fatalf("read acked body = %v, want hello reviewer\\n", stored.Items[0]["body"])
	}

	if deliveries[0]["message_id"] == "" {
		t.Fatalf("list acked payload = %v, want message_id", deliveries[0])
	}
	readByMessage := runCLI(t, "", "--state-dir", stateDir,
		"read",
		"--message", deliveries[0]["message_id"].(string),
		"--json",
	)
	if readByMessage.exitCode != 0 {
		t.Fatalf("read by message exit code = %d, stderr = %q", readByMessage.exitCode, readByMessage.stderr)
	}

	var storedMessage readResult
	if err := json.Unmarshal([]byte(readByMessage.stdout), &storedMessage); err != nil {
		t.Fatalf("json.Unmarshal(read by message stdout) error = %v; stdout = %q", err, readByMessage.stdout)
	}
	if len(storedMessage.Items) != 1 {
		t.Fatalf("len(read by message items) = %d, want 1", len(storedMessage.Items))
	}
	if storedMessage.Items[0]["message_id"] != deliveries[0]["message_id"] {
		t.Fatalf("read by message_id = %v, want %v", storedMessage.Items[0]["message_id"], deliveries[0]["message_id"])
	}
	if storedMessage.Items[0]["body"] != "hello reviewer\n" {
		t.Fatalf("read by message body = %v, want hello reviewer\\n", storedMessage.Items[0]["body"])
	}

	readLatest := runCLI(t, "", "--state-dir", stateDir,
		"read",
		"--latest",
		"--for", "workflow/reviewer/task-123",
		"--json",
	)
	if readLatest.exitCode != 0 {
		t.Fatalf("read latest acked exit code = %d, stderr = %q", readLatest.exitCode, readLatest.stderr)
	}

	var latest readResult
	if err := json.Unmarshal([]byte(readLatest.stdout), &latest); err != nil {
		t.Fatalf("json.Unmarshal(read latest acked stdout) error = %v; stdout = %q", err, readLatest.stdout)
	}
	if len(latest.Items) != 1 {
		t.Fatalf("len(read latest acked items) = %d, want 1", len(latest.Items))
	}
	if latest.Items[0]["delivery_id"] != message.DeliveryID {
		t.Fatalf("read latest acked delivery_id = %v, want %s", latest.Items[0]["delivery_id"], message.DeliveryID)
	}
	if latest.Items[0]["body"] != "hello reviewer\n" {
		t.Fatalf("read latest acked body = %v, want hello reviewer\\n", latest.Items[0]["body"])
	}
}

func TestCLIRenewExtendsLeaseAndPreservesToken(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "mailbox-state")

	send := runCLI(t, "hello reviewer\n", "--state-dir", stateDir,
		"send",
		"--to", "workflow/reviewer/task-123",
		"--from", "agent/sender",
		"--subject", "review request",
		"--body-file", "-",
	)
	if send.exitCode != 0 {
		t.Fatalf("send exit code = %d, stderr = %q", send.exitCode, send.stderr)
	}

	recv := runCLI(t, "", "--state-dir", stateDir,
		"recv",
		"--for", "workflow/reviewer/task-123",
		"--json",
	)
	if recv.exitCode != 0 {
		t.Fatalf("recv exit code = %d, stderr = %q", recv.exitCode, recv.stderr)
	}
	message := decodeReceivedMessage(t, recv.stdout)

	renew := runCLI(t, "", "--state-dir", stateDir,
		"renew",
		"--delivery", message.DeliveryID,
		"--lease-token", message.LeaseToken,
		"--for", "10m",
	)
	if renew.exitCode != 0 {
		t.Fatalf("renew exit code = %d, stderr = %q", renew.exitCode, renew.stderr)
	}
	fields := parseCLIFields(t, renew.stdout)
	if fields["delivery_id"] != message.DeliveryID {
		t.Fatalf("renew delivery_id = %q, want %q", fields["delivery_id"], message.DeliveryID)
	}
	if fields["lease_token"] != message.LeaseToken {
		t.Fatalf("renew lease_token = %q, want %q", fields["lease_token"], message.LeaseToken)
	}
	if fields["lease_expires_at"] == "" {
		t.Fatalf("renew lease_expires_at = %q, want non-empty", fields["lease_expires_at"])
	}

	ack := runCLI(t, "", "--state-dir", stateDir,
		"ack",
		"--delivery", message.DeliveryID,
		"--lease-token", message.LeaseToken,
	)
	if ack.exitCode != 0 {
		t.Fatalf("ack after renew exit code = %d, stderr = %q", ack.exitCode, ack.stderr)
	}
}

func TestCLIReadLatestDefaultsToAnyState(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "mailbox-state")

	firstSend := runCLI(t, "first body\n", "--state-dir", stateDir,
		"send",
		"--to", "workflow/history",
		"--from", "agent/sender",
		"--subject", "first",
		"--body-file", "-",
		"--json",
		"--full",
	)
	if firstSend.exitCode != 0 {
		t.Fatalf("first send exit code = %d, stderr = %q", firstSend.exitCode, firstSend.stderr)
	}

	firstRecv := runCLI(t, "", "--state-dir", stateDir,
		"recv",
		"--for", "workflow/history",
		"--json",
	)
	if firstRecv.exitCode != 0 {
		t.Fatalf("first recv exit code = %d, stderr = %q", firstRecv.exitCode, firstRecv.stderr)
	}
	firstMessage := decodeReceivedMessage(t, firstRecv.stdout)
	firstAck := runCLI(t, "", "--state-dir", stateDir,
		"ack",
		"--delivery", firstMessage.DeliveryID,
		"--lease-token", firstMessage.LeaseToken,
	)
	if firstAck.exitCode != 0 {
		t.Fatalf("first ack exit code = %d, stderr = %q", firstAck.exitCode, firstAck.stderr)
	}

	secondSend := runCLI(t, "second body\n", "--state-dir", stateDir,
		"send",
		"--to", "workflow/history",
		"--from", "agent/sender",
		"--subject", "second",
		"--body-file", "-",
	)
	if secondSend.exitCode != 0 {
		t.Fatalf("second send exit code = %d, stderr = %q", secondSend.exitCode, secondSend.stderr)
	}
	secondRecv := runCLI(t, "", "--state-dir", stateDir,
		"recv",
		"--for", "workflow/history",
		"--json",
	)
	if secondRecv.exitCode != 0 {
		t.Fatalf("second recv exit code = %d, stderr = %q", secondRecv.exitCode, secondRecv.stderr)
	}
	secondMessage := decodeReceivedMessage(t, secondRecv.stdout)
	secondAck := runCLI(t, "", "--state-dir", stateDir,
		"ack",
		"--delivery", secondMessage.DeliveryID,
		"--lease-token", secondMessage.LeaseToken,
	)
	if secondAck.exitCode != 0 {
		t.Fatalf("second ack exit code = %d, stderr = %q", secondAck.exitCode, secondAck.stderr)
	}

	queuedSend := runCLI(t, "queued body\n", "--state-dir", stateDir,
		"send",
		"--to", "workflow/history",
		"--from", "agent/sender",
		"--subject", "queued",
		"--body-file", "-",
	)
	if queuedSend.exitCode != 0 {
		t.Fatalf("queued send exit code = %d, stderr = %q", queuedSend.exitCode, queuedSend.stderr)
	}

	readLatest := runCLI(t, "", "--state-dir", stateDir,
		"read",
		"--latest",
		"--for", "workflow/history",
		"--json",
	)
	if readLatest.exitCode != 0 {
		t.Fatalf("read latest exit code = %d, stderr = %q", readLatest.exitCode, readLatest.stderr)
	}

	var latest readResult
	if err := json.Unmarshal([]byte(readLatest.stdout), &latest); err != nil {
		t.Fatalf("json.Unmarshal(read latest stdout) error = %v; stdout = %q", err, readLatest.stdout)
	}
	if !latest.HasMore {
		t.Fatal("read latest has_more = false, want true")
	}
	if len(latest.Items) != 1 {
		t.Fatalf("len(read latest items) = %d, want 1", len(latest.Items))
	}
	if latest.Items[0]["state"] != "queued" {
		t.Fatalf("read latest state = %v, want queued", latest.Items[0]["state"])
	}
	if latest.Items[0]["body"] != "queued body\n" {
		t.Fatalf("read latest body = %v, want queued body\\n", latest.Items[0]["body"])
	}
}

func TestCLIReadLatestHonorsLimitAndReportsMoreAvailable(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "mailbox-state")

	for _, body := range []string{"first body\n", "second body\n", "third body\n"} {
		send := runCLI(t, body, "--state-dir", stateDir,
			"send",
			"--to", "workflow/history-limit",
			"--from", "agent/sender",
			"--subject", strings.TrimSpace(body),
			"--body-file", "-",
		)
		if send.exitCode != 0 {
			t.Fatalf("send %q exit code = %d, stderr = %q", body, send.exitCode, send.stderr)
		}
		recv := runCLI(t, "", "--state-dir", stateDir,
			"recv",
			"--for", "workflow/history-limit",
			"--json",
		)
		if recv.exitCode != 0 {
			t.Fatalf("recv %q exit code = %d, stderr = %q", body, recv.exitCode, recv.stderr)
		}
		message := decodeReceivedMessage(t, recv.stdout)
		ack := runCLI(t, "", "--state-dir", stateDir,
			"ack",
			"--delivery", message.DeliveryID,
			"--lease-token", message.LeaseToken,
		)
		if ack.exitCode != 0 {
			t.Fatalf("ack %q exit code = %d, stderr = %q", body, ack.exitCode, ack.stderr)
		}
	}

	readLatest := runCLI(t, "", "--state-dir", stateDir,
		"read",
		"--latest",
		"--for", "workflow/history-limit",
		"--limit", "2",
		"--json",
	)
	if readLatest.exitCode != 0 {
		t.Fatalf("read latest limit exit code = %d, stderr = %q", readLatest.exitCode, readLatest.stderr)
	}

	var result readResult
	if err := json.Unmarshal([]byte(readLatest.stdout), &result); err != nil {
		t.Fatalf("json.Unmarshal(read latest limit stdout) error = %v; stdout = %q", err, readLatest.stdout)
	}
	if !result.HasMore {
		t.Fatal("read latest limit has_more = false, want true")
	}
	if len(result.Items) != 2 {
		t.Fatalf("len(read latest limit items) = %d, want 2", len(result.Items))
	}
	if result.Items[0]["body"] != "third body\n" {
		t.Fatalf("read latest limit first body = %v, want third body\\n", result.Items[0]["body"])
	}
	if result.Items[1]["body"] != "second body\n" {
		t.Fatalf("read latest limit second body = %v, want second body\\n", result.Items[1]["body"])
	}
}

func TestCLIRecvYAMLOutput(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "mailbox-state")

	send := runCLI(t, "hello reviewer\n", "--state-dir", stateDir,
		"send",
		"--to", "workflow/reviewer/task-123",
		"--from", "agent/sender",
		"--subject", "review request",
		"--body-file", "-",
	)
	if send.exitCode != 0 {
		t.Fatalf("send exit code = %d, stderr = %q", send.exitCode, send.stderr)
	}

	recv := runCLI(t, "", "--state-dir", stateDir,
		"recv",
		"--for", "workflow/reviewer/task-123",
		"--yaml",
	)
	if recv.exitCode != 0 {
		t.Fatalf("recv exit code = %d, stderr = %q", recv.exitCode, recv.stderr)
	}
	if recv.stderr != "" {
		t.Fatalf("recv stderr = %q, want empty", recv.stderr)
	}
	if !strings.Contains(recv.stdout, "recipient_address: \"workflow/reviewer/task-123\"\n") {
		t.Fatalf("recv stdout = %q, want YAML recipient_address", recv.stdout)
	}
	if !strings.Contains(recv.stdout, "lease_token: \"") {
		t.Fatalf("recv stdout = %q, want lease_token field", recv.stdout)
	}
	if strings.Contains(recv.stdout, "lease_expires_at: ") {
		t.Fatalf("recv stdout unexpectedly contains lease_expires_at: %q", recv.stdout)
	}
	if strings.Contains(recv.stdout, "message_id: ") {
		t.Fatalf("recv stdout unexpectedly contains message_id: %q", recv.stdout)
	}
	if !strings.Contains(recv.stdout, "body: \"hello reviewer\\n\"\n") {
		t.Fatalf("recv stdout = %q, want YAML body", recv.stdout)
	}
}

func TestCLISendJSONOutputIsCompact(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "mailbox-state")

	send := runCLI(t, "hello reviewer\n", "--state-dir", stateDir,
		"send",
		"--to", "workflow/reviewer/task-123",
		"--from", "agent/sender",
		"--subject", "review request",
		"--body-file", "-",
		"--json",
	)
	if send.exitCode != 0 {
		t.Fatalf("send --json exit code = %d, stderr = %q", send.exitCode, send.stderr)
	}
	if send.stderr != "" {
		t.Fatalf("send --json stderr = %q, want empty", send.stderr)
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(send.stdout), &result); err != nil {
		t.Fatalf("json.Unmarshal(send --json stdout) error = %v; stdout = %q", err, send.stdout)
	}
	if result["delivery_id"] == "" {
		t.Fatalf("send --json delivery_id = %v, want non-empty", result["delivery_id"])
	}
	if _, ok := result["message_id"]; ok {
		t.Fatalf("send --json payload unexpectedly contains message_id: %v", result)
	}
	if _, ok := result["blob_id"]; ok {
		t.Fatalf("send --json payload unexpectedly contains blob_id: %v", result)
	}
}

func TestCLISendYAMLOutputIsCompact(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "mailbox-state")

	send := runCLI(t, "hello reviewer\n", "--state-dir", stateDir,
		"send",
		"--to", "workflow/reviewer/task-123",
		"--from", "agent/sender",
		"--subject", "review request",
		"--body-file", "-",
		"--yaml",
	)
	if send.exitCode != 0 {
		t.Fatalf("send --yaml exit code = %d, stderr = %q", send.exitCode, send.stderr)
	}
	if send.stderr != "" {
		t.Fatalf("send --yaml stderr = %q, want empty", send.stderr)
	}
	if !strings.HasPrefix(send.stdout, "delivery_id: ") {
		t.Fatalf("send --yaml stdout = %q, want compact YAML mapping", send.stdout)
	}
	if strings.Contains(send.stdout, "message_id: ") {
		t.Fatalf("send --yaml stdout unexpectedly contains message_id: %q", send.stdout)
	}
	if strings.Contains(send.stdout, "blob_id: ") {
		t.Fatalf("send --yaml stdout unexpectedly contains blob_id: %q", send.stdout)
	}
}

func TestCLIRecvNoMessageExitCodeAndSilence(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "mailbox-state")

	immediate := runCLI(t, "", "--state-dir", stateDir,
		"recv",
		"--for", "workflow/empty",
	)
	if immediate.exitCode != 2 {
		t.Fatalf("immediate recv exit code = %d, want 2; stderr = %q", immediate.exitCode, immediate.stderr)
	}
	if immediate.stdout != "" {
		t.Fatalf("immediate recv stdout = %q, want empty", immediate.stdout)
	}
	if immediate.stderr != "" {
		t.Fatalf("immediate recv stderr = %q, want empty", immediate.stderr)
	}
}

func TestCLISendRejectsEmptyBody(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "mailbox-state")

	send := runCLI(t, "", "--state-dir", stateDir,
		"send",
		"--to", "workflow/reviewer/task-123",
		"--from", "agent/sender",
		"--subject", "review request",
		"--body-file", "-",
	)
	if send.exitCode != 1 {
		t.Fatalf("send empty body exit code = %d, want 1; stderr = %q", send.exitCode, send.stderr)
	}
	if send.stdout != "" {
		t.Fatalf("send empty body stdout = %q, want empty", send.stdout)
	}
	if !strings.Contains(send.stderr, mailbox.ErrEmptyBody.Error()) {
		t.Fatalf("send empty body stderr = %q, want substring %q", send.stderr, mailbox.ErrEmptyBody.Error())
	}
}

func TestCLISendFullTextIncludesLegacyFields(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "mailbox-state")

	send := runCLI(t, "hello reviewer\n", "--state-dir", stateDir,
		"send",
		"--to", "workflow/reviewer/task-123",
		"--from", "agent/sender",
		"--subject", "review request",
		"--body-file", "-",
		"--full",
	)
	if send.exitCode != 0 {
		t.Fatalf("send --full exit code = %d, stderr = %q", send.exitCode, send.stderr)
	}
	if !strings.Contains(send.stdout, "message_id=") || !strings.Contains(send.stdout, "delivery_id=") || !strings.Contains(send.stdout, "blob_id=") {
		t.Fatalf("send --full stdout = %q, want legacy full identifiers", send.stdout)
	}
}

func TestCLISendFullJSONIncludesLegacyFields(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "mailbox-state")

	send := runCLI(t, "hello reviewer\n", "--state-dir", stateDir,
		"send",
		"--to", "workflow/reviewer/task-123",
		"--from", "agent/sender",
		"--subject", "review request",
		"--body-file", "-",
		"--json",
		"--full",
	)
	if send.exitCode != 0 {
		t.Fatalf("send --json --full exit code = %d, stderr = %q", send.exitCode, send.stderr)
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(send.stdout), &result); err != nil {
		t.Fatalf("json.Unmarshal(send --json --full stdout) error = %v; stdout = %q", err, send.stdout)
	}
	if result["delivery_id"] == "" || result["message_id"] == "" || result["blob_id"] == "" {
		t.Fatalf("send --json --full payload = %v, want delivery_id, message_id, and blob_id", result)
	}
}

func TestCLIRecvMultipleAddressesPlainTextIncludesRecipientAddress(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "mailbox-state")

	sendOlder := runCLI(t, "older body\n", "--state-dir", stateDir,
		"send",
		"--to", "workflow/older",
		"--from", "agent/sender",
		"--subject", "older",
		"--body-file", "-",
	)
	if sendOlder.exitCode != 0 {
		t.Fatalf("send older exit code = %d, stderr = %q", sendOlder.exitCode, sendOlder.stderr)
	}
	sendNewer := runCLI(t, "newer body\n", "--state-dir", stateDir,
		"send",
		"--to", "workflow/newer",
		"--from", "agent/sender",
		"--subject", "newer",
		"--body-file", "-",
	)
	if sendNewer.exitCode != 0 {
		t.Fatalf("send newer exit code = %d, stderr = %q", sendNewer.exitCode, sendNewer.stderr)
	}

	recv := runCLI(t, "", "--state-dir", stateDir,
		"recv",
		"--for", "workflow/newer",
		"--for", "workflow/older",
	)
	if recv.exitCode != 0 {
		t.Fatalf("recv multi plain text exit code = %d, stderr = %q", recv.exitCode, recv.stderr)
	}
	if !strings.Contains(recv.stdout, "recipient_address=workflow/older") {
		t.Fatalf("recv multi plain text stdout = %q, want recipient_address=workflow/older", recv.stdout)
	}
	if !strings.Contains(recv.stdout, "older body\n") {
		t.Fatalf("recv multi plain text stdout = %q, want older body", recv.stdout)
	}
}

func TestCLIRecvHonorsMaxAndReportsMoreAvailable(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "mailbox-state")

	for _, mailboxName := range []string{"workflow/one", "workflow/two", "workflow/three"} {
		send := runCLI(t, "batch body\n", "--state-dir", stateDir,
			"send",
			"--to", mailboxName,
			"--from", "agent/sender",
			"--subject", mailboxName,
			"--body-file", "-",
		)
		if send.exitCode != 0 {
			t.Fatalf("send %s exit code = %d, stderr = %q", mailboxName, send.exitCode, send.stderr)
		}
	}

	recv := runCLI(t, "", "--state-dir", stateDir,
		"recv",
		"--for", "workflow/one",
		"--for", "workflow/two",
		"--for", "workflow/three",
		"--max", "2",
		"--json",
	)
	if recv.exitCode != 0 {
		t.Fatalf("recv exit code = %d, stderr = %q", recv.exitCode, recv.stderr)
	}

	result := decodeReceiveResult(t, recv.stdout)
	if len(result.Messages) != 2 {
		t.Fatalf("len(recv messages) = %d, want 2", len(result.Messages))
	}
	if !result.HasMore {
		t.Fatal("recv has_more = false, want true")
	}
	if result.Messages[0].RecipientAddress != "workflow/one" {
		t.Fatalf("recv messages[0].recipient_address = %q, want workflow/one", result.Messages[0].RecipientAddress)
	}
	if result.Messages[1].RecipientAddress != "workflow/two" {
		t.Fatalf("recv messages[1].recipient_address = %q, want workflow/two", result.Messages[1].RecipientAddress)
	}
	if result.Messages[0].LeaseToken == "" || result.Messages[1].LeaseToken == "" {
		t.Fatalf("recv lease tokens = %#v, want both non-empty", result.Messages)
	}
}

func TestCLIRecvDefaultTextDoesNotAppendMoreNotice(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "mailbox-state")

	for _, mailboxName := range []string{"workflow/one", "workflow/two"} {
		send := runCLI(t, "payload\n", "--state-dir", stateDir,
			"send",
			"--to", mailboxName,
			"--from", "agent/sender",
			"--subject", mailboxName,
			"--body-file", "-",
		)
		if send.exitCode != 0 {
			t.Fatalf("send %s exit code = %d, stderr = %q", mailboxName, send.exitCode, send.stderr)
		}
	}

	recv := runCLI(t, "", "--state-dir", stateDir,
		"recv",
		"--for", "workflow/one",
		"--for", "workflow/two",
	)
	if recv.exitCode != 0 {
		t.Fatalf("recv exit code = %d, stderr = %q", recv.exitCode, recv.stderr)
	}
	if strings.Contains(recv.stdout, "notice=more_messages_available") {
		t.Fatalf("recv stdout = %q, want no notice suffix in default text mode", recv.stdout)
	}
}

func TestCLIRecvMultipleAddressesIgnoresUnknownAddress(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "mailbox-state")

	send := runCLI(t, "known body\n", "--state-dir", stateDir,
		"send",
		"--to", "workflow/known",
		"--from", "agent/sender",
		"--subject", "known",
		"--body-file", "-",
	)
	if send.exitCode != 0 {
		t.Fatalf("send exit code = %d, stderr = %q", send.exitCode, send.stderr)
	}

	recv := runCLI(t, "", "--state-dir", stateDir,
		"recv",
		"--for", "workflow/known",
		"--for", "workflow/missing",
	)
	if recv.exitCode != 0 {
		t.Fatalf("recv mixed known/missing exit code = %d, want 0; stderr = %q", recv.exitCode, recv.stderr)
	}
	if !strings.Contains(recv.stdout, "recipient_address=workflow/known") {
		t.Fatalf("recv mixed known/missing stdout = %q, want recipient_address=workflow/known", recv.stdout)
	}
}

func TestCLIListYAMLOutput(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "mailbox-state")

	send := runCLI(t, "hello reviewer\n", "--state-dir", stateDir,
		"send",
		"--to", "workflow/reviewer/task-123",
		"--from", "agent/sender",
		"--subject", "review request",
		"--body-file", "-",
	)
	if send.exitCode != 0 {
		t.Fatalf("send exit code = %d, stderr = %q", send.exitCode, send.stderr)
	}

	list := runCLI(t, "", "--state-dir", stateDir,
		"list",
		"--for", "workflow/reviewer/task-123",
		"--yaml",
	)
	if list.exitCode != 0 {
		t.Fatalf("list exit code = %d, stderr = %q", list.exitCode, list.stderr)
	}
	if list.stderr != "" {
		t.Fatalf("list stderr = %q, want empty", list.stderr)
	}
	if !strings.HasPrefix(list.stdout, "-\n  delivery_id: ") {
		t.Fatalf("list stdout = %q, want YAML sequence", list.stdout)
	}
	if !strings.Contains(list.stdout, "recipient_address: \"workflow/reviewer/task-123\"\n") {
		t.Fatalf("list stdout = %q, want YAML recipient_address", list.stdout)
	}
}

func TestCLIListStructuredOutputUsesEmptyArraysForExistingEmptyInbox(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "mailbox-state")

	send := runCLI(t, "hello reviewer\n", "--state-dir", stateDir,
		"send",
		"--to", "workflow/reviewer/task-123",
		"--from", "agent/sender",
		"--subject", "review request",
		"--body-file", "-",
	)
	if send.exitCode != 0 {
		t.Fatalf("send exit code = %d, stderr = %q", send.exitCode, send.stderr)
	}

	recv := runCLI(t, "", "--state-dir", stateDir,
		"recv",
		"--for", "workflow/reviewer/task-123",
		"--json",
	)
	if recv.exitCode != 0 {
		t.Fatalf("recv exit code = %d, stderr = %q", recv.exitCode, recv.stderr)
	}

	jsonList := runCLI(t, "", "--state-dir", stateDir,
		"list",
		"--for", "workflow/reviewer/task-123",
		"--json",
	)
	if jsonList.exitCode != 0 {
		t.Fatalf("list --json exit code = %d, stderr = %q", jsonList.exitCode, jsonList.stderr)
	}
	if jsonList.stderr != "" {
		t.Fatalf("list --json stderr = %q, want empty", jsonList.stderr)
	}
	if jsonList.stdout != "[]\n" {
		t.Fatalf("list --json stdout = %q, want empty array", jsonList.stdout)
	}

	yamlList := runCLI(t, "", "--state-dir", stateDir,
		"list",
		"--for", "workflow/reviewer/task-123",
		"--yaml",
	)
	if yamlList.exitCode != 0 {
		t.Fatalf("list --yaml exit code = %d, stderr = %q", yamlList.exitCode, yamlList.stderr)
	}
	if yamlList.stderr != "" {
		t.Fatalf("list --yaml stderr = %q, want empty", yamlList.stderr)
	}
	if yamlList.stdout != "[]\n" {
		t.Fatalf("list --yaml stdout = %q, want empty array", yamlList.stdout)
	}
}

func TestCLIStaleJSONOutput(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "mailbox-state")
	oldestEligibleAt := seedStaleDelivery(t, stateDir, "workflow/stale-json", 10*time.Minute)

	stale := runCLI(t, "", "--state-dir", stateDir,
		"stale",
		"--for", "workflow/stale-json",
		"--older-than", "5m",
		"--json",
	)
	if stale.exitCode != 0 {
		t.Fatalf("stale --json exit code = %d, stderr = %q", stale.exitCode, stale.stderr)
	}
	if stale.stderr != "" {
		t.Fatalf("stale --json stderr = %q, want empty", stale.stderr)
	}

	var result []mailbox.StaleAddress
	if err := json.Unmarshal([]byte(stale.stdout), &result); err != nil {
		t.Fatalf("json.Unmarshal(stale --json stdout) error = %v; stdout = %q", err, stale.stdout)
	}
	if len(result) != 1 {
		t.Fatalf("len(stale --json result) = %d, want 1", len(result))
	}
	if result[0].Address != "workflow/stale-json" {
		t.Fatalf("stale --json address = %q, want workflow/stale-json", result[0].Address)
	}
	if result[0].OldestEligibleAt != oldestEligibleAt {
		t.Fatalf("stale --json oldest_eligible_at = %q, want %q", result[0].OldestEligibleAt, oldestEligibleAt)
	}
	if result[0].ClaimableCount != 1 {
		t.Fatalf("stale --json claimable_count = %d, want 1", result[0].ClaimableCount)
	}
}

func TestCLIStaleYAMLOutput(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "mailbox-state")
	oldestEligibleAt := seedStaleDelivery(t, stateDir, "workflow/stale-yaml", 10*time.Minute)

	stale := runCLI(t, "", "--state-dir", stateDir,
		"stale",
		"--for", "workflow/stale-yaml",
		"--older-than", "5m",
		"--yaml",
	)
	if stale.exitCode != 0 {
		t.Fatalf("stale --yaml exit code = %d, stderr = %q", stale.exitCode, stale.stderr)
	}
	if stale.stderr != "" {
		t.Fatalf("stale --yaml stderr = %q, want empty", stale.stderr)
	}
	if !strings.HasPrefix(stale.stdout, "-\n  address: \"workflow/stale-yaml\"\n") {
		t.Fatalf("stale --yaml stdout = %q, want YAML sequence", stale.stdout)
	}
	if !strings.Contains(stale.stdout, "  oldest_eligible_at: \""+oldestEligibleAt+"\"\n") {
		t.Fatalf("stale --yaml stdout = %q, want oldest_eligible_at", stale.stdout)
	}
	if !strings.Contains(stale.stdout, "  claimable_count: 1\n") {
		t.Fatalf("stale --yaml stdout = %q, want claimable_count", stale.stdout)
	}
}

func TestCLIStaleRequiresStructuredOutput(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "mailbox-state")
	seedStaleDelivery(t, stateDir, "workflow/stale-plain", 10*time.Minute)

	stale := runCLI(t, "", "--state-dir", stateDir,
		"stale",
		"--for", "workflow/stale-plain",
		"--older-than", "5m",
	)
	if stale.exitCode != 1 {
		t.Fatalf("stale plain-text exit code = %d, want 1; stderr = %q", stale.exitCode, stale.stderr)
	}
	if stale.stdout != "" {
		t.Fatalf("stale plain-text stdout = %q, want empty", stale.stdout)
	}
	if !strings.Contains(stale.stderr, "either --json or --yaml is required") {
		t.Fatalf("stale plain-text stderr = %q, want structured-output error", stale.stderr)
	}
}

func TestCLIWatchStreamsNDJSONWithoutClaiming(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "mailbox-state")

	send := runCLI(t, "watch body\n", "--state-dir", stateDir,
		"send",
		"--to", "workflow/watch",
		"--from", "agent/sender",
		"--subject", "watch me",
		"--body-file", "-",
	)
	if send.exitCode != 0 {
		t.Fatalf("send exit code = %d, stderr = %q", send.exitCode, send.stderr)
	}

	watch := runCLI(t, "", "--state-dir", stateDir,
		"watch",
		"--for", "workflow/watch",
		"--timeout", "120ms",
		"--json",
	)
	if watch.exitCode != 0 {
		t.Fatalf("watch exit code = %d, stderr = %q", watch.exitCode, watch.stderr)
	}
	if watch.stderr != "" {
		t.Fatalf("watch stderr = %q, want empty", watch.stderr)
	}

	lines := strings.Split(strings.TrimSpace(watch.stdout), "\n")
	if len(lines) != 1 {
		t.Fatalf("watch line count = %d, want 1; stdout = %q", len(lines), watch.stdout)
	}

	var delivery map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &delivery); err != nil {
		t.Fatalf("json.Unmarshal(watch line) error = %v; line = %q", err, lines[0])
	}
	if delivery["recipient_address"] != "workflow/watch" {
		t.Fatalf("watch recipient_address = %v, want workflow/watch", delivery["recipient_address"])
	}
	if delivery["subject"] != "watch me" {
		t.Fatalf("watch subject = %v, want watch me", delivery["subject"])
	}
	if _, ok := delivery["lease_token"]; ok {
		t.Fatalf("watch payload unexpectedly contains lease_token: %v", delivery)
	}
	if _, ok := delivery["body"]; ok {
		t.Fatalf("watch payload unexpectedly contains body: %v", delivery)
	}

	recv := runCLI(t, "", "--state-dir", stateDir,
		"recv",
		"--for", "workflow/watch",
		"--json",
	)
	if recv.exitCode != 0 {
		t.Fatalf("recv after watch exit code = %d, stderr = %q", recv.exitCode, recv.stderr)
	}
}

func TestCLIWaitReturnsOneJSONDeliveryWithoutClaiming(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "mailbox-state")

	send := runCLI(t, "wait body\n", "--state-dir", stateDir,
		"send",
		"--to", "workflow/wait",
		"--from", "agent/sender",
		"--subject", "wait for me",
		"--body-file", "-",
	)
	if send.exitCode != 0 {
		t.Fatalf("send exit code = %d, stderr = %q", send.exitCode, send.stderr)
	}

	wait := runCLI(t, "", "--state-dir", stateDir,
		"wait",
		"--for", "workflow/wait",
		"--json",
	)
	if wait.exitCode != 0 {
		t.Fatalf("wait exit code = %d, stderr = %q", wait.exitCode, wait.stderr)
	}
	if wait.stderr != "" {
		t.Fatalf("wait stderr = %q, want empty", wait.stderr)
	}

	var delivery map[string]any
	if err := json.Unmarshal([]byte(wait.stdout), &delivery); err != nil {
		t.Fatalf("json.Unmarshal(wait stdout) error = %v; stdout = %q", err, wait.stdout)
	}
	if delivery["recipient_address"] != "workflow/wait" {
		t.Fatalf("wait recipient_address = %v, want workflow/wait", delivery["recipient_address"])
	}
	if delivery["subject"] != "wait for me" {
		t.Fatalf("wait subject = %v, want wait for me", delivery["subject"])
	}
	if _, ok := delivery["state"]; ok {
		t.Fatalf("wait payload unexpectedly contains state: %v", delivery)
	}
	if _, ok := delivery["visible_at"]; ok {
		t.Fatalf("wait payload unexpectedly contains visible_at: %v", delivery)
	}
	if _, ok := delivery["lease_token"]; ok {
		t.Fatalf("wait payload unexpectedly contains lease_token: %v", delivery)
	}
	if _, ok := delivery["body"]; ok {
		t.Fatalf("wait payload unexpectedly contains body: %v", delivery)
	}

	recv := runCLI(t, "", "--state-dir", stateDir,
		"recv",
		"--for", "workflow/wait",
		"--json",
	)
	if recv.exitCode != 0 {
		t.Fatalf("recv after wait exit code = %d, stderr = %q", recv.exitCode, recv.stderr)
	}
}

func TestCLIWaitReturnsYAMLWithoutClaiming(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "mailbox-state")

	send := runCLI(t, "wait body\n", "--state-dir", stateDir,
		"send",
		"--to", "workflow/wait",
		"--from", "agent/sender",
		"--subject", "wait for me",
		"--body-file", "-",
	)
	if send.exitCode != 0 {
		t.Fatalf("send exit code = %d, stderr = %q", send.exitCode, send.stderr)
	}

	wait := runCLI(t, "", "--state-dir", stateDir,
		"wait",
		"--for", "workflow/wait",
		"--yaml",
	)
	if wait.exitCode != 0 {
		t.Fatalf("wait exit code = %d, stderr = %q", wait.exitCode, wait.stderr)
	}
	if wait.stderr != "" {
		t.Fatalf("wait stderr = %q, want empty", wait.stderr)
	}
	if !strings.HasPrefix(wait.stdout, "delivery_id: ") {
		t.Fatalf("wait stdout = %q, want YAML mapping", wait.stdout)
	}
	if !strings.Contains(wait.stdout, "recipient_address: \"workflow/wait\"\n") {
		t.Fatalf("wait stdout = %q, want recipient_address field", wait.stdout)
	}
	if strings.Contains(wait.stdout, "state: ") {
		t.Fatalf("wait stdout unexpectedly contains state: %q", wait.stdout)
	}
	if strings.Contains(wait.stdout, "visible_at: ") {
		t.Fatalf("wait stdout unexpectedly contains visible_at: %q", wait.stdout)
	}
	if strings.Contains(wait.stdout, "lease_token: ") {
		t.Fatalf("wait stdout unexpectedly contains lease_token: %q", wait.stdout)
	}
	if strings.Contains(wait.stdout, "body: ") {
		t.Fatalf("wait stdout unexpectedly contains body: %q", wait.stdout)
	}

	recv := runCLI(t, "", "--state-dir", stateDir,
		"recv",
		"--for", "workflow/wait",
		"--json",
	)
	if recv.exitCode != 0 {
		t.Fatalf("recv after wait exit code = %d, stderr = %q", recv.exitCode, recv.stderr)
	}
}

func TestCLIWaitTimeoutReturnsExitCodeTwoWithoutOutput(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "mailbox-state")

	wait := runCLI(t, "", "--state-dir", stateDir,
		"wait",
		"--for", "workflow/empty-wait",
		"--timeout", "30ms",
	)
	if wait.exitCode != 2 {
		t.Fatalf("wait timeout exit code = %d, want 2; stderr = %q", wait.exitCode, wait.stderr)
	}
	if wait.stdout != "" {
		t.Fatalf("wait timeout stdout = %q, want empty", wait.stdout)
	}
	if wait.stderr != "" {
		t.Fatalf("wait timeout stderr = %q, want empty", wait.stderr)
	}
}

func TestCLIWatchStreamsYAMLDocumentsWithoutClaiming(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "mailbox-state")

	send := runCLI(t, "watch body\n", "--state-dir", stateDir,
		"send",
		"--to", "workflow/watch",
		"--from", "agent/sender",
		"--subject", "watch me",
		"--body-file", "-",
	)
	if send.exitCode != 0 {
		t.Fatalf("send exit code = %d, stderr = %q", send.exitCode, send.stderr)
	}

	watch := runCLI(t, "", "--state-dir", stateDir,
		"watch",
		"--for", "workflow/watch",
		"--timeout", "120ms",
		"--yaml",
	)
	if watch.exitCode != 0 {
		t.Fatalf("watch exit code = %d, stderr = %q", watch.exitCode, watch.stderr)
	}
	if watch.stderr != "" {
		t.Fatalf("watch stderr = %q, want empty", watch.stderr)
	}
	if !strings.HasPrefix(watch.stdout, "---\ndelivery_id: ") {
		t.Fatalf("watch stdout = %q, want YAML document stream", watch.stdout)
	}
	if !strings.Contains(watch.stdout, "recipient_address: \"workflow/watch\"\n") {
		t.Fatalf("watch stdout = %q, want recipient_address field", watch.stdout)
	}
	if strings.Contains(watch.stdout, "lease_token: ") {
		t.Fatalf("watch stdout unexpectedly contains lease_token: %q", watch.stdout)
	}
	if strings.Contains(watch.stdout, "body: ") {
		t.Fatalf("watch stdout unexpectedly contains body: %q", watch.stdout)
	}

	recv := runCLI(t, "", "--state-dir", stateDir,
		"recv",
		"--for", "workflow/watch",
		"--json",
	)
	if recv.exitCode != 0 {
		t.Fatalf("recv after watch exit code = %d, stderr = %q", recv.exitCode, recv.stderr)
	}
}

func TestCLIWatchTimeoutExitsCleanlyWithoutOutput(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "mailbox-state")

	watch := runCLI(t, "", "--state-dir", stateDir,
		"watch",
		"--for", "workflow/empty-watch",
		"--timeout", "30ms",
	)
	if watch.exitCode != 0 {
		t.Fatalf("watch timeout exit code = %d, want 0; stderr = %q", watch.exitCode, watch.stderr)
	}
	if watch.stdout != "" {
		t.Fatalf("watch timeout stdout = %q, want empty", watch.stdout)
	}
	if watch.stderr != "" {
		t.Fatalf("watch timeout stderr = %q, want empty", watch.stderr)
	}
}

func TestCLIRecvFullJSONIncludesLegacyFields(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "mailbox-state")

	send := runCLI(t, "hello reviewer\n", "--state-dir", stateDir,
		"send",
		"--to", "workflow/reviewer/task-123",
		"--from", "agent/sender",
		"--subject", "review request",
		"--body-file", "-",
	)
	if send.exitCode != 0 {
		t.Fatalf("send exit code = %d, stderr = %q", send.exitCode, send.stderr)
	}

	recv := runCLI(t, "", "--state-dir", stateDir,
		"recv",
		"--for", "workflow/reviewer/task-123",
		"--json",
		"--full",
	)
	if recv.exitCode != 0 {
		t.Fatalf("recv --full exit code = %d, stderr = %q", recv.exitCode, recv.stderr)
	}

	message := decodeFullReceivedMessage(t, recv.stdout)
	if message.MessageID == "" {
		t.Fatalf("recv --full message_id = %q, want non-empty", message.MessageID)
	}
	if message.LeaseExpiresAt == "" {
		t.Fatalf("recv --full lease_expires_at = %q, want non-empty", message.LeaseExpiresAt)
	}
	if message.BodyBlobRef == "" {
		t.Fatalf("recv --full body_blob_ref = %q, want non-empty", message.BodyBlobRef)
	}
}

func TestCLIWaitFullJSONIncludesLegacyMetadata(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "mailbox-state")

	send := runCLI(t, "wait body\n", "--state-dir", stateDir,
		"send",
		"--to", "workflow/wait",
		"--from", "agent/sender",
		"--subject", "wait for me",
		"--body-file", "-",
	)
	if send.exitCode != 0 {
		t.Fatalf("send exit code = %d, stderr = %q", send.exitCode, send.stderr)
	}

	wait := runCLI(t, "", "--state-dir", stateDir,
		"wait",
		"--for", "workflow/wait",
		"--json",
		"--full",
	)
	if wait.exitCode != 0 {
		t.Fatalf("wait --full exit code = %d, stderr = %q", wait.exitCode, wait.stderr)
	}

	var delivery map[string]any
	if err := json.Unmarshal([]byte(wait.stdout), &delivery); err != nil {
		t.Fatalf("json.Unmarshal(wait --full stdout) error = %v; stdout = %q", err, wait.stdout)
	}
	if delivery["state"] != "queued" {
		t.Fatalf("wait --full state = %v, want queued", delivery["state"])
	}
	if _, ok := delivery["visible_at"]; !ok {
		t.Fatalf("wait --full payload = %v, want visible_at", delivery)
	}
	if _, ok := delivery["message_id"]; !ok {
		t.Fatalf("wait --full payload = %v, want message_id", delivery)
	}
}

func TestCLIHelpExitsZeroAndPrintsUsage(t *testing.T) {
	testCases := []struct {
		name         string
		args         []string
		wantContains string
	}{
		{
			name:         "root help",
			args:         []string{"--help"},
			wantContains: "Usage:\n  agent-mailbox [--state-dir PATH] <command> [options]",
		},
		{
			name:         "root help lists stale",
			args:         []string{"--help"},
			wantContains: "  stale               List stale personal inboxes",
		},
		{
			name:         "send help",
			args:         []string{"send", "--help"},
			wantContains: "Usage:\n  agent-mailbox send --to ADDRESS --body-file PATH [options] [--json | --yaml] [--full]",
		},
		{
			name:         "renew help",
			args:         []string{"renew", "--help"},
			wantContains: "Usage:\n  agent-mailbox renew --delivery ID --lease-token TOKEN --for DURATION",
		},
		{
			name:         "stale help",
			args:         []string{"stale", "--help"},
			wantContains: "Usage:\n  agent-mailbox stale --for ADDRESS [--for ADDRESS ...] --older-than DURATION [--json | --yaml]",
		},
		{
			name:         "recv help",
			args:         []string{"recv", "--help"},
			wantContains: "Usage:\n  agent-mailbox recv --for ADDRESS [--for ADDRESS ...] [--max COUNT] [--json | --yaml] [--full]",
		},
		{
			name:         "read help",
			args:         []string{"read", "--help"},
			wantContains: "Usage:\n  agent-mailbox read --message ID [--message ID ...] [--json | --yaml]",
		},
		{
			name:         "watch help",
			args:         []string{"watch", "--help"},
			wantContains: "Usage:\n  agent-mailbox watch --for ADDRESS [--for ADDRESS ...] [--state STATE] [--timeout DURATION] [--json | --yaml]",
		},
		{
			name:         "list help mentions acked state",
			args:         []string{"list", "--help"},
			wantContains: "  --state STATE      Filter by delivery state (queued, leased, acked, dead_letter)",
		},
		{
			name:         "wait help",
			args:         []string{"wait", "--help"},
			wantContains: "Usage:\n  agent-mailbox wait --for ADDRESS [--for ADDRESS ...] [--timeout DURATION] [--json | --yaml] [--full]",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := runCLI(t, "", tc.args...)
			if result.exitCode != 0 {
				t.Fatalf("exit code = %d, want 0; stderr = %q", result.exitCode, result.stderr)
			}
			if !strings.Contains(result.stdout, tc.wantContains) {
				t.Fatalf("stdout = %q, want substring %q", result.stdout, tc.wantContains)
			}
			if result.stderr != "" {
				t.Fatalf("stderr = %q, want empty", result.stderr)
			}
		})
	}
}

func TestCLIRejectsJSONAndYAMLTogether(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "mailbox-state")

	send := runCLI(t, "payload\n", "--state-dir", stateDir,
		"send",
		"--to", "workflow/reviewer/task-123",
		"--body-file", "-",
		"--json",
		"--yaml",
	)
	if send.exitCode != 1 {
		t.Fatalf("send exit code = %d, want 1; stderr = %q", send.exitCode, send.stderr)
	}
	if send.stdout != "" {
		t.Fatalf("send stdout = %q, want empty", send.stdout)
	}
	if !strings.Contains(send.stderr, "--json and --yaml are mutually exclusive") {
		t.Fatalf("send stderr = %q, want mutual exclusion error", send.stderr)
	}
}

func TestCLIHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}

	separator := -1
	for index, arg := range os.Args {
		if arg == "--" {
			separator = index
			break
		}
	}
	if separator == -1 {
		os.Exit(97)
	}

	os.Args = append([]string{"agent-mailbox"}, os.Args[separator+1:]...)
	main()
	os.Exit(0)
}

func seedStaleDelivery(t *testing.T, stateDir, address string, age time.Duration) string {
	t.Helper()

	send := runCLI(t, "stale body\n", "--state-dir", stateDir,
		"send",
		"--to", address,
		"--from", "agent/sender",
		"--subject", "stale subject",
		"--body-file", "-",
	)
	if send.exitCode != 0 {
		t.Fatalf("send stale seed exit code = %d, stderr = %q", send.exitCode, send.stderr)
	}

	oldestEligibleAt := time.Now().UTC().Add(-age)
	runtime, err := mailbox.OpenRuntime(context.Background(), stateDir)
	if err != nil {
		t.Fatalf("OpenRuntime(seed stale) error = %v", err)
	}
	defer runtime.Close()

	if _, err := runtime.DB().Exec(`
UPDATE deliveries
SET visible_at = ?
WHERE recipient_endpoint_id = (
  SELECT endpoint_id
  FROM endpoint_addresses
  WHERE address = ?
)
`, oldestEligibleAt.Format("2006-01-02T15:04:05.000000000Z07:00"), address); err != nil {
		t.Fatalf("Exec(update stale visible_at) error = %v", err)
	}

	return oldestEligibleAt.Format("2006-01-02T15:04:05.000000000Z07:00")
}

type cliResult struct {
	stdout   string
	stderr   string
	exitCode int
}

func runCLI(t *testing.T, stdin string, args ...string) cliResult {
	t.Helper()

	commandArgs := append([]string{"-test.run=TestCLIHelperProcess", "--"}, args...)
	cmd := exec.Command(os.Args[0], commandArgs...)
	cmd.Env = append(os.Environ(), "GO_WANT_HELPER_PROCESS=1")
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	exitCode := 0
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("run helper process error = %v", err)
		}
		exitCode = exitErr.ExitCode()
	}

	return cliResult{
		stdout:   stdout.String(),
		stderr:   stderr.String(),
		exitCode: exitCode,
	}
}

type receivedMessageSummary struct {
	DeliveryID       string `json:"delivery_id"`
	RecipientAddress string `json:"recipient_address"`
	LeaseToken       string `json:"lease_token"`
	Subject          string `json:"subject"`
	ContentType      string `json:"content_type"`
	Body             string `json:"body"`
}

type receiveResultSummary struct {
	Messages []receivedMessageSummary `json:"messages"`
	HasMore  bool                     `json:"has_more"`
}

type readResult struct {
	Items   []map[string]any `json:"items"`
	HasMore bool             `json:"has_more"`
}

func decodeReceiveResult(t *testing.T, raw string) receiveResultSummary {
	t.Helper()

	var result receiveResultSummary
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatalf("json.Unmarshal(recv stdout) error = %v; stdout = %q", err, raw)
	}
	return result
}

func decodeReceivedMessage(t *testing.T, raw string) receivedMessageSummary {
	t.Helper()

	var message receivedMessageSummary
	if err := json.Unmarshal([]byte(raw), &message); err != nil {
		t.Fatalf("json.Unmarshal(recv stdout) error = %v; stdout = %q", err, raw)
	}
	return message
}

func decodeFullReceivedMessage(t *testing.T, raw string) mailbox.ReceivedMessage {
	t.Helper()

	var message mailbox.ReceivedMessage
	if err := json.Unmarshal([]byte(raw), &message); err != nil {
		t.Fatalf("json.Unmarshal(recv stdout) error = %v; stdout = %q", err, raw)
	}
	return message
}

func parseCLIFields(t *testing.T, raw string) map[string]string {
	t.Helper()

	fields := make(map[string]string)
	for _, part := range strings.Fields(strings.TrimSpace(raw)) {
		key, value, found := strings.Cut(part, "=")
		if !found {
			t.Fatalf("parseCLIFields(%q) encountered token without '=': %q", raw, part)
		}
		fields[key] = value
	}
	return fields
}
