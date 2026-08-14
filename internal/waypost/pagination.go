package waypost

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const (
	DefaultPageSize = 50
	MaxPageSize     = 100
	MaxInputItems   = 100
	maxCursorLength = 4096
)

var (
	ErrInvalidPaginationCursor = errors.New("invalid pagination cursor")
	ErrPaginationRequired      = errors.New("pagination required")
)

type PageParams struct {
	Limit  int
	Cursor string
}

type Page[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"next_cursor,omitempty"`
}

type pageCursor struct {
	Version int      `json:"v"`
	Kind    string   `json:"kind"`
	Scope   string   `json:"scope"`
	Keys    []string `json:"keys"`
}

func normalizePageParams(params PageParams) (PageParams, error) {
	if params.Limit < 1 || params.Limit > MaxPageSize {
		return PageParams{}, invalidArgumentError(fmt.Errorf("limit must be between 1 and %d", MaxPageSize))
	}
	params.Cursor = strings.TrimSpace(params.Cursor)
	if len(params.Cursor) > maxCursorLength {
		return PageParams{}, invalidArgumentError(fmt.Errorf("%w: cursor is too long", ErrInvalidPaginationCursor))
	}
	return params, nil
}

func cursorScope(parts ...string) string {
	hash := sha256.New()
	for _, part := range parts {
		hash.Write([]byte(part))
		hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func encodePageCursor(kind, scope string, keys ...string) (string, error) {
	payload, err := json.Marshal(pageCursor{
		Version: 1,
		Kind:    kind,
		Scope:   scope,
		Keys:    keys,
	})
	if err != nil {
		return "", fmt.Errorf("encode pagination cursor: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

func decodePageCursor(raw, kind, scope string, keyCount int) ([]string, error) {
	if raw == "" {
		return nil, nil
	}
	if len(raw) > maxCursorLength {
		return nil, fmt.Errorf("%w: cursor is too long", ErrInvalidPaginationCursor)
	}
	payload, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return nil, ErrInvalidPaginationCursor
	}
	var cursor pageCursor
	if err := json.Unmarshal(payload, &cursor); err != nil {
		return nil, ErrInvalidPaginationCursor
	}
	if cursor.Version != 1 || cursor.Kind != kind || cursor.Scope != scope || len(cursor.Keys) != keyCount {
		return nil, fmt.Errorf("%w: cursor does not match this query", ErrInvalidPaginationCursor)
	}
	for _, key := range cursor.Keys {
		if key == "" {
			return nil, ErrInvalidPaginationCursor
		}
	}
	return cursor.Keys, nil
}

func validateInputItemCount(label string, count int) error {
	if count > MaxInputItems {
		return invalidArgumentError(fmt.Errorf("%s accepts at most %d items", label, MaxInputItems))
	}
	return nil
}

// ValidateInputItemCount applies the public adapter input fan-out limit.
func ValidateInputItemCount(label string, count int) error {
	return validateInputItemCount(label, count)
}

func completeCompatibilityPage[T any](page Page[T], operation string) ([]T, error) {
	if page.NextCursor != "" {
		return nil, fmt.Errorf("%s has more than %d results; use the paginated API: %w", operation, len(page.Items), ErrPaginationRequired)
	}
	return page.Items, nil
}
