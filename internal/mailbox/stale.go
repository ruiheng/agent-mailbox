package mailbox

import (
	"context"
	"errors"
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

type ClaimableAddress struct {
	Address          string `json:"address" yaml:"address"`
	OldestEligibleAt string `json:"oldest_eligible_at" yaml:"oldest_eligible_at"`
	ClaimableCount   int    `json:"claimable_count" yaml:"claimable_count"`
}

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

func (s *Store) ListClaimableAddresses(ctx context.Context, addresses []string) ([]ClaimableAddress, error) {
	addresses, err := normalizeAddresses("", addresses, "--for")
	if err != nil {
		return nil, err
	}

	scope, err := s.availability().resolvePersonal(ctx, s.readDB, addresses)
	if err != nil {
		return nil, err
	}
	if scope.empty() {
		return []ClaimableAddress{}, nil
	}

	return s.availability().listClaimablePersonalAddresses(ctx, s.readDB, scope, formatTimestamp(s.now()))
}

func (s *Store) listPersonalStaleAddresses(ctx context.Context, addresses []string, olderThan time.Duration) ([]StaleAddress, error) {
	addresses, err := normalizeAddresses("", addresses, "--for")
	if err != nil {
		return nil, err
	}

	scope, err := s.availability().resolvePersonal(ctx, s.readDB, addresses)
	if err != nil {
		return nil, err
	}
	if scope.empty() {
		return []StaleAddress{}, nil
	}

	now := s.now()
	staleBeforeText := formatTimestamp(now.Add(-olderThan))
	return s.availability().listPersonalStaleAddresses(ctx, s.readDB, scope, formatTimestamp(now), staleBeforeText)
}

func (s *Store) listGroupStaleAddresses(ctx context.Context, groupViews []GroupStaleView, olderThan time.Duration) ([]StaleAddress, error) {
	staleBeforeText := formatTimestamp(s.now().Add(-olderThan))
	stale := make([]StaleAddress, 0, len(groupViews))
	availability := s.availability()
	for _, groupView := range groupViews {
		scope, err := availability.resolveGroup(ctx, s.readDB, groupView.Address, groupView.Person)
		if err != nil {
			return nil, err
		}
		records, err := availability.listGroupMessages(ctx, s.readDB, scope, true, 0)
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
			Address:          scope.viewer.Group.Address,
			Person:           scope.viewer.Person,
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
