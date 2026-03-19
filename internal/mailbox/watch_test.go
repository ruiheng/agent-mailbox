package mailbox

import (
	"context"
	"testing"
	"time"
)

func TestWatchEmitsQueuedDeliveryWithoutClaiming(t *testing.T) {
	t.Parallel()

	runtime, store := newLeaseTestStore(t)
	defer runtime.Close()

	mustRegisterEndpoint(t, store, "workflow/reviewer/task-123")
	mustRegisterEndpoint(t, store, "agent/sender")
	sent := mustSendMessage(t, store, "workflow/reviewer/task-123", "agent/sender", "review request", "hello reviewer")

	var deliveries []ListedDelivery
	if err := store.Watch(context.Background(), WatchParams{
		Addresses: []string{"workflow/reviewer/task-123"},
		Timeout:   120 * time.Millisecond,
	}, func(delivery ListedDelivery) error {
		deliveries = append(deliveries, delivery)
		return nil
	}); err != nil {
		t.Fatalf("Watch() error = %v", err)
	}

	if len(deliveries) != 1 {
		t.Fatalf("len(deliveries) = %d, want 1", len(deliveries))
	}
	if deliveries[0].DeliveryID != sent.DeliveryID {
		t.Fatalf("watch delivery id = %q, want %q", deliveries[0].DeliveryID, sent.DeliveryID)
	}
	if deliveries[0].RecipientAddress != "workflow/reviewer/task-123" {
		t.Fatalf("watch recipient address = %q, want workflow/reviewer/task-123", deliveries[0].RecipientAddress)
	}

	queued, err := store.List(context.Background(), ListParams{Address: "workflow/reviewer/task-123"})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(queued) != 1 {
		t.Fatalf("len(queued) = %d, want 1", len(queued))
	}
	if queued[0].DeliveryID != sent.DeliveryID {
		t.Fatalf("queued delivery id = %q, want %q", queued[0].DeliveryID, sent.DeliveryID)
	}

	eventTypes := readDeliveryEventTypes(t, runtime, sent.DeliveryID)
	want := []string{"delivery_queued"}
	assertStringSlicesEqual(t, eventTypes, want)
}

func TestWatchMultipleAddressesUsesOldestFirstUnion(t *testing.T) {
	t.Parallel()

	_, store := newLeaseTestStore(t)

	current := time.Date(2026, 3, 18, 15, 0, 0, 0, time.UTC)
	store.now = func() time.Time {
		return current
	}

	mustRegisterEndpoint(t, store, "workflow/older")
	mustRegisterEndpoint(t, store, "workflow/newer")
	mustRegisterEndpoint(t, store, "agent/sender")

	older := mustSendMessage(t, store, "workflow/older", "agent/sender", "older", "older body")
	current = current.Add(time.Second)
	newer := mustSendMessage(t, store, "workflow/newer", "agent/sender", "newer", "newer body")

	var deliveries []ListedDelivery
	if err := store.Watch(context.Background(), WatchParams{
		Addresses: []string{"workflow/newer", "workflow/older", "workflow/newer"},
		Timeout:   120 * time.Millisecond,
	}, func(delivery ListedDelivery) error {
		deliveries = append(deliveries, delivery)
		return nil
	}); err != nil {
		t.Fatalf("Watch(multi) error = %v", err)
	}

	if len(deliveries) != 2 {
		t.Fatalf("len(deliveries) = %d, want 2", len(deliveries))
	}
	if deliveries[0].DeliveryID != older.DeliveryID {
		t.Fatalf("deliveries[0].delivery_id = %q, want %q", deliveries[0].DeliveryID, older.DeliveryID)
	}
	if deliveries[0].RecipientAddress != "workflow/older" {
		t.Fatalf("deliveries[0].recipient_address = %q, want workflow/older", deliveries[0].RecipientAddress)
	}
	if deliveries[1].DeliveryID != newer.DeliveryID {
		t.Fatalf("deliveries[1].delivery_id = %q, want %q", deliveries[1].DeliveryID, newer.DeliveryID)
	}
	if deliveries[1].RecipientAddress != "workflow/newer" {
		t.Fatalf("deliveries[1].recipient_address = %q, want workflow/newer", deliveries[1].RecipientAddress)
	}
}
