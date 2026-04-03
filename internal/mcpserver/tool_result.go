package mcpserver

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/ruiheng/agent-mailbox/internal/mailbox"
)

func (s *Service) toolResult(ctx context.Context, result map[string]any) (*mcp.CallToolResult, map[string]any, error) {
	withHint := s.withMailHintBestEffort(ctx, result)
	s.startUnreadPushLoop()
	return nil, withHint, nil
}

func (s *Service) withMailHintBestEffort(ctx context.Context, result map[string]any) map[string]any {
	bound := s.sessions.snapshotState().BoundAddresses
	if len(bound) == 0 {
		return result
	}

	available, err := withMailboxService(ctx, s.mailboxServices, func(service mailboxService) (bool, error) {
		return service.HasVisibleDelivery(ctx, mailbox.WaitParams{Addresses: bound})
	})
	if err != nil || !available {
		return result
	}

	result["mail_hint"] = defaultMailHint
	return result
}
