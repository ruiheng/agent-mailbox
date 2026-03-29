package mailbox

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
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

type AddressInspection struct {
	Address    string  `json:"address"`
	Kind       string  `json:"kind"`
	EndpointID *string `json:"endpoint_id,omitempty"`
	GroupID    *string `json:"group_id,omitempty"`
}

func (s *Store) CreateGroup(ctx context.Context, address string) (GroupRecord, error) {
	address = strings.TrimSpace(address)
	if address == "" {
		return GroupRecord{}, errors.New("group address is required")
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
	groupAddress = strings.TrimSpace(groupAddress)
	if groupAddress == "" {
		return GroupMembershipRecord{}, errors.New("group address is required")
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
	groupAddress = strings.TrimSpace(groupAddress)
	if groupAddress == "" {
		return GroupMembershipRecord{}, errors.New("group address is required")
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
	groupAddress = strings.TrimSpace(groupAddress)
	if groupAddress == "" {
		return nil, errors.New("group address is required")
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

func (s *Store) InspectAddress(ctx context.Context, address string) (AddressInspection, error) {
	address = strings.TrimSpace(address)
	if address == "" {
		return AddressInspection{}, errors.New("address is required")
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
