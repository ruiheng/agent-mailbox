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
	if acked.AckedAt == "" {
		t.Fatal("Ack(current token) acked_at = empty, want timestamp")
	}

	ackedDeliveries, err := store.List(context.Background(), ListParams{Address: "workflow/reviewer/task-123", State: "acked"})
	if err != nil {
		t.Fatalf("List(acked) error = %v", err)
	}
	if len(ackedDeliveries) != 1 {
		t.Fatalf("len(acked deliveries) = %d, want 1", len(ackedDeliveries))
	}
	if ackedDeliveries[0].State != "acked" {
		t.Fatalf("acked delivery state = %q, want acked", ackedDeliveries[0].State)
	}
	if ackedDeliveries[0].AckedAt == nil {
		t.Fatal("acked delivery acked_at = nil, want timestamp")
	}
	if *ackedDeliveries[0].AckedAt != acked.AckedAt {
		t.Fatalf("acked delivery acked_at = %q, want %q", *ackedDeliveries[0].AckedAt, acked.AckedAt)
	}

	eventTypes := readDeliveryEventTypes(t, runtime, sent.DeliveryID)
	want := []string{"delivery_queued", "delivery_leased", "delivery_leased", "delivery_acked"}
	assertStringSlicesEqual(t, eventTypes, want)
}

func TestReadDeliveryReturnsBodyForAckedDelivery(t *testing.T) {
	t.Parallel()

	runtime, store := newLeaseTestStore(t)
	defer runtime.Close()

	sent := mustSendMessage(t, store, "workflow/reviewer/task-123", "agent/sender", "review request", "hello reviewer")

	message, err := store.Receive(context.Background(), ReceiveParams{Address: "workflow/reviewer/task-123"})
	if err != nil {
		t.Fatalf("Receive() error = %v", err)
	}
	acked, err := store.Ack(context.Background(), message.DeliveryID, message.LeaseToken)
	if err != nil {
		t.Fatalf("Ack() error = %v", err)
	}

	read, err := store.ReadDelivery(context.Background(), sent.DeliveryID)
	if err != nil {
		t.Fatalf("ReadDelivery() error = %v", err)
	}
	if read.DeliveryID != sent.DeliveryID {
		t.Fatalf("ReadDelivery() delivery_id = %q, want %q", read.DeliveryID, sent.DeliveryID)
	}
	if read.State != "acked" {
		t.Fatalf("ReadDelivery() state = %q, want acked", read.State)
	}
	if read.Body != "hello reviewer" {
		t.Fatalf("ReadDelivery() body = %q, want hello reviewer", read.Body)
	}
	if read.AckedAt == nil {
		t.Fatal("ReadDelivery() acked_at = nil, want timestamp")
	}
	if *read.AckedAt != acked.AckedAt {
		t.Fatalf("ReadDelivery() acked_at = %q, want %q", *read.AckedAt, acked.AckedAt)
	}
}

func TestReadMessageReturnsBodyByMessageID(t *testing.T) {
	t.Parallel()

	runtime, store := newLeaseTestStore(t)
	defer runtime.Close()

	sent := mustSendMessage(t, store, "workflow/reviewer/task-123", "agent/sender", "review request", "hello reviewer")

	read, err := store.ReadMessage(context.Background(), sent.MessageID)
	if err != nil {
		t.Fatalf("ReadMessage() error = %v", err)
	}
	if read.MessageID != sent.MessageID {
		t.Fatalf("ReadMessage() message_id = %q, want %q", read.MessageID, sent.MessageID)
	}
	if read.Body != "hello reviewer" {
		t.Fatalf("ReadMessage() body = %q, want hello reviewer", read.Body)
	}
	if read.Subject != "review request" {
		t.Fatalf("ReadMessage() subject = %q, want review request", read.Subject)
	}
}

func TestReadLatestDeliveriesReturnsMostRecentAckedForAddress(t *testing.T) {
	t.Parallel()

	runtime, store := newLeaseTestStore(t)
	defer runtime.Close()

	current := time.Date(2026, 3, 18, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time {
		return current
	}

	mustSendMessage(t, store, "workflow/reviewer/task-123", "agent/sender", "first", "first body")
	firstReceived, err := store.Receive(context.Background(), ReceiveParams{Address: "workflow/reviewer/task-123"})
	if err != nil {
		t.Fatalf("Receive(first) error = %v", err)
	}
	if _, err := store.Ack(context.Background(), firstReceived.DeliveryID, firstReceived.LeaseToken); err != nil {
		t.Fatalf("Ack(first) error = %v", err)
	}

	current = current.Add(time.Minute)

	second := mustSendMessage(t, store, "workflow/reviewer/task-123", "agent/sender", "second", "second body")
	secondReceived, err := store.Receive(context.Background(), ReceiveParams{Address: "workflow/reviewer/task-123"})
	if err != nil {
		t.Fatalf("Receive(second) error = %v", err)
	}
	if _, err := store.Ack(context.Background(), secondReceived.DeliveryID, secondReceived.LeaseToken); err != nil {
		t.Fatalf("Ack(second) error = %v", err)
	}

	read, hasMore, err := store.ReadLatestDeliveries(context.Background(), []string{"workflow/reviewer/task-123"}, "acked", 1)
	if err != nil {
		t.Fatalf("ReadLatestDeliveries(acked) error = %v", err)
	}
	if hasMore != true {
		t.Fatalf("ReadLatestDeliveries(acked) has_more = %t, want true", hasMore)
	}
	if len(read) != 1 {
		t.Fatalf("len(ReadLatestDeliveries(acked)) = %d, want 1", len(read))
	}
	if read[0].DeliveryID != second.DeliveryID {
		t.Fatalf("ReadLatestDeliveries(acked) delivery_id = %q, want %q", read[0].DeliveryID, second.DeliveryID)
	}
	if read[0].Body != "second body" {
		t.Fatalf("ReadLatestDeliveries(acked) body = %q, want second body", read[0].Body)
	}

	current = current.Add(time.Minute)
	queued := mustSendMessage(t, store, "workflow/reviewer/task-123", "agent/sender", "queued", "queued body")

	readQueued, queuedHasMore, err := store.ReadLatestDeliveries(context.Background(), []string{"workflow/reviewer/task-123"}, "queued", 1)
	if err != nil {
		t.Fatalf("ReadLatestDeliveries(queued) error = %v", err)
	}
	if queuedHasMore {
		t.Fatalf("ReadLatestDeliveries(queued) has_more = true, want false")
	}
	if len(readQueued) != 1 {
		t.Fatalf("len(ReadLatestDeliveries(queued)) = %d, want 1", len(readQueued))
	}
	if readQueued[0].DeliveryID != queued.DeliveryID {
		t.Fatalf("ReadLatestDeliveries(queued) delivery_id = %q, want %q", readQueued[0].DeliveryID, queued.DeliveryID)
	}
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

func TestReceiveBatchClaimsUpToMaxAndReportsHasMore(t *testing.T) {
	t.Parallel()

	runtime, store := newLeaseTestStore(t)
	defer runtime.Close()

	current := time.Date(2026, 3, 18, 13, 35, 0, 0, time.UTC)
	store.now = func() time.Time {
		return current
	}

	firstSent := mustSendMessage(t, store, "workflow/batch", "agent/sender", "first", "first body")
	current = current.Add(time.Second)
	secondSent := mustSendMessage(t, store, "workflow/batch", "agent/sender", "second", "second body")
	current = current.Add(time.Second)
	thirdSent := mustSendMessage(t, store, "workflow/batch", "agent/sender", "third", "third body")

	result, err := store.ReceiveBatch(context.Background(), ReceiveBatchParams{
		Address: "workflow/batch",
		Max:     2,
	})
	if err != nil {
		t.Fatalf("ReceiveBatch(first) error = %v", err)
	}
	if len(result.Messages) != 2 {
		t.Fatalf("len(ReceiveBatch(first).Messages) = %d, want 2", len(result.Messages))
	}
	if !result.HasMore {
		t.Fatal("ReceiveBatch(first).HasMore = false, want true")
	}
	if result.Messages[0].DeliveryID != firstSent.DeliveryID {
		t.Fatalf("ReceiveBatch(first).Messages[0].delivery_id = %q, want %q", result.Messages[0].DeliveryID, firstSent.DeliveryID)
	}
	if result.Messages[1].DeliveryID != secondSent.DeliveryID {
		t.Fatalf("ReceiveBatch(first).Messages[1].delivery_id = %q, want %q", result.Messages[1].DeliveryID, secondSent.DeliveryID)
	}

	last, err := store.ReceiveBatch(context.Background(), ReceiveBatchParams{
		Address: "workflow/batch",
		Max:     2,
	})
	if err != nil {
		t.Fatalf("ReceiveBatch(second) error = %v", err)
	}
	if len(last.Messages) != 1 {
		t.Fatalf("len(ReceiveBatch(second).Messages) = %d, want 1", len(last.Messages))
	}
	if last.HasMore {
		t.Fatal("ReceiveBatch(second).HasMore = true, want false")
	}
	if last.Messages[0].DeliveryID != thirdSent.DeliveryID {
		t.Fatalf("ReceiveBatch(second).Messages[0].delivery_id = %q, want %q", last.Messages[0].DeliveryID, thirdSent.DeliveryID)
	}
}

func TestReceiveBatchReturnsRecoveryFailureAndReleasesEarlierClaims(t *testing.T) {
	t.Parallel()

	runtime, store := newLeaseTestStore(t)
	defer runtime.Close()

	current := time.Date(2026, 3, 18, 13, 40, 0, 0, time.UTC)
	store.now = func() time.Time { return current }

	firstSent := mustSendMessage(t, store, "workflow/batch-recovery", "agent/sender", "first", "first body")
	current = current.Add(time.Second)
	secondSent := mustSendMessage(t, store, "workflow/batch-recovery", "agent/sender", "second", "second body")

	blobPath := filepath.Join(runtime.BlobDir(), secondSent.BodyBlobRef)
	if err := os.WriteFile(blobPath, []byte{}, 0o600); err != nil {
		t.Fatalf("os.WriteFile(corrupt blob) error = %v", err)
	}

	nowCalls := 0
	store.now = func() time.Time {
		nowCalls++
		if nowCalls == 5 {
			return current.Add(defaultLeaseTimeout + time.Second)
		}
		return current
	}

	_, err := store.ReceiveBatch(context.Background(), ReceiveBatchParams{
		Address: "workflow/batch-recovery",
		Max:     2,
	})
	if !errors.Is(err, ErrReceiveRecovery) {
		t.Fatalf("ReceiveBatch(recovery failure) error = %v, want ErrReceiveRecovery", err)
	}
	if !errors.Is(err, ErrBodyIntegrity) {
		t.Fatalf("ReceiveBatch(recovery failure) error = %v, want ErrBodyIntegrity", err)
	}

	firstState, firstAttempts := readDeliveryStateAndAttemptCount(t, runtime, firstSent.DeliveryID)
	if firstState != "queued" {
		t.Fatalf("first delivery state after batch recovery failure = %q, want queued", firstState)
	}
	if firstAttempts != 0 {
		t.Fatalf("first delivery attempt_count after batch recovery failure = %d, want 0", firstAttempts)
	}
	firstVisibleAt := readDeliveryVisibleAt(t, runtime, firstSent.DeliveryID)
	if firstVisibleAt != firstSent.VisibleAtUTC {
		t.Fatalf("first delivery visible_at after batch recovery failure = %q, want %q", firstVisibleAt, firstSent.VisibleAtUTC)
	}

	secondState, secondAttempts := readDeliveryStateAndAttemptCount(t, runtime, secondSent.DeliveryID)
	if secondState != "leased" {
		t.Fatalf("second delivery state after batch recovery failure = %q, want leased", secondState)
	}
	if secondAttempts != 0 {
		t.Fatalf("second delivery attempt_count after batch recovery failure = %d, want 0", secondAttempts)
	}

	assertStringSlicesEqual(t, readDeliveryEventTypes(t, runtime, firstSent.DeliveryID), []string{
		"delivery_queued",
		"delivery_leased",
		"delivery_released",
	})
	assertStringSlicesEqual(t, readDeliveryEventTypes(t, runtime, secondSent.DeliveryID), []string{
		"delivery_queued",
		"delivery_leased",
	})
}

func TestReceiveBatchRejectsTooLargeMax(t *testing.T) {
	t.Parallel()

	_, store := newLeaseTestStore(t)

	_, err := store.ReceiveBatch(context.Background(), ReceiveBatchParams{
		Address: "workflow/batch",
		Max:     maxReceiveBatchSize + 1,
	})
	if err == nil {
		t.Fatal("ReceiveBatch(too large max) error = nil, want non-nil")
	}
	if !strings.Contains(err.Error(), "--max must be between 1 and 10") {
		t.Fatalf("ReceiveBatch(too large max) error = %q, want max range message", err)
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

	current := time.Date(2026, 3, 18, 15, 0, 0, 0, time.UTC)
	store.now = func() time.Time {
		return current
	}

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

	state, attemptCount := readDeliveryStateAndAttemptCount(t, runtime, sent.DeliveryID)
	if state != "queued" {
		t.Fatalf("delivery state after corrupt receive = %q, want queued", state)
	}
	if attemptCount != 1 {
		t.Fatalf("delivery attempt_count after corrupt receive = %d, want 1", attemptCount)
	}

	eventTypes := readDeliveryEventTypes(t, runtime, sent.DeliveryID)
	want := []string{"delivery_queued", "delivery_leased", "delivery_failed"}
	assertStringSlicesEqual(t, eventTypes, want)
}

func TestReceiveCorruptBodyReportsRecoveryFailureWhenFailTransitionFails(t *testing.T) {
	t.Parallel()

	runtime, store := newLeaseTestStore(t)
	defer runtime.Close()

	current := time.Date(2026, 3, 18, 15, 30, 0, 0, time.UTC)
	store.now = func() time.Time { return current }

	sent := mustSendMessage(t, store, "workflow/corrupt-recovery", "agent/sender", "corrupt", "expected body")
	blobPath := filepath.Join(runtime.BlobDir(), sent.BodyBlobRef)
	if err := os.WriteFile(blobPath, []byte{}, 0o600); err != nil {
		t.Fatalf("os.WriteFile(corrupt blob) error = %v", err)
	}

	nowCalls := 0
	store.now = func() time.Time {
		nowCalls++
		if nowCalls >= 3 {
			return current.Add(defaultLeaseTimeout + time.Second)
		}
		return current
	}

	_, err := store.Receive(context.Background(), ReceiveParams{Address: "workflow/corrupt-recovery"})
	if !errors.Is(err, ErrReceiveRecovery) {
		t.Fatalf("Receive(corrupt blob recovery failure) error = %v, want ErrReceiveRecovery", err)
	}
	if !errors.Is(err, ErrBodyIntegrity) {
		t.Fatalf("Receive(corrupt blob recovery failure) error = %v, want ErrBodyIntegrity", err)
	}

	var recoveryErr *ReceiveRecoveryError
	if !errors.As(err, &recoveryErr) {
		t.Fatalf("Receive(corrupt blob recovery failure) error = %T, want *ReceiveRecoveryError", err)
	}
	if recoveryErr.DeliveryID != sent.DeliveryID {
		t.Fatalf("Receive(corrupt blob recovery failure) delivery id = %q, want %q", recoveryErr.DeliveryID, sent.DeliveryID)
	}
	if !strings.Contains(err.Error(), sent.DeliveryID) {
		t.Fatalf("Receive(corrupt blob recovery failure) error = %q, want delivery id %q", err, sent.DeliveryID)
	}

	state, attemptCount := readDeliveryStateAndAttemptCount(t, runtime, sent.DeliveryID)
	if state != "leased" {
		t.Fatalf("delivery state after recovery failure = %q, want leased", state)
	}
	if attemptCount != 0 {
		t.Fatalf("delivery attempt_count after recovery failure = %d, want 0", attemptCount)
	}

	eventTypes := readDeliveryEventTypes(t, runtime, sent.DeliveryID)
	want := []string{"delivery_queued", "delivery_leased"}
	assertStringSlicesEqual(t, eventTypes, want)
}

func TestReceiveRetriesClaimContentionUntilLockClears(t *testing.T) {
	t.Parallel()

	stateDir := filepath.Join(t.TempDir(), "mailbox-state")
	lockerRuntime, err := OpenRuntime(context.Background(), stateDir)
	if err != nil {
		t.Fatalf("OpenRuntime(locker) error = %v", err)
	}
	defer lockerRuntime.Close()

	receiverRuntime, err := OpenRuntime(context.Background(), stateDir)
	if err != nil {
		t.Fatalf("OpenRuntime(receiver) error = %v", err)
	}
	defer receiverRuntime.Close()

	const address = "workflow/claim-retry"
	sent := mustSendMessage(t, lockerRuntime.Store(), address, "agent/sender", "retry", "retry body")

	lockTx, err := lockerRuntime.DB().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("BeginTx(writer lock) error = %v", err)
	}

	releaseDone := make(chan error, 1)
	go func() {
		time.Sleep(2 * claimRetryDelay)
		releaseDone <- lockTx.Rollback()
	}()

	message, err := receiverRuntime.Store().Receive(context.Background(), ReceiveParams{Address: address})
	if err != nil {
		t.Fatalf("Receive(retry after contention) error = %v", err)
	}
	if rollbackErr := <-releaseDone; rollbackErr != nil {
		t.Fatalf("Rollback(writer lock) error = %v", rollbackErr)
	}
	if message.DeliveryID != sent.DeliveryID {
		t.Fatalf("Receive(retry after contention) delivery id = %q, want %q", message.DeliveryID, sent.DeliveryID)
	}
	if message.RecipientAddress != address {
		t.Fatalf("Receive(retry after contention) recipient address = %q, want %q", message.RecipientAddress, address)
	}
}

func TestReceiveBatchReturnsClaimContentionWhenLockRetriesExhausted(t *testing.T) {
	t.Parallel()

	stateDir := filepath.Join(t.TempDir(), "mailbox-state")
	lockerRuntime, err := OpenRuntime(context.Background(), stateDir)
	if err != nil {
		t.Fatalf("OpenRuntime(locker) error = %v", err)
	}
	defer lockerRuntime.Close()

	receiverRuntime, err := OpenRuntime(context.Background(), stateDir)
	if err != nil {
		t.Fatalf("OpenRuntime(receiver) error = %v", err)
	}
	defer receiverRuntime.Close()

	const address = "workflow/claim-contention"
	sendTime := time.Date(2026, 3, 18, 16, 0, 0, 0, time.UTC)
	lockerRuntime.Store().now = func() time.Time { return sendTime }
	firstSent := mustSendMessage(t, lockerRuntime.Store(), address, "agent/sender", "first", "first body")
	sendTime = sendTime.Add(time.Second)
	secondSent := mustSendMessage(t, lockerRuntime.Store(), address, "agent/sender", "second", "second body")

	receiverStore := receiverRuntime.Store()
	receiveTime := sendTime.Add(time.Second)
	nowCalls := 0
	var lockBeginErr error
	releaseDone := make(chan error, 1)
	receiverStore.now = func() time.Time {
		nowCalls++
		if nowCalls == 3 {
			lockTx, err := lockerRuntime.DB().BeginTx(context.Background(), nil)
			if err != nil {
				lockBeginErr = err
				return receiveTime
			}
			go func() {
				time.Sleep(time.Duration(maxClaimRetryCount+2) * claimRetryDelay)
				releaseDone <- lockTx.Rollback()
			}()
		}
		return receiveTime
	}

	_, err = receiverStore.ReceiveBatch(context.Background(), ReceiveBatchParams{
		Address: address,
		Max:     2,
	})
	if !errors.Is(err, ErrClaimContention) {
		t.Fatalf("ReceiveBatch(contention exhaustion) error = %v, want ErrClaimContention", err)
	}
	if errors.Is(err, ErrNoMessage) {
		t.Fatalf("ReceiveBatch(contention exhaustion) error = %v, must not look like ErrNoMessage", err)
	}
	if lockBeginErr != nil {
		t.Fatalf("BeginTx(writer lock during batch) error = %v", lockBeginErr)
	}
	if rollbackErr := <-releaseDone; rollbackErr != nil {
		t.Fatalf("Rollback(writer lock) error = %v", rollbackErr)
	}

	var contentionErr *ClaimContentionError
	if !errors.As(err, &contentionErr) {
		t.Fatalf("ReceiveBatch(contention exhaustion) error = %T, want *ClaimContentionError", err)
	}
	if contentionErr.Attempts != maxClaimRetryCount+1 {
		t.Fatalf("ReceiveBatch(contention exhaustion) attempts = %d, want %d", contentionErr.Attempts, maxClaimRetryCount+1)
	}

	firstState, firstAttemptCount := readDeliveryStateAndAttemptCount(t, lockerRuntime, firstSent.DeliveryID)
	if firstState != "queued" {
		t.Fatalf("first delivery state after contention exhaustion = %q, want queued", firstState)
	}
	if firstAttemptCount != 0 {
		t.Fatalf("first delivery attempt_count after contention exhaustion = %d, want 0", firstAttemptCount)
	}
	firstVisibleAt := readDeliveryVisibleAt(t, lockerRuntime, firstSent.DeliveryID)
	if firstVisibleAt != firstSent.VisibleAtUTC {
		t.Fatalf("first delivery visible_at after contention exhaustion = %q, want %q", firstVisibleAt, firstSent.VisibleAtUTC)
	}

	secondState, secondAttemptCount := readDeliveryStateAndAttemptCount(t, lockerRuntime, secondSent.DeliveryID)
	if secondState != "queued" {
		t.Fatalf("second delivery state after contention exhaustion = %q, want queued", secondState)
	}
	if secondAttemptCount != 0 {
		t.Fatalf("second delivery attempt_count after contention exhaustion = %d, want 0", secondAttemptCount)
	}
	secondVisibleAt := readDeliveryVisibleAt(t, lockerRuntime, secondSent.DeliveryID)
	if secondVisibleAt != secondSent.VisibleAtUTC {
		t.Fatalf("second delivery visible_at after contention exhaustion = %q, want %q", secondVisibleAt, secondSent.VisibleAtUTC)
	}

	firstMessage, err := receiverStore.Receive(context.Background(), ReceiveParams{Address: address})
	if err != nil {
		t.Fatalf("Receive(first after contention rollback) error = %v", err)
	}
	if firstMessage.DeliveryID != firstSent.DeliveryID {
		t.Fatalf("Receive(first after contention rollback) delivery id = %q, want %q", firstMessage.DeliveryID, firstSent.DeliveryID)
	}
	if _, err := receiverStore.Ack(context.Background(), firstMessage.DeliveryID, firstMessage.LeaseToken); err != nil {
		t.Fatalf("Ack(first after contention rollback) error = %v", err)
	}

	secondMessage, err := receiverStore.Receive(context.Background(), ReceiveParams{Address: address})
	if err != nil {
		t.Fatalf("Receive(second after contention rollback) error = %v", err)
	}
	if secondMessage.DeliveryID != secondSent.DeliveryID {
		t.Fatalf("Receive(second after contention rollback) delivery id = %q, want %q", secondMessage.DeliveryID, secondSent.DeliveryID)
	}

	assertStringSlicesEqual(t, readDeliveryEventTypes(t, lockerRuntime, firstSent.DeliveryID), []string{
		"delivery_queued",
		"delivery_leased",
		"delivery_released",
		"delivery_leased",
		"delivery_acked",
	})
	assertStringSlicesEqual(t, readDeliveryEventTypes(t, lockerRuntime, secondSent.DeliveryID), []string{
		"delivery_queued",
		"delivery_leased",
	})
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

func readDeliveryStateAndAttemptCount(t *testing.T, runtime *Runtime, deliveryID string) (string, int) {
	t.Helper()

	var state string
	var attemptCount int
	if err := runtime.DB().QueryRow(`
SELECT state, attempt_count
FROM deliveries
WHERE delivery_id = ?
`, deliveryID).Scan(&state, &attemptCount); err != nil {
		t.Fatalf("QueryRow(delivery state/attempt_count) error = %v", err)
	}
	return state, attemptCount
}

func readDeliveryVisibleAt(t *testing.T, runtime *Runtime, deliveryID string) string {
	t.Helper()

	var visibleAt string
	if err := runtime.DB().QueryRow(`
SELECT visible_at
FROM deliveries
WHERE delivery_id = ?
`, deliveryID).Scan(&visibleAt); err != nil {
		t.Fatalf("QueryRow(delivery visible_at) error = %v", err)
	}
	return visibleAt
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
