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
	"strings"
	"time"
)

var ErrEmptyBody = errors.New("message body must not be empty")

type Store struct {
	db      *sql.DB
	blobDir string
	now     func() time.Time
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
}

type SendResult struct {
	MessageID    string
	DeliveryID   string
	BodyBlobRef  string
	RecipientID  string
	SenderID     *string
	BodySHA256   string
	BodySize     int64
	VisibleAtUTC string
}

type ListParams struct {
	Address string
	State   string
}

type ListedDelivery struct {
	DeliveryID          string  `json:"delivery_id"`
	MessageID           string  `json:"message_id"`
	RecipientAddress    string  `json:"recipient_address"`
	RecipientEndpointID string  `json:"recipient_endpoint_id"`
	SenderEndpointID    *string `json:"sender_endpoint_id,omitempty"`
	State               string  `json:"state"`
	VisibleAt           string  `json:"visible_at"`
	MessageCreatedAt    string  `json:"message_created_at"`
	Subject             string  `json:"subject"`
	ContentType         string  `json:"content_type"`
	SchemaVersion       string  `json:"schema_version"`
	BodyBlobRef         string  `json:"body_blob_ref"`
	BodySize            int64   `json:"body_size"`
	BodySHA256          string  `json:"body_sha256"`
}

func NewStore(db *sql.DB, blobDir string) *Store {
	return &Store{
		db:      db,
		blobDir: blobDir,
		now: func() time.Time {
			return time.Now().UTC()
		},
	}
}

func (s *Store) RegisterEndpoint(ctx context.Context, address string) (EndpointRegistration, error) {
	address = strings.TrimSpace(address)
	if address == "" {
		return EndpointRegistration{}, errors.New("endpoint address is required")
	}

	tx, err := s.db.BeginTx(ctx, nil)
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

	blobRef, bodySize, bodySHA256, err := s.writeBlob(params.Body)
	if err != nil {
		return SendResult{}, err
	}

	timestamp := formatTimestamp(s.now())
	messageID, err := newPrefixedID("msg")
	if err != nil {
		return SendResult{}, err
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

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SendResult{}, fmt.Errorf("begin send transaction: %w", err)
	}
	defer tx.Rollback()

	recipientRegistration, err := s.ensureEndpointAddress(ctx, tx, toAddress)
	if err != nil {
		return SendResult{}, fmt.Errorf("resolve recipient address: %w", err)
	}
	recipientEndpointID := recipientRegistration.EndpointID

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
		MessageID:    messageID,
		DeliveryID:   deliveryID,
		BodyBlobRef:  blobRef,
		RecipientID:  recipientEndpointID,
		SenderID:     senderEndpointID,
		BodySHA256:   bodySHA256,
		BodySize:     bodySize,
		VisibleAtUTC: timestamp,
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
	tmpPath := blobPath + ".tmp"

	if err := os.WriteFile(tmpPath, body, 0o600); err != nil {
		return "", 0, "", fmt.Errorf("write blob temp file: %w", err)
	}
	if err := os.Rename(tmpPath, blobPath); err != nil {
		return "", 0, "", fmt.Errorf("move blob into place: %w", err)
	}

	return blobRef, int64(len(body)), hex.EncodeToString(bodySHA256[:]), nil
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
