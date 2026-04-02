package mailbox

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestListStaleAddressesReturnsQueuedStaleAddressesSortedByOldestEligibleAt(t *testing.T) {
	t.Parallel()

	runtime, store := newLeaseTestStore(t)
	defer runtime.Close()

	current := time.Date(2026, 3, 31, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return current }

	oldest := mustSendMessage(t, store, "workflow/stale-old", "agent/sender", "first", "first body")
	current = current.Add(2 * time.Minute)
	mustSendMessage(t, store, "workflow/stale-old", "agent/sender", "second", "second body")
	current = current.Add(time.Minute)
	newer := mustSendMessage(t, store, "workflow/stale-new", "agent/sender", "third", "third body")
	current = time.Date(2026, 3, 31, 12, 9, 0, 0, time.UTC)
	mustSendMessage(t, store, "workflow/fresh", "agent/sender", "fresh", "fresh body")
	current = time.Date(2026, 3, 31, 12, 10, 0, 0, time.UTC)

	stale, err := store.ListStaleAddresses(context.Background(), StaleAddressesParams{
		Addresses: []string{"workflow/fresh", "workflow/stale-new", "workflow/stale-old"},
		OlderThan: 5 * time.Minute,
	})
	if err != nil {
		t.Fatalf("ListStaleAddresses() error = %v", err)
	}
	if len(stale) != 2 {
		t.Fatalf("len(ListStaleAddresses()) = %d, want 2", len(stale))
	}

	if stale[0].Address != "workflow/stale-old" {
		t.Fatalf("stale[0].address = %q, want workflow/stale-old", stale[0].Address)
	}
	if stale[0].OldestEligibleAt != oldest.VisibleAtUTC {
		t.Fatalf("stale[0].oldest_eligible_at = %q, want %q", stale[0].OldestEligibleAt, oldest.VisibleAtUTC)
	}
	if stale[0].ClaimableCount != 2 {
		t.Fatalf("stale[0].claimable_count = %d, want 2", stale[0].ClaimableCount)
	}

	if stale[1].Address != "workflow/stale-new" {
		t.Fatalf("stale[1].address = %q, want workflow/stale-new", stale[1].Address)
	}
	if stale[1].OldestEligibleAt != newer.VisibleAtUTC {
		t.Fatalf("stale[1].oldest_eligible_at = %q, want %q", stale[1].OldestEligibleAt, newer.VisibleAtUTC)
	}
	if stale[1].ClaimableCount != 1 {
		t.Fatalf("stale[1].claimable_count = %d, want 1", stale[1].ClaimableCount)
	}
}

func TestListStaleAddressesTreatsExpiredLeaseAsClaimable(t *testing.T) {
	t.Parallel()

	runtime, store := newLeaseTestStore(t)
	defer runtime.Close()

	current := time.Date(2026, 3, 31, 13, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return current }

	mustSendMessage(t, store, "workflow/expired-lease", "agent/sender", "lease me", "lease body")

	received, err := store.Receive(context.Background(), ReceiveParams{Address: "workflow/expired-lease"})
	if err != nil {
		t.Fatalf("Receive() error = %v", err)
	}

	current = current.Add(defaultLeaseTimeout + 6*time.Minute)

	stale, err := store.ListStaleAddresses(context.Background(), StaleAddressesParams{
		Addresses: []string{"workflow/expired-lease"},
		OlderThan: 5 * time.Minute,
	})
	if err != nil {
		t.Fatalf("ListStaleAddresses(expired lease) error = %v", err)
	}
	if len(stale) != 1 {
		t.Fatalf("len(ListStaleAddresses(expired lease)) = %d, want 1", len(stale))
	}
	if stale[0].Address != "workflow/expired-lease" {
		t.Fatalf("stale[0].address = %q, want workflow/expired-lease", stale[0].Address)
	}
	if stale[0].OldestEligibleAt != received.LeaseExpiresAt {
		t.Fatalf("stale[0].oldest_eligible_at = %q, want %q", stale[0].OldestEligibleAt, received.LeaseExpiresAt)
	}
	if stale[0].ClaimableCount != 1 {
		t.Fatalf("stale[0].claimable_count = %d, want 1", stale[0].ClaimableCount)
	}
}

func TestListStaleAddressesDeduplicatesAliasesByFirstSuppliedAddress(t *testing.T) {
	t.Parallel()

	runtime, store := newLeaseTestStore(t)
	defer runtime.Close()

	current := time.Date(2026, 3, 31, 14, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return current }

	mustSendMessage(t, store, "workflow/primary", "agent/sender", "primary", "primary body")

	endpointID, found, err := store.lookupEndpointID(context.Background(), runtime.DB(), "workflow/primary")
	if err != nil {
		t.Fatalf("lookupEndpointID(primary) error = %v", err)
	}
	if !found {
		t.Fatal("lookupEndpointID(primary) found = false, want true")
	}
	addAddressAlias(t, runtime, endpointID, "workflow/alias")

	current = current.Add(10 * time.Minute)

	firstLabel, err := store.ListStaleAddresses(context.Background(), StaleAddressesParams{
		Addresses: []string{"workflow/alias", "workflow/primary"},
		OlderThan: 5 * time.Minute,
	})
	if err != nil {
		t.Fatalf("ListStaleAddresses(alias first) error = %v", err)
	}
	if len(firstLabel) != 1 {
		t.Fatalf("len(ListStaleAddresses(alias first)) = %d, want 1", len(firstLabel))
	}
	if firstLabel[0].Address != "workflow/alias" {
		t.Fatalf("alias-first label = %q, want workflow/alias", firstLabel[0].Address)
	}

	secondLabel, err := store.ListStaleAddresses(context.Background(), StaleAddressesParams{
		Addresses: []string{"workflow/primary", "workflow/alias"},
		OlderThan: 5 * time.Minute,
	})
	if err != nil {
		t.Fatalf("ListStaleAddresses(primary first) error = %v", err)
	}
	if len(secondLabel) != 1 {
		t.Fatalf("len(ListStaleAddresses(primary first)) = %d, want 1", len(secondLabel))
	}
	if secondLabel[0].Address != "workflow/primary" {
		t.Fatalf("primary-first label = %q, want workflow/primary", secondLabel[0].Address)
	}
}

func TestListStaleAddressesReturnsEmptyForUnseenAddress(t *testing.T) {
	t.Parallel()

	_, store := newLeaseTestStore(t)

	stale, err := store.ListStaleAddresses(context.Background(), StaleAddressesParams{
		Addresses: []string{"workflow/unseen"},
		OlderThan: time.Minute,
	})
	if err != nil {
		t.Fatalf("ListStaleAddresses(unseen) error = %v", err)
	}
	if len(stale) != 0 {
		t.Fatalf("len(ListStaleAddresses(unseen)) = %d, want 0", len(stale))
	}
	if stale == nil {
		t.Fatal("ListStaleAddresses(unseen) returned nil slice, want empty slice")
	}
}

func TestListStaleAddressesRejectsKnownGroupAddress(t *testing.T) {
	t.Parallel()

	runtime, store := newLeaseTestStore(t)
	defer runtime.Close()

	group, err := store.CreateGroup(context.Background(), "group/stale")
	if err != nil {
		t.Fatalf("CreateGroup() error = %v", err)
	}

	_, err = store.ListStaleAddresses(context.Background(), StaleAddressesParams{
		Addresses: []string{group.Address},
		OlderThan: time.Minute,
	})
	if !errors.Is(err, ErrAddressReservedByGroup) {
		t.Fatalf("ListStaleAddresses(group address) error = %v, want ErrAddressReservedByGroup", err)
	}
}

func TestListStaleAddressesReturnsStaleGroupViewUsingUnreadVisibility(t *testing.T) {
	t.Parallel()

	runtime, store := newLeaseTestStore(t)
	defer runtime.Close()

	group, err := store.CreateGroup(context.Background(), "group/history-stale")
	if err != nil {
		t.Fatalf("CreateGroup() error = %v", err)
	}

	current := time.Date(2026, 3, 31, 15, 0, 0, 0, time.UTC)
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

	current = current.Add(10 * time.Minute)

	stale, err := store.ListStaleAddresses(context.Background(), StaleAddressesParams{
		GroupViews: []GroupStaleView{{
			Address: group.Address,
			Person:  "alice",
		}},
		OlderThan: 5 * time.Minute,
	})
	if err != nil {
		t.Fatalf("ListStaleAddresses(group view) error = %v", err)
	}
	if len(stale) != 1 {
		t.Fatalf("len(ListStaleAddresses(group view)) = %d, want 1", len(stale))
	}
	if stale[0].Address != group.Address {
		t.Fatalf("stale[0].address = %q, want %q", stale[0].Address, group.Address)
	}
	if stale[0].Person != "alice" {
		t.Fatalf("stale[0].person = %q, want alice", stale[0].Person)
	}
	if stale[0].OldestEligibleAt != preJoin.MessageCreatedAt {
		t.Fatalf("stale[0].oldest_eligible_at = %q, want %q", stale[0].OldestEligibleAt, preJoin.MessageCreatedAt)
	}
	if stale[0].ClaimableCount != 2 {
		t.Fatalf("stale[0].claimable_count = %d, want 2", stale[0].ClaimableCount)
	}

	if duringMembership.MessageCreatedAt <= stale[0].OldestEligibleAt {
		t.Fatalf("during-membership timestamp = %q, want newer than %q", duringMembership.MessageCreatedAt, stale[0].OldestEligibleAt)
	}
}

func TestListStaleAddressesReturnsEmptyForGroupViewerWithoutHistory(t *testing.T) {
	t.Parallel()

	runtime, store := newLeaseTestStore(t)
	defer runtime.Close()

	group, err := store.CreateGroup(context.Background(), "group/no-history")
	if err != nil {
		t.Fatalf("CreateGroup() error = %v", err)
	}
	mustSendGroupMessage(t, store, group.Address, "agent/sender", "message", "body")

	stale, err := store.ListStaleAddresses(context.Background(), StaleAddressesParams{
		GroupViews: []GroupStaleView{{
			Address: group.Address,
			Person:  "carol",
		}},
		OlderThan: time.Minute,
	})
	if err != nil {
		t.Fatalf("ListStaleAddresses(group unseen viewer) error = %v", err)
	}
	if len(stale) != 0 {
		t.Fatalf("len(ListStaleAddresses(group unseen viewer)) = %d, want 0", len(stale))
	}
}

func addAddressAlias(t *testing.T, runtime *Runtime, endpointID, address string) {
	t.Helper()

	if _, err := runtime.DB().Exec(`
INSERT INTO endpoint_addresses (address, endpoint_id, created_at)
VALUES (?, ?, ?)
`, address, endpointID, formatTimestamp(time.Date(2026, 3, 31, 14, 0, 0, 0, time.UTC))); err != nil {
		t.Fatalf("Exec(insert endpoint alias) error = %v", err)
	}
}
