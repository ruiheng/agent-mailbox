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
	Addresses  []string
	GroupViews []GroupStaleView
	OlderThan  time.Duration
}

type GroupStaleView struct {
	Address string `json:"address" yaml:"address"`
	Person  string `json:"person" yaml:"person"`
}

type StaleAddress struct {
	Address          string `json:"address" yaml:"address"`
	Person           string `json:"person,omitempty" yaml:"person,omitempty"`
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

	stale := make([]StaleAddress, 0)
	if len(params.Addresses) > 0 {
		personal, err := s.listPersonalStaleAddresses(ctx, params.Addresses, params.OlderThan)
		if err != nil {
			return nil, err
		}
		stale = append(stale, personal...)
	}

	if len(params.GroupViews) > 0 {
		groupViews, err := normalizeGroupStaleViews(params.GroupViews)
		if err != nil {
			return nil, err
		}
		groupStale, err := s.listGroupStaleAddresses(ctx, groupViews, params.OlderThan)
		if err != nil {
			return nil, err
		}
		stale = append(stale, groupStale...)
	}

	if len(stale) == 0 {
		return []StaleAddress{}, nil
	}

	sort.Slice(stale, func(i, j int) bool {
		if stale[i].OldestEligibleAt != stale[j].OldestEligibleAt {
			return stale[i].OldestEligibleAt < stale[j].OldestEligibleAt
		}
		if stale[i].Address != stale[j].Address {
			return stale[i].Address < stale[j].Address
		}
		return stale[i].Person < stale[j].Person
	})

	return stale, nil
}

func (s *Store) listPersonalStaleAddresses(ctx context.Context, addresses []string, olderThan time.Duration) ([]StaleAddress, error) {
	addresses, err := normalizeAddresses("", addresses, "--for")
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
	staleBeforeText := formatTimestamp(now.Add(-olderThan))

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

	return stale, nil
}

func (s *Store) listGroupStaleAddresses(ctx context.Context, groupViews []GroupStaleView, olderThan time.Duration) ([]StaleAddress, error) {
	staleBeforeText := formatTimestamp(s.now().Add(-olderThan))
	stale := make([]StaleAddress, 0, len(groupViews))
	for _, groupView := range groupViews {
		viewer, err := s.resolveGroupViewer(ctx, s.readDB, groupView.Address, groupView.Person)
		if err != nil {
			return nil, err
		}
		records, err := queryGroupMessageRecords(ctx, s.readDB, viewer, true, 0)
		if err != nil {
			return nil, err
		}
		if len(records) == 0 {
			continue
		}
		if records[0].MessageCreatedAt > staleBeforeText {
			continue
		}
		stale = append(stale, StaleAddress{
			Address:          viewer.Group.Address,
			Person:           viewer.Person,
			OldestEligibleAt: records[0].MessageCreatedAt,
			ClaimableCount:   len(records),
		})
	}
	return stale, nil
}

func normalizeGroupStaleViews(groupViews []GroupStaleView) ([]GroupStaleView, error) {
	if len(groupViews) == 0 {
		return nil, nil
	}

	normalized := make([]GroupStaleView, 0, len(groupViews))
	seen := make(map[string]struct{}, len(groupViews))
	for _, groupView := range groupViews {
		address := strings.TrimSpace(groupView.Address)
		if address == "" {
			return nil, errors.New("--for is required")
		}
		person := strings.TrimSpace(groupView.Person)
		if person == "" {
			return nil, errors.New("--as is required")
		}
		key := address + "\x00" + person
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		normalized = append(normalized, GroupStaleView{
			Address: address,
			Person:  person,
		})
	}
	return normalized, nil
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
