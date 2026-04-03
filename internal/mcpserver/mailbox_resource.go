package mcpserver

import (
	"context"
	"encoding/json"
	"log"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type resourceSubscriptionState struct {
	mu       sync.Mutex
	sessions map[*mcp.ServerSession]bool
}

func newResourceSubscriptionState() *resourceSubscriptionState {
	return &resourceSubscriptionState{
		sessions: map[*mcp.ServerSession]bool{},
	}
}

func (s *Service) registerMailboxOverviewResource(server *mcp.Server) {
	server.AddResource(&mcp.Resource{
		URI:         mailboxOverviewURI,
		Name:        "mailbox-bound-overview",
		Title:       "Mailbox Overview",
		Description: "Summary snapshot of the currently bound mailbox scope for this MCP server instance.",
		MIMEType:    "application/json",
	}, s.readMailboxOverviewResource)
}

func (s *Service) readMailboxOverviewResource(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	if req.Params.URI != mailboxOverviewURI {
		return nil, mcp.ResourceNotFoundError(req.Params.URI)
	}

	overview, err := s.mailboxOverviewSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(overview)
	if err != nil {
		return nil, err
	}
	return &mcp.ReadResourceResult{
		Contents: []*mcp.ResourceContents{{
			URI:      mailboxOverviewURI,
			MIMEType: "application/json",
			Text:     string(body),
		}},
	}, nil
}

func (s *Service) subscribeResource(_ context.Context, req *mcp.SubscribeRequest) error {
	if req.Params.URI != mailboxOverviewURI {
		return mcp.ResourceNotFoundError(req.Params.URI)
	}
	s.overviewSubscriptions.add(req.Session)
	return nil
}

func (s *Service) unsubscribeResource(_ context.Context, req *mcp.UnsubscribeRequest) error {
	if req.Params.URI != mailboxOverviewURI {
		return mcp.ResourceNotFoundError(req.Params.URI)
	}
	s.overviewSubscriptions.remove(req.Session)
	return nil
}

func (s *Service) mailboxOverviewSnapshot(ctx context.Context) (map[string]any, error) {
	bound, err := s.sessions.boundState(ctx)
	if err != nil {
		return nil, err
	}
	visible, err := s.visibleMailboxSnapshot(ctx, bound.BoundAddresses)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"bound_addresses":      bound.BoundAddresses,
		"default_sender":       nilIfEmpty(bound.DefaultSender),
		"has_visible_delivery": visible.QueuedVisibleCount > 0,
		"queued_visible_count": visible.QueuedVisibleCount,
		"oldest_eligible_at":   nilIfEmpty(visible.OldestEligibleAt),
	}, nil
}

func (s *Service) emitMailboxOverviewUpdatedBestEffort(ctx context.Context) {
	if !s.overviewSubscriptions.hasSubscribers() {
		return
	}
	if err := s.Server().ResourceUpdated(ctx, &mcp.ResourceUpdatedNotificationParams{URI: mailboxOverviewURI}); err != nil {
		log.Printf("mcpserver mailbox_overview_update_failed uri=%s err=%v", mailboxOverviewURI, err)
	}
}

func (s *resourceSubscriptionState) add(session *mcp.ServerSession) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[session] = true
}

func (s *resourceSubscriptionState) remove(session *mcp.ServerSession) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, session)
}

func (s *resourceSubscriptionState) hasSubscribers() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sessions) > 0
}
