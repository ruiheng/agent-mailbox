//go:build linux && amd64

package waypost

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"
)

const (
	remainingStateBenchmarkAddressCount  = 100
	remainingStateBenchmarkDeliveryCount = 100000
	remainingStateBenchmarkPairs         = 30
)

type remainingStateBenchmarkFixture struct {
	baseDir   string
	workDir   string
	addresses []string
	metadata  string
}

// BenchmarkReceiveRemainingByState measures the receive-path count query with
// the reviewed 100-address, 100,000-delivery distribution. Run it once with:
//
// go test ./internal/waypost -run '^$' -bench BenchmarkReceiveRemainingByState -benchtime=1x
//
// Set WAYPOST_ENFORCE_REMAINING_COUNT_BENCHMARK=1 on the declared benchmark
// runner to make the p95 regression threshold blocking.
func BenchmarkReceiveRemainingByState(b *testing.B) {
	b.StopTimer()
	fixture := seedRemainingStateBenchmarkFixture(b)
	b.Logf("remaining-state benchmark fixture: %s", fixture.metadata)

	for iteration := 0; iteration < b.N; iteration++ {
		countFree := make([]time.Duration, 0, remainingStateBenchmarkPairs)
		countEnabled := make([]time.Duration, 0, remainingStateBenchmarkPairs)
		for pair := 0; pair < remainingStateBenchmarkPairs; pair++ {
			countFree = append(countFree, benchmarkReceiveSample(b, fixture, fmt.Sprintf("free-%02d", pair), false))
			countEnabled = append(countEnabled, benchmarkReceiveSample(b, fixture, fmt.Sprintf("count-%02d", pair), true))
		}

		freeP95 := durationP95(countFree)
		enabledP95 := durationP95(countEnabled)
		b.Logf("count-free samples: %s", formatDurationSamples(countFree))
		b.Logf("count-enabled samples: %s", formatDurationSamples(countEnabled))
		b.Logf("count-free p95=%s count-enabled p95=%s", freeP95, enabledP95)
		if os.Getenv("WAYPOST_ENFORCE_REMAINING_COUNT_BENCHMARK") == "1" &&
			enabledP95-freeP95 > 2*time.Millisecond &&
			enabledP95 > time.Duration(float64(freeP95)*1.25) {
			b.Fatalf("remaining-state count p95 regression: count-free=%s count-enabled=%s", freeP95, enabledP95)
		}
	}
}

func seedRemainingStateBenchmarkFixture(b *testing.B) remainingStateBenchmarkFixture {
	b.Helper()

	ctx := context.Background()
	rootDir := b.TempDir()
	baseDir := filepath.Join(rootDir, "base")
	workDir := filepath.Join(rootDir, "work")
	runtimeState, err := OpenRuntime(ctx, baseDir)
	if err != nil {
		b.Fatalf("OpenRuntime(benchmark fixture): %v", err)
	}

	store := runtimeState.Store()
	seed, err := store.Send(ctx, SendParams{
		ToAddress:     "benchmark/blob-seed",
		FromAddress:   "benchmark/sender",
		Subject:       "benchmark",
		ContentType:   "text/plain",
		SchemaVersion: "v1",
		Body:          []byte("benchmark body"),
	})
	if err != nil {
		_ = runtimeState.Close()
		b.Fatalf("seed benchmark blob: %v", err)
	}

	var bodyBlobRef, bodySHA256 string
	var bodySize int64
	if err := runtimeState.DB().QueryRowContext(ctx, `
SELECT body_blob_ref, body_size, body_sha256
FROM messages
WHERE message_id = ?
`, seed.MessageID).Scan(&bodyBlobRef, &bodySize, &bodySHA256); err != nil {
		_ = runtimeState.Close()
		b.Fatalf("read benchmark blob metadata: %v", err)
	}
	if _, err := runtimeState.DB().ExecContext(ctx, `
DELETE FROM events;
DELETE FROM deliveries;
DELETE FROM messages;
`); err != nil {
		_ = runtimeState.Close()
		b.Fatalf("clear benchmark seed records: %v", err)
	}

	addresses := make([]string, 0, remainingStateBenchmarkAddressCount)
	endpointIDs := make([]string, 0, remainingStateBenchmarkAddressCount)
	for index := 0; index < remainingStateBenchmarkAddressCount; index++ {
		address := fmt.Sprintf("benchmark/recipient-%03d", index)
		registration, err := store.RegisterEndpoint(ctx, address)
		if err != nil {
			_ = runtimeState.Close()
			b.Fatalf("register benchmark endpoint %q: %v", address, err)
		}
		addresses = append(addresses, address)
		endpointIDs = append(endpointIDs, registration.EndpointID)
	}

	now := time.Now().UTC().Truncate(time.Second)
	visibleAt := formatTimestamp(now.Add(-time.Second))
	futureVisibleAt := formatTimestamp(now.Add(time.Hour))
	leaseExpiresAt := formatTimestamp(now.Add(time.Hour))
	createdAt := formatTimestamp(now.Add(-time.Minute))
	tx, err := runtimeState.DB().BeginTx(ctx, nil)
	if err != nil {
		_ = runtimeState.Close()
		b.Fatalf("begin benchmark fixture transaction: %v", err)
	}
	messageStmt, err := tx.PrepareContext(ctx, `
INSERT INTO messages (
  message_id, created_at, sender_endpoint_id, subject, content_type,
  schema_version, idempotency_key, body_blob_ref, body_size, body_sha256,
  forwarded_message_id, forwarded_from_address, reply_to_message_id, metadata_json
) VALUES (?, ?, NULL, 'benchmark', 'text/plain', 'v1', NULL, ?, ?, ?, NULL, NULL, NULL, '{}')
`)
	if err != nil {
		_ = tx.Rollback()
		_ = runtimeState.Close()
		b.Fatalf("prepare benchmark message insert: %v", err)
	}
	defer messageStmt.Close()
	deliveryStmt, err := tx.PrepareContext(ctx, `
INSERT INTO deliveries (
  delivery_id, message_id, recipient_endpoint_id, state, visible_at,
  lease_token, lease_expires_at, acked_at, attempt_count, last_error_code, last_error_text
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0, NULL, NULL)
`)
	if err != nil {
		_ = tx.Rollback()
		_ = runtimeState.Close()
		b.Fatalf("prepare benchmark delivery insert: %v", err)
	}
	defer deliveryStmt.Close()

	for index := 0; index < remainingStateBenchmarkDeliveryCount; index++ {
		messageID := fmt.Sprintf("msg_benchmark_%06d", index)
		deliveryID := fmt.Sprintf("dlv_benchmark_%06d", index)
		if _, err := messageStmt.ExecContext(ctx, messageID, createdAt, bodyBlobRef, bodySize, bodySHA256); err != nil {
			_ = tx.Rollback()
			_ = runtimeState.Close()
			b.Fatalf("insert benchmark message %d: %v", index, err)
		}

		state, deliveryVisibleAt := "queued", visibleAt
		recipientIndex := 1 + index%(len(endpointIDs)-1)
		var leaseToken, expiresAt, ackedAt any
		switch {
		case index < 50000:
			if index < 50 {
				recipientIndex = 0
			}
		case index < 60000:
			deliveryVisibleAt = futureVisibleAt
			if index-50000 < 10 {
				recipientIndex = 0
			}
		case index < 80000:
			state = "leased"
			leaseToken = fmt.Sprintf("lease_benchmark_%06d", index)
			expiresAt = leaseExpiresAt
			if index-60000 < 20 {
				recipientIndex = 0
			}
		case index < 90000:
			state = "dead_letter"
			if index-80000 < 10 {
				recipientIndex = 0
			}
		default:
			state = "acked"
			ackedAt = visibleAt
			if index-90000 < 10 {
				recipientIndex = 0
			}
		}
		if _, err := deliveryStmt.ExecContext(ctx, deliveryID, messageID, endpointIDs[recipientIndex], state, deliveryVisibleAt, leaseToken, expiresAt, ackedAt); err != nil {
			_ = tx.Rollback()
			_ = runtimeState.Close()
			b.Fatalf("insert benchmark delivery %d: %v", index, err)
		}
	}
	if err := tx.Commit(); err != nil {
		_ = runtimeState.Close()
		b.Fatalf("commit benchmark fixture: %v", err)
	}

	metadata := remainingStateBenchmarkMetadata(b, runtimeState, baseDir)
	assertRemainingStateBenchmarkScope(b, runtimeState, store, addresses)
	if err := runtimeState.Close(); err != nil {
		b.Fatalf("close benchmark fixture: %v", err)
	}
	return remainingStateBenchmarkFixture{
		baseDir:   baseDir,
		workDir:   workDir,
		addresses: addresses,
		metadata:  metadata,
	}
}

func assertRemainingStateBenchmarkScope(b *testing.B, runtimeState *Runtime, store *Store, addresses []string) {
	b.Helper()

	if len(addresses) != remainingStateBenchmarkAddressCount {
		b.Fatalf("benchmark address scope = %d, want %d", len(addresses), remainingStateBenchmarkAddressCount)
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(addresses)), ",")
	args := make([]any, 0, len(addresses))
	for _, address := range addresses {
		args = append(args, address)
	}
	rows, err := runtimeState.DB().QueryContext(context.Background(), fmt.Sprintf(`
SELECT d.state, COUNT(*)
FROM deliveries AS d
JOIN endpoint_addresses AS ea ON ea.endpoint_id = d.recipient_endpoint_id
WHERE ea.address IN (%s)
GROUP BY d.state
`, placeholders), args...)
	if err != nil {
		b.Fatalf("count full benchmark fixture scope: %v", err)
	}
	defer rows.Close()
	allStates := make(map[string]int)
	for rows.Next() {
		var state string
		var count int
		if err := rows.Scan(&state, &count); err != nil {
			b.Fatalf("scan full benchmark fixture scope: %v", err)
		}
		allStates[state] = count
	}
	if err := rows.Err(); err != nil {
		b.Fatalf("iterate full benchmark fixture scope: %v", err)
	}
	wantAllStates := map[string]int{
		"queued":      60000,
		"leased":      20000,
		"dead_letter": 10000,
		"acked":       10000,
	}
	if len(allStates) != len(wantAllStates) {
		b.Fatalf("benchmark full states = %v, want %v", allStates, wantAllStates)
	}
	for state, count := range wantAllStates {
		if got := allStates[state]; got != count {
			b.Fatalf("benchmark full state[%s] = %d, want %d", state, got, count)
		}
	}
	remaining, err := store.RemainingByState(context.Background(), addresses, nil)
	if err != nil {
		b.Fatalf("count benchmark fixture scope: %v", err)
	}
	want := map[string]int{
		"queued":      60000,
		"leased":      20000,
		"dead_letter": 10000,
	}
	if len(remaining) != len(want) {
		b.Fatalf("benchmark remaining states = %v, want %v", remaining, want)
	}
	for state, count := range want {
		if got := remaining[state]; got != count {
			b.Fatalf("benchmark remaining_by_state[%s] = %d, want %d", state, got, count)
		}
	}
}

func benchmarkReceiveSample(b *testing.B, fixture remainingStateBenchmarkFixture, name string, includeCount bool) time.Duration {
	b.Helper()
	b.StopTimer()
	stateDir := filepath.Join(fixture.workDir, name)
	if err := copyBenchmarkFixture(fixture.baseDir, stateDir); err != nil {
		b.Fatalf("copy benchmark fixture %q: %v", name, err)
	}

	runtimeState, err := OpenRuntime(context.Background(), stateDir)
	if err != nil {
		b.Fatalf("open benchmark fixture %q: %v", name, err)
	}
	store := runtimeState.Store()
	// Each sample uses an independent copied database. Warm the same indexed
	// count scope outside the timer so page-fault cost from a fresh copy does
	// not masquerade as count-query regression in the paired comparison.
	if _, err := store.RemainingByState(context.Background(), fixture.addresses, nil); err != nil {
		_ = runtimeState.Close()
		b.Fatalf("warm benchmark fixture %q: %v", name, err)
	}
	b.StartTimer()
	started := time.Now()
	if includeCount {
		_, err = store.receiveBatchWithLeasePolicy(context.Background(), fixture.addresses, 1, legacyReceiveLeasePolicy)
	} else {
		_, err = store.receiveOnceWithLeasePolicy(context.Background(), fixture.addresses, legacyReceiveLeasePolicy)
	}
	elapsed := time.Since(started)
	b.StopTimer()
	if closeErr := runtimeState.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		b.Fatalf("benchmark receive %q: %v", name, err)
	}
	if err := os.RemoveAll(stateDir); err != nil {
		b.Fatalf("remove benchmark fixture %q: %v", name, err)
	}
	return elapsed
}

func copyBenchmarkFixture(sourceDir, targetDir string) error {
	entries, err := os.ReadDir(sourceDir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(targetDir, 0o700); err != nil {
		return err
	}
	for _, entry := range entries {
		sourcePath := filepath.Join(sourceDir, entry.Name())
		targetPath := filepath.Join(targetDir, entry.Name())
		if entry.IsDir() {
			if err := copyBenchmarkFixture(sourcePath, targetPath); err != nil {
				return err
			}
			continue
		}
		contents, err := os.ReadFile(sourcePath)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if err := os.WriteFile(targetPath, contents, info.Mode()); err != nil {
			return err
		}
	}
	return nil
}

func durationP95(samples []time.Duration) time.Duration {
	ordered := append([]time.Duration(nil), samples...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	index := (len(ordered)*95+99)/100 - 1
	return ordered[index]
}

func formatDurationSamples(samples []time.Duration) string {
	values := make([]string, 0, len(samples))
	for _, sample := range samples {
		values = append(values, sample.String())
	}
	return strings.Join(values, ",")
}

func remainingStateBenchmarkMetadata(b *testing.B, runtimeState *Runtime, stateDir string) string {
	b.Helper()
	var sqliteVersion, journalMode, synchronous, cacheSize string
	for _, probe := range []struct {
		query  string
		target *string
	}{
		{"SELECT sqlite_version()", &sqliteVersion},
		{"PRAGMA journal_mode", &journalMode},
		{"PRAGMA synchronous", &synchronous},
		{"PRAGMA cache_size", &cacheSize},
	} {
		if err := runtimeState.DB().QueryRowContext(context.Background(), probe.query).Scan(probe.target); err != nil {
			b.Fatalf("benchmark metadata query %q: %v", probe.query, err)
		}
	}
	return fmt.Sprintf(
		"sqlite=%s journal_mode=%s synchronous=%s cache_size=%s cpu=%d goarch=%s storage=%s",
		sqliteVersion,
		journalMode,
		synchronous,
		cacheSize,
		runtime.NumCPU(),
		runtime.GOARCH,
		remainingStateBenchmarkStorageClass(stateDir),
	)
}

func remainingStateBenchmarkStorageClass(path string) string {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return "unknown"
	}
	switch uint64(stat.Type) {
	case 0x01021994:
		return "tmpfs"
	case 0x794c7630:
		return "overlay"
	case 0xEF53:
		return "ext"
	default:
		return fmt.Sprintf("0x%x", uint64(stat.Type))
	}
}
