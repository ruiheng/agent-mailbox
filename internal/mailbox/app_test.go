package mailbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

	tables := []string{"endpoints", "endpoint_addresses", "messages", "deliveries", "events"}
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

	rows, err := runtime.DB().Query(`PRAGMA table_info(endpoints)`)
	if err != nil {
		t.Fatalf("PRAGMA table_info(endpoints) error = %v", err)
	}
	defer rows.Close()

	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull, pk int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
			t.Fatalf("scan endpoints table info error = %v", err)
		}
		if name == "kind" {
			t.Fatal("endpoints schema still contains kind column")
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate endpoints table info error = %v", err)
	}
}

func TestSendImplicitlyCreatesAddressesOnce(t *testing.T) {
	t.Parallel()

	runtime, err := OpenRuntime(context.Background(), filepath.Join(t.TempDir(), "mailbox-state"))
	if err != nil {
		t.Fatalf("OpenRuntime() error = %v", err)
	}
	defer runtime.Close()

	store := runtime.Store()
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := store.Send(context.Background(), SendParams{
			ToAddress:     "workflow/reviewer/task-123",
			FromAddress:   "agent/sender",
			Subject:       "review request",
			ContentType:   "text/plain",
			SchemaVersion: "v1",
			Body:          []byte("hello reviewer"),
		}); err != nil {
			t.Fatalf("Send(attempt %d) error = %v", attempt+1, err)
		}
	}

	var addressCount int
	if err := runtime.DB().QueryRow(`SELECT COUNT(*) FROM endpoint_addresses`).Scan(&addressCount); err != nil {
		t.Fatalf("count endpoint_addresses error = %v", err)
	}
	if addressCount != 2 {
		t.Fatalf("endpoint address count = %d, want 2", addressCount)
	}

	var registrationEvents int
	if err := runtime.DB().QueryRow(`
SELECT COUNT(*)
FROM events
WHERE event_type = 'endpoint_registered'
`).Scan(&registrationEvents); err != nil {
		t.Fatalf("count endpoint_registered events error = %v", err)
	}
	if registrationEvents != 2 {
		t.Fatalf("endpoint_registered event count = %d, want 2", registrationEvents)
	}
}

func TestWriteBlobSyncsFileBeforeRenameAndDirectoryAfter(t *testing.T) {
	t.Parallel()

	blobDir := filepath.Join(t.TempDir(), "blobs")
	if err := os.MkdirAll(blobDir, 0o700); err != nil {
		t.Fatalf("os.MkdirAll(blobDir) error = %v", err)
	}

	store := NewStore(nil, nil, nil, blobDir)
	var ops []string
	store.createBlobTemp = func(dir, pattern string) (blobTempFile, error) {
		ops = append(ops, "create:"+dir)
		return &recordingBlobTempFile{
			name: filepath.Join(dir, "blob.tmp"),
			ops:  &ops,
		}, nil
	}
	store.renameFile = func(oldPath, newPath string) error {
		ops = append(ops, "rename:"+filepath.Base(oldPath)+"->"+filepath.Base(newPath))
		return nil
	}
	store.removeFile = func(path string) error {
		ops = append(ops, "remove:"+filepath.Base(path))
		return nil
	}
	store.syncDir = func(path string) error {
		ops = append(ops, "dir-sync:"+path)
		return nil
	}

	blobRef, bodySize, _, err := store.writeBlob([]byte("hello"))
	if err != nil {
		t.Fatalf("writeBlob() error = %v", err)
	}
	if !strings.HasPrefix(blobRef, "blob_") {
		t.Fatalf("blobRef = %q, want blob_ prefix", blobRef)
	}
	if bodySize != 5 {
		t.Fatalf("bodySize = %d, want 5", bodySize)
	}

	wantOps := []string{
		"create:" + blobDir,
		"write:hello",
		"file-sync",
		"close",
		"rename:blob.tmp->" + blobRef,
		"dir-sync:" + blobDir,
	}
	if len(ops) != len(wantOps) {
		t.Fatalf("len(ops) = %d, want %d; ops=%v", len(ops), len(wantOps), ops)
	}
	for i, want := range wantOps {
		if ops[i] != want {
			t.Fatalf("ops[%d] = %q, want %q (ops=%v)", i, ops[i], want, ops)
		}
	}
}

func TestSendAbortsBeforeMetadataCommitWhenBlobDirectorySyncFails(t *testing.T) {
	t.Parallel()

	runtime, err := OpenRuntime(context.Background(), filepath.Join(t.TempDir(), "mailbox-state"))
	if err != nil {
		t.Fatalf("OpenRuntime() error = %v", err)
	}
	defer runtime.Close()

	store := runtime.Store()
	store.syncDir = func(path string) error {
		return errors.New("dir sync failed")
	}

	_, err = store.Send(context.Background(), SendParams{
		ToAddress:     "workflow/reviewer/task-123",
		FromAddress:   "agent/sender",
		Subject:       "review request",
		ContentType:   "text/plain",
		SchemaVersion: "v1",
		Body:          []byte("hello reviewer"),
	})
	if err == nil || !strings.Contains(err.Error(), "sync blob directory") {
		t.Fatalf("Send() error = %v, want sync blob directory failure", err)
	}

	var messageCount int
	if err := runtime.DB().QueryRow(`SELECT COUNT(*) FROM messages`).Scan(&messageCount); err != nil {
		t.Fatalf("count messages error = %v", err)
	}
	if messageCount != 0 {
		t.Fatalf("message count = %d, want 0", messageCount)
	}

	var deliveryCount int
	if err := runtime.DB().QueryRow(`SELECT COUNT(*) FROM deliveries`).Scan(&deliveryCount); err != nil {
		t.Fatalf("count deliveries error = %v", err)
	}
	if deliveryCount != 0 {
		t.Fatalf("delivery count = %d, want 0", deliveryCount)
	}

	var endpointAddressCount int
	if err := runtime.DB().QueryRow(`SELECT COUNT(*) FROM endpoint_addresses`).Scan(&endpointAddressCount); err != nil {
		t.Fatalf("count endpoint_addresses error = %v", err)
	}
	if endpointAddressCount != 0 {
		t.Fatalf("endpoint address count = %d, want 0", endpointAddressCount)
	}
}

func TestSendContinuesWhenBlobDirectorySyncIsUnsupported(t *testing.T) {
	t.Parallel()

	runtime, err := OpenRuntime(context.Background(), filepath.Join(t.TempDir(), "mailbox-state"))
	if err != nil {
		t.Fatalf("OpenRuntime() error = %v", err)
	}
	defer runtime.Close()

	store := runtime.Store()
	store.syncDir = func(path string) error {
		return errors.ErrUnsupported
	}

	result, err := store.Send(context.Background(), SendParams{
		ToAddress:     "workflow/reviewer/task-123",
		FromAddress:   "agent/sender",
		Subject:       "review request",
		ContentType:   "text/plain",
		SchemaVersion: "v1",
		Body:          []byte("hello reviewer"),
	})
	if err != nil {
		t.Fatalf("Send() error = %v, want nil", err)
	}

	var state string
	if err := runtime.DB().QueryRow(`
SELECT state
FROM deliveries
WHERE delivery_id = ?
`, result.DeliveryID).Scan(&state); err != nil {
		t.Fatalf("QueryRow(delivery state) error = %v", err)
	}
	if state != "queued" {
		t.Fatalf("delivery state = %q, want queued", state)
	}
}

func TestSendImplicitAddressCreationIsConcurrentSafe(t *testing.T) {
	t.Parallel()

	runtime, err := OpenRuntime(context.Background(), filepath.Join(t.TempDir(), "mailbox-state"))
	if err != nil {
		t.Fatalf("OpenRuntime() error = %v", err)
	}
	defer runtime.Close()
	runtime.DB().SetMaxOpenConns(16)
	runtime.DB().SetMaxIdleConns(16)

	store := runtime.Store()

	type sendResult struct {
		result SendResult
		err    error
	}
	const workers = 16
	results := make(chan sendResult, workers)
	start := make(chan struct{})
	for range workers {
		go func() {
			<-start
			result, err := store.Send(context.Background(), SendParams{
				ToAddress:     "workflow/concurrent",
				Subject:       "race-safe send",
				ContentType:   "text/plain",
				SchemaVersion: "v1",
				Body:          []byte("hello"),
			})
			results <- sendResult{result: result, err: err}
		}()
	}
	close(start)

	var sendResults []sendResult
	for i := 0; i < workers; i++ {
		select {
		case result := <-results:
			sendResults = append(sendResults, result)
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for concurrent send results")
		}
	}
	for i, result := range sendResults {
		if result.err != nil {
			t.Fatalf("Send(%d) error = %v", i, result.err)
		}
	}
	seenDeliveryIDs := make(map[string]struct{}, workers)
	for _, result := range sendResults {
		if _, exists := seenDeliveryIDs[result.result.DeliveryID]; exists {
			t.Fatalf("concurrent sends reused delivery id %q", result.result.DeliveryID)
		}
		seenDeliveryIDs[result.result.DeliveryID] = struct{}{}
	}

	deliveries, err := store.List(context.Background(), ListParams{Address: "workflow/concurrent", State: "queued"})
	if err != nil {
		t.Fatalf("List(concurrent queued) error = %v", err)
	}
	if len(deliveries) != workers {
		t.Fatalf("len(concurrent queued) = %d, want %d", len(deliveries), workers)
	}

	var endpointAddressCount int
	if err := runtime.DB().QueryRow(`SELECT COUNT(*) FROM endpoint_addresses WHERE address = ?`, "workflow/concurrent").Scan(&endpointAddressCount); err != nil {
		t.Fatalf("count workflow/concurrent addresses error = %v", err)
	}
	if endpointAddressCount != 1 {
		t.Fatalf("workflow/concurrent address count = %d, want 1", endpointAddressCount)
	}

	var endpointCount int
	if err := runtime.DB().QueryRow(`
SELECT COUNT(*)
FROM endpoints
WHERE endpoint_id IN (
  SELECT endpoint_id
  FROM endpoint_addresses
  WHERE address = ?
)
`, "workflow/concurrent").Scan(&endpointCount); err != nil {
		t.Fatalf("count endpoints error = %v", err)
	}
	if endpointCount != 1 {
		t.Fatalf("endpoint count = %d, want 1", endpointCount)
	}

	for i, delivery := range deliveries {
		if delivery.RecipientAddress != "workflow/concurrent" {
			t.Fatalf("deliveries[%d] recipient address = %q, want workflow/concurrent", i, delivery.RecipientAddress)
		}
		if delivery.Subject != "race-safe send" {
			t.Fatalf("deliveries[%d] subject = %q, want race-safe send", i, delivery.Subject)
		}
	}

	if got := len(seenDeliveryIDs); got != workers {
		t.Fatalf("unique delivery id count = %d, want %d", got, workers)
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
			if deliveries[0].RecipientAddress != "workflow/reviewer/task-123" {
				t.Fatalf("recipient address = %q", deliveries[0].RecipientAddress)
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

func TestSendRejectsEmptyBody(t *testing.T) {
	t.Parallel()

	runtime, err := OpenRuntime(context.Background(), filepath.Join(t.TempDir(), "mailbox-state"))
	if err != nil {
		t.Fatalf("OpenRuntime() error = %v", err)
	}
	defer runtime.Close()

	store := runtime.Store()
	for _, body := range [][]byte{nil, []byte{}} {
		_, err := store.Send(context.Background(), SendParams{
			ToAddress:     "workflow/reviewer/task-123",
			FromAddress:   "agent/sender",
			Subject:       "review request",
			ContentType:   "text/plain",
			SchemaVersion: "v1",
			Body:          body,
		})
		if !errors.Is(err, ErrEmptyBody) {
			t.Fatalf("Send(empty body) error = %v, want ErrEmptyBody", err)
		}
	}

	var messageCount int
	if err := runtime.DB().QueryRow(`SELECT COUNT(*) FROM messages`).Scan(&messageCount); err != nil {
		t.Fatalf("count messages error = %v", err)
	}
	if messageCount != 0 {
		t.Fatalf("message count = %d, want 0", messageCount)
	}

	var deliveryCount int
	if err := runtime.DB().QueryRow(`SELECT COUNT(*) FROM deliveries`).Scan(&deliveryCount); err != nil {
		t.Fatalf("count deliveries error = %v", err)
	}
	if deliveryCount != 0 {
		t.Fatalf("delivery count = %d, want 0", deliveryCount)
	}

	entries, err := os.ReadDir(runtime.BlobDir())
	if err != nil {
		t.Fatalf("os.ReadDir(blob dir) error = %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("len(blob entries) = %d, want 0", len(entries))
	}
}

func TestAppSendRejectsEmptyBodyInput(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		stdin    string
		bodyFile string
	}{
		{
			name:     "empty stdin",
			bodyFile: "-",
		},
		{
			name: "empty file",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			stateDir := filepath.Join(t.TempDir(), "mailbox-state")

			bodyFile := tc.bodyFile
			if bodyFile == "" {
				path := filepath.Join(t.TempDir(), "body.txt")
				if err := os.WriteFile(path, []byte{}, 0o600); err != nil {
					t.Fatalf("os.WriteFile(empty body) error = %v", err)
				}
				bodyFile = path
			}

			app := NewApp(strings.NewReader(tc.stdin), &bytes.Buffer{}, &bytes.Buffer{})
			err := app.Run(context.Background(), []string{
				"--state-dir", stateDir,
				"send",
				"--to", "workflow/reviewer/task-123",
				"--from", "agent/sender",
				"--subject", "review request",
				"--body-file", bodyFile,
			})
			if !errors.Is(err, ErrEmptyBody) {
				t.Fatalf("Run(empty body) error = %v, want ErrEmptyBody", err)
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
			if messageCount != 0 {
				t.Fatalf("message count = %d, want 0", messageCount)
			}

			entries, err := os.ReadDir(runtime.BlobDir())
			if err != nil {
				t.Fatalf("os.ReadDir(blob dir) error = %v", err)
			}
			if len(entries) != 0 {
				t.Fatalf("len(blob entries) = %d, want 0", len(entries))
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
			name: "send missing body file",
			args: []string{"send", "--to", "workflow/reviewer/task-123"},
		},
		{
			name: "send invalid flag",
			args: []string{"send", "--bogus"},
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
			name: "list conflicting formats",
			args: []string{"list", "--for", "workflow/reviewer/task-123", "--json", "--yaml"},
		},
		{
			name: "recv missing for",
			args: []string{"recv"},
		},
		{
			name: "recv empty for",
			args: []string{"recv", "--for", "   "},
		},
		{
			name: "recv zero max",
			args: []string{"recv", "--for", "workflow/reviewer/task-123", "--max", "0"},
		},
		{
			name: "recv negative max",
			args: []string{"recv", "--for", "workflow/reviewer/task-123", "--max", "-1"},
		},
		{
			name: "recv too large max",
			args: []string{"recv", "--for", "workflow/reviewer/task-123", "--max", "11"},
		},
		{
			name: "recv conflicting formats",
			args: []string{"recv", "--for", "workflow/reviewer/task-123", "--json", "--yaml"},
		},
		{
			name: "watch missing for",
			args: []string{"watch", "--json"},
		},
		{
			name: "watch empty for",
			args: []string{"watch", "--for", "   "},
		},
		{
			name: "watch negative timeout",
			args: []string{"watch", "--for", "workflow/reviewer/task-123", "--timeout", "-1s"},
		},
		{
			name: "watch conflicting formats",
			args: []string{"watch", "--for", "workflow/reviewer/task-123", "--json", "--yaml"},
		},
		{
			name: "wait missing for",
			args: []string{"wait", "--json"},
		},
		{
			name: "wait empty for",
			args: []string{"wait", "--for", "   "},
		},
		{
			name: "wait negative timeout",
			args: []string{"wait", "--for", "workflow/reviewer/task-123", "--timeout", "-1s"},
		},
		{
			name: "wait conflicting formats",
			args: []string{"wait", "--for", "workflow/reviewer/task-123", "--json", "--yaml"},
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

func TestHelpCLIPathsDoNotCreateRuntimeState(t *testing.T) {
	t.Parallel()

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
			name:         "send help",
			args:         []string{"send", "--help"},
			wantContains: "Usage:\n  agent-mailbox send --to ADDRESS --body-file PATH [options]",
		},
		{
			name:         "recv help",
			args:         []string{"recv", "--help"},
			wantContains: "Usage:\n  agent-mailbox recv --for ADDRESS [--for ADDRESS ...] [--max COUNT] [--json | --yaml]",
		},
		{
			name:         "watch help",
			args:         []string{"watch", "--help"},
			wantContains: "Usage:\n  agent-mailbox watch --for ADDRESS [--for ADDRESS ...] [--state STATE] [--timeout DURATION] [--json | --yaml]",
		},
		{
			name:         "wait help",
			args:         []string{"wait", "--help"},
			wantContains: "Usage:\n  agent-mailbox wait --for ADDRESS [--for ADDRESS ...] [--timeout DURATION] [--json | --yaml]",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			stateDir := filepath.Join(t.TempDir(), "mailbox-state")
			stdout := &bytes.Buffer{}
			stderr := &bytes.Buffer{}
			app := NewApp(strings.NewReader(""), stdout, stderr)

			err := app.Run(context.Background(), append([]string{"--state-dir", stateDir}, tc.args...))
			if !errors.Is(err, ErrHelpRequested) {
				t.Fatalf("Run() error = %v, want ErrHelpRequested", err)
			}
			if !strings.Contains(stdout.String(), tc.wantContains) {
				t.Fatalf("stdout = %q, want substring %q", stdout.String(), tc.wantContains)
			}
			if stderr.Len() != 0 {
				t.Fatalf("stderr = %q, want empty", stderr.String())
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

type recordingBlobTempFile struct {
	name string
	ops  *[]string
}

func (f *recordingBlobTempFile) Write(data []byte) (int, error) {
	*f.ops = append(*f.ops, "write:"+string(data))
	return len(data), nil
}

func (f *recordingBlobTempFile) Sync() error {
	*f.ops = append(*f.ops, "file-sync")
	return nil
}

func (f *recordingBlobTempFile) Close() error {
	*f.ops = append(*f.ops, "close")
	return nil
}

func (f *recordingBlobTempFile) Name() string {
	return f.name
}
