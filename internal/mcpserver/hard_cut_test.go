package mcpserver

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

	"github.com/ruiheng/waypost/internal/waypost"
)

func TestWaypostStatusReportsAuthoritativeCLIContext(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "waypost-state")
	service := newService(Options{
		WaypostServiceFactory: fakeWaypostServiceFactory{service: &fakeWaypostService{t: t}},
		CommandRunner:         &fakeRunner{t: t, handler: func([]string, string) (RunResult, error) { return RunResult{}, nil }},
		StateDir:              stateDir,
		Executable:            "/opt/waypost/bin/waypost",
		DisableWakeScheduler:  true,
		DisableLeaseRenewLoop: true,
	})
	service.state.autoBindAttempted = true

	status := callServiceTool(t, service, "waypost_status", map[string]any{})
	if got := status["server_version"]; got != serverVersion {
		t.Fatalf("server_version = %v, want %q", got, serverVersion)
	}
	if got := status["executable"]; got != "/opt/waypost/bin/waypost" {
		t.Fatalf("executable = %v, want authoritative executable", got)
	}
	wantStateDir, err := filepath.Abs(stateDir)
	if err != nil {
		t.Fatalf("filepath.Abs(stateDir): %v", err)
	}
	if got := status["resolved_state_dir"]; got != wantStateDir {
		t.Fatalf("resolved_state_dir = %v, want %q", got, wantStateDir)
	}
}

func TestWaypostStatusCLIReplacementForward(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "waypost-state")
	service := newService(Options{
		StateDir:              stateDir,
		Executable:            os.Args[0],
		CommandRunner:         &fakeRunner{t: t, handler: func([]string, string) (RunResult, error) { return RunResult{}, nil }},
		NotifyDelay:           -1,
		DisableWakeScheduler:  true,
		DisableLeaseRenewLoop: true,
	})
	defer service.Close()
	service.state.autoBindAttempted = true

	status := callServiceTool(t, service, "waypost_status", map[string]any{})
	executable, ok := status["executable"].(string)
	if !ok || executable == "" {
		t.Fatalf("waypost_status executable = %v, want path", status["executable"])
	}
	resolvedStateDir, ok := status["resolved_state_dir"].(string)
	if !ok || resolvedStateDir == "" {
		t.Fatalf("waypost_status resolved_state_dir = %v, want path", status["resolved_state_dir"])
	}

	sent := callServiceTool(t, service, "waypost_send", map[string]any{
		"to_address":   "workflow/source",
		"from_address": "agent/sender",
		"subject":      "source",
		"body":         "body",
	})
	deliveryID := sent["delivery_id"].(string)

	cmd := exec.Command(executable,
		"-test.run=TestWaypostCLIHelperProcess", "--",
		"--state-dir", resolvedStateDir,
		"forward", "--delivery", deliveryID, "--to", "workflow/target", "--json",
	)
	cmd.Env = append(os.Environ(), "GO_WANT_WAYPOST_CLI_HELPER=1")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("reported CLI forward failed: %v; stderr = %q", err, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("reported CLI forward stderr = %q, want empty", stderr.String())
	}

	var forwarded map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &forwarded); err != nil {
		t.Fatalf("decode reported CLI forward output: %v; stdout = %q", err, stdout.String())
	}
	if forwarded["source_delivery_id"] != deliveryID || forwarded["delivery_id"] == "" {
		t.Fatalf("reported CLI forward output = %v", forwarded)
	}
}

func TestWaypostCLIHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_WAYPOST_CLI_HELPER") != "1" {
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

	args := os.Args[separator+1:]
	stateDir := ""
	if len(args) >= 2 && args[0] == "--state-dir" {
		stateDir = args[1]
		args = args[2:]
	}
	app := waypost.NewApp(os.Stdin, os.Stdout, os.Stderr)
	err := app.RunWithStateDir(context.Background(), stateDir, args)
	if err == nil || errors.Is(err, waypost.ErrHelpRequested) {
		os.Exit(0)
	}
	if errors.Is(err, waypost.ErrNoMessage) {
		os.Exit(2)
	}
	if !waypost.WriteCLIJSONError(os.Stderr, args, err) {
		_, _ = os.Stderr.WriteString(err.Error() + "\n")
	}
	os.Exit(1)
}

func TestExternalDurableFailReconcilesMCPLeaseHistory(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "waypost-state")
	service := newService(Options{
		StateDir:              stateDir,
		CommandRunner:         &fakeRunner{t: t, handler: func([]string, string) (RunResult, error) { return RunResult{}, nil }},
		NotifyDelay:           -1,
		DisableWakeScheduler:  true,
		DisableLeaseRenewLoop: true,
	})
	defer service.Close()
	service.state.boundAddresses = []string{"agent-deck/self"}
	service.state.defaultSender = "agent-deck/self"
	service.state.autoBindAttempted = true

	sent := callServiceTool(t, service, "waypost_send", map[string]any{
		"to_address": "agent-deck/self",
		"subject":    "external fail",
		"body":       "body",
	})
	deliveryID := sent["delivery_id"].(string)
	received := callServiceTool(t, service, "waypost_recv", map[string]any{
		"addresses": []string{"agent-deck/self"},
	})
	delivery := received["delivery"].(map[string]any)
	leaseToken := delivery["lease_token"].(string)

	_, err := withWaypostService[waypost.DeliveryTransitionResult, *waypost.Operations](context.Background(), service.waypostServices, func(ops *waypost.Operations) (waypost.DeliveryTransitionResult, error) {
		return ops.Fail(context.Background(), deliveryID, leaseToken, "external CLI failure")
	})
	if err != nil {
		t.Fatalf("durable Fail() error = %v", err)
	}

	history := callServiceTool(t, service, "waypost_claim_history", map[string]any{
		"delivery_id":         deliveryID,
		"include_terminal":    true,
		"include_lease_token": true,
	})
	items := history["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("claim history items = %d, want 1", len(items))
	}
	item := items[0].(map[string]any)
	if got := item["status"]; got != "queued" {
		t.Fatalf("claim history status = %v, want durable queued state", got)
	}
	if got := item["terminal_at"]; got == nil || got == "" {
		t.Fatalf("claim history terminal_at = %v, want durable transition time", got)
	}
	if _, ok := item["lease_token"]; ok {
		t.Fatalf("claim history exposed obsolete lease token: %v", item)
	}
	if service.activeLeases.hasTrackedLeases() {
		t.Fatal("externally failed lease remains active in MCP cache")
	}

	if err := service.processLeaseRenewals(context.Background()); err != nil {
		t.Fatalf("processLeaseRenewals() after reconciliation = %v", err)
	}
}

func TestTrackedLeaseInspectionErrorDoesNotReturnCachedHint(t *testing.T) {
	inspectErr := context.DeadlineExceeded
	waypostService := &fakeWaypostService{t: t}
	waypostService.inspectLeaseFunc = func(context.Context, string) (waypost.DeliveryLeaseState, error) {
		return waypost.DeliveryLeaseState{}, inspectErr
	}
	service := newService(Options{
		WaypostServiceFactory: fakeWaypostServiceFactory{service: waypostService},
		CommandRunner:         &fakeRunner{t: t, handler: func([]string, string) (RunResult, error) { return RunResult{}, nil }},
		DisableWakeScheduler:  true,
		DisableLeaseRenewLoop: true,
	})
	service.state.boundAddresses = []string{"agent-deck/self"}
	service.state.defaultSender = "agent-deck/self"
	service.state.autoBindAttempted = true
	service.activeLeases.trackReceive(waypost.ReceiveResult{Messages: []waypost.ReceivedMessage{{
		DeliveryID:       "dlv_inspect",
		LeaseToken:       "lease_inspect",
		LeaseExpiresAt:   time.Now().UTC().Add(time.Minute).Format(time.RFC3339Nano),
		RecipientAddress: "agent-deck/self",
	}}}, time.Now().UTC().Format(time.RFC3339Nano))

	err := callServiceToolExpectError(t, service, "waypost_recv", map[string]any{
		"addresses": []string{"agent-deck/self"},
	})
	if err == nil || !strings.Contains(err.Error(), inspectErr.Error()) {
		t.Fatalf("waypost_recv inspection error = %v, want %v", err, inspectErr)
	}
}
