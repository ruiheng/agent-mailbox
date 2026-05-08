package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/ruiheng/agent-mailbox/internal/mailbox"
)

type mailboxBindInput struct {
	Addresses      []string `json:"addresses"`
	DefaultSender  string   `json:"default_sender,omitempty"`
	DefaultWorkdir string   `json:"default_workdir,omitempty"`
}

type mailboxStatusInput struct{}

type mailboxSendInput struct {
	ToAddress            string `json:"to_address"`
	FromAddress          string `json:"from_address,omitempty"`
	Subject              string `json:"subject"`
	Body                 string `json:"body"`
	ContentType          string `json:"content_type,omitempty"`
	SchemaVersion        string `json:"schema_version,omitempty"`
	DisableNotifyMessage *bool  `json:"disable_notify_message,omitempty"`
	Group                bool   `json:"group,omitempty"`
	forwardedMessageID   string
	forwardedFromAddress string
}

type mailboxForwardInput struct {
	MessageID            string `json:"message_id,omitempty"`
	DeliveryID           string `json:"delivery_id,omitempty"`
	ToAddress            string `json:"to_address"`
	FromAddress          string `json:"from_address,omitempty"`
	Subject              string `json:"subject,omitempty"`
	Group                bool   `json:"group,omitempty"`
	DisableNotifyMessage *bool  `json:"disable_notify_message,omitempty"`
}

type mailboxWaitInput struct {
	Addresses []string `json:"addresses,omitempty"`
	AsPerson  string   `json:"as_person,omitempty"`
	Timeout   string   `json:"timeout,omitempty"`
}

type mailboxRecvInput struct {
	Addresses []string `json:"addresses,omitempty"`
	AsPerson  string   `json:"as_person,omitempty"`
}

type mailboxListInput struct {
	Address  string `json:"address,omitempty"`
	AsPerson string `json:"as_person,omitempty"`
	State    string `json:"state,omitempty"`
}

type mailboxReadInput struct {
	MessageIDs  []string `json:"message_ids,omitempty"`
	DeliveryIDs []string `json:"delivery_ids,omitempty"`
	Latest      bool     `json:"latest,omitempty"`
	Addresses   []string `json:"addresses,omitempty"`
	State       string   `json:"state,omitempty"`
	Limit       *int     `json:"limit,omitempty"`
}

type mailboxGroupInput struct {
	GroupAddress string `json:"group_address"`
}

type mailboxGroupMemberInput struct {
	GroupAddress string `json:"group_address"`
	Person       string `json:"person"`
}

type mailboxGroupSubscriberInput struct {
	GroupAddress  string `json:"group_address"`
	NotifyAddress string `json:"notify_address"`
	Person        string `json:"person,omitempty"`
}

type mailboxAddressInspectInput struct {
	Address string `json:"address"`
}

type mailboxAckInput struct {
	DeliveryID string `json:"delivery_id"`
	LeaseToken string `json:"lease_token"`
}

type mailboxDeferInput struct {
	DeliveryID string `json:"delivery_id"`
	LeaseToken string `json:"lease_token"`
	Until      string `json:"until"`
}

type mailboxFailInput struct {
	DeliveryID string `json:"delivery_id"`
	LeaseToken string `json:"lease_token"`
	Reason     string `json:"reason"`
}

type readLatestResult struct {
	Items   []mailbox.ReadDelivery
	HasMore bool
}

func (s *Service) registerMailboxTools(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "mailbox_bind",
		Description: "Bind one or more mailbox addresses into MCP server state.",
	}, s.mailboxBind)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "mailbox_status",
		Description: "Show the currently bound mailbox addresses, default sender, and default workdir stored in this MCP server.",
	}, s.mailboxStatus)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "mailbox_send",
		Description: "Send one mailbox message and automatically push-notify a non-local target when the address scheme supports it. Set disable_notify_message=true to skip notify for that send.",
	}, s.mailboxSend)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "mailbox_forward",
		Description: "Forward one stored mailbox message to a new recipient. Provide exactly one of message_id or delivery_id. The forward reuses the original body, content_type, and schema_version, and sends through the normal mailbox_send path.",
	}, s.mailboxForward)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "mailbox_wait",
		Description: "Observe whether mail is available without claiming it. Agent-managed session inbox addresses typically look like agent-deck/<session-id> or codex/<session-id>. Optional timeout is a duration string such as 30s, 5m, 120ms, or 1m30s.",
	}, s.mailboxWait)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "mailbox_recv",
		Description: "Receive mail immediately. If addresses is omitted, receive from all bound addresses; pass addresses only to override that inbox set for this call. After ack, use mailbox_read to reread persisted deliveries when context is lost.",
	}, s.mailboxRecv)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "mailbox_list",
		Description: "List persisted deliveries for one inbox. Use state='acked' to find deliveries that were already received and acknowledged before rereading them with mailbox_read.",
	}, s.mailboxList)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "mailbox_read",
		Description: "Read persisted mailbox messages or deliveries. Use latest=true with state='acked' to reread recently acknowledged mail after context loss.",
	}, s.mailboxRead)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "mailbox_ack",
		Description: "Acknowledge a claimed mailbox delivery. Acked deliveries remain readable later through mailbox_read.",
	}, s.mailboxAck)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "mailbox_release",
		Description: "Release a claimed mailbox delivery back to the queue.",
	}, s.mailboxRelease)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "mailbox_defer",
		Description: "Defer a claimed mailbox delivery until a later RFC3339 time.",
	}, s.mailboxDefer)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "mailbox_fail",
		Description: "Fail a claimed mailbox delivery with a reason.",
	}, s.mailboxFail)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "mailbox_group_create",
		Description: "Create a group mailbox address.",
	}, s.mailboxGroupCreate)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "mailbox_group_add_member",
		Description: "Add a person to a group mailbox.",
	}, s.mailboxGroupAddMember)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "mailbox_group_remove_member",
		Description: "Remove a person from a group mailbox.",
	}, s.mailboxGroupRemoveMember)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "mailbox_group_members",
		Description: "List group mailbox memberships.",
	}, s.mailboxGroupMembers)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "mailbox_group_add_subscriber",
		Description: "Add a best-effort notification target for group messages.",
	}, s.mailboxGroupAddSubscriber)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "mailbox_group_remove_subscriber",
		Description: "Remove a group notification target.",
	}, s.mailboxGroupRemoveSubscriber)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "mailbox_group_subscribers",
		Description: "List active group notification targets.",
	}, s.mailboxGroupSubscribers)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "mailbox_address_inspect",
		Description: "Inspect whether an address is an endpoint, group, or unbound.",
	}, s.mailboxAddressInspect)
}

func (s *Service) mailboxBind(ctx context.Context, _ *mcp.CallToolRequest, input mailboxBindInput) (*mcp.CallToolResult, map[string]any, error) {
	bound, err := s.sessions.bind(ctx, input)
	if err != nil {
		return nil, nil, err
	}
	out := boundStateMap(bound)
	out["status"] = "bound"
	return s.mailboxMutationToolResult(ctx, out)
}

func (s *Service) mailboxStatus(ctx context.Context, _ *mcp.CallToolRequest, _ mailboxStatusInput) (*mcp.CallToolResult, map[string]any, error) {
	bound, err := s.sessions.boundState(ctx)
	if err != nil {
		return nil, nil, err
	}
	return s.mailboxToolResult(ctx, map[string]any{
		"bound_addresses": bound.BoundAddresses,
		"default_sender":  orUnset(bound.DefaultSender),
		"default_workdir": orUnset(bound.DefaultWorkdir),
	})
}

func (s *Service) sendMailboxMessage(ctx context.Context, input mailboxSendInput) (map[string]any, error) {
	toAddress, err := mailbox.NormalizeAddress(input.ToAddress)
	if err != nil {
		if strings.TrimSpace(input.ToAddress) == "" {
			return nil, errors.New("recipient address is required")
		}
		return nil, err
	}
	input.ToAddress = toAddress

	fromAddress, err := s.sessions.senderAddress(ctx, input.FromAddress)
	if err != nil {
		return nil, err
	}
	input.FromAddress = fromAddress

	sendResult, err := withMailboxService(ctx, s.mailboxServices, func(service mailboxService) (mailbox.SendResult, error) {
		return service.Send(ctx, mailbox.SendParams{
			ToAddress:            input.ToAddress,
			FromAddress:          fromAddress,
			Subject:              input.Subject,
			ContentType:          strings.TrimSpace(input.ContentType),
			SchemaVersion:        strings.TrimSpace(input.SchemaVersion),
			ForwardedMessageID:   strings.TrimSpace(input.forwardedMessageID),
			ForwardedFromAddress: strings.TrimSpace(input.forwardedFromAddress),
			Body:                 []byte(input.Body),
			Group:                input.Group,
		})
	})
	if err != nil {
		return nil, err
	}

	notify := s.notifyMailboxSend(ctx, input, sendResult)
	var notifyScheme any
	if notify.Scheme != "" {
		notifyScheme = notify.Scheme
	}
	var notifyError any
	if notify.Err != nil {
		notifyError = notify.Err.Error()
	}

	out := map[string]any{
		"status":        "sent",
		"from_address":  fromAddress,
		"to_address":    input.ToAddress,
		"subject":       input.Subject,
		"notify_status": notify.Status,
		"notify_scheme": notifyScheme,
		"notify_error":  notifyError,
	}
	if sendResult.Mode == mailbox.SendModeGroup {
		out["mode"] = sendResult.Mode
		out["message_id"] = sendResult.MessageID
		out["group_id"] = sendResult.GroupID
		out["group_address"] = sendResult.GroupAddress
		out["eligible_count"] = sendResult.EligibleCount
		out["message_created_at"] = sendResult.MessageCreatedAt
		out["delivery_id"] = nil
	} else {
		out["delivery_id"] = sendResult.DeliveryID
	}
	return out, nil
}

func (s *Service) notifyMailboxSend(ctx context.Context, input mailboxSendInput, sendResult mailbox.SendResult) notificationOutcome {
	notifyCtx := context.WithoutCancel(ctx)
	if sendResult.Mode != mailbox.SendModeGroup {
		if s.sessions.isLocalAddress(notifyCtx, input.ToAddress) || wakeNotifyDisabled(input.DisableNotifyMessage) {
			return s.notifications.notifyMailboxSend(notifyCtx, input)
		}
		scope, _, err := directWakeScopeForAddress(input.ToAddress)
		if err != nil {
			return notificationOutcome{Status: "failed", Err: err}
		}
		hasAgentDeckWakeTarget := scope != nil && len(scope.targetsForChannel(WakeChannelAgentDeck)) > 0
		if hasAgentDeckWakeTarget && strings.TrimSpace(sendResult.DeliveryID) != "" {
			if err := s.waitBeforeNotify(notifyCtx); err != nil {
				return notificationOutcome{Status: "failed", Scheme: "mailbox", Err: err}
			}
			stillQueued, err := s.deliveryStillQueued(notifyCtx, sendResult.DeliveryID)
			if err != nil {
				return notificationOutcome{Status: "failed", Scheme: "mailbox", Err: err}
			}
			if !stillQueued {
				return notificationOutcome{Status: "skipped_already_claimed", Scheme: "mailbox"}
			}
		}
		return s.notifications.notifyMailboxSend(notifyCtx, input)
	}
	subscribers, err := withMailboxService(notifyCtx, s.mailboxServices, func(service mailboxService) ([]mailbox.GroupNotificationSubscriberRecord, error) {
		return service.ListGroupNotificationSubscribers(notifyCtx, sendResult.GroupAddress)
	})
	if err != nil {
		return notificationOutcome{
			Status: "failed",
			Scheme: "group_subscribers",
			Err:    err,
		}
	}
	return s.notifications.notifyGroupSubscribers(notifyCtx, input, subscribers)
}

func (s *Service) waitBeforeNotify(ctx context.Context) error {
	if s.notifyDelay <= 0 {
		return nil
	}
	timer := time.NewTimer(s.notifyDelay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (s *Service) deliveryStillQueued(ctx context.Context, deliveryID string) (bool, error) {
	deliveries, err := withMailboxService(ctx, s.mailboxServices, func(service mailboxService) ([]mailbox.ReadDelivery, error) {
		return service.ReadDeliveries(ctx, []string{deliveryID})
	})
	if err != nil {
		return false, err
	}
	if len(deliveries) != 1 {
		return false, fmt.Errorf("read delivery %q: got %d deliveries, want 1", deliveryID, len(deliveries))
	}
	return strings.TrimSpace(deliveries[0].State) == "queued", nil
}

func (s *Service) mailboxSend(ctx context.Context, _ *mcp.CallToolRequest, input mailboxSendInput) (*mcp.CallToolResult, map[string]any, error) {
	out, err := s.sendMailboxMessage(ctx, input)
	if err != nil {
		return nil, nil, err
	}
	return s.mailboxMutationToolResult(ctx, out)
}

func (s *Service) mailboxForward(ctx context.Context, _ *mcp.CallToolRequest, input mailboxForwardInput) (*mcp.CallToolResult, map[string]any, error) {
	prepared, err := withMailboxService(ctx, s.mailboxServices, func(service mailboxService) (mailbox.PreparedForward, error) {
		return mailbox.PrepareForward(ctx, service, "mailbox_forward", mailbox.ForwardParams{
			MessageID:   input.MessageID,
			DeliveryID:  input.DeliveryID,
			ToAddress:   input.ToAddress,
			FromAddress: input.FromAddress,
			Subject:     input.Subject,
			Group:       input.Group,
		})
	})
	if err != nil {
		return nil, nil, err
	}

	sendInput := mailboxSendInput{
		ToAddress:            prepared.SendParams.ToAddress,
		FromAddress:          prepared.SendParams.FromAddress,
		Subject:              prepared.SendParams.Subject,
		Body:                 string(prepared.SendParams.Body),
		ContentType:          prepared.SendParams.ContentType,
		SchemaVersion:        prepared.SendParams.SchemaVersion,
		DisableNotifyMessage: input.DisableNotifyMessage,
		forwardedMessageID:   prepared.SendParams.ForwardedMessageID,
		forwardedFromAddress: prepared.SendParams.ForwardedFromAddress,
		Group:                prepared.SendParams.Group,
	}

	out, err := s.sendMailboxMessage(ctx, sendInput)
	if err != nil {
		return nil, nil, err
	}
	out["status"] = "forwarded"
	out["source_message_id"] = prepared.SourceMessageID
	out["source_delivery_id"] = nilIfEmpty(prepared.SourceDeliveryID)
	return s.mailboxMutationToolResult(ctx, out)
}

func (s *Service) mailboxWait(ctx context.Context, _ *mcp.CallToolRequest, input mailboxWaitInput) (*mcp.CallToolResult, map[string]any, error) {
	addresses, err := s.sessions.mailboxAddresses(ctx, input.Addresses)
	if err != nil {
		return nil, nil, err
	}
	warnings := s.mailboxReceiveWarnings(ctx, len(input.Addresses) > 0)

	timeoutText := strings.TrimSpace(input.Timeout)
	timeout := time.Duration(0)
	if timeoutText != "" {
		timeout, err = time.ParseDuration(timeoutText)
		if err != nil {
			return nil, nil, fmt.Errorf("parse timeout: %w", err)
		}
	}
	if person := strings.TrimSpace(input.AsPerson); person != "" {
		return s.mailboxWaitGroup(ctx, addresses, person, timeout)
	}

	delivery, err := withMailboxService(ctx, s.mailboxServices, func(service mailboxService) (mailbox.ListedDelivery, error) {
		return service.Wait(ctx, mailbox.WaitParams{
			Addresses: addresses,
			Timeout:   timeout,
		})
	})
	if errors.Is(err, mailbox.ErrNoMessage) {
		return s.mailboxToolResult(ctx, map[string]any{
			"status":    "no_message",
			"addresses": addresses,
			"warnings":  warnings,
		})
	}
	if err != nil {
		return nil, nil, err
	}
	return s.mailboxToolResult(ctx, map[string]any{
		"status":    "message_available",
		"addresses": addresses,
		"delivery":  mailbox.CompactListedDelivery(delivery),
		"warnings":  warnings,
	})
}

func (s *Service) mailboxWaitGroup(ctx context.Context, addresses []string, person string, timeout time.Duration) (*mcp.CallToolResult, map[string]any, error) {
	address, err := singleGroupAddress(addresses, "mailbox_wait")
	if err != nil {
		return nil, nil, err
	}
	message, err := withMailboxService(ctx, s.mailboxServices, func(service mailboxService) (mailbox.GroupListedMessage, error) {
		return service.WaitGroupMessage(ctx, mailbox.GroupWaitParams{
			Address: address,
			Person:  person,
			Timeout: timeout,
		})
	})
	if errors.Is(err, mailbox.ErrNoMessage) {
		return s.mailboxToolResult(ctx, map[string]any{
			"status":    "no_message",
			"addresses": []string{address},
			"as_person": person,
		})
	}
	if err != nil {
		return nil, nil, err
	}
	return s.mailboxToolResult(ctx, map[string]any{
		"status":    "message_available",
		"addresses": []string{address},
		"as_person": person,
		"message":   mailbox.CompactGroupListedMessage(message),
	})
}

func (s *Service) mailboxRecv(ctx context.Context, _ *mcp.CallToolRequest, input mailboxRecvInput) (*mcp.CallToolResult, map[string]any, error) {
	addresses, err := s.sessions.mailboxAddresses(ctx, input.Addresses)
	if err != nil {
		return nil, nil, err
	}
	if person := strings.TrimSpace(input.AsPerson); person != "" {
		return s.mailboxRecvGroup(ctx, addresses, person)
	}
	warnings := s.mailboxReceiveWarnings(ctx, len(input.Addresses) > 0)

	delivery, err := withMailboxService(ctx, s.mailboxServices, func(service mailboxService) (mailbox.ReceiveResult, error) {
		// MCP intentionally claims work with a short liveness window and relies on
		// in-process renewals while this Service instance is alive. This keeps
		// abandoned work reclaimable quickly after MCP death or long stalls instead
		// of inheriting the mailbox core's legacy 5m receive lease.
		return service.ReceiveBatchWithLeaseTTL(ctx, mailbox.ReceiveBatchParams{
			Addresses: addresses,
			Max:       1,
		}, s.mcpLeaseTTL)
	})
	if errors.Is(err, mailbox.ErrNoMessage) {
		return s.mailboxToolResult(ctx, map[string]any{
			"status":    "no_message",
			"addresses": addresses,
			"warnings":  warnings,
		})
	}
	if err != nil {
		return nil, nil, err
	}
	s.activeLeases.trackReceive(delivery)
	s.startLeaseRenewLoop()
	return s.mailboxMutationToolResult(ctx, map[string]any{
		"status":    "received",
		"addresses": addresses,
		"delivery":  mailbox.CompactReceiveResult(delivery),
		"warnings":  warnings,
	})
}

func (s *Service) mailboxRecvGroup(ctx context.Context, addresses []string, person string) (*mcp.CallToolResult, map[string]any, error) {
	address, err := singleGroupAddress(addresses, "mailbox_recv")
	if err != nil {
		return nil, nil, err
	}
	message, err := withMailboxService(ctx, s.mailboxServices, func(service mailboxService) (mailbox.GroupReceivedMessage, error) {
		return service.ReceiveGroupMessage(ctx, mailbox.GroupReceiveParams{
			Address: address,
			Person:  person,
		})
	})
	if errors.Is(err, mailbox.ErrNoMessage) {
		return s.mailboxToolResult(ctx, map[string]any{
			"status":    "no_message",
			"addresses": []string{address},
			"as_person": person,
		})
	}
	if err != nil {
		return nil, nil, err
	}
	return s.mailboxMutationToolResult(ctx, map[string]any{
		"status":    "received",
		"addresses": []string{address},
		"as_person": person,
		"message":   mailbox.CompactGroupReceivedMessage(message),
	})
}

func singleGroupAddress(addresses []string, toolName string) (string, error) {
	if len(addresses) != 1 {
		return "", fmt.Errorf("%s with as_person requires exactly one group address", toolName)
	}
	address := addresses[0]
	if !strings.HasPrefix(address, "group/") {
		return "", fmt.Errorf("%s with as_person requires a group address", toolName)
	}
	return address, nil
}

func (s *Service) mailboxReceiveWarnings(ctx context.Context, explicitAddresses bool) []string {
	if explicitAddresses {
		return nil
	}
	bound, err := s.sessions.boundState(ctx)
	if err != nil {
		return []string{"unable to verify bound agent session state: " + err.Error()}
	}
	if bound.DetectedAgentSession == "" || bound.DetectedAgentDeckSession != "" {
		return nil
	}
	return []string{agentDeckBindRecoveryHint}
}

func (s *Service) mailboxList(ctx context.Context, _ *mcp.CallToolRequest, input mailboxListInput) (*mcp.CallToolResult, map[string]any, error) {
	var address string
	if strings.TrimSpace(input.Address) != "" {
		address = strings.TrimSpace(input.Address)
	} else {
		boundAddresses, err := s.sessions.mailboxAddresses(ctx, nil)
		if err != nil {
			return nil, nil, err
		}
		if len(boundAddresses) != 1 {
			return nil, nil, errors.New("mailbox_list requires address when multiple mailbox addresses are bound")
		}
		address = boundAddresses[0]
	}
	if input.AsPerson != "" && input.State != "" {
		return nil, nil, errors.New("mailbox_list does not support state together with as_person")
	}

	deliveries, err := withMailboxService(ctx, s.mailboxServices, func(service mailboxService) (any, error) {
		if input.AsPerson != "" {
			messages, err := service.ListGroupMessages(ctx, mailbox.GroupListParams{
				Address: address,
				Person:  input.AsPerson,
			})
			if err != nil {
				return nil, err
			}
			summaries := make([]mailbox.GroupListedMessageCompact, 0, len(messages))
			for _, message := range messages {
				summaries = append(summaries, mailbox.CompactGroupListedMessage(message))
			}
			return summaries, nil
		}
		return service.List(ctx, mailbox.ListParams{
			Address: address,
			State:   input.State,
		})
	})
	if err != nil {
		return nil, nil, err
	}
	return s.mailboxToolResult(ctx, map[string]any{
		"status":     "listed",
		"address":    address,
		"as_person":  nilIfEmpty(input.AsPerson),
		"state":      nilIfEmpty(input.State),
		"deliveries": deliveries,
	})
}

func (s *Service) mailboxRead(ctx context.Context, _ *mcp.CallToolRequest, input mailboxReadInput) (*mcp.CallToolResult, map[string]any, error) {
	hasMessageIDs := len(input.MessageIDs) > 0
	hasDeliveryIDs := len(input.DeliveryIDs) > 0
	wantsLatest := input.Latest
	modeCount := 0
	if hasMessageIDs {
		modeCount++
	}
	if hasDeliveryIDs {
		modeCount++
	}
	if wantsLatest {
		modeCount++
	}
	if modeCount != 1 {
		return nil, nil, errors.New("mailbox_read requires exactly one mode: message_ids, delivery_ids, or latest=true")
	}

	result := map[string]any{
		"status": "read",
		"mode":   "unknown",
	}

	switch {
	case wantsLatest:
		addresses, err := s.sessions.mailboxAddresses(ctx, input.Addresses)
		if err != nil {
			return nil, nil, err
		}
		result["mode"] = "latest"
		result["addresses"] = addresses
		if input.State == "" {
			result["state"] = "any"
		} else {
			result["state"] = input.State
		}
		if input.Limit == nil {
			result["limit"] = nil
		} else {
			result["limit"] = *input.Limit
		}
	case hasMessageIDs:
		if len(input.Addresses) > 0 || input.State != "" || input.Limit != nil {
			return nil, nil, errors.New("mailbox_read message_ids mode does not support addresses, state, or limit")
		}
		messageIDs := dedupe(input.MessageIDs)
		result["mode"] = "message_ids"
		result["message_ids"] = messageIDs
		messages, err := withMailboxService(ctx, s.mailboxServices, func(service mailboxService) ([]mailbox.ReadMessage, error) {
			return service.ReadMessages(ctx, messageIDs)
		})
		if err != nil {
			return nil, nil, err
		}
		result["items"] = messages
		result["has_more"] = false
		return s.mailboxToolResult(ctx, result)
	default:
		if len(input.Addresses) > 0 || input.State != "" || input.Limit != nil {
			return nil, nil, errors.New("mailbox_read delivery_ids mode does not support addresses, state, or limit")
		}
		deliveryIDs := dedupe(input.DeliveryIDs)
		result["mode"] = "delivery_ids"
		result["delivery_ids"] = deliveryIDs
		deliveries, err := withMailboxService(ctx, s.mailboxServices, func(service mailboxService) ([]mailbox.ReadDelivery, error) {
			return service.ReadDeliveries(ctx, deliveryIDs)
		})
		if err != nil {
			return nil, nil, err
		}
		result["items"] = deliveries
		result["has_more"] = false
		return s.mailboxToolResult(ctx, result)
	}

	latest, err := withMailboxService(ctx, s.mailboxServices, func(service mailboxService) (readLatestResult, error) {
		limit := 1
		if input.Limit != nil {
			limit = *input.Limit
		}
		items, hasMore, err := service.ReadLatestDeliveries(ctx, result["addresses"].([]string), input.State, limit)
		if err != nil {
			return readLatestResult{}, err
		}
		return readLatestResult{Items: items, HasMore: hasMore}, nil
	})
	if err != nil {
		return nil, nil, err
	}
	result["items"] = latest.Items
	result["has_more"] = latest.HasMore
	return s.mailboxToolResult(ctx, result)
}

func (s *Service) mailboxAck(ctx context.Context, _ *mcp.CallToolRequest, input mailboxAckInput) (*mcp.CallToolResult, map[string]any, error) {
	if err := s.activeLeases.terminalMutationAllowed(input.DeliveryID); err != nil {
		return nil, nil, err
	}
	_, err := withMailboxService(ctx, s.mailboxServices, func(service mailboxService) (mailbox.DeliveryTransitionResult, error) {
		return service.Ack(ctx, input.DeliveryID, input.LeaseToken)
	})
	if err != nil {
		return nil, nil, err
	}
	s.activeLeases.remove(input.DeliveryID)
	return s.mailboxMutationToolResult(ctx, map[string]any{"status": "acked", "delivery_id": input.DeliveryID})
}

func (s *Service) mailboxRelease(ctx context.Context, _ *mcp.CallToolRequest, input mailboxAckInput) (*mcp.CallToolResult, map[string]any, error) {
	if err := s.activeLeases.terminalMutationAllowed(input.DeliveryID); err != nil {
		return nil, nil, err
	}
	_, err := withMailboxService(ctx, s.mailboxServices, func(service mailboxService) (mailbox.DeliveryTransitionResult, error) {
		return service.Release(ctx, input.DeliveryID, input.LeaseToken)
	})
	if err != nil {
		return nil, nil, err
	}
	s.activeLeases.remove(input.DeliveryID)
	return s.mailboxMutationToolResult(ctx, map[string]any{"status": "released", "delivery_id": input.DeliveryID})
}

func (s *Service) mailboxDefer(ctx context.Context, _ *mcp.CallToolRequest, input mailboxDeferInput) (*mcp.CallToolResult, map[string]any, error) {
	if err := s.activeLeases.terminalMutationAllowed(input.DeliveryID); err != nil {
		return nil, nil, err
	}
	until, err := time.Parse(time.RFC3339Nano, input.Until)
	if err != nil {
		return nil, nil, fmt.Errorf("parse until: %w", err)
	}
	_, err = withMailboxService(ctx, s.mailboxServices, func(service mailboxService) (mailbox.DeliveryTransitionResult, error) {
		return service.Defer(ctx, input.DeliveryID, input.LeaseToken, until)
	})
	if err != nil {
		return nil, nil, err
	}
	s.activeLeases.remove(input.DeliveryID)
	return s.mailboxMutationToolResult(ctx, map[string]any{"status": "deferred", "delivery_id": input.DeliveryID, "until": input.Until})
}

func (s *Service) mailboxFail(ctx context.Context, _ *mcp.CallToolRequest, input mailboxFailInput) (*mcp.CallToolResult, map[string]any, error) {
	if err := s.activeLeases.terminalMutationAllowed(input.DeliveryID); err != nil {
		return nil, nil, err
	}
	_, err := withMailboxService(ctx, s.mailboxServices, func(service mailboxService) (mailbox.DeliveryTransitionResult, error) {
		return service.Fail(ctx, input.DeliveryID, input.LeaseToken, input.Reason)
	})
	if err != nil {
		return nil, nil, err
	}
	s.activeLeases.remove(input.DeliveryID)
	return s.mailboxMutationToolResult(ctx, map[string]any{"status": "failed", "delivery_id": input.DeliveryID, "reason": input.Reason})
}

func (s *Service) mailboxGroupCreate(ctx context.Context, _ *mcp.CallToolRequest, input mailboxGroupInput) (*mcp.CallToolResult, map[string]any, error) {
	group, err := withMailboxService(ctx, s.mailboxServices, func(service mailboxService) (mailbox.GroupRecord, error) {
		return service.CreateGroup(ctx, input.GroupAddress)
	})
	if err != nil {
		return nil, nil, err
	}
	return s.mailboxMutationToolResult(ctx, map[string]any{
		"status": "created",
		"group":  group,
	})
}

func (s *Service) mailboxGroupAddMember(ctx context.Context, _ *mcp.CallToolRequest, input mailboxGroupMemberInput) (*mcp.CallToolResult, map[string]any, error) {
	membership, err := withMailboxService(ctx, s.mailboxServices, func(service mailboxService) (mailbox.GroupMembershipRecord, error) {
		return service.AddGroupMember(ctx, input.GroupAddress, input.Person)
	})
	if err != nil {
		return nil, nil, err
	}
	return s.mailboxMutationToolResult(ctx, map[string]any{
		"status":     "added",
		"membership": membership,
	})
}

func (s *Service) mailboxGroupRemoveMember(ctx context.Context, _ *mcp.CallToolRequest, input mailboxGroupMemberInput) (*mcp.CallToolResult, map[string]any, error) {
	membership, err := withMailboxService(ctx, s.mailboxServices, func(service mailboxService) (mailbox.GroupMembershipRecord, error) {
		return service.RemoveGroupMember(ctx, input.GroupAddress, input.Person)
	})
	if err != nil {
		return nil, nil, err
	}
	return s.mailboxMutationToolResult(ctx, map[string]any{
		"status":     "removed",
		"membership": membership,
	})
}

func (s *Service) mailboxGroupMembers(ctx context.Context, _ *mcp.CallToolRequest, input mailboxGroupInput) (*mcp.CallToolResult, map[string]any, error) {
	memberships, err := withMailboxService(ctx, s.mailboxServices, func(service mailboxService) ([]mailbox.GroupMembershipRecord, error) {
		return service.ListGroupMembers(ctx, input.GroupAddress)
	})
	if err != nil {
		return nil, nil, err
	}
	return s.mailboxToolResult(ctx, map[string]any{
		"status":      "listed",
		"group":       input.GroupAddress,
		"memberships": memberships,
	})
}

func (s *Service) mailboxGroupAddSubscriber(ctx context.Context, _ *mcp.CallToolRequest, input mailboxGroupSubscriberInput) (*mcp.CallToolResult, map[string]any, error) {
	subscriber, err := withMailboxService(ctx, s.mailboxServices, func(service mailboxService) (mailbox.GroupNotificationSubscriberRecord, error) {
		return service.AddGroupNotificationSubscriber(ctx, input.GroupAddress, input.NotifyAddress, input.Person)
	})
	if err != nil {
		return nil, nil, err
	}
	return s.mailboxMutationToolResult(ctx, map[string]any{
		"status":     "added",
		"subscriber": subscriber,
	})
}

func (s *Service) mailboxGroupRemoveSubscriber(ctx context.Context, _ *mcp.CallToolRequest, input mailboxGroupSubscriberInput) (*mcp.CallToolResult, map[string]any, error) {
	subscriber, err := withMailboxService(ctx, s.mailboxServices, func(service mailboxService) (mailbox.GroupNotificationSubscriberRecord, error) {
		return service.RemoveGroupNotificationSubscriber(ctx, input.GroupAddress, input.NotifyAddress)
	})
	if err != nil {
		return nil, nil, err
	}
	return s.mailboxMutationToolResult(ctx, map[string]any{
		"status":     "removed",
		"subscriber": subscriber,
	})
}

func (s *Service) mailboxGroupSubscribers(ctx context.Context, _ *mcp.CallToolRequest, input mailboxGroupInput) (*mcp.CallToolResult, map[string]any, error) {
	subscribers, err := withMailboxService(ctx, s.mailboxServices, func(service mailboxService) ([]mailbox.GroupNotificationSubscriberRecord, error) {
		return service.ListGroupNotificationSubscribers(ctx, input.GroupAddress)
	})
	if err != nil {
		return nil, nil, err
	}
	return s.mailboxToolResult(ctx, map[string]any{
		"status":      "listed",
		"group":       input.GroupAddress,
		"subscribers": subscribers,
	})
}

func (s *Service) mailboxAddressInspect(ctx context.Context, _ *mcp.CallToolRequest, input mailboxAddressInspectInput) (*mcp.CallToolResult, map[string]any, error) {
	inspection, err := withMailboxService(ctx, s.mailboxServices, func(service mailboxService) (mailbox.AddressInspection, error) {
		return service.InspectAddress(ctx, input.Address)
	})
	if err != nil {
		return nil, nil, err
	}
	return s.mailboxToolResult(ctx, map[string]any{
		"status":     "inspected",
		"inspection": inspection,
	})
}

func (s *Service) mailboxToolResult(ctx context.Context, result map[string]any) (*mcp.CallToolResult, map[string]any, error) {
	return s.toolResult(ctx, result)
}

func (s *Service) mailboxMutationToolResult(ctx context.Context, result map[string]any) (*mcp.CallToolResult, map[string]any, error) {
	s.emitMailboxOverviewUpdatedBestEffort(ctx)
	return s.toolResult(ctx, result)
}
