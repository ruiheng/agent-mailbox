package mailbox

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	sqlite3 "github.com/mattn/go-sqlite3"
)

const (
	defaultLeaseTimeout     = 5 * time.Minute
	initialPollDelay        = 50 * time.Millisecond
	maxPollDelay            = time.Second
	maxClaimRetryCount      = 3
	claimRetryDelay         = 10 * time.Millisecond
	maxDeliveryAttemptCount = 3
	maxReceiveBatchSize     = 10
)

var (
	ErrNoMessage          = errors.New("no message available")
	ErrClaimContention    = errors.New("receive claim contention")
	ErrBodyIntegrity      = errors.New("body blob failed integrity check")
	ErrLeaseExpired       = errors.New("lease expired")
	ErrLeaseTokenMismatch = errors.New("lease token does not match current lease")
	ErrReceiveRecovery    = errors.New("receive recovery failed")
)

type ClaimContentionError struct {
	Attempts int
	Cause    error
}

func (e *ClaimContentionError) Error() string {
	return fmt.Sprintf("receive claim contention after %d attempts: %v", e.Attempts, e.Cause)
}

func (e *ClaimContentionError) Unwrap() []error {
	return []error{ErrClaimContention, e.Cause}
}

type ReceiveRecoveryError struct {
	DeliveryID  string
	ReceiveErr  error
	RecoveryErr error
}

func (e *ReceiveRecoveryError) Error() string {
	return fmt.Sprintf("receive recovery failed for delivery %q: read error: %v: recovery error: %v", e.DeliveryID, e.ReceiveErr, e.RecoveryErr)
}

func (e *ReceiveRecoveryError) Unwrap() []error {
	return []error{ErrReceiveRecovery, e.ReceiveErr, e.RecoveryErr}
}

type ReceiveParams struct {
	Address   string
	Addresses []string
}

type ReceiveBatchParams struct {
	Address   string
	Addresses []string
	Max       int
}

type ReceivedMessage struct {
	DeliveryID          string  `json:"delivery_id"`
	MessageID           string  `json:"message_id"`
	RecipientAddress    string  `json:"recipient_address"`
	RecipientEndpointID string  `json:"recipient_endpoint_id"`
	SenderEndpointID    *string `json:"sender_endpoint_id,omitempty"`
	State               string  `json:"state"`
	VisibleAt           string  `json:"visible_at"`
	LeaseToken          string  `json:"lease_token"`
	LeaseExpiresAt      string  `json:"lease_expires_at"`
	AttemptCount        int     `json:"attempt_count"`
	MessageCreatedAt    string  `json:"message_created_at"`
	Subject             string  `json:"subject"`
	ContentType         string  `json:"content_type"`
	SchemaVersion       string  `json:"schema_version"`
	BodyBlobRef         string  `json:"body_blob_ref"`
	BodySize            int64   `json:"body_size"`
	BodySHA256          string  `json:"body_sha256"`
	Body                string  `json:"body"`
}

type ReceiveResult struct {
	Messages []ReceivedMessage `json:"messages"`
	HasMore  bool              `json:"has_more"`
}

type DeliveryTransitionResult struct {
	DeliveryID   string `json:"delivery_id"`
	State        string `json:"state"`
	VisibleAt    string `json:"visible_at,omitempty"`
	AckedAt      string `json:"acked_at,omitempty"`
	AttemptCount int    `json:"attempt_count"`
}

type claimCandidate struct {
	DeliveryID          string
	MessageID           string
	RecipientEndpointID string
	SenderEndpointID    sql.NullString
	State               string
	VisibleAt           string
	AttemptCount        int
	MessageCreatedAt    string
	Subject             string
	ContentType         string
	SchemaVersion       string
	BodyBlobRef         string
	BodySize            int64
	BodySHA256          string
}

type leasedDeliveryRecord struct {
	DeliveryID          string
	MessageID           string
	RecipientEndpointID string
	State               string
	VisibleAt           string
	LeaseToken          sql.NullString
	LeaseExpiresAt      sql.NullString
	AttemptCount        int
}

type deliveryTransitionSpec struct {
	State                string
	VisibleAt            string
	AckedAt              any
	AttemptCount         int
	LastErrorText        any
	PrimaryEventType     string
	PrimaryEventDetail   map[string]any
	SecondaryEventType   string
	SecondaryEventDetail map[string]any
}

type resolvedRecipient struct {
	Address    string
	EndpointID string
}

func (s *Store) Receive(ctx context.Context, params ReceiveParams) (ReceivedMessage, error) {
	addresses, err := normalizeAddresses(params.Address, params.Addresses, "--for")
	if err != nil {
		return ReceivedMessage{}, err
	}

	return s.receiveOnce(ctx, addresses)
}

func (s *Store) ReceiveBatch(ctx context.Context, params ReceiveBatchParams) (ReceiveResult, error) {
	addresses, err := normalizeAddresses(params.Address, params.Addresses, "--for")
	if err != nil {
		return ReceiveResult{}, err
	}

	maxMessages := params.Max
	if maxMessages < 1 || maxMessages > maxReceiveBatchSize {
		return ReceiveResult{}, fmt.Errorf("--max must be between 1 and %d", maxReceiveBatchSize)
	}

	messages := make([]ReceivedMessage, 0, maxMessages)
	for len(messages) < maxMessages {
		message, err := s.receiveOnce(ctx, addresses)
		if err == nil {
			messages = append(messages, message)
			continue
		}
		if errors.Is(err, ErrNoMessage) {
			break
		}
		if errors.Is(err, ErrReceiveRecovery) || errors.Is(err, ErrClaimContention) {
			if releaseErr := s.releaseReceivedBatchMessages(ctx, messages); releaseErr != nil {
				return ReceiveResult{}, errors.Join(err, releaseErr)
			}
			return ReceiveResult{}, err
		}
		if len(messages) > 0 {
			break
		}
		return ReceiveResult{}, err
	}

	if len(messages) == 0 {
		return ReceiveResult{}, ErrNoMessage
	}

	hasMore := false
	if len(messages) == maxMessages {
		hasMore, err = s.hasClaimableDelivery(ctx, addresses)
		if err != nil {
			return ReceiveResult{}, err
		}
	}

	return ReceiveResult{
		Messages: messages,
		HasMore:  hasMore,
	}, nil
}

func (s *Store) resolveRecipients(ctx context.Context, addresses []string) ([]resolvedRecipient, error) {
	recipients := make([]resolvedRecipient, 0, len(addresses))
	seenEndpointIDs := make(map[string]struct{}, len(addresses))
	for _, address := range addresses {
		endpointID, found, err := s.lookupEndpointID(ctx, s.readDB, address)
		if err != nil {
			return nil, fmt.Errorf("resolve recipient address: %w", err)
		}
		if !found {
			continue
		}
		// Multiple addresses may resolve to one endpoint. Claim against the union once.
		if _, exists := seenEndpointIDs[endpointID]; exists {
			continue
		}
		seenEndpointIDs[endpointID] = struct{}{}
		recipients = append(recipients, resolvedRecipient{
			Address:    address,
			EndpointID: endpointID,
		})
	}
	return recipients, nil
}

func (s *Store) Ack(ctx context.Context, deliveryID, leaseToken string) (DeliveryTransitionResult, error) {
	return s.transitionLeasedDelivery(ctx, deliveryID, leaseToken, func(now time.Time, delivery leasedDeliveryRecord) (deliveryTransitionSpec, error) {
		transitionAt := formatTimestamp(now)
		return deliveryTransitionSpec{
			State:            "acked",
			VisibleAt:        transitionAt,
			AckedAt:          transitionAt,
			AttemptCount:     delivery.AttemptCount,
			LastErrorText:    nil,
			PrimaryEventType: "delivery_acked",
			PrimaryEventDetail: map[string]any{
				"previous_state":   delivery.State,
				"lease_expires_at": delivery.LeaseExpiresAt.String,
			},
		}, nil
	})
}

func (s *Store) Release(ctx context.Context, deliveryID, leaseToken string) (DeliveryTransitionResult, error) {
	return s.transitionLeasedDelivery(ctx, deliveryID, leaseToken, func(now time.Time, delivery leasedDeliveryRecord) (deliveryTransitionSpec, error) {
		visibleAt := formatTimestamp(now)
		return deliveryTransitionSpec{
			State:            "queued",
			VisibleAt:        visibleAt,
			AttemptCount:     delivery.AttemptCount,
			LastErrorText:    nil,
			PrimaryEventType: "delivery_released",
			PrimaryEventDetail: map[string]any{
				"previous_state":   delivery.State,
				"visible_at":       visibleAt,
				"lease_expires_at": delivery.LeaseExpiresAt.String,
			},
		}, nil
	})
}

func (s *Store) Defer(ctx context.Context, deliveryID, leaseToken string, until time.Time) (DeliveryTransitionResult, error) {
	return s.transitionLeasedDelivery(ctx, deliveryID, leaseToken, func(now time.Time, delivery leasedDeliveryRecord) (deliveryTransitionSpec, error) {
		if !until.UTC().After(now) {
			return deliveryTransitionSpec{}, errors.New("defer time must be in the future")
		}
		visibleAt := formatTimestamp(until)
		return deliveryTransitionSpec{
			State:            "queued",
			VisibleAt:        visibleAt,
			AttemptCount:     delivery.AttemptCount,
			LastErrorText:    nil,
			PrimaryEventType: "delivery_deferred",
			PrimaryEventDetail: map[string]any{
				"previous_state":   delivery.State,
				"visible_at":       visibleAt,
				"lease_expires_at": delivery.LeaseExpiresAt.String,
			},
		}, nil
	})
}

func (s *Store) Fail(ctx context.Context, deliveryID, leaseToken, reason string) (DeliveryTransitionResult, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return DeliveryTransitionResult{}, errors.New("failure reason is required")
	}

	return s.transitionLeasedDelivery(ctx, deliveryID, leaseToken, func(now time.Time, delivery leasedDeliveryRecord) (deliveryTransitionSpec, error) {
		attemptCount := delivery.AttemptCount + 1
		visibleAt := formatTimestamp(now)
		nextState := "queued"
		secondaryEventType := ""
		var secondaryEventDetail map[string]any
		if attemptCount >= maxDeliveryAttemptCount {
			nextState = "dead_letter"
			secondaryEventType = "delivery_dead_letter"
			secondaryEventDetail = map[string]any{
				"reason":        reason,
				"attempt_count": attemptCount,
			}
		}

		return deliveryTransitionSpec{
			State:            nextState,
			VisibleAt:        visibleAt,
			AttemptCount:     attemptCount,
			LastErrorText:    reason,
			PrimaryEventType: "delivery_failed",
			PrimaryEventDetail: map[string]any{
				"previous_state": delivery.State,
				"reason":         reason,
				"attempt_count":  attemptCount,
				"next_state":     nextState,
			},
			SecondaryEventType:   secondaryEventType,
			SecondaryEventDetail: secondaryEventDetail,
		}, nil
	})
}

func (s *Store) receiveOnce(ctx context.Context, addresses []string) (ReceivedMessage, error) {
	recipients, err := s.resolveRecipients(ctx, addresses)
	if err != nil {
		return ReceivedMessage{}, err
	}
	if len(recipients) == 0 {
		return ReceivedMessage{}, ErrNoMessage
	}

	addressByEndpointID := make(map[string]string, len(recipients))
	recipientEndpointIDs := make([]string, 0, len(recipients))
	for _, recipient := range recipients {
		addressByEndpointID[recipient.EndpointID] = recipient.Address
		recipientEndpointIDs = append(recipientEndpointIDs, recipient.EndpointID)
	}

	return s.claimNextDelivery(ctx, addressByEndpointID, recipientEndpointIDs)
}

func (s *Store) claimNextDelivery(ctx context.Context, addressByEndpointID map[string]string, recipientEndpointIDs []string) (ReceivedMessage, error) {
	for attempt := 1; ; attempt++ {
		nowText := formatTimestamp(s.now())
		if _, err := loadClaimCandidate(ctx, s.readDB, recipientEndpointIDs, nowText); errors.Is(err, sql.ErrNoRows) {
			return ReceivedMessage{}, ErrNoMessage
		} else if err != nil {
			return ReceivedMessage{}, fmt.Errorf("query claimable delivery: %w", err)
		}

		message, err := s.claimNextDeliveryOnce(ctx, addressByEndpointID, recipientEndpointIDs, nowText)
		if err == nil {
			return message, nil
		}
		if !isSQLiteBusy(err) {
			return ReceivedMessage{}, err
		}
		if attempt > maxClaimRetryCount {
			return ReceivedMessage{}, &ClaimContentionError{
				Attempts: attempt,
				Cause:    err,
			}
		}
		if err := waitForClaimRetry(ctx); err != nil {
			return ReceivedMessage{}, err
		}
	}
}

func (s *Store) claimNextDeliveryOnce(ctx context.Context, addressByEndpointID map[string]string, recipientEndpointIDs []string, nowText string) (ReceivedMessage, error) {
	for {
		tx, err := s.claimDB.BeginTx(ctx, nil)
		if err != nil {
			return ReceivedMessage{}, fmt.Errorf("begin receive transaction: %w", err)
		}

		candidate, err := loadClaimCandidate(ctx, tx, recipientEndpointIDs, nowText)
		if errors.Is(err, sql.ErrNoRows) {
			_ = tx.Rollback()
			return ReceivedMessage{}, ErrNoMessage
		}
		if err != nil {
			_ = tx.Rollback()
			return ReceivedMessage{}, fmt.Errorf("query claimable delivery: %w", err)
		}

		leaseToken, err := newPrefixedID("lease")
		if err != nil {
			_ = tx.Rollback()
			return ReceivedMessage{}, err
		}
		leaseExpiresAt := formatTimestamp(s.now().Add(defaultLeaseTimeout))

		result, err := tx.ExecContext(ctx, `
UPDATE deliveries
SET state = 'leased',
    lease_token = ?,
    lease_expires_at = ?,
    acked_at = NULL
WHERE delivery_id = ?
  AND recipient_endpoint_id = ?
  AND (
    (state = 'queued' AND visible_at <= ?)
    OR
    (state = 'leased' AND lease_expires_at IS NOT NULL AND lease_expires_at <= ?)
  )
`, leaseToken, leaseExpiresAt, candidate.DeliveryID, candidate.RecipientEndpointID, nowText, nowText)
		if err != nil {
			_ = tx.Rollback()
			return ReceivedMessage{}, fmt.Errorf("claim delivery: %w", err)
		}

		rowsAffected, err := result.RowsAffected()
		if err != nil {
			_ = tx.Rollback()
			return ReceivedMessage{}, fmt.Errorf("read claim rows affected: %w", err)
		}
		if rowsAffected == 0 {
			_ = tx.Rollback()
			continue
		}

		address := addressByEndpointID[candidate.RecipientEndpointID]
		if err := insertDeliveryEvent(ctx, tx, "delivery_leased", candidate.RecipientEndpointID, candidate.MessageID, candidate.DeliveryID, map[string]any{
			"previous_state":    candidate.State,
			"recipient_address": address,
			"lease_expires_at":  leaseExpiresAt,
			"reclaimed":         candidate.State == "leased",
		}, nowText); err != nil {
			_ = tx.Rollback()
			return ReceivedMessage{}, err
		}

		if err := tx.Commit(); err != nil {
			_ = tx.Rollback()
			return ReceivedMessage{}, fmt.Errorf("commit receive transaction: %w", err)
		}

		body, err := s.readBlob(candidate.BodyBlobRef, candidate.BodySize, candidate.BodySHA256)
		if err != nil {
			return ReceivedMessage{}, s.recoverReceiveFailure(ctx, candidate.DeliveryID, leaseToken, err)
		}

		message := ReceivedMessage{
			DeliveryID:          candidate.DeliveryID,
			MessageID:           candidate.MessageID,
			RecipientAddress:    address,
			RecipientEndpointID: candidate.RecipientEndpointID,
			State:               "leased",
			VisibleAt:           candidate.VisibleAt,
			LeaseToken:          leaseToken,
			LeaseExpiresAt:      leaseExpiresAt,
			AttemptCount:        candidate.AttemptCount,
			MessageCreatedAt:    candidate.MessageCreatedAt,
			Subject:             candidate.Subject,
			ContentType:         candidate.ContentType,
			SchemaVersion:       candidate.SchemaVersion,
			BodyBlobRef:         candidate.BodyBlobRef,
			BodySize:            candidate.BodySize,
			BodySHA256:          candidate.BodySHA256,
			Body:                string(body),
		}
		if candidate.SenderEndpointID.Valid {
			message.SenderEndpointID = &candidate.SenderEndpointID.String
		}
		return message, nil
	}
}

func waitForClaimRetry(ctx context.Context) error {
	timer := time.NewTimer(claimRetryDelay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (s *Store) releaseReceivedBatchMessages(ctx context.Context, messages []ReceivedMessage) error {
	var releaseErrs []error
	for i := len(messages) - 1; i >= 0; i-- {
		message := messages[i]
		if _, err := s.Release(ctx, message.DeliveryID, message.LeaseToken); err != nil {
			releaseErrs = append(releaseErrs, fmt.Errorf("release received batch delivery %q: %w", message.DeliveryID, err))
		}
	}
	if len(releaseErrs) == 0 {
		return nil
	}
	return errors.Join(releaseErrs...)
}

func (s *Store) recoverReceiveFailure(ctx context.Context, deliveryID, leaseToken string, readErr error) error {
	if _, err := s.Fail(ctx, deliveryID, leaseToken, readErr.Error()); err != nil {
		return &ReceiveRecoveryError{
			DeliveryID:  deliveryID,
			ReceiveErr:  readErr,
			RecoveryErr: err,
		}
	}
	return readErr
}

func (s *Store) hasClaimableDelivery(ctx context.Context, addresses []string) (bool, error) {
	recipients, err := s.resolveRecipients(ctx, addresses)
	if err != nil {
		return false, err
	}
	if len(recipients) == 0 {
		return false, nil
	}

	recipientEndpointIDs := make([]string, 0, len(recipients))
	for _, recipient := range recipients {
		recipientEndpointIDs = append(recipientEndpointIDs, recipient.EndpointID)
	}

	nowText := formatTimestamp(s.now())
	if _, err := loadClaimCandidate(ctx, s.readDB, recipientEndpointIDs, nowText); errors.Is(err, sql.ErrNoRows) {
		return false, nil
	} else if err != nil {
		return false, fmt.Errorf("query claimable delivery: %w", err)
	}

	return true, nil
}

func (s *Store) transitionLeasedDelivery(ctx context.Context, deliveryID, leaseToken string, build func(time.Time, leasedDeliveryRecord) (deliveryTransitionSpec, error)) (DeliveryTransitionResult, error) {
	deliveryID = strings.TrimSpace(deliveryID)
	leaseToken = strings.TrimSpace(leaseToken)
	if deliveryID == "" {
		return DeliveryTransitionResult{}, errors.New("delivery id is required")
	}
	if leaseToken == "" {
		return DeliveryTransitionResult{}, errors.New("lease token is required")
	}

	now := s.now()
	tx, err := s.writeDB.BeginTx(ctx, nil)
	if err != nil {
		return DeliveryTransitionResult{}, fmt.Errorf("begin delivery transition transaction: %w", err)
	}
	defer tx.Rollback()

	delivery, err := loadLeasedDeliveryRecord(ctx, tx, deliveryID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return DeliveryTransitionResult{}, fmt.Errorf("delivery %q not found", deliveryID)
		}
		return DeliveryTransitionResult{}, fmt.Errorf("load delivery %q: %w", deliveryID, err)
	}
	if delivery.State != "leased" {
		return DeliveryTransitionResult{}, fmt.Errorf("delivery %q is in state %q, want leased", deliveryID, delivery.State)
	}
	if !delivery.LeaseToken.Valid || delivery.LeaseToken.String != leaseToken {
		return DeliveryTransitionResult{}, fmt.Errorf("validate lease token for %q: %w", deliveryID, ErrLeaseTokenMismatch)
	}
	if !delivery.LeaseExpiresAt.Valid {
		return DeliveryTransitionResult{}, fmt.Errorf("delivery %q is missing lease expiry", deliveryID)
	}

	expiresAt, err := time.Parse(time.RFC3339Nano, delivery.LeaseExpiresAt.String)
	if err != nil {
		return DeliveryTransitionResult{}, fmt.Errorf("parse lease expiry for %q: %w", deliveryID, err)
	}
	if !expiresAt.After(now) {
		return DeliveryTransitionResult{}, fmt.Errorf("validate lease expiry for %q: %w", deliveryID, ErrLeaseExpired)
	}

	spec, err := build(now, delivery)
	if err != nil {
		return DeliveryTransitionResult{}, err
	}

	result, err := tx.ExecContext(ctx, `
UPDATE deliveries
SET state = ?,
    visible_at = ?,
    lease_token = NULL,
    lease_expires_at = NULL,
    acked_at = ?,
    attempt_count = ?,
    last_error_code = NULL,
    last_error_text = ?
WHERE delivery_id = ?
  AND state = 'leased'
  AND lease_token = ?
  AND lease_expires_at = ?
`, spec.State, spec.VisibleAt, spec.AckedAt, spec.AttemptCount, spec.LastErrorText, delivery.DeliveryID, leaseToken, delivery.LeaseExpiresAt.String)
	if err != nil {
		return DeliveryTransitionResult{}, fmt.Errorf("update delivery %q: %w", deliveryID, err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return DeliveryTransitionResult{}, fmt.Errorf("read transition rows affected for %q: %w", deliveryID, err)
	}
	if rowsAffected == 0 {
		return DeliveryTransitionResult{}, fmt.Errorf("delivery %q changed while updating", deliveryID)
	}

	eventTimestamp := formatTimestamp(now)
	if err := insertDeliveryEvent(ctx, tx, spec.PrimaryEventType, delivery.RecipientEndpointID, delivery.MessageID, delivery.DeliveryID, spec.PrimaryEventDetail, eventTimestamp); err != nil {
		return DeliveryTransitionResult{}, err
	}
	if spec.SecondaryEventType != "" {
		if err := insertDeliveryEvent(ctx, tx, spec.SecondaryEventType, delivery.RecipientEndpointID, delivery.MessageID, delivery.DeliveryID, spec.SecondaryEventDetail, eventTimestamp); err != nil {
			return DeliveryTransitionResult{}, err
		}
	}

	if err := tx.Commit(); err != nil {
		return DeliveryTransitionResult{}, fmt.Errorf("commit transition for %q: %w", deliveryID, err)
	}

	resultSummary := DeliveryTransitionResult{
		DeliveryID:   delivery.DeliveryID,
		State:        spec.State,
		VisibleAt:    spec.VisibleAt,
		AttemptCount: spec.AttemptCount,
	}
	if ackedAt, ok := spec.AckedAt.(string); ok {
		resultSummary.AckedAt = ackedAt
	}
	return resultSummary, nil
}

func loadClaimCandidate(ctx context.Context, querier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, recipientEndpointIDs []string, nowText string) (claimCandidate, error) {
	if len(recipientEndpointIDs) == 0 {
		return claimCandidate{}, sql.ErrNoRows
	}

	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(recipientEndpointIDs)), ",")
	args := make([]any, 0, len(recipientEndpointIDs)+2)
	for _, recipientEndpointID := range recipientEndpointIDs {
		args = append(args, recipientEndpointID)
	}
	args = append(args, nowText, nowText)

	var candidate claimCandidate
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
  AND (
    (d.state = 'queued' AND d.visible_at <= ?)
    OR
    (d.state = 'leased' AND d.lease_expires_at IS NOT NULL AND d.lease_expires_at <= ?)
  )
ORDER BY d.visible_at ASC, m.created_at ASC, d.delivery_id ASC
LIMIT 1
`, placeholders), args...).Scan(
		&candidate.DeliveryID,
		&candidate.MessageID,
		&candidate.RecipientEndpointID,
		&candidate.SenderEndpointID,
		&candidate.State,
		&candidate.VisibleAt,
		&candidate.AttemptCount,
		&candidate.MessageCreatedAt,
		&candidate.Subject,
		&candidate.ContentType,
		&candidate.SchemaVersion,
		&candidate.BodyBlobRef,
		&candidate.BodySize,
		&candidate.BodySHA256,
	)
	if err != nil {
		return claimCandidate{}, err
	}
	return candidate, nil
}

func isSQLiteBusy(err error) bool {
	var sqliteErr sqlite3.Error
	if !errors.As(err, &sqliteErr) {
		return false
	}
	return sqliteErr.Code == sqlite3.ErrBusy || sqliteErr.Code == sqlite3.ErrLocked
}

func loadLeasedDeliveryRecord(ctx context.Context, tx *sql.Tx, deliveryID string) (leasedDeliveryRecord, error) {
	var delivery leasedDeliveryRecord
	err := tx.QueryRowContext(ctx, `
SELECT delivery_id, message_id, recipient_endpoint_id, state, visible_at, lease_token, lease_expires_at, attempt_count
FROM deliveries
WHERE delivery_id = ?
`, deliveryID).Scan(
		&delivery.DeliveryID,
		&delivery.MessageID,
		&delivery.RecipientEndpointID,
		&delivery.State,
		&delivery.VisibleAt,
		&delivery.LeaseToken,
		&delivery.LeaseExpiresAt,
		&delivery.AttemptCount,
	)
	if err != nil {
		return leasedDeliveryRecord{}, err
	}
	return delivery, nil
}

func insertDeliveryEvent(ctx context.Context, tx *sql.Tx, eventType, endpointID, messageID, deliveryID string, detail map[string]any, createdAt string) error {
	eventID, err := newPrefixedID("evt")
	if err != nil {
		return err
	}
	detailJSON, err := marshalDetail(detail)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO events (event_id, created_at, event_type, endpoint_id, message_id, delivery_id, detail_json)
VALUES (?, ?, ?, ?, ?, ?, ?)
`, eventID, createdAt, eventType, endpointID, messageID, deliveryID, detailJSON); err != nil {
		return fmt.Errorf("insert %s event: %w", eventType, err)
	}
	return nil
}

func (s *Store) readBlob(blobRef string, expectedSize int64, expectedSHA256 string) ([]byte, error) {
	body, err := os.ReadFile(filepath.Join(s.blobDir, blobRef))
	if err != nil {
		return nil, fmt.Errorf("read body blob %q: %w", blobRef, err)
	}
	if int64(len(body)) != expectedSize {
		return nil, fmt.Errorf("%w for %q: size=%d want=%d", ErrBodyIntegrity, blobRef, len(body), expectedSize)
	}

	bodySHA256 := sha256.Sum256(body)
	if actualSHA256 := hex.EncodeToString(bodySHA256[:]); actualSHA256 != expectedSHA256 {
		return nil, fmt.Errorf("%w for %q: sha256=%s want=%s", ErrBodyIntegrity, blobRef, actualSHA256, expectedSHA256)
	}
	return body, nil
}
