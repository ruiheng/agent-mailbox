package mailbox

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
)

var ErrEmptyBody = errors.New("message body must not be empty")

type blobTempFile interface {
	Write([]byte) (int, error)
	Sync() error
	Close() error
	Name() string
}

type Store struct {
	readDB         *sql.DB
	writeDB        *sql.DB
	claimDB        *sql.DB
	blobDir        string
	now            func() time.Time
	createBlobTemp func(dir, pattern string) (blobTempFile, error)
	renameFile     func(oldPath, newPath string) error
	removeFile     func(path string) error
	syncDir        func(path string) error
}

type EndpointRegistration struct {
	EndpointID string
	Address    string
	Created    bool
}

type SendParams struct {
	ToAddress     string
	FromAddress   string
	Subject       string
	ContentType   string
	SchemaVersion string
	Body          []byte
	Group         bool
}

type SendResult struct {
	Mode             string
	MessageID        string
	DeliveryID       string
	BodyBlobRef      string
	RecipientID      string
	SenderID         *string
	BodySHA256       string
	BodySize         int64
	VisibleAtUTC     string
	GroupID          string
	GroupAddress     string
	EligibleCount    int
	MessageCreatedAt string
}

const (
	SendModePersonal = "personal"
	SendModeGroup    = "group"
)

type ListParams struct {
	Address string
	State   string
}

type GroupListParams struct {
	Address string
	Person  string
}

type ListedDelivery struct {
	DeliveryID          string  `json:"delivery_id"`
	MessageID           string  `json:"message_id"`
	RecipientAddress    string  `json:"recipient_address"`
	RecipientEndpointID string  `json:"recipient_endpoint_id"`
	SenderEndpointID    *string `json:"sender_endpoint_id,omitempty"`
	State               string  `json:"state"`
	VisibleAt           string  `json:"visible_at"`
	AckedAt             *string `json:"acked_at,omitempty"`
	MessageCreatedAt    string  `json:"message_created_at"`
	Subject             string  `json:"subject"`
	ContentType         string  `json:"content_type"`
	SchemaVersion       string  `json:"schema_version"`
	BodyBlobRef         string  `json:"body_blob_ref"`
	BodySize            int64   `json:"body_size"`
	BodySHA256          string  `json:"body_sha256"`
}

type ReadDelivery struct {
	DeliveryID          string  `json:"delivery_id"`
	MessageID           string  `json:"message_id"`
	RecipientAddress    string  `json:"recipient_address"`
	RecipientEndpointID string  `json:"recipient_endpoint_id"`
	SenderEndpointID    *string `json:"sender_endpoint_id,omitempty"`
	State               string  `json:"state"`
	VisibleAt           string  `json:"visible_at"`
	AckedAt             *string `json:"acked_at,omitempty"`
	MessageCreatedAt    string  `json:"message_created_at"`
	Subject             string  `json:"subject"`
	ContentType         string  `json:"content_type"`
	SchemaVersion       string  `json:"schema_version"`
	BodyBlobRef         string  `json:"body_blob_ref"`
	BodySize            int64   `json:"body_size"`
	BodySHA256          string  `json:"body_sha256"`
	Body                string  `json:"body"`
}

type ReadMessage struct {
	MessageID        string  `json:"message_id"`
	SenderEndpointID *string `json:"sender_endpoint_id,omitempty"`
	MessageCreatedAt string  `json:"message_created_at"`
	Subject          string  `json:"subject"`
	ContentType      string  `json:"content_type"`
	SchemaVersion    string  `json:"schema_version"`
	BodyBlobRef      string  `json:"body_blob_ref"`
	BodySize         int64   `json:"body_size"`
	BodySHA256       string  `json:"body_sha256"`
	Body             string  `json:"body"`
}

type GroupListedMessage struct {
	MessageID        string  `json:"message_id"`
	GroupID          string  `json:"group_id"`
	GroupAddress     string  `json:"group_address"`
	Person           string  `json:"person"`
	SenderEndpointID *string `json:"sender_endpoint_id,omitempty"`
	MessageCreatedAt string  `json:"message_created_at"`
	Subject          string  `json:"subject"`
	ContentType      string  `json:"content_type"`
	SchemaVersion    string  `json:"schema_version"`
	Read             bool    `json:"read"`
	FirstReadAt      *string `json:"first_read_at,omitempty"`
	ReadCount        int     `json:"read_count"`
	EligibleCount    int     `json:"eligible_count"`
}

func NewStore(readDB, writeDB, claimDB *sql.DB, blobDir string) *Store {
	if readDB == nil {
		readDB = writeDB
	}
	if writeDB == nil {
		writeDB = readDB
	}
	if claimDB == nil {
		claimDB = writeDB
	}
	return &Store{
		readDB:  readDB,
		writeDB: writeDB,
		claimDB: claimDB,
		blobDir: blobDir,
		now: func() time.Time {
			return time.Now().UTC()
		},
		createBlobTemp: func(dir, pattern string) (blobTempFile, error) {
			return os.CreateTemp(dir, pattern)
		},
		renameFile: os.Rename,
		removeFile: os.Remove,
		syncDir:    syncDirPath,
	}
}

func (s *Store) RegisterEndpoint(ctx context.Context, address string) (EndpointRegistration, error) {
	address = strings.TrimSpace(address)
	if address == "" {
		return EndpointRegistration{}, errors.New("endpoint address is required")
	}
	if err := s.rejectGroupAddress(ctx, address); err != nil {
		return EndpointRegistration{}, err
	}

	tx, err := s.writeDB.BeginTx(ctx, nil)
	if err != nil {
		return EndpointRegistration{}, fmt.Errorf("begin endpoint registration transaction: %w", err)
	}
	defer tx.Rollback()

	registration, err := s.ensureEndpointAddress(ctx, tx, address)
	if err != nil {
		return EndpointRegistration{}, err
	}

	if err := tx.Commit(); err != nil {
		return EndpointRegistration{}, fmt.Errorf("commit endpoint registration transaction: %w", err)
	}
	return registration, nil
}

func (s *Store) Send(ctx context.Context, params SendParams) (SendResult, error) {
	toAddress := strings.TrimSpace(params.ToAddress)
	if toAddress == "" {
		return SendResult{}, errors.New("recipient address is required")
	}
	if len(params.Body) == 0 {
		return SendResult{}, ErrEmptyBody
	}
	contentType := strings.TrimSpace(params.ContentType)
	if contentType == "" {
		contentType = "text/plain"
	}
	schemaVersion := strings.TrimSpace(params.SchemaVersion)
	if schemaVersion == "" {
		schemaVersion = "v1"
	}
	if address := strings.TrimSpace(params.FromAddress); address != "" {
		if err := s.rejectGroupAddress(ctx, address); err != nil {
			return SendResult{}, err
		}
	}
	if params.Group {
		if _, found, err := lookupGroupRecord(ctx, s.readDB, toAddress); err != nil {
			return SendResult{}, fmt.Errorf("resolve group address %q: %w", toAddress, err)
		} else if !found {
			return SendResult{}, fmt.Errorf("group %q: %w", toAddress, ErrGroupNotFound)
		}
	} else {
		if err := s.rejectGroupAddress(ctx, toAddress); err != nil {
			return SendResult{}, err
		}
	}

	blobRef, bodySize, bodySHA256, err := s.writeBlob(params.Body)
	if err != nil {
		return SendResult{}, err
	}

	timestamp := formatTimestamp(s.now())
	messageID, err := newPrefixedID("msg")
	if err != nil {
		return SendResult{}, err
	}

	tx, err := s.writeDB.BeginTx(ctx, nil)
	if err != nil {
		return SendResult{}, fmt.Errorf("begin send transaction: %w", err)
	}
	defer tx.Rollback()

	var senderEndpointID *string
	if address := strings.TrimSpace(params.FromAddress); address != "" {
		registration, err := s.ensureEndpointAddress(ctx, tx, address)
		if err != nil {
			return SendResult{}, fmt.Errorf("resolve sender address: %w", err)
		}
		senderEndpointID = &registration.EndpointID
	}
	var senderEndpointValue any
	if senderEndpointID != nil {
		senderEndpointValue = *senderEndpointID
	}

	if _, err := tx.ExecContext(ctx, `
INSERT INTO messages (
  message_id,
  created_at,
  sender_endpoint_id,
  subject,
  content_type,
  schema_version,
  idempotency_key,
  body_blob_ref,
  body_size,
  body_sha256,
  reply_to_message_id,
  metadata_json
) VALUES (?, ?, ?, ?, ?, ?, NULL, ?, ?, ?, NULL, '{}')
	`, messageID, timestamp, senderEndpointValue, params.Subject, contentType, schemaVersion, blobRef, bodySize, bodySHA256); err != nil {
		return SendResult{}, fmt.Errorf("insert message: %w", err)
	}

	if params.Group {
		result, err := s.sendGroupMessage(ctx, tx, messageID, timestamp, toAddress, params.Subject, senderEndpointValue, senderEndpointID)
		if err != nil {
			return SendResult{}, err
		}
		if err := tx.Commit(); err != nil {
			return SendResult{}, fmt.Errorf("commit send transaction: %w", err)
		}
		result.Mode = SendModeGroup
		result.MessageID = messageID
		result.BodyBlobRef = blobRef
		result.SenderID = senderEndpointID
		result.BodySHA256 = bodySHA256
		result.BodySize = bodySize
		result.MessageCreatedAt = timestamp
		return result, nil
	}

	deliveryID, err := newPrefixedID("dlv")
	if err != nil {
		return SendResult{}, err
	}
	messageEventID, err := newPrefixedID("evt")
	if err != nil {
		return SendResult{}, err
	}
	deliveryEventID, err := newPrefixedID("evt")
	if err != nil {
		return SendResult{}, err
	}

	recipientRegistration, err := s.ensureEndpointAddress(ctx, tx, toAddress)
	if err != nil {
		return SendResult{}, fmt.Errorf("resolve recipient address: %w", err)
	}
	recipientEndpointID := recipientRegistration.EndpointID

	if _, err := tx.ExecContext(ctx, `
INSERT INTO deliveries (
  delivery_id,
  message_id,
  recipient_endpoint_id,
  state,
  visible_at,
  lease_token,
  lease_expires_at,
  acked_at,
  attempt_count,
  last_error_code,
  last_error_text
) VALUES (?, ?, ?, 'queued', ?, NULL, NULL, NULL, 0, NULL, NULL)
`, deliveryID, messageID, recipientEndpointID, timestamp); err != nil {
		return SendResult{}, fmt.Errorf("insert delivery: %w", err)
	}

	messageDetailJSON, err := marshalDetail(map[string]string{
		"recipient_address": toAddress,
		"subject":           params.Subject,
	})
	if err != nil {
		return SendResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO events (event_id, created_at, event_type, endpoint_id, message_id, detail_json)
VALUES (?, ?, ?, ?, ?, ?)
`, messageEventID, timestamp, "message_created", senderEndpointValue, messageID, messageDetailJSON); err != nil {
		return SendResult{}, fmt.Errorf("insert message event: %w", err)
	}

	deliveryDetailJSON, err := marshalDetail(map[string]string{
		"recipient_address": toAddress,
		"state":             "queued",
	})
	if err != nil {
		return SendResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO events (event_id, created_at, event_type, endpoint_id, message_id, delivery_id, detail_json)
VALUES (?, ?, ?, ?, ?, ?, ?)
`, deliveryEventID, timestamp, "delivery_queued", recipientEndpointID, messageID, deliveryID, deliveryDetailJSON); err != nil {
		return SendResult{}, fmt.Errorf("insert delivery event: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return SendResult{}, fmt.Errorf("commit send transaction: %w", err)
	}

	return SendResult{
		Mode:             SendModePersonal,
		MessageID:        messageID,
		DeliveryID:       deliveryID,
		BodyBlobRef:      blobRef,
		RecipientID:      recipientEndpointID,
		SenderID:         senderEndpointID,
		BodySHA256:       bodySHA256,
		BodySize:         bodySize,
		VisibleAtUTC:     timestamp,
		MessageCreatedAt: timestamp,
	}, nil
}

func (s *Store) sendGroupMessage(
	ctx context.Context,
	tx *sql.Tx,
	messageID string,
	timestamp string,
	toAddress string,
	subject string,
	senderEndpointValue any,
	senderEndpointID *string,
) (SendResult, error) {
	group, found, err := lookupGroupRecord(ctx, tx, toAddress)
	if err != nil {
		return SendResult{}, fmt.Errorf("resolve group address %q: %w", toAddress, err)
	}
	if !found {
		return SendResult{}, fmt.Errorf("group %q: %w", toAddress, ErrGroupNotFound)
	}

	memberships, err := listActiveGroupMemberships(ctx, tx, group.GroupID)
	if err != nil {
		return SendResult{}, fmt.Errorf("list active members for group %q: %w", toAddress, err)
	}
	eligibleCount := len(memberships)

	if _, err := tx.ExecContext(ctx, `
INSERT INTO group_messages (
  message_id,
  group_id,
  created_at,
  eligible_count
) VALUES (?, ?, ?, ?)
	`, messageID, group.GroupID, timestamp, eligibleCount); err != nil {
		return SendResult{}, fmt.Errorf("insert group message: %w", err)
	}

	for _, membership := range memberships {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO group_message_eligibility (
  message_id,
  person_id,
  membership_id,
  eligible_at
) VALUES (?, ?, ?, ?)
		`, messageID, membership.PersonID, membership.MembershipID, timestamp); err != nil {
			return SendResult{}, fmt.Errorf("insert eligibility for group %q person %q: %w", toAddress, membership.Person, err)
		}
	}

	messageEventID, err := newPrefixedID("evt")
	if err != nil {
		return SendResult{}, err
	}
	groupMessageEventID, err := newPrefixedID("evt")
	if err != nil {
		return SendResult{}, err
	}

	messageDetailJSON, err := marshalDetail(map[string]string{
		"recipient_address": toAddress,
		"subject":           subject,
		"mode":              SendModeGroup,
	})
	if err != nil {
		return SendResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO events (event_id, created_at, event_type, endpoint_id, message_id, detail_json)
VALUES (?, ?, ?, ?, ?, ?)
	`, messageEventID, timestamp, "message_created", senderEndpointValue, messageID, messageDetailJSON); err != nil {
		return SendResult{}, fmt.Errorf("insert group message event: %w", err)
	}

	groupDetailJSON, err := marshalDetail(map[string]string{
		"group_id":       group.GroupID,
		"group_address":  group.Address,
		"eligible_count": fmt.Sprintf("%d", eligibleCount),
	})
	if err != nil {
		return SendResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO events (event_id, created_at, event_type, endpoint_id, message_id, detail_json)
VALUES (?, ?, ?, ?, ?, ?)
	`, groupMessageEventID, timestamp, "group_message_created", senderEndpointValue, messageID, groupDetailJSON); err != nil {
		return SendResult{}, fmt.Errorf("insert group creation event: %w", err)
	}

	return SendResult{
		GroupID:          group.GroupID,
		GroupAddress:     group.Address,
		EligibleCount:    eligibleCount,
		MessageCreatedAt: timestamp,
		SenderID:         senderEndpointID,
	}, nil
}

func (s *Store) List(ctx context.Context, params ListParams) ([]ListedDelivery, error) {
	address := strings.TrimSpace(params.Address)
	if address == "" {
		return nil, errors.New("recipient address is required")
	}

	recipients, err := s.resolveRecipients(ctx, []string{address})
	if err != nil {
		return nil, err
	}
	return s.listDeliveriesForRecipients(ctx, recipients, strings.TrimSpace(params.State))
}

func (s *Store) ReadDelivery(ctx context.Context, deliveryID string) (ReadDelivery, error) {
	deliveryID = strings.TrimSpace(deliveryID)
	if deliveryID == "" {
		return ReadDelivery{}, errors.New("delivery id is required")
	}

	var result ReadDelivery
	var senderID sql.NullString
	var ackedAt sql.NullString
	err := s.readDB.QueryRowContext(ctx, `
SELECT
  d.delivery_id,
  d.message_id,
  ea.address,
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
JOIN endpoint_addresses AS ea ON ea.endpoint_id = d.recipient_endpoint_id
WHERE d.delivery_id = ?
`, deliveryID).Scan(
		&result.DeliveryID,
		&result.MessageID,
		&result.RecipientAddress,
		&result.RecipientEndpointID,
		&senderID,
		&result.State,
		&result.VisibleAt,
		&ackedAt,
		&result.MessageCreatedAt,
		&result.Subject,
		&result.ContentType,
		&result.SchemaVersion,
		&result.BodyBlobRef,
		&result.BodySize,
		&result.BodySHA256,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ReadDelivery{}, fmt.Errorf("delivery %q not found", deliveryID)
		}
		return ReadDelivery{}, fmt.Errorf("load delivery %q: %w", deliveryID, err)
	}
	if senderID.Valid {
		result.SenderEndpointID = &senderID.String
	}
	if ackedAt.Valid {
		result.AckedAt = &ackedAt.String
	}

	body, err := s.readBlob(result.BodyBlobRef, result.BodySize, result.BodySHA256)
	if err != nil {
		return ReadDelivery{}, err
	}
	result.Body = string(body)

	return result, nil
}

func (s *Store) ReadMessage(ctx context.Context, messageID string) (ReadMessage, error) {
	messageID = strings.TrimSpace(messageID)
	if messageID == "" {
		return ReadMessage{}, errors.New("message id is required")
	}

	var result ReadMessage
	var senderID sql.NullString
	err := s.readDB.QueryRowContext(ctx, `
SELECT
  m.message_id,
  m.sender_endpoint_id,
  m.created_at,
  m.subject,
  m.content_type,
  m.schema_version,
  m.body_blob_ref,
  m.body_size,
  m.body_sha256
FROM messages AS m
WHERE m.message_id = ?
`, messageID).Scan(
		&result.MessageID,
		&senderID,
		&result.MessageCreatedAt,
		&result.Subject,
		&result.ContentType,
		&result.SchemaVersion,
		&result.BodyBlobRef,
		&result.BodySize,
		&result.BodySHA256,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ReadMessage{}, fmt.Errorf("message %q not found", messageID)
		}
		return ReadMessage{}, fmt.Errorf("load message %q: %w", messageID, err)
	}
	if senderID.Valid {
		result.SenderEndpointID = &senderID.String
	}

	body, err := s.readBlob(result.BodyBlobRef, result.BodySize, result.BodySHA256)
	if err != nil {
		return ReadMessage{}, err
	}
	result.Body = string(body)

	return result, nil
}

func (s *Store) ReadDeliveries(ctx context.Context, deliveryIDs []string) ([]ReadDelivery, error) {
	results := make([]ReadDelivery, 0, len(deliveryIDs))
	for _, deliveryID := range deliveryIDs {
		delivery, err := s.ReadDelivery(ctx, deliveryID)
		if err != nil {
			return nil, err
		}
		results = append(results, delivery)
	}
	return results, nil
}

func (s *Store) ReadMessages(ctx context.Context, messageIDs []string) ([]ReadMessage, error) {
	results := make([]ReadMessage, 0, len(messageIDs))
	for _, messageID := range messageIDs {
		message, err := s.ReadMessage(ctx, messageID)
		if err != nil {
			return nil, err
		}
		results = append(results, message)
	}
	return results, nil
}

func (s *Store) ReadLatestDeliveries(ctx context.Context, addresses []string, state string, limit int) ([]ReadDelivery, bool, error) {
	if len(addresses) == 0 {
		return []ReadDelivery{}, false, nil
	}
	if limit <= 0 {
		return nil, false, errors.New("limit must be greater than 0")
	}
	state = strings.TrimSpace(state)

	recipients, err := s.resolveRecipients(ctx, addresses)
	if err != nil {
		return nil, false, err
	}
	if len(recipients) == 0 {
		return []ReadDelivery{}, false, nil
	}

	recipientEndpointIDs := make([]string, 0, len(recipients))
	for _, recipient := range recipients {
		recipientEndpointIDs = append(recipientEndpointIDs, recipient.EndpointID)
	}

	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(recipientEndpointIDs)), ",")
	args := make([]any, 0, len(recipientEndpointIDs)+2)
	for _, recipientEndpointID := range recipientEndpointIDs {
		args = append(args, recipientEndpointID)
	}

	orderClause := "ORDER BY d.visible_at DESC, m.created_at DESC, d.delivery_id DESC"
	if state == "acked" {
		orderClause = "ORDER BY d.acked_at DESC, m.created_at DESC, d.delivery_id DESC"
	} else if state == "" {
		orderClause = "ORDER BY m.created_at DESC, d.delivery_id DESC"
	}

	whereClause := "WHERE d.recipient_endpoint_id IN (%s)"
	if state != "" {
		whereClause += "\n  AND d.state = ?"
		args = append(args, state)
	}
	args = append(args, limit+1)

	rows, err := s.readDB.QueryContext(ctx, fmt.Sprintf(`
SELECT d.delivery_id
FROM deliveries AS d
JOIN messages AS m ON m.message_id = d.message_id
`+whereClause+`
%s
LIMIT ?
`, placeholders, orderClause), args...)
	if err != nil {
		return nil, false, fmt.Errorf("load latest deliveries for state %q: %w", state, err)
	}
	defer rows.Close()

	deliveryIDs := make([]string, 0, limit+1)
	for rows.Next() {
		var deliveryID string
		if err := rows.Scan(&deliveryID); err != nil {
			return nil, false, fmt.Errorf("scan latest delivery id: %w", err)
		}
		deliveryIDs = append(deliveryIDs, deliveryID)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("iterate latest delivery ids: %w", err)
	}

	hasMore := len(deliveryIDs) > limit
	if hasMore {
		deliveryIDs = deliveryIDs[:limit]
	}

	deliveries, err := s.ReadDeliveries(ctx, deliveryIDs)
	if err != nil {
		return nil, false, err
	}
	return deliveries, hasMore, nil
}

func (s *Store) lookupEndpointID(ctx context.Context, querier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, address string) (string, bool, error) {
	var endpointID string
	err := querier.QueryRowContext(ctx, `
SELECT endpoint_id
FROM endpoint_addresses
WHERE address = ?
`, address).Scan(&endpointID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("lookup address %q: %w", address, err)
	}
	return endpointID, true, nil
}

func (s *Store) rejectGroupAddress(ctx context.Context, address string) error {
	groupRecord, found, err := lookupGroupRecord(ctx, s.readDB, address)
	if err != nil {
		return fmt.Errorf("resolve group address reservation for %q: %w", address, err)
	}
	if found {
		return fmt.Errorf("endpoint address %q is already bound to group %q: %w", address, groupRecord.GroupID, ErrAddressReservedByGroup)
	}
	return nil
}

func (s *Store) ensureEndpointAddress(ctx context.Context, tx *sql.Tx, address string) (EndpointRegistration, error) {
	endpointID, found, err := s.lookupEndpointID(ctx, tx, address)
	if err != nil {
		return EndpointRegistration{}, fmt.Errorf("read existing endpoint address: %w", err)
	}
	if found {
		return EndpointRegistration{
			EndpointID: endpointID,
			Address:    address,
			Created:    false,
		}, nil
	}

	timestamp := formatTimestamp(s.now())
	endpointID, err = newPrefixedID("ep")
	if err != nil {
		return EndpointRegistration{}, err
	}

	if _, err := tx.ExecContext(ctx, `
INSERT INTO endpoints (endpoint_id, created_at, metadata_json)
VALUES (?, ?, '{}')
`, endpointID, timestamp); err != nil {
		return EndpointRegistration{}, fmt.Errorf("insert endpoint: %w", err)
	}

	if groupRecord, found, err := lookupGroupRecord(ctx, tx, address); err != nil {
		return EndpointRegistration{}, fmt.Errorf("check group collision for endpoint address %q: %w", address, err)
	} else if found {
		if _, err := tx.ExecContext(ctx, `
DELETE FROM endpoints
WHERE endpoint_id = ?
`, endpointID); err != nil {
			return EndpointRegistration{}, fmt.Errorf("delete unused endpoint after group collision: %w", err)
		}
		return EndpointRegistration{}, fmt.Errorf("endpoint address %q is already bound to group %q: %w", address, groupRecord.GroupID, ErrAddressReservedByGroup)
	}

	result, err := tx.ExecContext(ctx, `
INSERT OR IGNORE INTO endpoint_addresses (address, endpoint_id, created_at)
VALUES (?, ?, ?)
`, address, endpointID, timestamp)
	if err != nil {
		return EndpointRegistration{}, fmt.Errorf("insert endpoint address: %w", err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return EndpointRegistration{}, fmt.Errorf("read endpoint address insert rows affected: %w", err)
	}
	if rowsAffected == 0 {
		if _, err := tx.ExecContext(ctx, `
DELETE FROM endpoints
WHERE endpoint_id = ?
`, endpointID); err != nil {
			return EndpointRegistration{}, fmt.Errorf("delete unused endpoint: %w", err)
		}
		existingEndpointID, found, err := s.lookupEndpointID(ctx, tx, address)
		if err != nil {
			return EndpointRegistration{}, fmt.Errorf("reload existing endpoint address: %w", err)
		}
		if !found {
			return EndpointRegistration{}, fmt.Errorf("reload existing endpoint address %q: not found after conflict", address)
		}
		return EndpointRegistration{
			EndpointID: existingEndpointID,
			Address:    address,
			Created:    false,
		}, nil
	}

	eventID, err := newPrefixedID("evt")
	if err != nil {
		return EndpointRegistration{}, err
	}
	detailJSON, err := marshalDetail(map[string]string{
		"address": address,
	})
	if err != nil {
		return EndpointRegistration{}, err
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO events (event_id, created_at, event_type, endpoint_id, detail_json)
VALUES (?, ?, ?, ?, ?)
`, eventID, timestamp, "endpoint_registered", endpointID, detailJSON); err != nil {
		return EndpointRegistration{}, fmt.Errorf("insert endpoint event: %w", err)
	}

	return EndpointRegistration{
		EndpointID: endpointID,
		Address:    address,
		Created:    true,
	}, nil
}

func (s *Store) writeBlob(body []byte) (string, int64, string, error) {
	blobRef, err := newPrefixedID("blob")
	if err != nil {
		return "", 0, "", err
	}
	bodySHA256 := sha256.Sum256(body)
	blobPath := filepath.Join(s.blobDir, blobRef)

	if err := s.persistBlob(blobPath, body); err != nil {
		return "", 0, "", err
	}

	return blobRef, int64(len(body)), hex.EncodeToString(bodySHA256[:]), nil
}

func (s *Store) persistBlob(blobPath string, body []byte) error {
	tmpFile, err := s.createBlobTemp(s.blobDir, filepath.Base(blobPath)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create blob temp file: %w", err)
	}

	tmpPath := tmpFile.Name()
	cleanupTemp := true
	closed := false
	defer func() {
		if !closed {
			_ = tmpFile.Close()
		}
		if cleanupTemp {
			_ = s.removeFile(tmpPath)
		}
	}()

	written, err := tmpFile.Write(body)
	if err != nil {
		return fmt.Errorf("write blob temp file: %w", err)
	}
	if written != len(body) {
		return fmt.Errorf("write blob temp file: short write %d/%d", written, len(body))
	}
	if err := tmpFile.Sync(); err != nil {
		return fmt.Errorf("sync blob temp file: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("close blob temp file: %w", err)
	}
	closed = true

	if err := s.renameFile(tmpPath, blobPath); err != nil {
		return fmt.Errorf("move blob into place: %w", err)
	}
	cleanupTemp = false

	if err := s.syncDir(s.blobDir); err != nil {
		if !errors.Is(err, errors.ErrUnsupported) {
			return fmt.Errorf("sync blob directory: %w", err)
		}
	}
	return nil
}

func syncDirPath(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open directory %q: %w", path, err)
	}
	defer dir.Close()

	if err := dir.Sync(); err != nil {
		if isUnsupportedDirectorySyncError(err) {
			return fmt.Errorf("sync directory %q: %w", path, errors.ErrUnsupported)
		}
		return fmt.Errorf("sync directory %q: %w", path, err)
	}
	return nil
}

func isUnsupportedDirectorySyncError(err error) bool {
	if runtime.GOOS == "windows" {
		return true
	}
	return errors.Is(err, syscall.EINVAL)
}

func marshalDetail(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("marshal event detail: %w", err)
	}
	return string(data), nil
}

func newPrefixedID(prefix string) (string, error) {
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate %s id: %w", prefix, err)
	}
	return fmt.Sprintf("%s_%s", prefix, hex.EncodeToString(raw[:])), nil
}

func formatTimestamp(value time.Time) string {
	return value.UTC().Format("2006-01-02T15:04:05.000000000Z07:00")
}
