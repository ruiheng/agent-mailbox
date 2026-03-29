package mailbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestGroupMembershipLifecycle(t *testing.T) {
	t.Parallel()

	runtime, err := OpenRuntime(context.Background(), filepath.Join(t.TempDir(), "mailbox-state"))
	if err != nil {
		t.Fatalf("OpenRuntime() error = %v", err)
	}
	defer runtime.Close()

	store := runtime.Store()
	group, err := store.CreateGroup(context.Background(), "group/ops")
	if err != nil {
		t.Fatalf("CreateGroup() error = %v", err)
	}

	firstMembership, err := store.AddGroupMember(context.Background(), group.Address, "alice")
	if err != nil {
		t.Fatalf("AddGroupMember(first) error = %v", err)
	}
	if !firstMembership.Active {
		t.Fatal("first membership active = false, want true")
	}

	if _, err := store.AddGroupMember(context.Background(), group.Address, "alice"); !errors.Is(err, ErrActiveMembershipExists) {
		t.Fatalf("AddGroupMember(duplicate) error = %v, want ErrActiveMembershipExists", err)
	}

	removedMembership, err := store.RemoveGroupMember(context.Background(), group.Address, "alice")
	if err != nil {
		t.Fatalf("RemoveGroupMember() error = %v", err)
	}
	if removedMembership.Active {
		t.Fatal("removed membership active = true, want false")
	}
	if removedMembership.LeftAt == nil || strings.TrimSpace(*removedMembership.LeftAt) == "" {
		t.Fatalf("removed membership left_at = %v, want non-empty timestamp", removedMembership.LeftAt)
	}

	if _, err := store.RemoveGroupMember(context.Background(), group.Address, "alice"); !errors.Is(err, ErrActiveMembershipMissing) {
		t.Fatalf("RemoveGroupMember(missing) error = %v, want ErrActiveMembershipMissing", err)
	}

	secondMembership, err := store.AddGroupMember(context.Background(), group.Address, "alice")
	if err != nil {
		t.Fatalf("AddGroupMember(rejoin) error = %v", err)
	}
	if secondMembership.MembershipID == firstMembership.MembershipID {
		t.Fatalf("rejoin membership_id = %q, want a new membership id", secondMembership.MembershipID)
	}

	memberships, err := store.ListGroupMembers(context.Background(), group.Address)
	if err != nil {
		t.Fatalf("ListGroupMembers() error = %v", err)
	}
	if len(memberships) != 2 {
		t.Fatalf("len(memberships) = %d, want 2", len(memberships))
	}

	activeCount := 0
	historicalCount := 0
	for _, membership := range memberships {
		if membership.Person != "alice" {
			t.Fatalf("membership person = %q, want alice", membership.Person)
		}
		if membership.GroupID != group.GroupID {
			t.Fatalf("membership group_id = %q, want %q", membership.GroupID, group.GroupID)
		}
		if membership.Active {
			activeCount++
			if membership.LeftAt != nil {
				t.Fatalf("active membership left_at = %v, want nil", membership.LeftAt)
			}
		} else {
			historicalCount++
			if membership.LeftAt == nil {
				t.Fatal("historical membership left_at = nil, want timestamp")
			}
		}
	}
	if activeCount != 1 {
		t.Fatalf("active membership count = %d, want 1", activeCount)
	}
	if historicalCount != 1 {
		t.Fatalf("historical membership count = %d, want 1", historicalCount)
	}
}

func TestGroupAndEndpointNamespaceCollision(t *testing.T) {
	t.Parallel()

	runtime, err := OpenRuntime(context.Background(), filepath.Join(t.TempDir(), "mailbox-state"))
	if err != nil {
		t.Fatalf("OpenRuntime() error = %v", err)
	}
	defer runtime.Close()

	store := runtime.Store()
	if _, err := store.Send(context.Background(), SendParams{
		ToAddress:     "workflow/reviewer/task-123",
		Subject:       "personal message",
		ContentType:   "text/plain",
		SchemaVersion: "v1",
		Body:          []byte("hello"),
	}); err != nil {
		t.Fatalf("Send(endpoint) error = %v", err)
	}

	if _, err := store.CreateGroup(context.Background(), "workflow/reviewer/task-123"); !errors.Is(err, ErrAddressReservedByEndpoint) {
		t.Fatalf("CreateGroup(endpoint collision) error = %v, want ErrAddressReservedByEndpoint", err)
	}

	group, err := store.CreateGroup(context.Background(), "group/reviewer")
	if err != nil {
		t.Fatalf("CreateGroup(group/reviewer) error = %v", err)
	}
	if _, err := store.Send(context.Background(), SendParams{
		ToAddress:     group.Address,
		Subject:       "group collision",
		ContentType:   "text/plain",
		SchemaVersion: "v1",
		Body:          []byte("hello group"),
	}); !errors.Is(err, ErrAddressReservedByGroup) {
		t.Fatalf("Send(group collision) error = %v, want ErrAddressReservedByGroup", err)
	}

	var endpointAddressCount int
	if err := runtime.DB().QueryRow(`SELECT COUNT(*) FROM endpoint_addresses WHERE address = ?`, group.Address).Scan(&endpointAddressCount); err != nil {
		t.Fatalf("count endpoint_addresses(group) error = %v", err)
	}
	if endpointAddressCount != 0 {
		t.Fatalf("group address endpoint count = %d, want 0", endpointAddressCount)
	}
}

func TestGroupControlPlaneCLI(t *testing.T) {
	t.Parallel()

	stateDir := filepath.Join(t.TempDir(), "mailbox-state")

	createStdout := &bytes.Buffer{}
	createApp := NewApp(strings.NewReader(""), createStdout, &bytes.Buffer{})
	if err := createApp.Run(context.Background(), []string{
		"--state-dir", stateDir,
		"group",
		"create",
		"--group", "group/ops",
		"--json",
	}); err != nil {
		t.Fatalf("group create error = %v", err)
	}

	var group GroupRecord
	if err := json.Unmarshal(createStdout.Bytes(), &group); err != nil {
		t.Fatalf("json.Unmarshal(group create) error = %v", err)
	}
	if group.Address != "group/ops" {
		t.Fatalf("group address = %q, want group/ops", group.Address)
	}

	addStdout := &bytes.Buffer{}
	addApp := NewApp(strings.NewReader(""), addStdout, &bytes.Buffer{})
	if err := addApp.Run(context.Background(), []string{
		"--state-dir", stateDir,
		"group",
		"add-member",
		"--group", "group/ops",
		"--person", "alice",
		"--json",
	}); err != nil {
		t.Fatalf("group add-member error = %v", err)
	}

	var membership GroupMembershipRecord
	if err := json.Unmarshal(addStdout.Bytes(), &membership); err != nil {
		t.Fatalf("json.Unmarshal(group add-member) error = %v", err)
	}
	if !membership.Active {
		t.Fatal("membership active = false, want true")
	}

	membersStdout := &bytes.Buffer{}
	membersApp := NewApp(strings.NewReader(""), membersStdout, &bytes.Buffer{})
	if err := membersApp.Run(context.Background(), []string{
		"--state-dir", stateDir,
		"group",
		"members",
		"--group", "group/ops",
		"--json",
	}); err != nil {
		t.Fatalf("group members error = %v", err)
	}

	var memberships []GroupMembershipRecord
	if err := json.Unmarshal(membersStdout.Bytes(), &memberships); err != nil {
		t.Fatalf("json.Unmarshal(group members) error = %v", err)
	}
	if len(memberships) != 1 {
		t.Fatalf("len(group members) = %d, want 1", len(memberships))
	}
	if memberships[0].Person != "alice" {
		t.Fatalf("group members person = %q, want alice", memberships[0].Person)
	}

	inspectStdout := &bytes.Buffer{}
	inspectApp := NewApp(strings.NewReader(""), inspectStdout, &bytes.Buffer{})
	if err := inspectApp.Run(context.Background(), []string{
		"--state-dir", stateDir,
		"address",
		"inspect",
		"--address", "group/ops",
		"--json",
	}); err != nil {
		t.Fatalf("address inspect(group) error = %v", err)
	}

	var inspection AddressInspection
	if err := json.Unmarshal(inspectStdout.Bytes(), &inspection); err != nil {
		t.Fatalf("json.Unmarshal(address inspect group) error = %v", err)
	}
	if inspection.Kind != AddressKindGroup {
		t.Fatalf("group inspect kind = %q, want %q", inspection.Kind, AddressKindGroup)
	}
	if inspection.GroupID == nil || *inspection.GroupID != group.GroupID {
		t.Fatalf("group inspect group_id = %v, want %q", inspection.GroupID, group.GroupID)
	}

	sendApp := NewApp(strings.NewReader("hello endpoint"), &bytes.Buffer{}, &bytes.Buffer{})
	if err := sendApp.Run(context.Background(), []string{
		"--state-dir", stateDir,
		"send",
		"--to", "workflow/personal",
		"--body-file", "-",
	}); err != nil {
		t.Fatalf("send endpoint error = %v", err)
	}

	endpointInspectStdout := &bytes.Buffer{}
	endpointInspectApp := NewApp(strings.NewReader(""), endpointInspectStdout, &bytes.Buffer{})
	if err := endpointInspectApp.Run(context.Background(), []string{
		"--state-dir", stateDir,
		"address",
		"inspect",
		"--address", "workflow/personal",
		"--json",
	}); err != nil {
		t.Fatalf("address inspect(endpoint) error = %v", err)
	}

	var endpointInspection AddressInspection
	if err := json.Unmarshal(endpointInspectStdout.Bytes(), &endpointInspection); err != nil {
		t.Fatalf("json.Unmarshal(address inspect endpoint) error = %v", err)
	}
	if endpointInspection.Kind != AddressKindEndpoint {
		t.Fatalf("endpoint inspect kind = %q, want %q", endpointInspection.Kind, AddressKindEndpoint)
	}
	if endpointInspection.EndpointID == nil || strings.TrimSpace(*endpointInspection.EndpointID) == "" {
		t.Fatalf("endpoint inspect endpoint_id = %v, want non-empty", endpointInspection.EndpointID)
	}

	unboundStdout := &bytes.Buffer{}
	unboundApp := NewApp(strings.NewReader(""), unboundStdout, &bytes.Buffer{})
	if err := unboundApp.Run(context.Background(), []string{
		"--state-dir", stateDir,
		"address",
		"inspect",
		"--address", "group/missing",
		"--json",
	}); err != nil {
		t.Fatalf("address inspect(unbound) error = %v", err)
	}

	var unboundInspection AddressInspection
	if err := json.Unmarshal(unboundStdout.Bytes(), &unboundInspection); err != nil {
		t.Fatalf("json.Unmarshal(address inspect unbound) error = %v", err)
	}
	if unboundInspection.Kind != AddressKindUnbound {
		t.Fatalf("unbound inspect kind = %q, want %q", unboundInspection.Kind, AddressKindUnbound)
	}
}
