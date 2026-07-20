package waypost

import (
	"context"
	"strings"
)

type claimMetadataContextKey struct{}

type ClaimMetadata struct {
	Source             string
	Tool               string
	BoundAddresses     []string
	AgentDeckSessionID string
	AgentSessionID     string
	Workdir            string
}

func WithClaimMetadata(ctx context.Context, metadata ClaimMetadata) context.Context {
	return context.WithValue(ctx, claimMetadataContextKey{}, normalizeClaimMetadata(metadata))
}

func claimMetadataFromContext(ctx context.Context) ClaimMetadata {
	if ctx == nil {
		return ClaimMetadata{}
	}
	metadata, ok := ctx.Value(claimMetadataContextKey{}).(ClaimMetadata)
	if !ok {
		return ClaimMetadata{}
	}
	return normalizeClaimMetadata(metadata)
}

func normalizeClaimMetadata(metadata ClaimMetadata) ClaimMetadata {
	metadata.Source = strings.TrimSpace(metadata.Source)
	metadata.Tool = strings.TrimSpace(metadata.Tool)
	metadata.AgentDeckSessionID = strings.TrimSpace(metadata.AgentDeckSessionID)
	metadata.AgentSessionID = strings.TrimSpace(metadata.AgentSessionID)
	metadata.Workdir = strings.TrimSpace(metadata.Workdir)
	metadata.BoundAddresses = normalizeClaimAddressList(metadata.BoundAddresses)
	return metadata
}

func normalizeClaimAddressList(addresses []string) []string {
	normalized, err := NormalizeAddressList(addresses)
	if err == nil {
		return normalized
	}
	out := make([]string, 0, len(addresses))
	seen := map[string]bool{}
	for _, address := range addresses {
		trimmed := strings.TrimSpace(address)
		if trimmed == "" || seen[trimmed] {
			continue
		}
		seen[trimmed] = true
		out = append(out, trimmed)
	}
	return out
}
