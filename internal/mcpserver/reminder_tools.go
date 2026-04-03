package mcpserver

import (
	"context"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/ruiheng/agent-mailbox/internal/mailbox"
)

type mailboxReminderSubscribeInput struct {
	Addresses  []string                 `json:"addresses,omitempty"`
	GroupViews []reminderGroupViewInput `json:"group_views,omitempty"`
	Route      string                   `json:"route"`
	OlderThan  string                   `json:"older_than"`
	ActivePush bool                     `json:"active_push,omitempty"`
}

type mailboxReminderUnsubscribeInput struct {
	Addresses  []string                 `json:"addresses,omitempty"`
	GroupViews []reminderGroupViewInput `json:"group_views,omitempty"`
	Route      string                   `json:"route"`
}

type mailboxReminderStatusInput struct{}

func (s *Service) registerReminderTools(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "mailbox_reminder_subscribe",
		Description: "Subscribe one reminder selector/route pair for passive mailbox reminder hints. Repeated calls upsert by canonicalized selector plus route.",
	}, s.mailboxReminderSubscribe)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "mailbox_reminder_unsubscribe",
		Description: "Remove one reminder subscription identified by canonicalized selector plus route. Repeated calls are idempotent.",
	}, s.mailboxReminderUnsubscribe)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "mailbox_reminder_status",
		Description: "List current reminder subscriptions together with their latest passive runtime summary.",
	}, s.mailboxReminderStatus)
}

func (s *Service) mailboxReminderSubscribe(ctx context.Context, _ *mcp.CallToolRequest, input mailboxReminderSubscribeInput) (*mcp.CallToolResult, map[string]any, error) {
	subscription, existed, err := s.reminders.subscribe(ctx, input)
	if err != nil {
		return nil, nil, err
	}
	if subscription.Policy.ActivePush {
		s.startActiveReminderLoop()
	}

	return nil, map[string]any{
		"status":       "subscribed",
		"updated":      existed,
		"subscription": subscription,
	}, nil
}

func (s *Service) mailboxReminderUnsubscribe(_ context.Context, _ *mcp.CallToolRequest, input mailboxReminderUnsubscribeInput) (*mcp.CallToolResult, map[string]any, error) {
	subscriptionKey, existed, err := s.reminders.unsubscribe(input)
	if err != nil {
		return nil, nil, err
	}

	return nil, map[string]any{
		"status":           "unsubscribed",
		"removed":          existed,
		"subscription_key": subscriptionKey,
	}, nil
}

func (s *Service) mailboxReminderStatus(ctx context.Context, _ *mcp.CallToolRequest, _ mailboxReminderStatusInput) (*mcp.CallToolResult, map[string]any, error) {
	entries, err := s.reminders.status(ctx)
	if err != nil {
		return nil, nil, err
	}
	return nil, map[string]any{
		"status":        "listed",
		"subscriptions": entries,
	}, nil
}

func (s *Service) startActiveReminderLoop() {
	if s.disableActiveReminderLoop {
		return
	}
	s.activeReminderLoopOnce.Do(func() {
		go s.runActiveReminderLoop()
	})
}

func (s *Service) runActiveReminderLoop() {
	_ = s.processActiveReminderSubscriptions(context.Background())
	ticker := time.NewTicker(s.reminderPollInterval)
	defer ticker.Stop()
	for range ticker.C {
		_ = s.processActiveReminderSubscriptions(context.Background())
	}
}

func (s *Service) processActiveReminderSubscriptions(ctx context.Context) error {
	return s.reminders.processActiveSubscriptions(ctx)
}

func (s *Service) withPassiveReminderPayloadBestEffort(ctx context.Context, result map[string]any) map[string]any {
	payload, err := s.reminders.passivePayload(ctx)
	if err != nil {
		return result
	}
	if payload != nil {
		result["reminders"] = payload
	}
	return result
}

func (s *Service) listReminderStaleAddresses(ctx context.Context, addresses []string, person string, policy reminderPolicy) ([]staleAddress, error) {
	olderThan, err := time.ParseDuration(policy.OlderThanRaw)
	if err != nil {
		return nil, err
	}

	params := mailbox.StaleAddressesParams{OlderThan: olderThan}
	if strings.TrimSpace(person) != "" {
		params.GroupViews = []mailbox.GroupStaleView{{
			Address: addresses[0],
			Person:  strings.TrimSpace(person),
		}}
	} else {
		params.Addresses = append([]string(nil), addresses...)
	}

	values, err := withMailboxService(ctx, s.mailboxServices, func(service mailboxService) ([]mailbox.StaleAddress, error) {
		return service.ListStaleAddresses(ctx, params)
	})
	if err != nil {
		return nil, err
	}

	staleEntries := make([]staleAddress, 0, len(values))
	for _, value := range values {
		staleEntries = append(staleEntries, staleAddress{
			Address:          value.Address,
			Person:           value.Person,
			OldestEligibleAt: value.OldestEligibleAt,
			ClaimableCount:   value.ClaimableCount,
		})
	}
	return staleEntries, nil
}
