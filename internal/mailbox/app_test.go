package mailbox

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenRuntimeInitializesStateAndSchema(t *testing.T) {
	t.Parallel()

	stateDir := filepath.Join(t.TempDir(), "mailbox-state")
	runtime, err := OpenRuntime(context.Background(), stateDir)
	if err != nil {
		t.Fatalf("OpenRuntime() error = %v", err)
	}
	defer runtime.Close()

	if info, err := os.Stat(runtime.StateDir()); err != nil {
		t.Fatalf("os.Stat(stateDir) error = %v", err)
	} else if info.Mode().Perm() != 0o700 {
		t.Fatalf("state dir permissions = %o, want 700", info.Mode().Perm())
	}

	if info, err := os.Stat(runtime.BlobDir()); err != nil {
		t.Fatalf("os.Stat(blobDir) error = %v", err)
	} else if info.Mode().Perm() != 0o700 {
		t.Fatalf("blob dir permissions = %o, want 700", info.Mode().Perm())
	}

	var journalMode string
	if err := runtime.DB().QueryRow(`PRAGMA journal_mode;`).Scan(&journalMode); err != nil {
		t.Fatalf("QueryRow(PRAGMA journal_mode) error = %v", err)
	}
	if strings.ToLower(journalMode) != "wal" {
		t.Fatalf("journal mode = %q, want wal", journalMode)
	}

	tables := []string{"endpoints", "endpoint_aliases", "messages", "deliveries", "events"}
	for _, table := range tables {
		var name string
		if err := runtime.DB().QueryRow(`
SELECT name
FROM sqlite_master
WHERE type = 'table' AND name = ?
`, table).Scan(&name); err != nil {
			t.Fatalf("table %s missing: %v", table, err)
		}
	}
}

func TestRegisterEndpointIdempotencyAndKindConflict(t *testing.T) {
	t.Parallel()

	runtime, err := OpenRuntime(context.Background(), filepath.Join(t.TempDir(), "mailbox-state"))
	if err != nil {
		t.Fatalf("OpenRuntime() error = %v", err)
	}
	defer runtime.Close()

	store := runtime.Store()
	first, err := store.RegisterEndpoint(context.Background(), "workflow/reviewer/task-123", "workflow")
	if err != nil {
		t.Fatalf("RegisterEndpoint(first) error = %v", err)
	}
	if !first.Created {
		t.Fatalf("first registration Created = false, want true")
	}

	second, err := store.RegisterEndpoint(context.Background(), "workflow/reviewer/task-123", "workflow")
	if err != nil {
		t.Fatalf("RegisterEndpoint(second) error = %v", err)
	}
	if second.Created {
		t.Fatalf("second registration Created = true, want false")
	}
	if second.EndpointID != first.EndpointID {
		t.Fatalf("endpoint id changed on idempotent registration: got %q want %q", second.EndpointID, first.EndpointID)
	}

	if _, err := store.RegisterEndpoint(context.Background(), "workflow/reviewer/task-123", "agent"); err == nil {
		t.Fatal("RegisterEndpoint(kind conflict) error = nil, want non-nil")
	} else if !strings.Contains(err.Error(), `already exists with kind "workflow"`) {
		t.Fatalf("RegisterEndpoint(kind conflict) error = %v, want kind mismatch", err)
	}
}

func TestSendAndListHappyPath(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		bodyFile string
		stdin    string
		body     string
	}{
		{
			name:     "stdin",
			bodyFile: "-",
			stdin:    "hello from stdin",
			body:     "hello from stdin",
		},
		{
			name: "file",
			body: "hello from file",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			stateDir := filepath.Join(t.TempDir(), "mailbox-state")
			registerApp := NewApp(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
			if err := registerApp.Run(context.Background(), []string{
				"--state-dir", stateDir,
				"endpoint", "register",
				"--alias", "workflow/reviewer/task-123",
				"--kind", "workflow",
			}); err != nil {
				t.Fatalf("register recipient error = %v", err)
			}
			if err := registerApp.Run(context.Background(), []string{
				"--state-dir", stateDir,
				"endpoint", "register",
				"--alias", "agent/sender",
				"--kind", "agent",
			}); err != nil {
				t.Fatalf("register sender error = %v", err)
			}

			bodyFile := tc.bodyFile
			if bodyFile == "" {
				path := filepath.Join(t.TempDir(), "body.txt")
				if err := os.WriteFile(path, []byte(tc.body), 0o600); err != nil {
					t.Fatalf("os.WriteFile(body) error = %v", err)
				}
				bodyFile = path
			}

			sendStdout := &bytes.Buffer{}
			sendApp := NewApp(strings.NewReader(tc.stdin), sendStdout, &bytes.Buffer{})
			if err := sendApp.Run(context.Background(), []string{
				"--state-dir", stateDir,
				"send",
				"--to", "workflow/reviewer/task-123",
				"--from", "agent/sender",
				"--subject", "review request",
				"--body-file", bodyFile,
			}); err != nil {
				t.Fatalf("send error = %v", err)
			}
			if !strings.Contains(sendStdout.String(), "message_id=") {
				t.Fatalf("send output = %q, want message_id", sendStdout.String())
			}

			listStdout := &bytes.Buffer{}
			listApp := NewApp(strings.NewReader(""), listStdout, &bytes.Buffer{})
			if err := listApp.Run(context.Background(), []string{
				"--state-dir", stateDir,
				"list",
				"--for", "workflow/reviewer/task-123",
				"--json",
			}); err != nil {
				t.Fatalf("list error = %v", err)
			}

			var deliveries []ListedDelivery
			if err := json.Unmarshal(listStdout.Bytes(), &deliveries); err != nil {
				t.Fatalf("json.Unmarshal(list output) error = %v", err)
			}
			if len(deliveries) != 1 {
				t.Fatalf("len(deliveries) = %d, want 1", len(deliveries))
			}
			if deliveries[0].Subject != "review request" {
				t.Fatalf("delivery subject = %q, want review request", deliveries[0].Subject)
			}
			if deliveries[0].RecipientAlias != "workflow/reviewer/task-123" {
				t.Fatalf("recipient alias = %q", deliveries[0].RecipientAlias)
			}
			if deliveries[0].SenderEndpointID == nil {
				t.Fatal("sender endpoint id = nil, want non-nil")
			}

			runtime, err := OpenRuntime(context.Background(), stateDir)
			if err != nil {
				t.Fatalf("OpenRuntime(verify) error = %v", err)
			}
			defer runtime.Close()

			var messageCount int
			if err := runtime.DB().QueryRow(`SELECT COUNT(*) FROM messages`).Scan(&messageCount); err != nil {
				t.Fatalf("count messages error = %v", err)
			}
			if messageCount != 1 {
				t.Fatalf("message count = %d, want 1", messageCount)
			}

			var deliveryCount int
			if err := runtime.DB().QueryRow(`SELECT COUNT(*) FROM deliveries`).Scan(&deliveryCount); err != nil {
				t.Fatalf("count deliveries error = %v", err)
			}
			if deliveryCount != 1 {
				t.Fatalf("delivery count = %d, want 1", deliveryCount)
			}

			var eventCount int
			if err := runtime.DB().QueryRow(`SELECT COUNT(*) FROM events`).Scan(&eventCount); err != nil {
				t.Fatalf("count events error = %v", err)
			}
			if eventCount != 4 {
				t.Fatalf("event count = %d, want 4", eventCount)
			}

			var blobRef string
			if err := runtime.DB().QueryRow(`SELECT body_blob_ref FROM messages LIMIT 1`).Scan(&blobRef); err != nil {
				t.Fatalf("select body_blob_ref error = %v", err)
			}
			body, err := os.ReadFile(filepath.Join(runtime.BlobDir(), blobRef))
			if err != nil {
				t.Fatalf("os.ReadFile(blob) error = %v", err)
			}
			if string(body) != tc.body {
				t.Fatalf("blob body = %q, want %q", string(body), tc.body)
			}
		})
	}
}

func TestInvalidCLIPathsDoNotCreateRuntimeState(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string
		args []string
	}{
		{
			name: "unknown command",
			args: []string{"unknown"},
		},
		{
			name: "unknown endpoint subcommand",
			args: []string{"endpoint", "unknown"},
		},
		{
			name: "send missing body file",
			args: []string{"send", "--to", "workflow/reviewer/task-123"},
		},
		{
			name: "send invalid flag",
			args: []string{"send", "--bogus"},
		},
		{
			name: "endpoint register missing alias",
			args: []string{"endpoint", "register", "--kind", "workflow"},
		},
		{
			name: "endpoint register missing kind",
			args: []string{"endpoint", "register", "--alias", "workflow/reviewer/task-123"},
		},
		{
			name: "send missing to",
			args: []string{"send", "--body-file", "-"},
		},
		{
			name: "list missing for",
			args: []string{"list", "--json"},
		},
		{
			name: "recv missing for",
			args: []string{"recv", "--wait"},
		},
		{
			name: "recv empty for",
			args: []string{"recv", "--for", "   "},
		},
		{
			name: "recv timeout without wait",
			args: []string{"recv", "--for", "workflow/reviewer/task-123", "--timeout", "1s"},
		},
		{
			name: "recv negative timeout",
			args: []string{"recv", "--for", "workflow/reviewer/task-123", "--wait", "--timeout", "-1s"},
		},
		{
			name: "ack missing delivery",
			args: []string{"ack", "--lease-token", "lease_token"},
		},
		{
			name: "ack missing lease token",
			args: []string{"ack", "--delivery", "dlv_123"},
		},
		{
			name: "release missing delivery",
			args: []string{"release", "--lease-token", "lease_token"},
		},
		{
			name: "release missing lease token",
			args: []string{"release", "--delivery", "dlv_123"},
		},
		{
			name: "defer missing delivery",
			args: []string{"defer", "--lease-token", "lease_token", "--until", "2026-03-18T12:00:00Z"},
		},
		{
			name: "defer missing lease token",
			args: []string{"defer", "--delivery", "dlv_123", "--until", "2026-03-18T12:00:00Z"},
		},
		{
			name: "fail missing delivery",
			args: []string{"fail", "--lease-token", "lease_token", "--reason", "tool crashed"},
		},
		{
			name: "fail missing lease token",
			args: []string{"fail", "--delivery", "dlv_123", "--reason", "tool crashed"},
		},
		{
			name: "fail missing reason",
			args: []string{"fail", "--delivery", "dlv_123", "--lease-token", "lease_token"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			stateDir := filepath.Join(t.TempDir(), "mailbox-state")
			app := NewApp(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
			err := app.Run(context.Background(), append([]string{"--state-dir", stateDir}, tc.args...))
			if err == nil {
				t.Fatal("Run() error = nil, want non-nil")
			}

			assertPathMissing(t, stateDir)
			assertPathMissing(t, filepath.Join(stateDir, databaseFilename))
			assertPathMissing(t, filepath.Join(stateDir, blobsDirName))
		})
	}
}

func assertPathMissing(t *testing.T, path string) {
	t.Helper()

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("path %q exists or returned unexpected error: %v", path, err)
	}
}
