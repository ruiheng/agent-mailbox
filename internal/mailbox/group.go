package mailbox

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	AddressKindEndpoint = "endpoint"
	AddressKindGroup    = "group"
	AddressKindUnbound  = "unbound"
)

var (
	ErrGroupNotFound             = errors.New("group not found")
	ErrGroupExists               = errors.New("group already exists")
	ErrAddressReservedByGroup    = errors.New("address already registered as group")
	ErrAddressReservedByEndpoint = errors.New("address already registered as endpoint")
	ErrActiveMembershipExists    = errors.New("active group membership already exists")
	ErrActiveMembershipMissing   = errors.New("active group membership not found")
	ErrActiveSubscriberExists    = errors.New("active group notification subscriber already exists")
	ErrActiveSubscriberMissing   = errors.New("active group notification subscriber not found")
)

type PersonRecord struct {
	PersonID  string `json:"person_id"`
	Person    string `json:"person"`
	CreatedAt string `json:"created_at"`
}

type GroupRecord struct {
	GroupID   string `json:"group_id"`
	Address   string `json:"address"`
	CreatedAt string `json:"created_at"`
}

type GroupMembershipRecord struct {
	MembershipID string  `json:"membership_id"`
	GroupID      string  `json:"group_id"`
	GroupAddress string  `json:"group_address"`
	PersonID     string  `json:"person_id"`
	Person       string  `json:"person"`
	JoinedAt     string  `json:"joined_at"`
	LeftAt       *string `json:"left_at,omitempty"`
	Active       bool    `json:"active"`
}

type GroupNotificationSubscriberRecord struct {
	SubscriberID  string  `json:"subscriber_id"`
	GroupID       string  `json:"group_id"`
	GroupAddress  string  `json:"group_address"`
	NotifyAddress string  `json:"notify_address"`
	Person        string  `json:"person,omitempty"`
	CreatedAt     string  `json:"created_at"`
	RemovedAt     *string `json:"removed_at,omitempty"`
	Active        bool    `json:"active"`
}

type AddressInspection struct {
	Address    string  `json:"address"`
	Kind       string  `json:"kind"`
	EndpointID *string `json:"endpoint_id,omitempty"`
	GroupID    *string `json:"group_id,omitempty"`
}

func (s *Store) CreateGroup(ctx context.Context, address string) (GroupRecord, error) {
	rawAddress := address
	address, err := NormalizeGroupAddress(rawAddress)
	if err != nil {
		if strings.TrimSpace(rawAddress) == "" {
			return GroupRecord{}, errors.New("group address is required")
		}
		return GroupRecord{}, err
	}

	tx, err := s.writeDB.BeginTx(ctx, nil)
	if err != nil {
		return GroupRecord{}, fmt.Errorf("begin group creation transaction: %w", err)
	}
	defer tx.Rollback()

	groupID, err := newPrefixedID("grp")
	if err != nil {
		return GroupRecord{}, err
	}
	createdAt := formatTimestamp(s.now())

	result, err := tx.ExecContext(ctx, `
INSERT INTO groups (group_id, address, created_at, metadata_json)
SELECT ?, ?, ?, '{}'
WHERE NOT EXISTS (
  SELECT 1
  FROM endpoint_addresses
  WHERE address = ?
)
  AND NOT EXISTS (
    SELECT 1
    FROM groups
    WHERE address = ?
  )
`, groupID, address, createdAt, address, address)
	if err != nil {
		return GroupRecord{}, fmt.Errorf("insert group: %w", err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return GroupRecord{}, fmt.Errorf("read group creation rows affected: %w", err)
	}
	if rowsAffected == 0 {
		if endpointID, found, err := s.lookupEndpointID(ctx, tx, address); err != nil {
			return GroupRecord{}, fmt.Errorf("check endpoint collision for group %q: %w", address, err)
		} else if found {
			return GroupRecord{}, fmt.Errorf("group address %q is already bound to endpoint %q: %w", address, endpointID, ErrAddressReservedByEndpoint)
		}
		if existingGroup, found, err := lookupGroupRecord(ctx, tx, address); err != nil {
			return GroupRecord{}, fmt.Errorf("check existing group %q: %w", address, err)
		} else if found {
			return GroupRecord{}, fmt.Errorf("group address %q is already bound to group %q: %w", address, existingGroup.GroupID, ErrGroupExists)
		}
		return GroupRecord{}, fmt.Errorf("create group %q: address reservation conflict", address)
	}

	if err := tx.Commit(); err != nil {
		return GroupRecord{}, fmt.Errorf("commit group creation transaction: %w", err)
	}

	return GroupRecord{
		GroupID:   groupID,
		Address:   address,
		CreatedAt: createdAt,
	}, nil
}

func (s *Store) AddGroupMember(ctx context.Context, groupAddress, person string) (GroupMembershipRecord, error) {
	rawGroupAddress := groupAddress
	groupAddress, err := NormalizeGroupAddress(rawGroupAddress)
	if err != nil {
		if strings.TrimSpace(rawGroupAddress) == "" {
			return GroupMembershipRecord{}, errors.New("group address is required")
		}
		return GroupMembershipRecord{}, err
	}
	person = strings.TrimSpace(person)
	if person == "" {
		return GroupMembershipRecord{}, errors.New("person is required")
	}

	tx, err := s.writeDB.BeginTx(ctx, nil)
	if err != nil {
		return GroupMembershipRecord{}, fmt.Errorf("begin add-member transaction: %w", err)
	}
	defer tx.Rollback()

	group, found, err := lookupGroupRecord(ctx, tx, groupAddress)
	if err != nil {
		return GroupMembershipRecord{}, fmt.Errorf("load group %q: %w", groupAddress, err)
	}
	if !found {
		return GroupMembershipRecord{}, fmt.Errorf("group %q: %w", groupAddress, ErrGroupNotFound)
	}

	personRecord, err := ensurePersonRecord(ctx, tx, formatTimestamp(s.now()), person)
	if err != nil {
		return GroupMembershipRecord{}, err
	}

	membershipID, err := newPrefixedID("gm")
	if err != nil {
		return GroupMembershipRecord{}, err
	}
	joinedAt := formatTimestamp(s.now())

	result, err := tx.ExecContext(ctx, `
INSERT OR IGNORE INTO group_memberships (
  membership_id,
  group_id,
  person_id,
  joined_at,
  left_at,
  metadata_json
) VALUES (?, ?, ?, ?, NULL, '{}')
`, membershipID, group.GroupID, personRecord.PersonID, joinedAt)
	if err != nil {
		return GroupMembershipRecord{}, fmt.Errorf("insert membership for group %q person %q: %w", groupAddress, person, err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return GroupMembershipRecord{}, fmt.Errorf("read add-member rows affected: %w", err)
	}
	if rowsAffected == 0 {
		if _, found, err := lookupActiveGroupMembership(ctx, tx, group.GroupID, personRecord.PersonID); err != nil {
			return GroupMembershipRecord{}, fmt.Errorf("check active membership for group %q person %q: %w", groupAddress, person, err)
		} else if found {
			return GroupMembershipRecord{}, fmt.Errorf("group %q person %q: %w", groupAddress, person, ErrActiveMembershipExists)
		}
		return GroupMembershipRecord{}, fmt.Errorf("add member group %q person %q: membership insert did not apply", groupAddress, person)
	}

	if err := tx.Commit(); err != nil {
		return GroupMembershipRecord{}, fmt.Errorf("commit add-member transaction: %w", err)
	}

	return GroupMembershipRecord{
		MembershipID: membershipID,
		GroupID:      group.GroupID,
		GroupAddress: group.Address,
		PersonID:     personRecord.PersonID,
		Person:       personRecord.Person,
		JoinedAt:     joinedAt,
		Active:       true,
	}, nil
}

func (s *Store) RemoveGroupMember(ctx context.Context, groupAddress, person string) (GroupMembershipRecord, error) {
	rawGroupAddress := groupAddress
	groupAddress, err := NormalizeGroupAddress(rawGroupAddress)
	if err != nil {
		if strings.TrimSpace(rawGroupAddress) == "" {
			return GroupMembershipRecord{}, errors.New("group address is required")
		}
		return GroupMembershipRecord{}, err
	}
	person = strings.TrimSpace(person)
	if person == "" {
		return GroupMembershipRecord{}, errors.New("person is required")
	}

	tx, err := s.writeDB.BeginTx(ctx, nil)
	if err != nil {
		return GroupMembershipRecord{}, fmt.Errorf("begin remove-member transaction: %w", err)
	}
	defer tx.Rollback()

	group, found, err := lookupGroupRecord(ctx, tx, groupAddress)
	if err != nil {
		return GroupMembershipRecord{}, fmt.Errorf("load group %q: %w", groupAddress, err)
	}
	if !found {
		return GroupMembershipRecord{}, fmt.Errorf("group %q: %w", groupAddress, ErrGroupNotFound)
	}

	personRecord, found, err := lookupPersonRecord(ctx, tx, person)
	if err != nil {
		return GroupMembershipRecord{}, fmt.Errorf("load person %q: %w", person, err)
	}
	if !found {
		return GroupMembershipRecord{}, fmt.Errorf("group %q person %q: %w", groupAddress, person, ErrActiveMembershipMissing)
	}

	membership, found, err := lookupActiveGroupMembership(ctx, tx, group.GroupID, personRecord.PersonID)
	if err != nil {
		return GroupMembershipRecord{}, fmt.Errorf("load active membership for group %q person %q: %w", groupAddress, person, err)
	}
	if !found {
		return GroupMembershipRecord{}, fmt.Errorf("group %q person %q: %w", groupAddress, person, ErrActiveMembershipMissing)
	}

	leftAt := formatTimestamp(s.now())
	result, err := tx.ExecContext(ctx, `
UPDATE group_memberships
SET left_at = ?
WHERE membership_id = ?
  AND left_at IS NULL
`, leftAt, membership.MembershipID)
	if err != nil {
		return GroupMembershipRecord{}, fmt.Errorf("close membership %q: %w", membership.MembershipID, err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return GroupMembershipRecord{}, fmt.Errorf("read remove-member rows affected: %w", err)
	}
	if rowsAffected != 1 {
		return GroupMembershipRecord{}, fmt.Errorf("close membership %q: changed while updating", membership.MembershipID)
	}

	if err := tx.Commit(); err != nil {
		return GroupMembershipRecord{}, fmt.Errorf("commit remove-member transaction: %w", err)
	}

	membership.LeftAt = &leftAt
	membership.Active = false
	return membership, nil
}

func (s *Store) ListGroupMembers(ctx context.Context, groupAddress string) ([]GroupMembershipRecord, error) {
	rawGroupAddress := groupAddress
	groupAddress, err := NormalizeGroupAddress(rawGroupAddress)
	if err != nil {
		if strings.TrimSpace(rawGroupAddress) == "" {
			return nil, errors.New("group address is required")
		}
		return nil, err
	}

	group, found, err := lookupGroupRecord(ctx, s.readDB, groupAddress)
	if err != nil {
		return nil, fmt.Errorf("load group %q: %w", groupAddress, err)
	}
	if !found {
		return nil, fmt.Errorf("group %q: %w", groupAddress, ErrGroupNotFound)
	}

	rows, err := s.readDB.QueryContext(ctx, `
SELECT gm.membership_id, gm.person_id, p.person, gm.joined_at, gm.left_at
FROM group_memberships AS gm
JOIN persons AS p ON p.person_id = gm.person_id
WHERE gm.group_id = ?
ORDER BY gm.joined_at ASC, gm.membership_id ASC
`, group.GroupID)
	if err != nil {
		return nil, fmt.Errorf("list members for group %q: %w", groupAddress, err)
	}
	defer rows.Close()

	var memberships []GroupMembershipRecord
	for rows.Next() {
		var record GroupMembershipRecord
		var leftAt sql.NullString
		if err := rows.Scan(&record.MembershipID, &record.PersonID, &record.Person, &record.JoinedAt, &leftAt); err != nil {
			return nil, fmt.Errorf("scan member for group %q: %w", groupAddress, err)
		}
		record.GroupID = group.GroupID
		record.GroupAddress = group.Address
		record.Active = !leftAt.Valid
		if leftAt.Valid {
			record.LeftAt = &leftAt.String
		}
		memberships = append(memberships, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate members for group %q: %w", groupAddress, err)
	}
	return memberships, nil
}

func (s *Store) ListGroups(ctx context.Context) ([]GroupRecord, error) {
	rows, err := s.readDB.QueryContext(ctx, `
SELECT group_id, address, created_at
FROM groups
ORDER BY created_at ASC, address ASC
`)
	if err != nil {
		return nil, fmt.Errorf("query groups: %w", err)
	}
	defer rows.Close()

	records := make([]GroupRecord, 0)
	for rows.Next() {
		var record GroupRecord
		if err := rows.Scan(&record.GroupID, &record.Address, &record.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan group row: %w", err)
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate groups: %w", err)
	}
	return records, nil
}

func (s *Store) AddGroupNotificationSubscriber(ctx context.Context, groupAddress, notifyAddress, person string) (GroupNotificationSubscriberRecord, error) {
	rawGroupAddress := groupAddress
	groupAddress, err := NormalizeGroupAddress(rawGroupAddress)
	if err != nil {
		if strings.TrimSpace(rawGroupAddress) == "" {
			return GroupNotificationSubscriberRecord{}, errors.New("group address is required")
		}
		return GroupNotificationSubscriberRecord{}, err
	}
	rawNotifyAddress := notifyAddress
	notifyAddress, err = NormalizeAddress(rawNotifyAddress)
	if err != nil {
		if strings.TrimSpace(rawNotifyAddress) == "" {
			return GroupNotificationSubscriberRecord{}, errors.New("notify address is required")
		}
		return GroupNotificationSubscriberRecord{}, err
	}
	if IsGroupAddress(notifyAddress) {
		return GroupNotificationSubscriberRecord{}, fmt.Errorf("notify address %q uses reserved group/ prefix", notifyAddress)
	}
	person = strings.TrimSpace(person)

	tx, err := s.writeDB.BeginTx(ctx, nil)
	if err != nil {
		return GroupNotificationSubscriberRecord{}, fmt.Errorf("begin add-subscriber transaction: %w", err)
	}
	defer tx.Rollback()

	group, found, err := lookupGroupRecord(ctx, tx, groupAddress)
	if err != nil {
		return GroupNotificationSubscriberRecord{}, fmt.Errorf("load group %q: %w", groupAddress, err)
	}
	if !found {
		return GroupNotificationSubscriberRecord{}, fmt.Errorf("group %q: %w", groupAddress, ErrGroupNotFound)
	}

	subscriberID, err := newPrefixedID("gns")
	if err != nil {
		return GroupNotificationSubscriberRecord{}, err
	}
	createdAt := formatTimestamp(s.now())

	result, err := tx.ExecContext(ctx, `
INSERT OR IGNORE INTO group_notification_subscribers (
  subscriber_id,
  group_id,
  notify_address,
  person,
  created_at,
  removed_at,
  metadata_json
) VALUES (?, ?, ?, ?, ?, NULL, '{}')
`, subscriberID, group.GroupID, notifyAddress, person, createdAt)
	if err != nil {
		return GroupNotificationSubscriberRecord{}, fmt.Errorf("insert subscriber for group %q notify %q: %w", groupAddress, notifyAddress, err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return GroupNotificationSubscriberRecord{}, fmt.Errorf("read add-subscriber rows affected: %w", err)
	}
	if rowsAffected == 0 {
		if _, found, err := lookupActiveGroupNotificationSubscriber(ctx, tx, group.GroupID, notifyAddress); err != nil {
			return GroupNotificationSubscriberRecord{}, fmt.Errorf("check active subscriber for group %q notify %q: %w", groupAddress, notifyAddress, err)
		} else if found {
			return GroupNotificationSubscriberRecord{}, fmt.Errorf("group %q notify %q: %w", groupAddress, notifyAddress, ErrActiveSubscriberExists)
		}
		return GroupNotificationSubscriberRecord{}, fmt.Errorf("add subscriber group %q notify %q: subscriber insert did not apply", groupAddress, notifyAddress)
	}

	if err := tx.Commit(); err != nil {
		return GroupNotificationSubscriberRecord{}, fmt.Errorf("commit add-subscriber transaction: %w", err)
	}

	return GroupNotificationSubscriberRecord{
		SubscriberID:  subscriberID,
		GroupID:       group.GroupID,
		GroupAddress:  group.Address,
		NotifyAddress: notifyAddress,
		Person:        person,
		CreatedAt:     createdAt,
		Active:        true,
	}, nil
}

func (s *Store) RemoveGroupNotificationSubscriber(ctx context.Context, groupAddress, notifyAddress string) (GroupNotificationSubscriberRecord, error) {
	rawGroupAddress := groupAddress
	groupAddress, err := NormalizeGroupAddress(rawGroupAddress)
	if err != nil {
		if strings.TrimSpace(rawGroupAddress) == "" {
			return GroupNotificationSubscriberRecord{}, errors.New("group address is required")
		}
		return GroupNotificationSubscriberRecord{}, err
	}
	rawNotifyAddress := notifyAddress
	notifyAddress, err = NormalizeAddress(rawNotifyAddress)
	if err != nil {
		if strings.TrimSpace(rawNotifyAddress) == "" {
			return GroupNotificationSubscriberRecord{}, errors.New("notify address is required")
		}
		return GroupNotificationSubscriberRecord{}, err
	}

	tx, err := s.writeDB.BeginTx(ctx, nil)
	if err != nil {
		return GroupNotificationSubscriberRecord{}, fmt.Errorf("begin remove-subscriber transaction: %w", err)
	}
	defer tx.Rollback()

	group, found, err := lookupGroupRecord(ctx, tx, groupAddress)
	if err != nil {
		return GroupNotificationSubscriberRecord{}, fmt.Errorf("load group %q: %w", groupAddress, err)
	}
	if !found {
		return GroupNotificationSubscriberRecord{}, fmt.Errorf("group %q: %w", groupAddress, ErrGroupNotFound)
	}

	subscriber, found, err := lookupActiveGroupNotificationSubscriber(ctx, tx, group.GroupID, notifyAddress)
	if err != nil {
		return GroupNotificationSubscriberRecord{}, fmt.Errorf("load active subscriber for group %q notify %q: %w", groupAddress, notifyAddress, err)
	}
	if !found {
		return GroupNotificationSubscriberRecord{}, fmt.Errorf("group %q notify %q: %w", groupAddress, notifyAddress, ErrActiveSubscriberMissing)
	}

	removedAt := formatTimestamp(s.now())
	result, err := tx.ExecContext(ctx, `
UPDATE group_notification_subscribers
SET removed_at = ?
WHERE subscriber_id = ?
  AND removed_at IS NULL
`, removedAt, subscriber.SubscriberID)
	if err != nil {
		return GroupNotificationSubscriberRecord{}, fmt.Errorf("close subscriber %q: %w", subscriber.SubscriberID, err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return GroupNotificationSubscriberRecord{}, fmt.Errorf("read remove-subscriber rows affected: %w", err)
	}
	if rowsAffected != 1 {
		return GroupNotificationSubscriberRecord{}, fmt.Errorf("close subscriber %q: changed while updating", subscriber.SubscriberID)
	}

	if err := tx.Commit(); err != nil {
		return GroupNotificationSubscriberRecord{}, fmt.Errorf("commit remove-subscriber transaction: %w", err)
	}

	subscriber.RemovedAt = &removedAt
	subscriber.Active = false
	return subscriber, nil
}

func (s *Store) ListGroupNotificationSubscribers(ctx context.Context, groupAddress string) ([]GroupNotificationSubscriberRecord, error) {
	rawGroupAddress := groupAddress
	groupAddress, err := NormalizeGroupAddress(rawGroupAddress)
	if err != nil {
		if strings.TrimSpace(rawGroupAddress) == "" {
			return nil, errors.New("group address is required")
		}
		return nil, err
	}

	group, found, err := lookupGroupRecord(ctx, s.readDB, groupAddress)
	if err != nil {
		return nil, fmt.Errorf("load group %q: %w", groupAddress, err)
	}
	if !found {
		return nil, fmt.Errorf("group %q: %w", groupAddress, ErrGroupNotFound)
	}

	subscribers, err := listActiveGroupNotificationSubscribers(ctx, s.readDB, group.GroupID)
	if err != nil {
		return nil, fmt.Errorf("list subscribers for group %q: %w", groupAddress, err)
	}
	for i := range subscribers {
		subscribers[i].GroupAddress = group.Address
	}
	return subscribers, nil
}

func (s *Store) InspectAddress(ctx context.Context, address string) (AddressInspection, error) {
	rawAddress := address
	address, err := NormalizeAddress(rawAddress)
	if err != nil {
		if strings.TrimSpace(rawAddress) == "" {
			return AddressInspection{}, errors.New("address is required")
		}
		return AddressInspection{}, err
	}

	endpointID, endpointFound, err := s.lookupEndpointID(ctx, s.readDB, address)
	if err != nil {
		return AddressInspection{}, fmt.Errorf("inspect endpoint address %q: %w", address, err)
	}
	groupRecord, groupFound, err := lookupGroupRecord(ctx, s.readDB, address)
	if err != nil {
		return AddressInspection{}, fmt.Errorf("inspect group address %q: %w", address, err)
	}
	if endpointFound && groupFound {
		return AddressInspection{}, fmt.Errorf("address %q is bound as both endpoint %q and group %q", address, endpointID, groupRecord.GroupID)
	}
	if endpointFound {
		return AddressInspection{
			Address:    address,
			Kind:       AddressKindEndpoint,
			EndpointID: &endpointID,
		}, nil
	}
	if groupFound {
		return AddressInspection{
			Address: address,
			Kind:    AddressKindGroup,
			GroupID: &groupRecord.GroupID,
		}, nil
	}
	return AddressInspection{
		Address: address,
		Kind:    AddressKindUnbound,
	}, nil
}

func (s *Store) ListGroupMessages(ctx context.Context, params GroupListParams) ([]GroupListedMessage, error) {
	scope, err := s.resolveGroup(ctx, s.readDB, params.Address, params.Person)
	if err != nil {
		return nil, err
	}
	records, err := s.listGroupMessages(ctx, s.readDB, scope, false, 0)
	if err != nil {
		return nil, err
	}

	messages := make([]GroupListedMessage, 0, len(records))
	for _, record := range records {
		messages = append(messages, buildGroupListedMessage(scope.viewer, record))
	}
	return messages, nil
}

func (s *Store) ListGroupTranscript(ctx context.Context, params GroupTranscriptParams) ([]GroupTranscriptMessage, error) {
	rawAddress := params.Address
	groupAddress, err := NormalizeGroupAddress(rawAddress)
	if err != nil {
		if strings.TrimSpace(rawAddress) == "" {
			return nil, errors.New("group address is required")
		}
		return nil, err
	}

	group, found, err := lookupGroupRecord(ctx, s.readDB, groupAddress)
	if err != nil {
		return nil, fmt.Errorf("load group %q: %w", groupAddress, err)
	}
	if !found {
		return nil, fmt.Errorf("group %q: %w", groupAddress, ErrGroupNotFound)
	}

	rows, err := s.readDB.QueryContext(ctx, `
SELECT
  gm.message_id,
  m.forwarded_message_id,
  m.forwarded_from_address,
  m.sender_endpoint_id,
  (
    SELECT sender_ea.address
    FROM endpoint_addresses AS sender_ea
    WHERE sender_ea.endpoint_id = m.sender_endpoint_id
    ORDER BY sender_ea.created_at ASC, sender_ea.address ASC
    LIMIT 1
  ) AS sender_address,
  gm.created_at,
  m.subject,
  m.content_type,
  m.schema_version,
  m.body_blob_ref,
  m.body_size,
  m.body_sha256,
  (
    SELECT COUNT(*)
    FROM group_message_eligibility AS ge
    JOIN group_reads AS gr
      ON gr.message_id = ge.message_id
     AND gr.person_id = ge.person_id
    WHERE ge.message_id = gm.message_id
  ) AS read_count,
  gm.eligible_count
FROM group_messages AS gm
JOIN messages AS m ON m.message_id = gm.message_id
WHERE gm.group_id = ?
ORDER BY gm.created_at ASC, gm.message_id ASC
`, group.GroupID)
	if err != nil {
		return nil, fmt.Errorf("query group transcript for %q: %w", group.Address, err)
	}
	defer rows.Close()

	messages := make([]GroupTranscriptMessage, 0)
	for rows.Next() {
		var message GroupTranscriptMessage
		var forwardedMessageID sql.NullString
		var forwardedFromAddress sql.NullString
		var senderEndpointID sql.NullString
		var senderAddress sql.NullString
		var bodyBlobRef string
		var bodySize int64
		var bodySHA256 string
		if err := rows.Scan(
			&message.MessageID,
			&forwardedMessageID,
			&forwardedFromAddress,
			&senderEndpointID,
			&senderAddress,
			&message.MessageCreatedAt,
			&message.Subject,
			&message.ContentType,
			&message.SchemaVersion,
			&bodyBlobRef,
			&bodySize,
			&bodySHA256,
			&message.ReadCount,
			&message.EligibleCount,
		); err != nil {
			return nil, fmt.Errorf("scan group transcript row: %w", err)
		}
		body, err := s.readBlob(bodyBlobRef, bodySize, bodySHA256)
		if err != nil {
			return nil, err
		}
		message.GroupID = group.GroupID
		message.GroupAddress = group.Address
		message.Body = string(body)
		message.DisplaySender = "unknown"
		if forwardedMessageID.Valid {
			message.ForwardedMessageID = &forwardedMessageID.String
		}
		if forwardedFromAddress.Valid {
			message.ForwardedFromAddress = &forwardedFromAddress.String
			message.DisplaySender = forwardedFromAddress.String
		}
		if senderEndpointID.Valid {
			message.SenderEndpointID = &senderEndpointID.String
		}
		if senderAddress.Valid {
			message.SenderAddress = &senderAddress.String
			if message.DisplaySender == "unknown" {
				message.DisplaySender = senderAddress.String
			}
		}
		messages = append(messages, message)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate group transcript: %w", err)
	}
	return messages, nil
}

func (s *Store) WaitGroupMessage(ctx context.Context, params GroupWaitParams) (GroupListedMessage, error) {
	if params.Timeout < 0 {
		return GroupListedMessage{}, errors.New("--timeout must be greater than or equal to 0")
	}

	var deadline time.Time
	if params.Timeout > 0 {
		deadline = time.Now().Add(params.Timeout)
	}

	delay := initialPollDelay
	for {
		attemptCtx := ctx
		cancel := func() {}
		if !deadline.IsZero() {
			attemptCtx, cancel = context.WithDeadline(ctx, deadline)
		}

		message, err := s.waitGroupMessageOnce(attemptCtx, params)
		cancel()
		if err == nil {
			return message, nil
		}
		if !deadline.IsZero() && errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
			return GroupListedMessage{}, ErrNoMessage
		}
		if !errors.Is(err, ErrNoMessage) {
			return GroupListedMessage{}, err
		}
		if !deadline.IsZero() {
			remaining := time.Until(deadline)
			if remaining <= 0 {
				return GroupListedMessage{}, ErrNoMessage
			}
			if delay > remaining {
				delay = remaining
			}
		}

		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return GroupListedMessage{}, ctx.Err()
		case <-timer.C:
		}

		if delay < maxPollDelay {
			delay *= 2
			if delay > maxPollDelay {
				delay = maxPollDelay
			}
		}
	}
}

func (s *Store) waitGroupMessageOnce(ctx context.Context, params GroupWaitParams) (GroupListedMessage, error) {
	scope, err := s.resolveGroup(ctx, s.readDB, params.Address, params.Person)
	if err != nil {
		return GroupListedMessage{}, err
	}
	records, err := s.listGroupMessages(ctx, s.readDB, scope, true, 1)
	if err != nil {
		return GroupListedMessage{}, err
	}
	if len(records) == 0 {
		return GroupListedMessage{}, ErrNoMessage
	}
	return buildGroupListedMessage(scope.viewer, records[0]), nil
}

func (s *Store) ReceiveGroupMessage(ctx context.Context, params GroupReceiveParams) (GroupReceivedMessage, error) {
	for attempt := 1; ; attempt++ {
		message, err := s.receiveGroupMessageOnce(ctx, params)
		if err == nil {
			return message, nil
		}
		if !isSQLiteBusy(err) {
			return GroupReceivedMessage{}, err
		}
		if attempt > maxClaimRetryCount {
			return GroupReceivedMessage{}, &ClaimContentionError{
				Attempts: attempt,
				Cause:    err,
			}
		}
		if err := waitForClaimRetry(ctx); err != nil {
			return GroupReceivedMessage{}, err
		}
	}
}

func (s *Store) receiveGroupMessageOnce(ctx context.Context, params GroupReceiveParams) (GroupReceivedMessage, error) {
	for {
		tx, err := s.writeDB.BeginTx(ctx, nil)
		if err != nil {
			return GroupReceivedMessage{}, fmt.Errorf("begin group receive transaction: %w", err)
		}

		scope, err := s.resolveGroup(ctx, tx, params.Address, params.Person)
		if err != nil {
			_ = tx.Rollback()
			return GroupReceivedMessage{}, err
		}
		if scope.viewer.PersonID == "" {
			_ = tx.Rollback()
			return GroupReceivedMessage{}, ErrNoMessage
		}

		records, err := s.listGroupMessages(ctx, tx, scope, true, 1)
		if err != nil {
			_ = tx.Rollback()
			return GroupReceivedMessage{}, err
		}
		if len(records) == 0 {
			_ = tx.Rollback()
			return GroupReceivedMessage{}, ErrNoMessage
		}
		record := records[0]

		body, err := s.readBlob(record.BodyBlobRef, record.BodySize, record.BodySHA256)
		if err != nil {
			_ = tx.Rollback()
			return GroupReceivedMessage{}, err
		}

		firstReadAt := formatTimestamp(s.now())
		result, err := tx.ExecContext(ctx, `
INSERT OR IGNORE INTO group_reads (message_id, person_id, first_read_at)
VALUES (?, ?, ?)
`, record.MessageID, scope.viewer.PersonID, firstReadAt)
		if err != nil {
			_ = tx.Rollback()
			return GroupReceivedMessage{}, fmt.Errorf("mark group message read %q for %q: %w", record.MessageID, scope.viewer.Person, err)
		}
		rowsAffected, err := result.RowsAffected()
		if err != nil {
			_ = tx.Rollback()
			return GroupReceivedMessage{}, fmt.Errorf("read group message read rows affected for %q: %w", record.MessageID, err)
		}
		if rowsAffected == 0 {
			_ = tx.Rollback()
			continue
		}

		if err := tx.Commit(); err != nil {
			_ = tx.Rollback()
			return GroupReceivedMessage{}, fmt.Errorf("commit group receive transaction: %w", err)
		}

		record.FirstReadAt = sql.NullString{String: firstReadAt, Valid: true}
		if record.ViewerEligible != 0 {
			record.ReadCount++
		}
		return buildGroupReceivedMessage(scope.viewer, record, string(body)), nil
	}
}

func buildGroupListedMessage(viewer groupViewerState, record groupMessageRecord) GroupListedMessage {
	message := GroupListedMessage{
		MessageID:        record.MessageID,
		GroupID:          viewer.Group.GroupID,
		GroupAddress:     viewer.Group.Address,
		Person:           viewer.Person,
		MessageCreatedAt: record.MessageCreatedAt,
		Subject:          record.Subject,
		ContentType:      record.ContentType,
		SchemaVersion:    record.SchemaVersion,
		Read:             record.FirstReadAt.Valid,
		ReadCount:        record.ReadCount,
		EligibleCount:    record.EligibleCount,
	}
	if record.ForwardedMessageID.Valid {
		message.ForwardedMessageID = &record.ForwardedMessageID.String
	}
	if record.ForwardedFromAddress.Valid {
		message.ForwardedFromAddress = &record.ForwardedFromAddress.String
	}
	if record.SenderEndpointID.Valid {
		message.SenderEndpointID = &record.SenderEndpointID.String
	}
	if record.FirstReadAt.Valid {
		message.FirstReadAt = &record.FirstReadAt.String
	}
	return message
}

func buildGroupReceivedMessage(viewer groupViewerState, record groupMessageRecord, body string) GroupReceivedMessage {
	message := GroupReceivedMessage{
		MessageID:        record.MessageID,
		GroupID:          viewer.Group.GroupID,
		GroupAddress:     viewer.Group.Address,
		Person:           viewer.Person,
		MessageCreatedAt: record.MessageCreatedAt,
		Subject:          record.Subject,
		ContentType:      record.ContentType,
		SchemaVersion:    record.SchemaVersion,
		BodyBlobRef:      record.BodyBlobRef,
		BodySize:         record.BodySize,
		BodySHA256:       record.BodySHA256,
		Body:             body,
		ReadCount:        record.ReadCount,
		EligibleCount:    record.EligibleCount,
		FirstReadAt:      record.FirstReadAt.String,
	}
	if record.ForwardedMessageID.Valid {
		message.ForwardedMessageID = &record.ForwardedMessageID.String
	}
	if record.ForwardedFromAddress.Valid {
		message.ForwardedFromAddress = &record.ForwardedFromAddress.String
	}
	if record.SenderEndpointID.Valid {
		message.SenderEndpointID = &record.SenderEndpointID.String
	}
	return message
}

func lookupLatestGroupMembershipByPerson(ctx context.Context, querier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, groupID, person string) (GroupMembershipRecord, bool, error) {
	var membership GroupMembershipRecord
	var leftAt sql.NullString
	err := querier.QueryRowContext(ctx, `
SELECT gm.membership_id, gm.person_id, p.person, gm.joined_at, gm.left_at, g.address
FROM group_memberships AS gm
JOIN persons AS p ON p.person_id = gm.person_id
JOIN groups AS g ON g.group_id = gm.group_id
WHERE gm.group_id = ?
  AND p.person = ?
ORDER BY gm.joined_at DESC, gm.membership_id DESC
LIMIT 1
`, groupID, person).Scan(
		&membership.MembershipID,
		&membership.PersonID,
		&membership.Person,
		&membership.JoinedAt,
		&leftAt,
		&membership.GroupAddress,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return GroupMembershipRecord{}, false, nil
	}
	if err != nil {
		return GroupMembershipRecord{}, false, err
	}
	membership.GroupID = groupID
	membership.Active = !leftAt.Valid
	if leftAt.Valid {
		membership.LeftAt = &leftAt.String
	}
	return membership, true, nil
}

func lookupGroupRecord(ctx context.Context, querier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, address string) (GroupRecord, bool, error) {
	var group GroupRecord
	err := querier.QueryRowContext(ctx, `
SELECT group_id, address, created_at
FROM groups
WHERE address = ?
`, address).Scan(&group.GroupID, &group.Address, &group.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return GroupRecord{}, false, nil
	}
	if err != nil {
		return GroupRecord{}, false, err
	}
	return group, true, nil
}

func lookupPersonRecord(ctx context.Context, querier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, person string) (PersonRecord, bool, error) {
	var record PersonRecord
	err := querier.QueryRowContext(ctx, `
SELECT person_id, person, created_at
FROM persons
WHERE person = ?
`, person).Scan(&record.PersonID, &record.Person, &record.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return PersonRecord{}, false, nil
	}
	if err != nil {
		return PersonRecord{}, false, err
	}
	return record, true, nil
}

func ensurePersonRecord(ctx context.Context, tx *sql.Tx, createdAt string, person string) (PersonRecord, error) {
	record, found, err := lookupPersonRecord(ctx, tx, person)
	if err != nil {
		return PersonRecord{}, fmt.Errorf("read existing person %q: %w", person, err)
	}
	if found {
		return record, nil
	}

	personID, err := newPrefixedID("prs")
	if err != nil {
		return PersonRecord{}, err
	}
	result, err := tx.ExecContext(ctx, `
INSERT OR IGNORE INTO persons (person_id, person, created_at, metadata_json)
VALUES (?, ?, ?, '{}')
`, personID, person, createdAt)
	if err != nil {
		return PersonRecord{}, fmt.Errorf("insert person %q: %w", person, err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return PersonRecord{}, fmt.Errorf("read person insert rows affected: %w", err)
	}
	if rowsAffected == 0 {
		record, found, err = lookupPersonRecord(ctx, tx, person)
		if err != nil {
			return PersonRecord{}, fmt.Errorf("reload person %q after conflict: %w", person, err)
		}
		if !found {
			return PersonRecord{}, fmt.Errorf("reload person %q after conflict: not found", person)
		}
		return record, nil
	}

	return PersonRecord{
		PersonID:  personID,
		Person:    person,
		CreatedAt: createdAt,
	}, nil
}

func lookupActiveGroupMembership(ctx context.Context, querier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, groupID, personID string) (GroupMembershipRecord, bool, error) {
	var record GroupMembershipRecord
	var leftAt sql.NullString
	err := querier.QueryRowContext(ctx, `
SELECT gm.membership_id, gm.joined_at, gm.left_at, p.person, g.address
FROM group_memberships AS gm
JOIN persons AS p ON p.person_id = gm.person_id
JOIN groups AS g ON g.group_id = gm.group_id
WHERE gm.group_id = ?
  AND gm.person_id = ?
  AND gm.left_at IS NULL
`, groupID, personID).Scan(&record.MembershipID, &record.JoinedAt, &leftAt, &record.Person, &record.GroupAddress)
	if errors.Is(err, sql.ErrNoRows) {
		return GroupMembershipRecord{}, false, nil
	}
	if err != nil {
		return GroupMembershipRecord{}, false, err
	}
	record.GroupID = groupID
	record.PersonID = personID
	record.Active = !leftAt.Valid
	if leftAt.Valid {
		record.LeftAt = &leftAt.String
	}
	return record, true, nil
}

func lookupActiveGroupNotificationSubscriber(ctx context.Context, querier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, groupID, notifyAddress string) (GroupNotificationSubscriberRecord, bool, error) {
	var record GroupNotificationSubscriberRecord
	err := querier.QueryRowContext(ctx, `
SELECT gns.subscriber_id, gns.notify_address, gns.person, gns.created_at, g.address
FROM group_notification_subscribers AS gns
JOIN groups AS g ON g.group_id = gns.group_id
WHERE gns.group_id = ?
  AND gns.notify_address = ?
  AND gns.removed_at IS NULL
`, groupID, notifyAddress).Scan(&record.SubscriberID, &record.NotifyAddress, &record.Person, &record.CreatedAt, &record.GroupAddress)
	if errors.Is(err, sql.ErrNoRows) {
		return GroupNotificationSubscriberRecord{}, false, nil
	}
	if err != nil {
		return GroupNotificationSubscriberRecord{}, false, err
	}
	record.GroupID = groupID
	record.Active = true
	return record, true, nil
}

func listActiveGroupMemberships(ctx context.Context, querier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, groupID string) ([]GroupMembershipRecord, error) {
	rows, err := querier.QueryContext(ctx, `
SELECT gm.membership_id, gm.person_id, p.person, gm.joined_at, g.address
FROM group_memberships AS gm
JOIN persons AS p ON p.person_id = gm.person_id
JOIN groups AS g ON g.group_id = gm.group_id
WHERE gm.group_id = ?
  AND gm.left_at IS NULL
ORDER BY gm.joined_at ASC, gm.membership_id ASC
	`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var memberships []GroupMembershipRecord
	for rows.Next() {
		var membership GroupMembershipRecord
		if err := rows.Scan(&membership.MembershipID, &membership.PersonID, &membership.Person, &membership.JoinedAt, &membership.GroupAddress); err != nil {
			return nil, err
		}
		membership.GroupID = groupID
		membership.Active = true
		memberships = append(memberships, membership)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return memberships, nil
}

func listActiveGroupNotificationSubscribers(ctx context.Context, querier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, groupID string) ([]GroupNotificationSubscriberRecord, error) {
	rows, err := querier.QueryContext(ctx, `
SELECT subscriber_id, notify_address, person, created_at
FROM group_notification_subscribers
WHERE group_id = ?
  AND removed_at IS NULL
ORDER BY created_at ASC, subscriber_id ASC
`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var subscribers []GroupNotificationSubscriberRecord
	for rows.Next() {
		var subscriber GroupNotificationSubscriberRecord
		if err := rows.Scan(&subscriber.SubscriberID, &subscriber.NotifyAddress, &subscriber.Person, &subscriber.CreatedAt); err != nil {
			return nil, err
		}
		subscriber.GroupID = groupID
		subscriber.Active = true
		subscribers = append(subscribers, subscriber)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return subscribers, nil
}
