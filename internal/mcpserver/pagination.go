package mcpserver

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/ruiheng/waypost/internal/waypost"
)

func memoryCursorScope(parts ...string) string {
	hash := sha256.New()
	for _, part := range parts {
		hash.Write([]byte(part))
		hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil))
}

type memoryPageCursor struct {
	Version int    `json:"v"`
	Kind    string `json:"kind"`
	Scope   string `json:"scope"`
	Key     string `json:"key"`
}

func normalizeMemoryPage(limit *int, rawCursor, kind, scope string) (int, string, error) {
	pageSize := waypost.DefaultPageSize
	if limit != nil {
		pageSize = *limit
	}
	if pageSize < 1 || pageSize > waypost.MaxPageSize {
		return 0, "", fmt.Errorf("limit must be between 1 and %d", waypost.MaxPageSize)
	}
	rawCursor = strings.TrimSpace(rawCursor)
	if rawCursor == "" {
		return pageSize, "", nil
	}
	if len(rawCursor) > 4096 {
		return 0, "", errors.New("cursor is too long")
	}
	payload, err := base64.RawURLEncoding.DecodeString(rawCursor)
	if err != nil {
		return 0, "", errors.New("invalid pagination cursor")
	}
	var cursor memoryPageCursor
	if err := json.Unmarshal(payload, &cursor); err != nil || cursor.Version != 1 || cursor.Kind != kind || cursor.Scope != scope || cursor.Key == "" {
		return 0, "", errors.New("pagination cursor does not match this query")
	}
	return pageSize, cursor.Key, nil
}

func encodeMemoryPageCursor(kind, scope, key string) (string, error) {
	payload, err := json.Marshal(memoryPageCursor{Version: 1, Kind: kind, Scope: scope, Key: key})
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

func validateMCPItems(label string, count int) error {
	return waypost.ValidateInputItemCount(label, count)
}
