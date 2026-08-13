package waypost

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPersonalListPageUsesBoundedKeysetCursor(t *testing.T) {
	t.Parallel()

	runtime, store := newLeaseTestStore(t)
	defer runtime.Close()

	current := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return current }
	first := mustSendMessage(t, store, "workflow/paged", "agent/one", "first", "first body")
	current = current.Add(time.Second)
	second := mustSendMessage(t, store, "workflow/paged", "agent/two", "second", "second body")
	current = current.Add(time.Second)
	third := mustSendMessage(t, store, "workflow/paged", "agent/three", "third", "third body")

	page, err := store.ListPage(context.Background(), ListParams{Address: "workflow/paged", Limit: 2})
	if err != nil {
		t.Fatalf("ListPage(first) error = %v", err)
	}
	if len(page.Items) != 2 || page.Items[0].DeliveryID != first.DeliveryID || page.Items[1].DeliveryID != second.DeliveryID {
		t.Fatalf("ListPage(first) items = %+v", page.Items)
	}
	if page.NextCursor == "" {
		t.Fatal("ListPage(first) next_cursor is empty")
	}

	next, err := store.ListPage(context.Background(), ListParams{Address: "workflow/paged", Limit: 2, Cursor: page.NextCursor})
	if err != nil {
		t.Fatalf("ListPage(next) error = %v", err)
	}
	if len(next.Items) != 1 || next.Items[0].DeliveryID != third.DeliveryID || next.NextCursor != "" {
		t.Fatalf("ListPage(next) = %+v", next)
	}

	_, err = store.ListPage(context.Background(), ListParams{Address: "workflow/paged", State: "acked", Limit: 2, Cursor: page.NextCursor})
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("ListPage(cursor with different filter) error = %v", err)
	}
}

func TestLatestStatePageUsesVisibilityOrderAndCursor(t *testing.T) {
	t.Parallel()

	runtime, store := newLeaseTestStore(t)
	defer runtime.Close()

	current := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return current }
	older := mustSendMessage(t, store, "workflow/latest-state", "agent/sender", "older", "older body")
	claimed, err := store.Receive(context.Background(), ReceiveParams{Address: "workflow/latest-state"})
	if err != nil {
		t.Fatalf("Receive(older) error = %v", err)
	}

	current = current.Add(time.Minute)
	newer := mustSendMessage(t, store, "workflow/latest-state", "agent/sender", "newer", "newer body")
	current = current.Add(time.Minute)
	if _, err := store.Release(context.Background(), claimed.DeliveryID, claimed.LeaseToken); err != nil {
		t.Fatalf("Release(older) error = %v", err)
	}

	first, err := store.ReadLatestDeliveriesPage(context.Background(), ReadLatestParams{
		Addresses: []string{"workflow/latest-state"},
		State:     "queued",
		Limit:     1,
	})
	if err != nil {
		t.Fatalf("ReadLatestDeliveriesPage(first) error = %v", err)
	}
	if len(first.Items) != 1 || first.Items[0].DeliveryID != older.DeliveryID || first.NextCursor == "" {
		t.Fatalf("ReadLatestDeliveriesPage(first) = %+v, want released older delivery and cursor", first)
	}

	second, err := store.ReadLatestDeliveriesPage(context.Background(), ReadLatestParams{
		Addresses: []string{"workflow/latest-state"},
		State:     "queued",
		Limit:     1,
		Cursor:    first.NextCursor,
	})
	if err != nil {
		t.Fatalf("ReadLatestDeliveriesPage(second) error = %v", err)
	}
	if len(second.Items) != 1 || second.Items[0].DeliveryID != newer.DeliveryID || second.NextCursor != "" {
		t.Fatalf("ReadLatestDeliveriesPage(second) = %+v, want newer delivery and no cursor", second)
	}
}

func TestPaginationRejectsOversizedPagesAndInputs(t *testing.T) {
	t.Parallel()

	if _, err := normalizePageParams(PageParams{}); err == nil {
		t.Fatal("normalizePageParams(zero) error = nil")
	}
	if _, err := normalizePageParams(PageParams{Limit: MaxPageSize + 1}); err == nil {
		t.Fatal("normalizePageParams(too large) error = nil")
	}
	values := make([]string, MaxInputItems+1)
	for i := range values {
		values[i] = "workflow/address"
	}
	if _, err := normalizeAddresses("", values, "--for"); err == nil {
		t.Fatal("normalizeAddresses(too many) error = nil")
	}
}

func TestEmptyPagesStillValidateCursors(t *testing.T) {
	t.Parallel()

	runtime, store := newLeaseTestStore(t)
	defer runtime.Close()

	group, err := store.CreateGroup(context.Background(), "group/empty-cursor")
	if err != nil {
		t.Fatalf("CreateGroup() error = %v", err)
	}

	tests := []struct {
		name string
		call func() error
	}{
		{
			name: "personal list unseen recipient",
			call: func() error {
				_, err := store.ListPage(context.Background(), ListParams{
					Address: "workflow/unseen",
					Limit:   1,
					Cursor:  "malformed",
				})
				return err
			},
		},
		{
			name: "latest unseen recipient",
			call: func() error {
				_, err := store.ReadLatestDeliveriesPage(context.Background(), ReadLatestParams{
					Addresses: []string{"workflow/unseen"},
					Limit:     1,
					Cursor:    "malformed",
				})
				return err
			},
		},
		{
			name: "group list unresolved sender",
			call: func() error {
				_, err := store.ListGroupMessagesPage(context.Background(), GroupListParams{
					Address:     group.Address,
					Person:      "reader",
					FromAddress: "agent/unseen",
					Limit:       1,
					Cursor:      "malformed",
				})
				return err
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.call(); !errors.Is(err, ErrInvalidPaginationCursor) {
				t.Fatalf("empty page cursor error = %v, want ErrInvalidPaginationCursor", err)
			}
		})
	}
}

func TestCursorScopeIncludesUnresolvedSenderAddress(t *testing.T) {
	t.Parallel()

	runtime, store := newLeaseTestStore(t)
	defer runtime.Close()

	mustSendMessage(t, store, "workflow/cursor-sender", "agent/sender", "first", "body")
	mustSendMessage(t, store, "workflow/cursor-sender", "agent/sender", "second", "body")
	page, err := store.ListPage(context.Background(), ListParams{Address: "workflow/cursor-sender", Limit: 1})
	if err != nil {
		t.Fatalf("ListPage() error = %v", err)
	}
	if page.NextCursor == "" {
		t.Fatal("ListPage() next_cursor is empty")
	}
	_, err = store.ListPage(context.Background(), ListParams{
		Address:     "workflow/cursor-sender",
		FromAddress: "agent/unseen",
		Limit:       1,
		Cursor:      page.NextCursor,
	})
	if !errors.Is(err, ErrInvalidPaginationCursor) {
		t.Fatalf("ListPage(cursor with unresolved sender) error = %v, want ErrInvalidPaginationCursor", err)
	}
}

func TestLegacyReadLatestRejectsZeroLimit(t *testing.T) {
	t.Parallel()

	runtime, store := newLeaseTestStore(t)
	defer runtime.Close()

	if _, _, err := store.ReadLatestDeliveries(context.Background(), []string{"workflow/zero-limit"}, "", 0); err == nil || !strings.Contains(err.Error(), "limit must be between 1") {
		t.Fatalf("ReadLatestDeliveries(limit=0) error = %v", err)
	}
}

func TestCLIRejectsExplicitZeroPageLimits(t *testing.T) {
	t.Parallel()

	stateDir := filepath.Join(t.TempDir(), "waypost-state")
	for _, args := range [][]string{
		{"list", "--for", "workflow/zero-limit", "--limit", "0"},
		{"read", "--latest", "--for", "workflow/zero-limit", "--limit", "0"},
		{"group", "list", "--limit", "0"},
	} {
		app := NewApp(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
		if err := app.RunWithStateDir(context.Background(), stateDir, args); err == nil || !strings.Contains(err.Error(), "limit must be between 1") {
			t.Fatalf("RunWithStateDir(%v) error = %v", args, err)
		}
	}
}

func TestGroupListPageFiltersSenderBeforeLimit(t *testing.T) {
	t.Parallel()

	runtime, store := newLeaseTestStore(t)
	defer runtime.Close()

	group, err := store.CreateGroup(context.Background(), "group/paged")
	if err != nil {
		t.Fatalf("CreateGroup() error = %v", err)
	}
	if _, err := store.AddGroupMember(context.Background(), group.Address, "reader"); err != nil {
		t.Fatalf("AddGroupMember() error = %v", err)
	}
	mustSendGroupMessage(t, store, group.Address, "agent/other", "other", "other body")
	wanted := mustSendGroupMessage(t, store, group.Address, "agent/wanted", "wanted", "wanted body")

	page, err := store.ListGroupMessagesPage(context.Background(), GroupListParams{
		Address:     group.Address,
		Person:      "reader",
		FromAddress: "agent/wanted",
		Limit:       1,
	})
	if err != nil {
		t.Fatalf("ListGroupMessagesPage() error = %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].MessageID != wanted.MessageID {
		t.Fatalf("ListGroupMessagesPage() = %+v", page.Items)
	}
}

func TestCompatibilityListWrappersRequirePagination(t *testing.T) {
	runtime, store := newLeaseTestStore(t)
	defer runtime.Close()

	group, err := store.CreateGroup(context.Background(), "group/compatibility-page")
	if err != nil {
		t.Fatalf("CreateGroup() error = %v", err)
	}
	subscriberGroup, err := store.CreateGroup(context.Background(), "group/compatibility-subscribers")
	if err != nil {
		t.Fatalf("CreateGroup(subscribers) error = %v", err)
	}
	for index := 0; index < MaxPageSize-1; index++ {
		if _, err := store.CreateGroup(context.Background(), fmt.Sprintf("group/compatibility-%03d", index)); err != nil {
			t.Fatalf("CreateGroup(%d) error = %v", index, err)
		}
	}
	for index := 0; index < MaxPageSize+1; index++ {
		person := fmt.Sprintf("person-%03d", index)
		if _, err := store.AddGroupMember(context.Background(), group.Address, person); err != nil {
			t.Fatalf("AddGroupMember(%d) error = %v", index, err)
		}
		if _, err := store.AddGroupNotificationSubscriber(
			context.Background(),
			subscriberGroup.Address,
			fmt.Sprintf("agent/subscriber-%03d", index),
			person,
		); err != nil {
			t.Fatalf("AddGroupNotificationSubscriber(%d) error = %v", index, err)
		}
		mustSendGroupMessage(t, store, group.Address, "agent/sender", fmt.Sprintf("group-%03d", index), "body")
		mustSendMessage(t, store, "workflow/compatibility-page", "agent/sender", fmt.Sprintf("personal-%03d", index), "body")
	}

	tests := []struct {
		name string
		call func() error
	}{
		{name: "personal list", call: func() error {
			_, err := store.List(context.Background(), ListParams{Address: "workflow/compatibility-page"})
			return err
		}},
		{name: "groups", call: func() error {
			_, err := store.ListGroups(context.Background())
			return err
		}},
		{name: "members", call: func() error {
			_, err := store.ListGroupMembers(context.Background(), group.Address)
			return err
		}},
		{name: "subscribers", call: func() error {
			_, err := store.ListGroupNotificationSubscribers(context.Background(), subscriberGroup.Address)
			return err
		}},
		{name: "group messages", call: func() error {
			_, err := store.ListGroupMessages(context.Background(), GroupListParams{Address: group.Address, Person: "person-000"})
			return err
		}},
		{name: "group transcript", call: func() error {
			_, err := store.ListGroupTranscript(context.Background(), GroupTranscriptParams{Address: group.Address})
			return err
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.call(); !errors.Is(err, ErrPaginationRequired) {
				t.Fatalf("compatibility wrapper error = %v, want ErrPaginationRequired", err)
			}
		})
	}
}
