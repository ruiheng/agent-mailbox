package mailbox

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReceiveReclaimsExpiredLeaseAndRejectsStaleToken(t *testing.T) {
	t.Parallel()

	runtime, store := newLeaseTestStore(t)
	defer runtime.Close()

	current := time.Date(2026, 3, 18, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time {
		return current
	}

	sent := mustSendMessage(t, store, "workflow/reviewer/task-123", "agent/sender", "review request", "hello reviewer")

	first, err := store.Receive(context.Background(), ReceiveParams{Address: "workflow/reviewer/task-123"})
	if err != nil {
		t.Fatalf("Receive(first) error = %v", err)
	}
	if first.DeliveryID != sent.DeliveryID {
		t.Fatalf("Receive(first) delivery id = %q, want %q", first.DeliveryID, sent.DeliveryID)
	}
	if first.LeaseExpiresAt != formatTimestamp(current.Add(defaultLeaseTimeout)) {
		t.Fatalf("Receive(first) lease expiry = %q", first.LeaseExpiresAt)
	}

	current = current.Add(defaultLeaseTimeout + time.Second)

	second, err := store.Receive(context.Background(), ReceiveParams{Address: "workflow/reviewer/task-123"})
	if err != nil {
		t.Fatalf("Receive(second) error = %v", err)
	}
	if second.DeliveryID != first.DeliveryID {
		t.Fatalf("Receive(second) delivery id = %q, want %q", second.DeliveryID, first.DeliveryID)
	}
	if second.LeaseToken == first.LeaseToken {
		t.Fatal("Receive(second) reused the old lease token")
	}

	if _, err := store.Ack(context.Background(), first.DeliveryID, first.LeaseToken); !errors.Is(err, ErrLeaseTokenMismatch) {
		t.Fatalf("Ack(stale token) error = %v, want ErrLeaseTokenMismatch", err)
	}

	acked, err := store.Ack(context.Background(), second.DeliveryID, second.LeaseToken)
	if err != nil {
		t.Fatalf("Ack(current token) error = %v", err)
	}
	if acked.State != "acked" {
		t.Fatalf("Ack(current token) state = %q, want acked", acked.State)
	}

	eventTypes := readDeliveryEventTypes(t, runtime, sent.DeliveryID)
	want := []string{"delivery_queued", "delivery_leased", "delivery_leased", "delivery_acked"}
	assertStringSlicesEqual(t, eventTypes, want)
}

func TestReleaseDeferAndReceiveTimeout(t *testing.T) {
	t.Parallel()

	runtime, store := newLeaseTestStore(t)
	defer runtime.Close()

	current := time.Date(2026, 3, 18, 13, 0, 0, 0, time.UTC)
	store.now = func() time.Time {
		return current
	}

	mustSendMessage(t, store, "workflow/reviewer/task-123", "agent/sender", "review request", "hello reviewer")

	first, err := store.Receive(context.Background(), ReceiveParams{Address: "workflow/reviewer/task-123"})
	if err != nil {
		t.Fatalf("Receive(first) error = %v", err)
	}

	released, err := store.Release(context.Background(), first.DeliveryID, first.LeaseToken)
	if err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	if released.State != "queued" {
		t.Fatalf("Release() state = %q, want queued", released.State)
	}

	second, err := store.Receive(context.Background(), ReceiveParams{Address: "workflow/reviewer/task-123"})
	if err != nil {
		t.Fatalf("Receive(second) error = %v", err)
	}

	until := current.Add(10 * time.Minute)
	deferred, err := store.Defer(context.Background(), second.DeliveryID, second.LeaseToken, until)
	if err != nil {
		t.Fatalf("Defer() error = %v", err)
	}
	if deferred.VisibleAt != formatTimestamp(until) {
		t.Fatalf("Defer() visible_at = %q, want %q", deferred.VisibleAt, formatTimestamp(until))
	}

	visible, err := store.List(context.Background(), ListParams{Address: "workflow/reviewer/task-123"})
	if err != nil {
		t.Fatalf("List(visible queued) error = %v", err)
	}
	if len(visible) != 0 {
		t.Fatalf("len(visible queued) = %d, want 0", len(visible))
	}

	queued, err := store.List(context.Background(), ListParams{Address: "workflow/reviewer/task-123", State: "queued"})
	if err != nil {
		t.Fatalf("List(all queued) error = %v", err)
	}
	if len(queued) != 1 {
		t.Fatalf("len(all queued) = %d, want 1", len(queued))
	}
	if queued[0].VisibleAt != formatTimestamp(until) {
		t.Fatalf("queued visible_at = %q, want %q", queued[0].VisibleAt, formatTimestamp(until))
	}

	if _, err := store.Receive(context.Background(), ReceiveParams{
		Address: "workflow/empty",
	}); !errors.Is(err, ErrNoMessage) {
		t.Fatalf("Receive(empty) error = %v, want ErrNoMessage", err)
	}
}

func TestReceiveMultipleAddressesOrdersUnionOldestFirst(t *testing.T) {
	t.Parallel()

	runtime, store := newLeaseTestStore(t)
	defer runtime.Close()

	current := time.Date(2026, 3, 18, 13, 30, 0, 0, time.UTC)
	store.now = func() time.Time {
		return current
	}

	older := mustSendMessage(t, store, "workflow/older", "agent/sender", "older", "older body")
	current = current.Add(time.Second)
	newer := mustSendMessage(t, store, "workflow/newer", "agent/sender", "newer", "newer body")

	first, err := store.Receive(context.Background(), ReceiveParams{
		Addresses: []string{"workflow/newer", "workflow/older", "workflow/newer"},
	})
	if err != nil {
		t.Fatalf("Receive(first multi) error = %v", err)
	}
	if first.DeliveryID != older.DeliveryID {
		t.Fatalf("Receive(first multi) delivery id = %q, want %q", first.DeliveryID, older.DeliveryID)
	}
	if first.RecipientAddress != "workflow/older" {
		t.Fatalf("Receive(first multi) recipient address = %q, want workflow/older", first.RecipientAddress)
	}

	if _, err := store.Ack(context.Background(), first.DeliveryID, first.LeaseToken); err != nil {
		t.Fatalf("Ack(first multi) error = %v", err)
	}

	second, err := store.Receive(context.Background(), ReceiveParams{
		Addresses: []string{"workflow/newer", "workflow/older", "workflow/newer"},
	})
	if err != nil {
		t.Fatalf("Receive(second multi) error = %v", err)
	}
	if second.DeliveryID != newer.DeliveryID {
		t.Fatalf("Receive(second multi) delivery id = %q, want %q", second.DeliveryID, newer.DeliveryID)
	}
	if second.RecipientAddress != "workflow/newer" {
		t.Fatalf("Receive(second multi) recipient address = %q, want workflow/newer", second.RecipientAddress)
	}
}

func TestReceiveMultipleAddressesUsesDeliveryIDTiebreak(t *testing.T) {
	t.Parallel()

	runtime, store := newLeaseTestStore(t)
	defer runtime.Close()

	current := time.Date(2026, 3, 18, 13, 45, 0, 0, time.UTC)
	store.now = func() time.Time {
		return current
	}

	alpha := mustSendMessage(t, store, "workflow/alpha", "agent/sender", "alpha", "alpha body")
	beta := mustSendMessage(t, store, "workflow/beta", "agent/sender", "beta", "beta body")

	wantDeliveryID := alpha.DeliveryID
	wantAddress := "workflow/alpha"
	if beta.DeliveryID < wantDeliveryID {
		wantDeliveryID = beta.DeliveryID
		wantAddress = "workflow/beta"
	}

	message, err := store.Receive(context.Background(), ReceiveParams{
		Addresses: []string{"workflow/beta", "workflow/alpha"},
	})
	if err != nil {
		t.Fatalf("Receive(tiebreak multi) error = %v", err)
	}
	if message.DeliveryID != wantDeliveryID {
		t.Fatalf("Receive(tiebreak multi) delivery id = %q, want %q", message.DeliveryID, wantDeliveryID)
	}
	if message.RecipientAddress != wantAddress {
		t.Fatalf("Receive(tiebreak multi) recipient address = %q, want %q", message.RecipientAddress, wantAddress)
	}
}

func TestReceiveMultipleAddressesReclaimsExpiredLeaseAcrossUnion(t *testing.T) {
	t.Parallel()

	runtime, store := newLeaseTestStore(t)
	defer runtime.Close()

	current := time.Date(2026, 3, 18, 14, 30, 0, 0, time.UTC)
	store.now = func() time.Time {
		return current
	}

	reclaimed := mustSendMessage(t, store, "workflow/reclaim", "agent/sender", "reclaim me", "reclaim body")
	first, err := store.Receive(context.Background(), ReceiveParams{Address: "workflow/reclaim"})
	if err != nil {
		t.Fatalf("Receive(initial reclaim) error = %v", err)
	}

	current = current.Add(defaultLeaseTimeout + time.Second)
	fresh := mustSendMessage(t, store, "workflow/fresh", "agent/sender", "fresh", "fresh body")

	second, err := store.Receive(context.Background(), ReceiveParams{
		Addresses: []string{"workflow/fresh", "workflow/reclaim"},
	})
	if err != nil {
		t.Fatalf("Receive(reclaim multi) error = %v", err)
	}
	if second.DeliveryID != reclaimed.DeliveryID {
		t.Fatalf("Receive(reclaim multi) delivery id = %q, want %q", second.DeliveryID, reclaimed.DeliveryID)
	}
	if second.RecipientAddress != "workflow/reclaim" {
		t.Fatalf("Receive(reclaim multi) recipient address = %q, want workflow/reclaim", second.RecipientAddress)
	}
	if second.LeaseToken == first.LeaseToken {
		t.Fatal("Receive(reclaim multi) reused the expired lease token")
	}

	if _, err := store.Ack(context.Background(), second.DeliveryID, second.LeaseToken); err != nil {
		t.Fatalf("Ack(reclaim multi current token) error = %v", err)
	}

	third, err := store.Receive(context.Background(), ReceiveParams{
		Addresses: []string{"workflow/fresh", "workflow/reclaim"},
	})
	if err != nil {
		t.Fatalf("Receive(fresh after reclaim) error = %v", err)
	}
	if third.DeliveryID != fresh.DeliveryID {
		t.Fatalf("Receive(fresh after reclaim) delivery id = %q, want %q", third.DeliveryID, fresh.DeliveryID)
	}
}

func TestFailRetryPolicyDeadLettersAfterThirdFailure(t *testing.T) {
	t.Parallel()

	runtime, store := newLeaseTestStore(t)
	defer runtime.Close()

	current := time.Date(2026, 3, 18, 14, 0, 0, 0, time.UTC)
	store.now = func() time.Time {
		return current
	}

	sent := mustSendMessage(t, store, "workflow/reviewer/task-123", "agent/sender", "review request", "hello reviewer")

	for attempt := 1; attempt <= 3; attempt++ {
		message, err := store.Receive(context.Background(), ReceiveParams{Address: "workflow/reviewer/task-123"})
		if err != nil {
			t.Fatalf("Receive(attempt %d) error = %v", attempt, err)
		}

		failed, err := store.Fail(context.Background(), message.DeliveryID, message.LeaseToken, "tool crashed")
		if err != nil {
			t.Fatalf("Fail(attempt %d) error = %v", attempt, err)
		}

		wantState := "queued"
		if attempt == 3 {
			wantState = "dead_letter"
		}
		if failed.State != wantState {
			t.Fatalf("Fail(attempt %d) state = %q, want %q", attempt, failed.State, wantState)
		}
		if failed.AttemptCount != attempt {
			t.Fatalf("Fail(attempt %d) attempt_count = %d, want %d", attempt, failed.AttemptCount, attempt)
		}

		current = current.Add(time.Second)
	}

	deadLetters, err := store.List(context.Background(), ListParams{Address: "workflow/reviewer/task-123", State: "dead_letter"})
	if err != nil {
		t.Fatalf("List(dead_letter) error = %v", err)
	}
	if len(deadLetters) != 1 {
		t.Fatalf("len(dead_letter) = %d, want 1", len(deadLetters))
	}
	if deadLetters[0].DeliveryID != sent.DeliveryID {
		t.Fatalf("dead-letter delivery id = %q, want %q", deadLetters[0].DeliveryID, sent.DeliveryID)
	}

	eventTypes := readDeliveryEventTypes(t, runtime, sent.DeliveryID)
	want := []string{
		"delivery_queued",
		"delivery_leased",
		"delivery_failed",
		"delivery_leased",
		"delivery_failed",
		"delivery_leased",
		"delivery_failed",
		"delivery_dead_letter",
	}
	assertStringSlicesEqual(t, eventTypes, want)
}

func newLeaseTestStore(t *testing.T) (*Runtime, *Store) {
	t.Helper()

	runtime, err := OpenRuntime(context.Background(), filepath.Join(t.TempDir(), "mailbox-state"))
	if err != nil {
		t.Fatalf("OpenRuntime() error = %v", err)
	}
	return runtime, runtime.Store()
}

func mustSendMessage(t *testing.T, store *Store, toAddress, fromAddress, subject, body string) SendResult {
	t.Helper()

	result, err := store.Send(context.Background(), SendParams{
		ToAddress:     toAddress,
		FromAddress:   fromAddress,
		Subject:       subject,
		ContentType:   "text/plain",
		SchemaVersion: "v1",
		Body:          []byte(body),
	})
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	return result
}

func TestReceiveRejectsCorruptBodyBlob(t *testing.T) {
	t.Parallel()

	runtime, store := newLeaseTestStore(t)
	defer runtime.Close()

	sent := mustSendMessage(t, store, "workflow/corrupt", "agent/sender", "corrupt", "expected body")
	blobPath := filepath.Join(runtime.BlobDir(), sent.BodyBlobRef)
	if err := os.WriteFile(blobPath, []byte{}, 0o600); err != nil {
		t.Fatalf("os.WriteFile(corrupt blob) error = %v", err)
	}

	_, err := store.Receive(context.Background(), ReceiveParams{Address: "workflow/corrupt"})
	if !errors.Is(err, ErrBodyIntegrity) {
		t.Fatalf("Receive(corrupt blob) error = %v, want ErrBodyIntegrity", err)
	}
	if !strings.Contains(err.Error(), sent.BodyBlobRef) {
		t.Fatalf("Receive(corrupt blob) error = %q, want blob ref %q", err, sent.BodyBlobRef)
	}
}

func TestReceiveConcurrentClaimUsesSingleClaimerAcrossRuntimes(t *testing.T) {
	t.Parallel()

	stateDir := filepath.Join(t.TempDir(), "mailbox-state")
	firstRuntime, err := OpenRuntime(context.Background(), stateDir)
	if err != nil {
		t.Fatalf("OpenRuntime(first) error = %v", err)
	}
	defer firstRuntime.Close()

	secondRuntime, err := OpenRuntime(context.Background(), stateDir)
	if err != nil {
		t.Fatalf("OpenRuntime(second) error = %v", err)
	}
	defer secondRuntime.Close()

	const address = "workflow/single-claimer"
	sent := mustSendMessage(t, firstRuntime.Store(), address, "agent/sender", "race", "race body")

	type receiveResult struct {
		message ReceivedMessage
		err     error
	}

	start := make(chan struct{})
	results := make(chan receiveResult, 2)
	receive := func(store *Store) {
		<-start
		message, err := store.Receive(context.Background(), ReceiveParams{Address: address})
		results <- receiveResult{message: message, err: err}
	}

	go receive(firstRuntime.Store())
	go receive(secondRuntime.Store())
	close(start)

	var got []receiveResult
	for i := 0; i < 2; i++ {
		select {
		case result := <-results:
			got = append(got, result)
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for concurrent receive results")
		}
	}

	successCount := 0
	noMessageCount := 0
	for i, result := range got {
		switch {
		case result.err == nil:
			successCount++
			if result.message.DeliveryID != sent.DeliveryID {
				t.Fatalf("receive[%d] delivery id = %q, want %q", i, result.message.DeliveryID, sent.DeliveryID)
			}
		case errors.Is(result.err, ErrNoMessage):
			noMessageCount++
		default:
			t.Fatalf("receive[%d] error = %v, want nil or ErrNoMessage", i, result.err)
		}
	}
	if successCount != 1 || noMessageCount != 1 {
		t.Fatalf("concurrent receive results = %+v, want one success and one ErrNoMessage", got)
	}
}

func TestListUnseenAddressReturnsEmpty(t *testing.T) {
	t.Parallel()

	_, store := newLeaseTestStore(t)

	deliveries, err := store.List(context.Background(), ListParams{Address: "workflow/unseen"})
	if err != nil {
		t.Fatalf("List(unseen) error = %v", err)
	}
	if len(deliveries) != 0 {
		t.Fatalf("len(List unseen) = %d, want 0", len(deliveries))
	}
	if deliveries == nil {
		t.Fatal("List(unseen) returned nil slice, want empty slice")
	}
}

func readDeliveryEventTypes(t *testing.T, runtime *Runtime, deliveryID string) []string {
	t.Helper()

	rows, err := runtime.DB().Query(`
SELECT event_type
FROM events
WHERE delivery_id = ?
ORDER BY rowid
`, deliveryID)
	if err != nil {
		t.Fatalf("Query(events) error = %v", err)
	}
	defer rows.Close()

	var eventTypes []string
	for rows.Next() {
		var eventType string
		if err := rows.Scan(&eventType); err != nil {
			t.Fatalf("Scan(event_type) error = %v", err)
		}
		eventTypes = append(eventTypes, eventType)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err() = %v", err)
	}
	return eventTypes
}

func assertStringSlicesEqual(t *testing.T, got, want []string) {
	t.Helper()

	if len(got) != len(want) {
		t.Fatalf("len(got) = %d, want %d (got=%v want=%v)", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got[%d] = %q, want %q (got=%v want=%v)", i, got[i], want[i], got, want)
		}
	}
}
