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
	"unicode"
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
	writeBlobHook  func(inWriteTransaction bool) error
}

type writeTransactionContextKey struct{}

type EndpointRegistration struct {
	EndpointID string
	Address    string
	Created    bool
}

type SendParams struct {
	ToAddress            string
	FromAddress          string
	Subject              string
	ContentType          string
	SchemaVersion        string
	ForwardedMessageID   string
	ForwardedFromAddress string
	Body                 []byte
	Group                bool
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

type GroupTranscriptParams struct {
	Address string
}

type ListedDelivery struct {
	DeliveryID           string  `json:"delivery_id"`
	MessageID            string  `json:"message_id"`
	ForwardedMessageID   *string `json:"-"`
	ForwardedFromAddress *string `json:"forwarded_from_address,omitempty"`
	RecipientAddress     string  `json:"recipient_address"`
	RecipientEndpointID  string  `json:"recipient_endpoint_id"`
	SenderEndpointID     *string `json:"sender_endpoint_id,omitempty"`
	State                string  `json:"state"`
	VisibleAt            string  `json:"visible_at"`
	AckedAt              *string `json:"acked_at,omitempty"`
	MessageCreatedAt     string  `json:"message_created_at"`
	Subject              string  `json:"subject"`
	ContentType          string  `json:"content_type"`
	SchemaVersion        string  `json:"schema_version"`
	BodyBlobRef          string  `json:"body_blob_ref"`
	BodySize             int64   `json:"body_size"`
	BodySHA256           string  `json:"body_sha256"`
}

type ReadDelivery struct {
	DeliveryID           string  `json:"delivery_id"`
	MessageID            string  `json:"message_id"`
	ForwardedMessageID   *string `json:"-"`
	ForwardedFromAddress *string `json:"forwarded_from_address,omitempty"`
	RecipientAddress     string  `json:"recipient_address"`
	RecipientEndpointID  string  `json:"recipient_endpoint_id"`
	SenderEndpointID     *string `json:"sender_endpoint_id,omitempty"`
	SenderAddress        *string `json:"-"`
	State                string  `json:"state"`
	VisibleAt            string  `json:"visible_at"`
	AckedAt              *string `json:"acked_at,omitempty"`
	MessageCreatedAt     string  `json:"message_created_at"`
	Subject              string  `json:"subject"`
	ContentType          string  `json:"content_type"`
	SchemaVersion        string  `json:"schema_version"`
	BodyBlobRef          string  `json:"body_blob_ref"`
	BodySize             int64   `json:"body_size"`
	BodySHA256           string  `json:"body_sha256"`
	Body                 string  `json:"body"`
}

type ReadMessage struct {
	MessageID            string  `json:"message_id"`
	ForwardedMessageID   *string `json:"-"`
	ForwardedFromAddress *string `json:"forwarded_from_address,omitempty"`
	SenderEndpointID     *string `json:"sender_endpoint_id,omitempty"`
	SenderAddress        *string `json:"-"`
	MessageCreatedAt     string  `json:"message_created_at"`
	Subject              string  `json:"subject"`
	ContentType          string  `json:"content_type"`
	SchemaVersion        string  `json:"schema_version"`
	BodyBlobRef          string  `json:"body_blob_ref"`
	BodySize             int64   `json:"body_size"`
	BodySHA256           string  `json:"body_sha256"`
	Body                 string  `json:"body"`
}

type GroupListedMessage struct {
	MessageID            string  `json:"message_id"`
	ForwardedMessageID   *string `json:"-"`
	ForwardedFromAddress *string `json:"forwarded_from_address,omitempty"`
	GroupID              string  `json:"group_id"`
	GroupAddress         string  `json:"group_address"`
	Person               string  `json:"person"`
	SenderEndpointID     *string `json:"sender_endpoint_id,omitempty"`
	MessageCreatedAt     string  `json:"message_created_at"`
	Subject              string  `json:"subject"`
	ContentType          string  `json:"content_type"`
	SchemaVersion        string  `json:"schema_version"`
	Read                 bool    `json:"read"`
	FirstReadAt          *string `json:"first_read_at,omitempty"`
	ReadCount            int     `json:"read_count"`
	EligibleCount        int     `json:"eligible_count"`
}

type GroupTranscriptMessage struct {
	MessageID            string  `json:"message_id"`
	ForwardedMessageID   *string `json:"-"`
	ForwardedFromAddress *string `json:"forwarded_from_address,omitempty"`
	GroupID              string  `json:"group_id"`
	GroupAddress         string  `json:"group_address"`
	SenderEndpointID     *string `json:"sender_endpoint_id,omitempty"`
	SenderAddress        *string `json:"sender_address,omitempty"`
	DisplaySender        string  `json:"display_sender"`
	MessageCreatedAt     string  `json:"message_created_at"`
	Subject              string  `json:"subject"`
	ContentType          string  `json:"content_type"`
	SchemaVersion        string  `json:"schema_version"`
	Body                 string  `json:"body"`
	ReadCount            int     `json:"read_count"`
	EligibleCount        int     `json:"eligible_count"`
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
	rawAddress := address
	address, err := NormalizeAddress(rawAddress)
	if err != nil {
		if strings.TrimSpace(rawAddress) == "" {
			return EndpointRegistration{}, errors.New("endpoint address is required")
		}
		return EndpointRegistration{}, err
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
	toAddress, err := NormalizeAddress(params.ToAddress)
	if err != nil {
		if strings.TrimSpace(params.ToAddress) == "" {
			return SendResult{}, errors.New("recipient address is required")
		}
		return SendResult{}, err
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
	fromAddress, err := NormalizeOptionalAddress(params.FromAddress)
	if err != nil {
		return SendResult{}, err
	}
	if fromAddress != "" {
		if err := s.rejectGroupAddress(ctx, fromAddress); err != nil {
			return SendResult{}, err
		}
	}
	if params.Group {
		if !IsGroupAddress(toAddress) {
			return SendResult{}, fmt.Errorf("invalid group address %q: group addresses must start with group/", params.ToAddress)
		}
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

	blobRef, bodySize, bodySHA256, err := s.writeBlob(ctx, params.Body)
	if err != nil {
		return SendResult{}, err
	}

	timestamp := formatTimestamp(s.now())
	messageID, err := newPrefixedID("msg")
	if err != nil {
		return SendResult{}, err
	}
	var groupPlan groupSendPlan
	if params.Group {
		groupPlan, err = s.prepareGroupSendPlan(ctx, toAddress, messageID, params.Subject, fromAddress)
		if err != nil {
			return SendResult{}, err
		}
	}

	tx, err := s.writeDB.BeginTx(ctx, nil)
	if err != nil {
		return SendResult{}, fmt.Errorf("begin send transaction: %w", err)
	}
	defer tx.Rollback()
	txCtx := context.WithValue(ctx, writeTransactionContextKey{}, true)

	var senderEndpointID *string
	if fromAddress != "" {
		registration, err := s.ensureEndpointAddress(txCtx, tx, fromAddress)
		if err != nil {
			return SendResult{}, fmt.Errorf("resolve sender address: %w", err)
		}
		senderEndpointID = &registration.EndpointID
	}
	var senderEndpointValue any
	if senderEndpointID != nil {
		senderEndpointValue = *senderEndpointID
	}
	var forwardedMessageIDValue any
	if forwardedMessageID := strings.TrimSpace(params.ForwardedMessageID); forwardedMessageID != "" {
		forwardedMessageIDValue = forwardedMessageID
	}
	var forwardedFromAddressValue any
	if forwardedFromAddress := strings.TrimSpace(params.ForwardedFromAddress); forwardedFromAddress != "" {
		forwardedFromAddressValue = forwardedFromAddress
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
  forwarded_message_id,
  forwarded_from_address,
  reply_to_message_id,
  metadata_json
) VALUES (?, ?, ?, ?, ?, ?, NULL, ?, ?, ?, ?, ?, NULL, '{}')
		`, messageID, timestamp, senderEndpointValue, params.Subject, contentType, schemaVersion, blobRef, bodySize, bodySHA256, forwardedMessageIDValue, forwardedFromAddressValue); err != nil {
		return SendResult{}, fmt.Errorf("insert message: %w", err)
	}

	if params.Group {
		result, err := s.sendGroupMessage(txCtx, tx, groupPlan, messageID, timestamp, toAddress, fromAddress, params.Subject, senderEndpointValue, senderEndpointID)
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

	queued, err := s.queuePersonalDelivery(txCtx, tx, messageID, timestamp, toAddress, senderEndpointValue,
		map[string]string{
			"recipient_address": toAddress,
			"subject":           params.Subject,
		},
		map[string]string{
			"recipient_address": toAddress,
			"state":             "queued",
		})
	if err != nil {
		return SendResult{}, err
	}

	if err := tx.Commit(); err != nil {
		return SendResult{}, fmt.Errorf("commit send transaction: %w", err)
	}

	return SendResult{
		Mode:             SendModePersonal,
		MessageID:        messageID,
		DeliveryID:       queued.DeliveryID,
		BodyBlobRef:      blobRef,
		RecipientID:      queued.RecipientEndpointID,
		SenderID:         senderEndpointID,
		BodySHA256:       bodySHA256,
		BodySize:         bodySize,
		VisibleAtUTC:     timestamp,
		MessageCreatedAt: timestamp,
	}, nil
}

type queuedPersonalDelivery struct {
	DeliveryID          string
	RecipientEndpointID string
}

type groupSendPlan struct {
	group                GroupRecord
	memberships          []GroupMembershipRecord
	subscriberDeliveries []groupSubscriberDelivery
}

type groupSubscriberDelivery struct {
	NotifyAddress string
	MessageID     string
	BlobRef       string
	BodySize      int64
	BodySHA256    string
}

func (s *Store) queuePersonalDelivery(ctx context.Context, tx *sql.Tx, messageID, timestamp, toAddress string, senderEndpointValue any, messageDetail, deliveryDetail map[string]string) (queuedPersonalDelivery, error) {
	if IsGroupAddress(toAddress) {
		return queuedPersonalDelivery{}, fmt.Errorf("personal delivery target %q uses reserved group/ prefix", toAddress)
	}

	deliveryID, err := newPrefixedID("dlv")
	if err != nil {
		return queuedPersonalDelivery{}, err
	}
	messageEventID, err := newPrefixedID("evt")
	if err != nil {
		return queuedPersonalDelivery{}, err
	}
	deliveryEventID, err := newPrefixedID("evt")
	if err != nil {
		return queuedPersonalDelivery{}, err
	}

	recipientRegistration, err := s.ensureEndpointAddress(ctx, tx, toAddress)
	if err != nil {
		return queuedPersonalDelivery{}, fmt.Errorf("resolve recipient address: %w", err)
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
		return queuedPersonalDelivery{}, fmt.Errorf("insert delivery: %w", err)
	}

	messageDetailJSON, err := marshalDetail(messageDetail)
	if err != nil {
		return queuedPersonalDelivery{}, err
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO events (event_id, created_at, event_type, endpoint_id, message_id, detail_json)
VALUES (?, ?, ?, ?, ?, ?)
`, messageEventID, timestamp, "message_created", senderEndpointValue, messageID, messageDetailJSON); err != nil {
		return queuedPersonalDelivery{}, fmt.Errorf("insert message event: %w", err)
	}

	deliveryDetailJSON, err := marshalDetail(deliveryDetail)
	if err != nil {
		return queuedPersonalDelivery{}, err
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO events (event_id, created_at, event_type, endpoint_id, message_id, delivery_id, detail_json)
VALUES (?, ?, ?, ?, ?, ?, ?)
`, deliveryEventID, timestamp, "delivery_queued", recipientEndpointID, messageID, deliveryID, deliveryDetailJSON); err != nil {
		return queuedPersonalDelivery{}, fmt.Errorf("insert delivery event: %w", err)
	}
	return queuedPersonalDelivery{
		DeliveryID:          deliveryID,
		RecipientEndpointID: recipientEndpointID,
	}, nil
}

func (s *Store) sendGroupMessage(
	ctx context.Context,
	tx *sql.Tx,
	plan groupSendPlan,
	messageID string,
	timestamp string,
	toAddress string,
	fromAddress string,
	subject string,
	senderEndpointValue any,
	senderEndpointID *string,
) (SendResult, error) {
	group := plan.group
	memberships := plan.memberships
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

	if err := markGroupSenderRead(ctx, tx, messageID, group.GroupID, fromAddress, timestamp); err != nil {
		return SendResult{}, fmt.Errorf("mark group sender read: %w", err)
	}
	if err := s.queueGroupSubscriberDeliveries(ctx, tx, group, plan.subscriberDeliveries, messageID, timestamp, fromAddress, senderEndpointValue); err != nil {
		return SendResult{}, fmt.Errorf("queue group subscriber deliveries: %w", err)
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

func (s *Store) prepareGroupSendPlan(ctx context.Context, toAddress, groupMessageID, groupSubject, fromAddress string) (groupSendPlan, error) {
	group, found, err := lookupGroupRecord(ctx, s.readDB, toAddress)
	if err != nil {
		return groupSendPlan{}, fmt.Errorf("resolve group address %q: %w", toAddress, err)
	}
	if !found {
		return groupSendPlan{}, fmt.Errorf("group %q: %w", toAddress, ErrGroupNotFound)
	}
	memberships, err := listActiveGroupMemberships(ctx, s.readDB, group.GroupID)
	if err != nil {
		return groupSendPlan{}, fmt.Errorf("list active members for group %q: %w", toAddress, err)
	}
	subscribers, err := listActiveGroupNotificationSubscribers(ctx, s.readDB, group.GroupID)
	if err != nil {
		return groupSendPlan{}, fmt.Errorf("list subscribers for group %q: %w", group.Address, err)
	}

	activePeople := activeGroupMemberPeople(memberships)
	deliveries := make([]groupSubscriberDelivery, 0, len(subscribers))
	for _, subscriber := range subscribers {
		if strings.TrimSpace(subscriber.NotifyAddress) == strings.TrimSpace(fromAddress) {
			continue
		}
		person := strings.TrimSpace(subscriber.Person)
		if person == "" || IsGroupAddress(subscriber.NotifyAddress) {
			continue
		}
		if _, ok := activePeople[person]; !ok {
			continue
		}
		blobRef, bodySize, bodySHA256, err := s.writeBlob(ctx, []byte(groupSubscriberDeliveryBody(group.Address, person, groupMessageID, groupSubject)))
		if err != nil {
			return groupSendPlan{}, err
		}
		messageID, err := newPrefixedID("msg")
		if err != nil {
			return groupSendPlan{}, err
		}
		deliveries = append(deliveries, groupSubscriberDelivery{
			NotifyAddress: subscriber.NotifyAddress,
			MessageID:     messageID,
			BlobRef:       blobRef,
			BodySize:      bodySize,
			BodySHA256:    bodySHA256,
		})
	}
	return groupSendPlan{
		group:                group,
		memberships:          memberships,
		subscriberDeliveries: deliveries,
	}, nil
}

func markGroupSenderRead(ctx context.Context, tx *sql.Tx, messageID, groupID, fromAddress, timestamp string) error {
	fromAddress = strings.TrimSpace(fromAddress)
	if fromAddress == "" {
		return nil
	}
	_, err := tx.ExecContext(ctx, `
INSERT OR IGNORE INTO group_reads (message_id, person_id, first_read_at)
SELECT ?, person_id, ?
FROM (
  SELECT gm.person_id
  FROM group_memberships AS gm
  JOIN persons AS p ON p.person_id = gm.person_id
  WHERE gm.group_id = ?
    AND gm.left_at IS NULL
    AND p.person = ?
  UNION
  SELECT gm.person_id
  FROM group_notification_subscribers AS gns
  JOIN persons AS p ON p.person = gns.person
  JOIN group_memberships AS gm
    ON gm.group_id = gns.group_id
   AND gm.person_id = p.person_id
   AND gm.left_at IS NULL
  WHERE gns.group_id = ?
    AND gns.removed_at IS NULL
    AND gns.notify_address = ?
    AND gns.person <> ''
)
`, messageID, timestamp, groupID, fromAddress, groupID, fromAddress)
	return err
}

func (s *Store) queueGroupSubscriberDeliveries(ctx context.Context, tx *sql.Tx, group GroupRecord, deliveries []groupSubscriberDelivery, groupMessageID, timestamp, fromAddress string, senderEndpointValue any) error {
	for _, delivery := range deliveries {
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
  forwarded_message_id,
  forwarded_from_address,
  reply_to_message_id,
  metadata_json
) VALUES (?, ?, ?, ?, 'text/plain', 'group-notification/v1', NULL, ?, ?, ?, ?, ?, NULL, '{}')
`, delivery.MessageID, timestamp, senderEndpointValue, groupSubscriberDeliverySubject(group.Address), delivery.BlobRef, delivery.BodySize, delivery.BodySHA256, groupMessageID, fromAddressOrNil(fromAddress)); err != nil {
			return fmt.Errorf("insert subscriber notification message for %q: %w", delivery.NotifyAddress, err)
		}

		if _, err := s.queuePersonalDelivery(ctx, tx, delivery.MessageID, timestamp, delivery.NotifyAddress, senderEndpointValue,
			map[string]string{
				"group_address":     group.Address,
				"group_message_id":  groupMessageID,
				"recipient_address": delivery.NotifyAddress,
				"mode":              "group_notification",
			},
			map[string]string{
				"group_address":     group.Address,
				"group_message_id":  groupMessageID,
				"recipient_address": delivery.NotifyAddress,
				"state":             "queued",
			}); err != nil {
			return fmt.Errorf("queue subscriber notification delivery for %q: %w", delivery.NotifyAddress, err)
		}
	}
	return nil
}

func activeGroupMemberPeople(memberships []GroupMembershipRecord) map[string]struct{} {
	people := make(map[string]struct{}, len(memberships))
	for _, membership := range memberships {
		people[membership.Person] = struct{}{}
	}
	return people
}

func groupSubscriberDeliverySubject(groupAddress string) string {
	return "Group mailbox update: " + groupAddress
}

func groupSubscriberDeliveryBody(groupAddress, person, messageID, subject string) string {
	lines := []string{
		"Action: group_message_available",
		"Group-Address: " + noticeLineValue(groupAddress),
		"Message-ID: " + noticeLineValue(messageID),
	}
	if strings.TrimSpace(person) != "" {
		lines = append(lines, "As-Person: "+noticeLineValue(strings.TrimSpace(person)))
	}
	if strings.TrimSpace(subject) != "" {
		lines = append(lines, "Subject: "+noticeLineValue(strings.TrimSpace(subject)))
	}
	return strings.Join(lines, "\n") + "\n"
}

func noticeLineValue(value string) string {
	var out strings.Builder
	for _, r := range value {
		switch r {
		case '\\':
			out.WriteString(`\\`)
		case '\r':
			out.WriteString(`\r`)
		case '\n':
			out.WriteString(`\n`)
		case '\t':
			out.WriteString(`\t`)
		default:
			if unicode.IsControl(r) {
				fmt.Fprintf(&out, `\u%04x`, r)
				continue
			}
			out.WriteRune(r)
		}
	}
	return out.String()
}

func fromAddressOrNil(fromAddress string) any {
	fromAddress = strings.TrimSpace(fromAddress)
	if fromAddress == "" {
		return nil
	}
	return fromAddress
}

func (s *Store) List(ctx context.Context, params ListParams) ([]ListedDelivery, error) {
	rawAddress := params.Address
	address, err := NormalizeAddress(rawAddress)
	if err != nil {
		if strings.TrimSpace(rawAddress) == "" {
			return nil, errors.New("recipient address is required")
		}
		return nil, err
	}

	scope, err := s.resolvePersonal(ctx, s.readDB, []string{address})
	if err != nil {
		return nil, err
	}
	return s.listPersonalDeliveries(ctx, s.readDB, scope, strings.TrimSpace(params.State), formatTimestamp(s.now()))
}

func (s *Store) ReadDelivery(ctx context.Context, deliveryID string) (ReadDelivery, error) {
	deliveryID = strings.TrimSpace(deliveryID)
	if deliveryID == "" {
		return ReadDelivery{}, errors.New("delivery id is required")
	}

	var result ReadDelivery
	var senderID sql.NullString
	var senderAddress sql.NullString
	var forwardedMessageID sql.NullString
	var forwardedFromAddress sql.NullString
	var ackedAt sql.NullString
	err := s.readDB.QueryRowContext(ctx, `
SELECT
  d.delivery_id,
  d.message_id,
  m.forwarded_message_id,
  m.forwarded_from_address,
  ea.address,
  d.recipient_endpoint_id,
  m.sender_endpoint_id,
  (
    SELECT sender_ea.address
    FROM endpoint_addresses AS sender_ea
    WHERE sender_ea.endpoint_id = m.sender_endpoint_id
    ORDER BY sender_ea.created_at ASC, sender_ea.address ASC
    LIMIT 1
  ) AS sender_address,
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
		&forwardedMessageID,
		&forwardedFromAddress,
		&result.RecipientAddress,
		&result.RecipientEndpointID,
		&senderID,
		&senderAddress,
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
	if senderAddress.Valid {
		result.SenderAddress = &senderAddress.String
	}
	if forwardedMessageID.Valid {
		result.ForwardedMessageID = &forwardedMessageID.String
	}
	if forwardedFromAddress.Valid {
		result.ForwardedFromAddress = &forwardedFromAddress.String
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
	var senderAddress sql.NullString
	var forwardedMessageID sql.NullString
	var forwardedFromAddress sql.NullString
	err := s.readDB.QueryRowContext(ctx, `
SELECT
  m.message_id,
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
		&forwardedMessageID,
		&forwardedFromAddress,
		&senderID,
		&senderAddress,
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
	if senderAddress.Valid {
		result.SenderAddress = &senderAddress.String
	}
	if forwardedMessageID.Valid {
		result.ForwardedMessageID = &forwardedMessageID.String
	}
	if forwardedFromAddress.Valid {
		result.ForwardedFromAddress = &forwardedFromAddress.String
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
	addresses, err := NormalizeAddressList(addresses)
	if err != nil {
		return nil, false, err
	}
	if len(addresses) == 0 {
		return []ReadDelivery{}, false, nil
	}
	if limit <= 0 {
		return nil, false, errors.New("limit must be greater than 0")
	}
	state = strings.TrimSpace(state)

	scope, err := s.resolvePersonal(ctx, s.readDB, addresses)
	if err != nil {
		return nil, false, err
	}
	if scope.empty() {
		return []ReadDelivery{}, false, nil
	}

	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(scope.recipientEndpointIDs)), ",")
	args := make([]any, 0, len(scope.recipientEndpointIDs)+2)
	for _, recipientEndpointID := range scope.recipientEndpointIDs {
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
SELECT
  d.delivery_id,
  d.message_id,
  m.forwarded_message_id,
  m.forwarded_from_address,
  (
    SELECT recipient_ea.address
    FROM endpoint_addresses AS recipient_ea
    WHERE recipient_ea.endpoint_id = d.recipient_endpoint_id
    ORDER BY recipient_ea.created_at ASC, recipient_ea.address ASC
    LIMIT 1
  ) AS recipient_address,
  d.recipient_endpoint_id,
  m.sender_endpoint_id,
  (
    SELECT sender_ea.address
    FROM endpoint_addresses AS sender_ea
    WHERE sender_ea.endpoint_id = m.sender_endpoint_id
    ORDER BY sender_ea.created_at ASC, sender_ea.address ASC
    LIMIT 1
  ) AS sender_address,
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
`+whereClause+`
%s
LIMIT ?
`, placeholders, orderClause), args...)
	if err != nil {
		return nil, false, fmt.Errorf("load latest deliveries for state %q: %w", state, err)
	}
	defer rows.Close()

	deliveries := make([]ReadDelivery, 0, limit+1)
	for rows.Next() {
		var delivery ReadDelivery
		var forwardedMessageID sql.NullString
		var forwardedFromAddress sql.NullString
		var senderID sql.NullString
		var senderAddress sql.NullString
		var ackedAt sql.NullString
		if err := rows.Scan(
			&delivery.DeliveryID,
			&delivery.MessageID,
			&forwardedMessageID,
			&forwardedFromAddress,
			&delivery.RecipientAddress,
			&delivery.RecipientEndpointID,
			&senderID,
			&senderAddress,
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
			return nil, false, fmt.Errorf("scan latest delivery: %w", err)
		}
		if forwardedMessageID.Valid {
			delivery.ForwardedMessageID = &forwardedMessageID.String
		}
		if forwardedFromAddress.Valid {
			delivery.ForwardedFromAddress = &forwardedFromAddress.String
		}
		if senderID.Valid {
			delivery.SenderEndpointID = &senderID.String
		}
		if senderAddress.Valid {
			delivery.SenderAddress = &senderAddress.String
		}
		if ackedAt.Valid {
			delivery.AckedAt = &ackedAt.String
		}
		deliveries = append(deliveries, delivery)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("iterate latest deliveries: %w", err)
	}

	hasMore := len(deliveries) > limit
	if hasMore {
		deliveries = deliveries[:limit]
	}

	for i := range deliveries {
		body, err := s.readBlob(deliveries[i].BodyBlobRef, deliveries[i].BodySize, deliveries[i].BodySHA256)
		if err != nil {
			return nil, false, err
		}
		deliveries[i].Body = string(body)
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
	if IsGroupAddress(address) {
		return fmt.Errorf("endpoint address %q uses reserved group/ prefix", address)
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

func (s *Store) writeBlob(ctx context.Context, body []byte) (string, int64, string, error) {
	if s.writeBlobHook != nil {
		inWriteTransaction, _ := ctx.Value(writeTransactionContextKey{}).(bool)
		if err := s.writeBlobHook(inWriteTransaction); err != nil {
			return "", 0, "", err
		}
	}
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
