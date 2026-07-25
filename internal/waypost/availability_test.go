package waypost

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestAvailabilityClaimablePersonalDeliveryUsesFirstAliasAndStableOrdering(t *testing.T) {
	t.Parallel()

	runtime, store := newLeaseTestStore(t)
	defer runtime.Close()

	current := time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return current }

	older := mustSendMessage(t, store, "workflow/primary", "agent/sender", "older", "older body")
	addAddressAlias(t, runtime, older.RecipientID, "workflow/alias")

	current = current.Add(time.Second)
	newer := mustSendMessage(t, store, "workflow/secondary", "agent/sender", "newer", "newer body")

	scope, err := store.resolvePersonal(context.Background(), store.readDB, []string{
		"workflow/alias",
		"workflow/primary",
		"workflow/secondary",
	})
	if err != nil {
		t.Fatalf("resolvePersonal() error = %v", err)
	}

	candidate, err := store.claimablePersonalDelivery(context.Background(), store.readDB, scope, formatTimestamp(store.now()))
	if err != nil {
		t.Fatalf("claimablePersonalDelivery() error = %v", err)
	}
	if candidate.DeliveryID != older.DeliveryID {
		t.Fatalf("claimablePersonalDelivery().delivery_id = %q, want %q", candidate.DeliveryID, older.DeliveryID)
	}
	if candidate.RecipientAddress != "workflow/alias" {
		t.Fatalf("claimablePersonalDelivery().recipient_address = %q, want workflow/alias", candidate.RecipientAddress)
	}

	deliveries, err := store.listPersonalDeliveries(context.Background(), store.readDB, scope, "", formatTimestamp(store.now()))
	if err != nil {
		t.Fatalf("listPersonalDeliveries() error = %v", err)
	}
	if len(deliveries) != 2 {
		t.Fatalf("len(listPersonalDeliveries()) = %d, want 2", len(deliveries))
	}
	if deliveries[0].DeliveryID != older.DeliveryID || deliveries[1].DeliveryID != newer.DeliveryID {
		t.Fatalf("listPersonalDeliveries() delivery ids = [%q %q], want [%q %q]", deliveries[0].DeliveryID, deliveries[1].DeliveryID, older.DeliveryID, newer.DeliveryID)
	}
	if deliveries[0].RecipientAddress != "workflow/alias" {
		t.Fatalf("listPersonalDeliveries()[0].recipient_address = %q, want workflow/alias", deliveries[0].RecipientAddress)
	}
}

func TestRemainingByStateUsesRecipientStateVisibleIndex(t *testing.T) {
	t.Parallel()

	runtime, store := newLeaseTestStore(t)
	defer runtime.Close()

	sent := mustSendMessage(t, store, "workflow/count-plan", "agent/sender", "count", "body")
	rows, err := store.readDB.QueryContext(context.Background(), `
EXPLAIN QUERY PLAN
SELECT
  COUNT(*) FILTER (WHERE d.state = 'queued'),
  COUNT(*) FILTER (WHERE d.state = 'leased'),
  COUNT(*) FILTER (WHERE d.state = 'dead_letter')
FROM deliveries AS d
WHERE d.recipient_endpoint_id IN (?)
  AND d.state IN ('queued', 'leased', 'dead_letter')
`, sent.RecipientID)
	if err != nil {
		t.Fatalf("EXPLAIN QUERY PLAN error = %v", err)
	}
	defer rows.Close()

	usedIndex := false
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatalf("scan query plan: %v", err)
		}
		upper := strings.ToUpper(detail)
		if strings.Contains(detail, "idx_deliveries_recipient_state_visible") {
			usedIndex = true
		}
		if strings.Contains(upper, "SCAN DELIVERIES") || strings.Contains(upper, "SCAN D") {
			t.Fatalf("remaining-state query plan scans deliveries: %q", detail)
		}
		if strings.Contains(upper, "TEMP B-TREE") {
			t.Fatalf("remaining-state query plan materializes a temporary B-tree: %q", detail)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate query plan: %v", err)
	}
	if !usedIndex {
		t.Fatal("remaining-state query plan did not use idx_deliveries_recipient_state_visible")
	}
}

func TestRemainingByStateDeductsExcludedClaims(t *testing.T) {
	t.Parallel()

	runtime, store := newLeaseTestStore(t)
	defer runtime.Close()

	first := mustSendMessage(t, store, "workflow/excluded-count", "agent/sender", "first", "body")
	mustSendMessage(t, store, "workflow/excluded-count", "agent/sender", "second", "body")

	remaining, err := store.RemainingByState(context.Background(), []string{"workflow/excluded-count"}, []string{first.DeliveryID})
	if err != nil {
		t.Fatalf("RemainingByState() error = %v", err)
	}
	if got := remaining["queued"]; got != 1 {
		t.Fatalf("remaining_by_state[queued] = %d, want 1", got)
	}
	if len(remaining) != 1 {
		t.Fatalf("remaining_by_state = %v, want only one queued delivery", remaining)
	}
}

func TestAvailabilityListPersonalStaleAddressesUsesClaimableEligibility(t *testing.T) {
	t.Parallel()

	runtime, store := newLeaseTestStore(t)
	defer runtime.Close()

	current := time.Date(2026, 4, 1, 11, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return current }

	queued := mustSendMessage(t, store, "workflow/stale-queued", "agent/sender", "queued", "queued body")

	current = current.Add(time.Second)
	mustSendMessage(t, store, "workflow/stale-leased", "agent/sender", "leased", "leased body")
	received, err := store.Receive(context.Background(), ReceiveParams{Address: "workflow/stale-leased"})
	if err != nil {
		t.Fatalf("Receive(stale leased) error = %v", err)
	}

	current = current.Add(defaultLeaseTimeout + time.Second)
	scope, err := store.resolvePersonal(context.Background(), store.readDB, []string{
		"workflow/stale-queued",
		"workflow/stale-leased",
	})
	if err != nil {
		t.Fatalf("resolvePersonal() error = %v", err)
	}

	stale, err := store.queryPersonalStaleAddresses(
		context.Background(),
		store.readDB,
		scope,
		formatTimestamp(store.now()),
		formatTimestamp(store.now().Add(-time.Second)),
	)
	if err != nil {
		t.Fatalf("listPersonalStaleAddresses() error = %v", err)
	}
	if len(stale) != 2 {
		t.Fatalf("len(listPersonalStaleAddresses()) = %d, want 2", len(stale))
	}
	if stale[0].Address != "workflow/stale-queued" || stale[1].Address != "workflow/stale-leased" {
		t.Fatalf("listPersonalStaleAddresses() addresses = [%q %q], want [workflow/stale-queued workflow/stale-leased]", stale[0].Address, stale[1].Address)
	}
	if stale[0].OldestEligibleAt != queued.VisibleAtUTC {
		t.Fatalf("stale[0].oldest_eligible_at = %q, want %q", stale[0].OldestEligibleAt, queued.VisibleAtUTC)
	}
	if stale[1].OldestEligibleAt != received.LeaseExpiresAt {
		t.Fatalf("stale[1].oldest_eligible_at = %q, want %q", stale[1].OldestEligibleAt, received.LeaseExpiresAt)
	}
}

func TestAvailabilityListClaimablePersonalAddressesUsesClaimableEligibility(t *testing.T) {
	t.Parallel()

	runtime, store := newLeaseTestStore(t)
	defer runtime.Close()

	current := time.Date(2026, 4, 1, 11, 30, 0, 0, time.UTC)
	store.now = func() time.Time { return current }

	queued := mustSendMessage(t, store, "workflow/claimable-queued", "agent/sender", "queued", "queued body")

	current = current.Add(time.Second)
	mustSendMessage(t, store, "workflow/claimable-leased", "agent/sender", "leased", "leased body")
	received, err := store.Receive(context.Background(), ReceiveParams{Address: "workflow/claimable-leased"})
	if err != nil {
		t.Fatalf("Receive(claimable leased) error = %v", err)
	}

	current = current.Add(defaultLeaseTimeout + time.Second)
	scope, err := store.resolvePersonal(context.Background(), store.readDB, []string{
		"workflow/claimable-queued",
		"workflow/claimable-leased",
	})
	if err != nil {
		t.Fatalf("resolvePersonal() error = %v", err)
	}

	claimable, err := store.listClaimablePersonalAddresses(
		context.Background(),
		store.readDB,
		scope,
		formatTimestamp(store.now()),
	)
	if err != nil {
		t.Fatalf("listClaimablePersonalAddresses() error = %v", err)
	}
	if len(claimable) != 2 {
		t.Fatalf("len(listClaimablePersonalAddresses()) = %d, want 2", len(claimable))
	}
	if claimable[0].Address != "workflow/claimable-queued" || claimable[1].Address != "workflow/claimable-leased" {
		t.Fatalf("listClaimablePersonalAddresses() addresses = [%q %q], want [workflow/claimable-queued workflow/claimable-leased]", claimable[0].Address, claimable[1].Address)
	}
	if claimable[0].OldestEligibleAt != queued.VisibleAtUTC {
		t.Fatalf("claimable[0].oldest_eligible_at = %q, want %q", claimable[0].OldestEligibleAt, queued.VisibleAtUTC)
	}
	if claimable[1].OldestEligibleAt != received.LeaseExpiresAt {
		t.Fatalf("claimable[1].oldest_eligible_at = %q, want %q", claimable[1].OldestEligibleAt, received.LeaseExpiresAt)
	}
	if claimable[0].ClaimableCount != 1 || claimable[1].ClaimableCount != 1 {
		t.Fatalf("claimable counts = [%d %d], want [1 1]", claimable[0].ClaimableCount, claimable[1].ClaimableCount)
	}
}

func TestAvailabilityListGroupMessagesUsesVisibilityCutoffForUnreadView(t *testing.T) {
	t.Parallel()

	runtime, store := newLeaseTestStore(t)
	defer runtime.Close()

	group, err := store.CreateGroup(context.Background(), "group/availability")
	if err != nil {
		t.Fatalf("CreateGroup() error = %v", err)
	}

	current := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return current }

	preJoin := mustSendGroupMessage(t, store, group.Address, "agent/sender", "pre-join", "pre-join body")

	current = current.Add(time.Second)
	if _, err := store.AddGroupMember(context.Background(), group.Address, "alice"); err != nil {
		t.Fatalf("AddGroupMember(alice) error = %v", err)
	}

	current = current.Add(time.Second)
	duringMembership := mustSendGroupMessage(t, store, group.Address, "agent/sender", "during-membership", "during body")

	current = current.Add(time.Second)
	if _, err := store.RemoveGroupMember(context.Background(), group.Address, "alice"); err != nil {
		t.Fatalf("RemoveGroupMember(alice) error = %v", err)
	}

	current = current.Add(time.Second)
	mustSendGroupMessage(t, store, group.Address, "agent/sender", "after-leave", "after body")

	scope, err := store.resolveGroup(context.Background(), store.readDB, group.Address, "alice")
	if err != nil {
		t.Fatalf("resolveGroup() error = %v", err)
	}
	if scope.viewer.VisibilityCutoff == nil {
		t.Fatal("resolveGroup().viewer.VisibilityCutoff = nil, want non-nil")
	}

	records, err := store.listGroupMessages(context.Background(), store.readDB, scope, true, 0)
	if err != nil {
		t.Fatalf("listGroupMessages(unreadOnly) error = %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("len(listGroupMessages(unreadOnly)) = %d, want 2", len(records))
	}
	if records[0].MessageID != preJoin.MessageID || records[1].MessageID != duringMembership.MessageID {
		t.Fatalf("listGroupMessages(unreadOnly) message ids = [%q %q], want [%q %q]", records[0].MessageID, records[1].MessageID, preJoin.MessageID, duringMembership.MessageID)
	}
}
