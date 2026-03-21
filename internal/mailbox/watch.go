package mailbox

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

type WatchParams struct {
	Address   string
	Addresses []string
	State     string
	Timeout   time.Duration
}

type WaitParams struct {
	Address   string
	Addresses []string
	Timeout   time.Duration
}

func (s *Store) Wait(ctx context.Context, params WaitParams) (ListedDelivery, error) {
	addresses, err := normalizeAddresses(params.Address, params.Addresses, "--for")
	if err != nil {
		return ListedDelivery{}, err
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
		delivery, err := s.waitOnce(attemptCtx, addresses)
		cancel()
		if err == nil {
			return delivery, nil
		}
		if !deadline.IsZero() && errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
			return ListedDelivery{}, ErrNoMessage
		}
		if !errors.Is(err, ErrNoMessage) {
			return ListedDelivery{}, err
		}
		if !deadline.IsZero() {
			remaining := time.Until(deadline)
			if remaining <= 0 {
				return ListedDelivery{}, ErrNoMessage
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
			return ListedDelivery{}, ctx.Err()
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

func (s *Store) Watch(ctx context.Context, params WatchParams, emit func(ListedDelivery) error) error {
	addresses, err := normalizeAddresses(params.Address, params.Addresses, "--for")
	if err != nil {
		return err
	}

	state := strings.TrimSpace(params.State)
	emitted := make(map[string]string)
	idleDeadline := time.Time{}
	if params.Timeout > 0 {
		idleDeadline = time.Now().Add(params.Timeout)
	}

	delay := initialPollDelay
	for {
		recipients, err := s.resolveRecipients(ctx, addresses)
		if err != nil {
			return err
		}

		deliveries, err := s.listDeliveriesForRecipients(ctx, recipients, state)
		if err != nil {
			return err
		}

		observedNewDelivery := false
		for _, delivery := range deliveries {
			fingerprint := watchFingerprint(delivery)
			if emitted[delivery.DeliveryID] == fingerprint {
				continue
			}
			if err := emit(delivery); err != nil {
				return err
			}
			emitted[delivery.DeliveryID] = fingerprint
			observedNewDelivery = true
		}

		if observedNewDelivery {
			delay = initialPollDelay
			if params.Timeout > 0 {
				idleDeadline = time.Now().Add(params.Timeout)
			}
		} else if delay < maxPollDelay {
			delay *= 2
			if delay > maxPollDelay {
				delay = maxPollDelay
			}
		}

		waitFor := delay
		if !idleDeadline.IsZero() {
			remaining := time.Until(idleDeadline)
			if remaining <= 0 {
				return nil
			}
			if waitFor > remaining {
				waitFor = remaining
			}
		}

		timer := time.NewTimer(waitFor)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (s *Store) waitOnce(ctx context.Context, addresses []string) (ListedDelivery, error) {
	recipients, err := s.resolveRecipients(ctx, addresses)
	if err != nil {
		return ListedDelivery{}, err
	}

	deliveries, err := s.listDeliveriesForRecipients(ctx, recipients, "")
	if err != nil {
		return ListedDelivery{}, err
	}
	if len(deliveries) == 0 {
		return ListedDelivery{}, ErrNoMessage
	}

	return deliveries[0], nil
}

func (s *Store) listDeliveriesForRecipients(ctx context.Context, recipients []resolvedRecipient, state string) ([]ListedDelivery, error) {
	if len(recipients) == 0 {
		return []ListedDelivery{}, nil
	}

	addressByEndpointID := make(map[string]string, len(recipients))
	recipientEndpointIDs := make([]string, 0, len(recipients))
	for _, recipient := range recipients {
		addressByEndpointID[recipient.EndpointID] = recipient.Address
		recipientEndpointIDs = append(recipientEndpointIDs, recipient.EndpointID)
	}

	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(recipientEndpointIDs)), ",")
	args := make([]any, 0, len(recipientEndpointIDs)+1)
	for _, recipientEndpointID := range recipientEndpointIDs {
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
		args = append(args, formatTimestamp(s.now()))
	} else {
		query += `
  AND d.state = ?
`
		args = append(args, state)
	}
	query += `
ORDER BY d.visible_at ASC, m.created_at ASC, d.delivery_id ASC
`

	rows, err := s.readDB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query deliveries: %w", err)
	}
	defer rows.Close()

	deliveries := make([]ListedDelivery, 0)
	for rows.Next() {
		var delivery ListedDelivery
		var senderID sql.NullString
		if err := rows.Scan(
			&delivery.DeliveryID,
			&delivery.MessageID,
			&delivery.RecipientEndpointID,
			&senderID,
			&delivery.State,
			&delivery.VisibleAt,
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
		delivery.RecipientAddress = addressByEndpointID[delivery.RecipientEndpointID]
		if senderID.Valid {
			delivery.SenderEndpointID = &senderID.String
		}
		deliveries = append(deliveries, delivery)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate delivery rows: %w", err)
	}

	return deliveries, nil
}

func watchFingerprint(delivery ListedDelivery) string {
	senderEndpointID := ""
	if delivery.SenderEndpointID != nil {
		senderEndpointID = *delivery.SenderEndpointID
	}

	parts := []string{
		delivery.MessageID,
		delivery.RecipientAddress,
		delivery.RecipientEndpointID,
		senderEndpointID,
		delivery.State,
		delivery.VisibleAt,
		delivery.MessageCreatedAt,
		delivery.Subject,
		delivery.ContentType,
		delivery.SchemaVersion,
		delivery.BodyBlobRef,
		strconv.FormatInt(delivery.BodySize, 10),
		delivery.BodySHA256,
	}
	return strings.Join(parts, "\x00")
}
