package mailbox

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

type StaleAddressesParams struct {
	Addresses []string
	OlderThan time.Duration
}

type StaleAddress struct {
	Address          string `json:"address" yaml:"address"`
	OldestEligibleAt string `json:"oldest_eligible_at" yaml:"oldest_eligible_at"`
	ClaimableCount   int    `json:"claimable_count" yaml:"claimable_count"`
}

type staleAddressRecord struct {
	RecipientEndpointID string
	OldestEligibleAt    string
	ClaimableCount      int
}

const claimableDeliveryFilter = `(
    (d.state = 'queued' AND d.visible_at <= ?)
    OR
    (d.state = 'leased' AND d.lease_expires_at IS NOT NULL AND d.lease_expires_at <= ?)
  )`

const claimableDeliveryEligibleAtExpr = `CASE
    WHEN d.state = 'queued' THEN d.visible_at
    WHEN d.state = 'leased' THEN d.lease_expires_at
  END`

func (s *Store) ListStaleAddresses(ctx context.Context, params StaleAddressesParams) ([]StaleAddress, error) {
	if params.OlderThan <= 0 {
		return nil, errors.New("older_than must be greater than 0")
	}

	addresses, err := normalizeAddresses("", params.Addresses, "--for")
	if err != nil {
		return nil, err
	}

	recipients, err := s.resolveRecipients(ctx, addresses)
	if err != nil {
		return nil, err
	}
	if len(recipients) == 0 {
		return []StaleAddress{}, nil
	}

	addressByEndpointID := make(map[string]string, len(recipients))
	recipientEndpointIDs := make([]string, 0, len(recipients))
	for _, recipient := range recipients {
		addressByEndpointID[recipient.EndpointID] = recipient.Address
		recipientEndpointIDs = append(recipientEndpointIDs, recipient.EndpointID)
	}

	now := s.now()
	nowText := formatTimestamp(now)
	staleBeforeText := formatTimestamp(now.Add(-params.OlderThan))

	records, err := loadStaleAddressRecords(ctx, s.readDB, recipientEndpointIDs, nowText, staleBeforeText)
	if err != nil {
		return nil, fmt.Errorf("query stale addresses: %w", err)
	}

	stale := make([]StaleAddress, 0, len(records))
	for _, record := range records {
		stale = append(stale, StaleAddress{
			Address:          addressByEndpointID[record.RecipientEndpointID],
			OldestEligibleAt: record.OldestEligibleAt,
			ClaimableCount:   record.ClaimableCount,
		})
	}

	sort.Slice(stale, func(i, j int) bool {
		if stale[i].OldestEligibleAt != stale[j].OldestEligibleAt {
			return stale[i].OldestEligibleAt < stale[j].OldestEligibleAt
		}
		return stale[i].Address < stale[j].Address
	})

	return stale, nil
}

func loadStaleAddressRecords(ctx context.Context, querier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, recipientEndpointIDs []string, nowText, staleBeforeText string) ([]staleAddressRecord, error) {
	if len(recipientEndpointIDs) == 0 {
		return []staleAddressRecord{}, nil
	}

	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(recipientEndpointIDs)), ",")
	args := make([]any, 0, len(recipientEndpointIDs)+3)
	for _, recipientEndpointID := range recipientEndpointIDs {
		args = append(args, recipientEndpointID)
	}
	args = append(args, nowText, nowText, staleBeforeText)

	query := fmt.Sprintf(`
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
`, claimableDeliveryEligibleAtExpr, placeholders, claimableDeliveryFilter, claimableDeliveryEligibleAtExpr)

	rows, err := querier.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	records := make([]staleAddressRecord, 0)
	for rows.Next() {
		var record staleAddressRecord
		if err := rows.Scan(&record.RecipientEndpointID, &record.OldestEligibleAt, &record.ClaimableCount); err != nil {
			return nil, fmt.Errorf("scan stale address row: %w", err)
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate stale address rows: %w", err)
	}

	return records, nil
}
