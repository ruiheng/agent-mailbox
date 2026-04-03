package mailbox

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

const claimableDeliveryFilter = `(
    (d.state = 'queued' AND d.visible_at <= ?)
    OR
    (d.state = 'leased' AND d.lease_expires_at IS NOT NULL AND d.lease_expires_at <= ?)
  )`

const claimableDeliveryEligibleAtExpr = `CASE
    WHEN d.state = 'queued' THEN d.visible_at
    WHEN d.state = 'leased' THEN d.lease_expires_at
  END`

type rowQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type rowsQuerier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

type resolvedRecipient struct {
	Address    string
	EndpointID string
}

type personalAvailabilityScope struct {
	addressByEndpointID  map[string]string
	recipientEndpointIDs []string
}

type personalDeliveryRecord struct {
	DeliveryID          string
	MessageID           string
	RecipientAddress    string
	RecipientEndpointID string
	SenderEndpointID    sql.NullString
	State               string
	VisibleAt           string
	AckedAt             sql.NullString
	AttemptCount        int
	MessageCreatedAt    string
	Subject             string
	ContentType         string
	SchemaVersion       string
	BodyBlobRef         string
	BodySize            int64
	BodySHA256          string
}

type groupAvailabilityScope struct {
	viewer groupViewerState
}

type groupViewerState struct {
	Group            GroupRecord
	Person           string
	PersonID         string
	VisibilityCutoff *string
}

type groupMessageRecord struct {
	MessageID        string
	SenderEndpointID sql.NullString
	MessageCreatedAt string
	Subject          string
	ContentType      string
	SchemaVersion    string
	BodyBlobRef      string
	BodySize         int64
	BodySHA256       string
	FirstReadAt      sql.NullString
	ViewerEligible   int
	ReadCount        int
	EligibleCount    int
}

type availabilityOwner struct {
	store *Store
}

func (s *Store) availability() availabilityOwner {
	return availabilityOwner{store: s}
}

func (o availabilityOwner) resolvePersonal(ctx context.Context, querier rowQuerier, addresses []string) (personalAvailabilityScope, error) {
	recipients := make([]resolvedRecipient, 0, len(addresses))
	seenEndpointIDs := make(map[string]struct{}, len(addresses))
	for _, address := range addresses {
		group, found, err := lookupGroupRecord(ctx, querier, address)
		if err != nil {
			return personalAvailabilityScope{}, fmt.Errorf("resolve recipient address %q: %w", address, err)
		}
		if found {
			return personalAvailabilityScope{}, fmt.Errorf("recipient address %q is reserved by group %q: %w", address, group.GroupID, ErrAddressReservedByGroup)
		}

		endpointID, found, err := o.store.lookupEndpointID(ctx, querier, address)
		if err != nil {
			return personalAvailabilityScope{}, fmt.Errorf("resolve recipient address %q: %w", address, err)
		}
		if !found {
			continue
		}
		if _, exists := seenEndpointIDs[endpointID]; exists {
			continue
		}
		seenEndpointIDs[endpointID] = struct{}{}
		recipients = append(recipients, resolvedRecipient{
			Address:    address,
			EndpointID: endpointID,
		})
	}

	scope := personalAvailabilityScope{
		addressByEndpointID:  make(map[string]string, len(recipients)),
		recipientEndpointIDs: make([]string, 0, len(recipients)),
	}
	for _, recipient := range recipients {
		scope.addressByEndpointID[recipient.EndpointID] = recipient.Address
		scope.recipientEndpointIDs = append(scope.recipientEndpointIDs, recipient.EndpointID)
	}
	return scope, nil
}

func (scope personalAvailabilityScope) empty() bool {
	return len(scope.recipientEndpointIDs) == 0
}

func (o availabilityOwner) claimablePersonalDelivery(ctx context.Context, querier rowQuerier, scope personalAvailabilityScope, nowText string) (personalDeliveryRecord, error) {
	if scope.empty() {
		return personalDeliveryRecord{}, sql.ErrNoRows
	}

	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(scope.recipientEndpointIDs)), ",")
	args := make([]any, 0, len(scope.recipientEndpointIDs)+2)
	for _, recipientEndpointID := range scope.recipientEndpointIDs {
		args = append(args, recipientEndpointID)
	}
	args = append(args, nowText, nowText)

	var record personalDeliveryRecord
	err := querier.QueryRowContext(ctx, fmt.Sprintf(`
SELECT
  d.delivery_id,
  d.message_id,
  d.recipient_endpoint_id,
  m.sender_endpoint_id,
  d.state,
  d.visible_at,
  d.attempt_count,
  m.created_at,
  m.subject,
  m.content_type,
  m.schema_version,
  m.body_blob_ref,
  m.body_size,
  m.body_sha256
FROM deliveries AS d
JOIN messages AS m ON m.message_id = d.message_id
WHERE d.recipient_endpoint_id IN (%s)
  AND %s
ORDER BY d.visible_at ASC, m.created_at ASC, d.delivery_id ASC
LIMIT 1
`, placeholders, claimableDeliveryFilter), args...).Scan(
		&record.DeliveryID,
		&record.MessageID,
		&record.RecipientEndpointID,
		&record.SenderEndpointID,
		&record.State,
		&record.VisibleAt,
		&record.AttemptCount,
		&record.MessageCreatedAt,
		&record.Subject,
		&record.ContentType,
		&record.SchemaVersion,
		&record.BodyBlobRef,
		&record.BodySize,
		&record.BodySHA256,
	)
	if err != nil {
		return personalDeliveryRecord{}, err
	}
	record.RecipientAddress = scope.addressByEndpointID[record.RecipientEndpointID]
	return record, nil
}

func (o availabilityOwner) listPersonalDeliveries(ctx context.Context, querier rowsQuerier, scope personalAvailabilityScope, state, nowText string) ([]ListedDelivery, error) {
	if scope.empty() {
		return []ListedDelivery{}, nil
	}

	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(scope.recipientEndpointIDs)), ",")
	args := make([]any, 0, len(scope.recipientEndpointIDs)+1)
	for _, recipientEndpointID := range scope.recipientEndpointIDs {
		args = append(args, recipientEndpointID)
	}

	query := fmt.Sprintf(`
SELECT
  d.delivery_id,
  d.message_id,
  d.recipient_endpoint_id,
  m.sender_endpoint_id,
  d.state,
  d.visible_at,
  d.acked_at,
  m.created_at,
  m.subject,
  m.content_type,
  m.schema_version,
  m.body_blob_ref,
  m.body_size,
  m.body_sha256
FROM deliveries AS d
JOIN messages AS m ON m.message_id = d.message_id
WHERE d.recipient_endpoint_id IN (%s)
`, placeholders)
	if state == "" {
		query += `
  AND d.state = 'queued'
  AND d.visible_at <= ?
`
		args = append(args, nowText)
	} else {
		query += `
  AND d.state = ?
`
		args = append(args, state)
	}
	query += `
ORDER BY d.visible_at ASC, m.created_at ASC, d.delivery_id ASC
`

	rows, err := querier.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query deliveries: %w", err)
	}
	defer rows.Close()

	deliveries := make([]ListedDelivery, 0)
	for rows.Next() {
		var delivery ListedDelivery
		var ackedAt sql.NullString
		var senderID sql.NullString
		if err := rows.Scan(
			&delivery.DeliveryID,
			&delivery.MessageID,
			&delivery.RecipientEndpointID,
			&senderID,
			&delivery.State,
			&delivery.VisibleAt,
			&ackedAt,
			&delivery.MessageCreatedAt,
			&delivery.Subject,
			&delivery.ContentType,
			&delivery.SchemaVersion,
			&delivery.BodyBlobRef,
			&delivery.BodySize,
			&delivery.BodySHA256,
		); err != nil {
			return nil, fmt.Errorf("scan delivery row: %w", err)
		}
		delivery.RecipientAddress = scope.addressByEndpointID[delivery.RecipientEndpointID]
		if senderID.Valid {
			delivery.SenderEndpointID = &senderID.String
		}
		if ackedAt.Valid {
			delivery.AckedAt = &ackedAt.String
		}
		deliveries = append(deliveries, delivery)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate delivery rows: %w", err)
	}

	return deliveries, nil
}

func (o availabilityOwner) listPersonalStaleAddresses(ctx context.Context, querier rowsQuerier, scope personalAvailabilityScope, nowText, staleBeforeText string) ([]StaleAddress, error) {
	if scope.empty() {
		return []StaleAddress{}, nil
	}

	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(scope.recipientEndpointIDs)), ",")
	args := make([]any, 0, len(scope.recipientEndpointIDs)+3)
	for _, recipientEndpointID := range scope.recipientEndpointIDs {
		args = append(args, recipientEndpointID)
	}
	args = append(args, nowText, nowText, staleBeforeText)

	rows, err := querier.QueryContext(ctx, fmt.Sprintf(`
SELECT
  d.recipient_endpoint_id,
  MIN(%s) AS oldest_eligible_at,
  COUNT(*) AS claimable_count
FROM deliveries AS d
WHERE d.recipient_endpoint_id IN (%s)
  AND %s
GROUP BY d.recipient_endpoint_id
HAVING MIN(%s) <= ?
ORDER BY oldest_eligible_at ASC, d.recipient_endpoint_id ASC
`, claimableDeliveryEligibleAtExpr, placeholders, claimableDeliveryFilter, claimableDeliveryEligibleAtExpr), args...)
	if err != nil {
		return nil, fmt.Errorf("query stale addresses: %w", err)
	}
	defer rows.Close()

	stale := make([]StaleAddress, 0)
	for rows.Next() {
		var endpointID string
		var oldestEligibleAt string
		var claimableCount int
		if err := rows.Scan(&endpointID, &oldestEligibleAt, &claimableCount); err != nil {
			return nil, fmt.Errorf("scan stale address row: %w", err)
		}
		stale = append(stale, StaleAddress{
			Address:          scope.addressByEndpointID[endpointID],
			OldestEligibleAt: oldestEligibleAt,
			ClaimableCount:   claimableCount,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate stale address rows: %w", err)
	}

	return stale, nil
}

func (o availabilityOwner) resolveGroup(ctx context.Context, querier rowQuerier, groupAddress, person string) (groupAvailabilityScope, error) {
	groupAddress = strings.TrimSpace(groupAddress)
	if groupAddress == "" {
		return groupAvailabilityScope{}, errors.New("group address is required")
	}
	person = strings.TrimSpace(person)
	if person == "" {
		return groupAvailabilityScope{}, errors.New("person is required")
	}

	group, found, err := lookupGroupRecord(ctx, querier, groupAddress)
	if err != nil {
		return groupAvailabilityScope{}, fmt.Errorf("load group %q: %w", groupAddress, err)
	}
	if !found {
		return groupAvailabilityScope{}, fmt.Errorf("group %q: %w", groupAddress, ErrGroupNotFound)
	}

	membership, found, err := lookupLatestGroupMembershipByPerson(ctx, querier, group.GroupID, person)
	if err != nil {
		return groupAvailabilityScope{}, fmt.Errorf("load group membership for %q in %q: %w", person, groupAddress, err)
	}
	if !found {
		return groupAvailabilityScope{
			viewer: groupViewerState{
				Group:  group,
				Person: person,
			},
		}, nil
	}

	viewer := groupViewerState{
		Group:    group,
		Person:   membership.Person,
		PersonID: membership.PersonID,
	}
	if !membership.Active {
		viewer.VisibilityCutoff = membership.LeftAt
	}
	return groupAvailabilityScope{viewer: viewer}, nil
}

func (o availabilityOwner) listGroupMessages(ctx context.Context, querier rowsQuerier, scope groupAvailabilityScope, unreadOnly bool, limit int) ([]groupMessageRecord, error) {
	viewer := scope.viewer
	if viewer.PersonID == "" {
		return []groupMessageRecord{}, nil
	}

	args := []any{viewer.PersonID, viewer.PersonID, viewer.Group.GroupID}
	query := `
SELECT
  gm.message_id,
  m.sender_endpoint_id,
  gm.created_at,
  m.subject,
  m.content_type,
  m.schema_version,
  m.body_blob_ref,
  m.body_size,
  m.body_sha256,
  gr.first_read_at,
  CASE
    WHEN EXISTS (
      SELECT 1
      FROM group_message_eligibility AS ge_viewer
      WHERE ge_viewer.message_id = gm.message_id
        AND ge_viewer.person_id = ?
    ) THEN 1
    ELSE 0
  END AS viewer_eligible,
  (
    SELECT COUNT(*)
    FROM group_message_eligibility AS ge
    JOIN group_reads AS gr_eligible
      ON gr_eligible.message_id = ge.message_id
     AND gr_eligible.person_id = ge.person_id
    WHERE ge.message_id = gm.message_id
  ) AS read_count,
  gm.eligible_count
FROM group_messages AS gm
JOIN messages AS m ON m.message_id = gm.message_id
LEFT JOIN group_reads AS gr
  ON gr.message_id = gm.message_id
 AND gr.person_id = ?
WHERE gm.group_id = ?
`
	if viewer.VisibilityCutoff != nil {
		query += `
  AND gm.created_at <= ?
`
		args = append(args, *viewer.VisibilityCutoff)
	}
	if unreadOnly {
		query += `
  AND gr.first_read_at IS NULL
`
	}
	query += `
ORDER BY gm.created_at ASC, gm.message_id ASC
`
	if limit > 0 {
		query += `
LIMIT ?
`
		args = append(args, limit)
	}

	rows, err := querier.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query group messages for %q in %q: %w", viewer.Person, viewer.Group.Address, err)
	}
	defer rows.Close()

	records := make([]groupMessageRecord, 0)
	for rows.Next() {
		var record groupMessageRecord
		if err := rows.Scan(
			&record.MessageID,
			&record.SenderEndpointID,
			&record.MessageCreatedAt,
			&record.Subject,
			&record.ContentType,
			&record.SchemaVersion,
			&record.BodyBlobRef,
			&record.BodySize,
			&record.BodySHA256,
			&record.FirstReadAt,
			&record.ViewerEligible,
			&record.ReadCount,
			&record.EligibleCount,
		); err != nil {
			return nil, fmt.Errorf("scan group message row: %w", err)
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate group messages: %w", err)
	}
	return records, nil
}
