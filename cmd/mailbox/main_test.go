package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ruiheng/agent-mailbox/internal/mailbox"
)

func TestCLIRegisterSendRecvAckFlow(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "mailbox-state")

	registerRecipient := runCLI(t, "", "--state-dir", stateDir,
		"endpoint", "register",
		"--alias", "workflow/reviewer/task-123",
	)
	if registerRecipient.exitCode != 0 {
		t.Fatalf("register recipient exit code = %d, stderr = %q", registerRecipient.exitCode, registerRecipient.stderr)
	}
	if !strings.Contains(registerRecipient.stdout, "endpoint_id=") {
		t.Fatalf("register recipient stdout = %q, want endpoint_id", registerRecipient.stdout)
	}
	if strings.Contains(registerRecipient.stdout, "kind=") {
		t.Fatalf("register recipient stdout = %q, should not include kind", registerRecipient.stdout)
	}

	registerSender := runCLI(t, "", "--state-dir", stateDir,
		"endpoint", "register",
		"--alias", "agent/sender",
	)
	if registerSender.exitCode != 0 {
		t.Fatalf("register sender exit code = %d, stderr = %q", registerSender.exitCode, registerSender.stderr)
	}

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
	if !strings.Contains(send.stdout, "message_id=") || !strings.Contains(send.stdout, "delivery_id=") {
		t.Fatalf("send stdout = %q, want message and delivery ids", send.stdout)
	}

	recv := runCLI(t, "", "--state-dir", stateDir,
		"recv",
		"--for", "workflow/reviewer/task-123",
		"--json",
	)
	if recv.exitCode != 0 {
		t.Fatalf("recv exit code = %d, stderr = %q", recv.exitCode, recv.stderr)
	}

	var message mailbox.ReceivedMessage
	if err := json.Unmarshal([]byte(recv.stdout), &message); err != nil {
		t.Fatalf("json.Unmarshal(recv stdout) error = %v; stdout = %q", err, recv.stdout)
	}
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
}

func TestCLIRecvNoMessageExitCodeAndSilence(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "mailbox-state")

	register := runCLI(t, "", "--state-dir", stateDir,
		"endpoint", "register",
		"--alias", "workflow/empty",
	)
	if register.exitCode != 0 {
		t.Fatalf("register exit code = %d, stderr = %q", register.exitCode, register.stderr)
	}
	registerSecond := runCLI(t, "", "--state-dir", stateDir,
		"endpoint", "register",
		"--alias", "workflow/empty-2",
	)
	if registerSecond.exitCode != 0 {
		t.Fatalf("register second exit code = %d, stderr = %q", registerSecond.exitCode, registerSecond.stderr)
	}

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

	timedOut := runCLI(t, "", "--state-dir", stateDir,
		"recv",
		"--for", "workflow/empty",
		"--wait",
		"--timeout", "20ms",
	)
	if timedOut.exitCode != 2 {
		t.Fatalf("timed recv exit code = %d, want 2; stderr = %q", timedOut.exitCode, timedOut.stderr)
	}
	if timedOut.stdout != "" {
		t.Fatalf("timed recv stdout = %q, want empty", timedOut.stdout)
	}
	if timedOut.stderr != "" {
		t.Fatalf("timed recv stderr = %q, want empty", timedOut.stderr)
	}

	multiTimedOut := runCLI(t, "", "--state-dir", stateDir,
		"recv",
		"--for", "workflow/empty",
		"--for", "workflow/empty-2",
		"--wait",
		"--timeout", "20ms",
	)
	if multiTimedOut.exitCode != 2 {
		t.Fatalf("multi timed recv exit code = %d, want 2; stderr = %q", multiTimedOut.exitCode, multiTimedOut.stderr)
	}
	if multiTimedOut.stdout != "" {
		t.Fatalf("multi timed recv stdout = %q, want empty", multiTimedOut.stdout)
	}
	if multiTimedOut.stderr != "" {
		t.Fatalf("multi timed recv stderr = %q, want empty", multiTimedOut.stderr)
	}
}

func TestCLIRecvMultipleAliasesPlainTextIncludesRecipientAlias(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "mailbox-state")

	for _, alias := range []string{"workflow/older", "workflow/newer"} {
		register := runCLI(t, "", "--state-dir", stateDir,
			"endpoint", "register",
			"--alias", alias,
		)
		if register.exitCode != 0 {
			t.Fatalf("register %s exit code = %d, stderr = %q", alias, register.exitCode, register.stderr)
		}
	}
	registerSender := runCLI(t, "", "--state-dir", stateDir,
		"endpoint", "register",
		"--alias", "agent/sender",
	)
	if registerSender.exitCode != 0 {
		t.Fatalf("register sender exit code = %d, stderr = %q", registerSender.exitCode, registerSender.stderr)
	}

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
	if !strings.Contains(recv.stdout, "recipient_alias=workflow/older") {
		t.Fatalf("recv multi plain text stdout = %q, want recipient_alias=workflow/older", recv.stdout)
	}
	if !strings.Contains(recv.stdout, "older body\n") {
		t.Fatalf("recv multi plain text stdout = %q, want older body", recv.stdout)
	}
}

func TestCLIRecvMultipleAliasesUnknownAliasFails(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "mailbox-state")

	register := runCLI(t, "", "--state-dir", stateDir,
		"endpoint", "register",
		"--alias", "workflow/known",
	)
	if register.exitCode != 0 {
		t.Fatalf("register exit code = %d, stderr = %q", register.exitCode, register.stderr)
	}

	recv := runCLI(t, "", "--state-dir", stateDir,
		"recv",
		"--for", "workflow/known",
		"--for", "workflow/missing",
	)
	if recv.exitCode != 1 {
		t.Fatalf("recv unknown alias exit code = %d, want 1; stderr = %q", recv.exitCode, recv.stderr)
	}
	if !strings.Contains(recv.stderr, `alias "workflow/missing" not found`) {
		t.Fatalf("recv unknown alias stderr = %q, want missing alias error", recv.stderr)
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
