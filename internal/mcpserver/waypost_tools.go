package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/ruiheng/waypost/internal/waypost"
)

type waypostBindInput struct {
	Addresses      []string `json:"addresses"`
	DefaultSender  string   `json:"default_sender,omitempty"`
	DefaultWorkdir string   `json:"default_workdir,omitempty"`
}

type waypostStatusInput struct{}

type waypostDebugInput struct{}

type waypostSendInput struct {
	ToAddress            string `json:"to_address"`
	FromAddress          string `json:"from_address,omitempty"`
	AsPerson             string `json:"as_person,omitempty"`
	Subject              string `json:"subject"`
	Body                 string `json:"body"`
	ContentType          string `json:"content_type,omitempty"`
	SchemaVersion        string `json:"schema_version,omitempty"`
	DisableNotifyMessage *bool  `json:"disable_notify_message,omitempty"`
	Group                bool   `json:"group,omitempty"`
	forwardedMessageID   string
	forwardedFromAddress string
}

type waypostForwardInput struct {
	MessageID            string `json:"message_id,omitempty"`
	DeliveryID           string `json:"delivery_id,omitempty"`
	ToAddress            string `json:"to_address"`
	FromAddress          string `json:"from_address,omitempty"`
	Subject              string `json:"subject,omitempty"`
	Group                bool   `json:"group,omitempty"`
	DisableNotifyMessage *bool  `json:"disable_notify_message,omitempty"`
}

type waypostWaitInput struct {
	Addresses []string `json:"addresses,omitempty"`
	AsPerson  string   `json:"as_person,omitempty"`
	Timeout   string   `json:"timeout,omitempty"`
}

type waypostRecvInput struct {
	Addresses        []string `json:"addresses,omitempty"`
	AsPerson         string   `json:"as_person,omitempty"`
	KnownDeliveryIDs []string `json:"known_delivery_ids,omitempty"`
}

type waypostClaimHistoryInput struct {
	DeliveryID        string `json:"delivery_id,omitempty"`
	IncludeTerminal   bool   `json:"include_terminal,omitempty"`
	IncludeLeaseToken bool   `json:"include_lease_token,omitempty"`
}

type waypostListInput struct {
	Address  string `json:"address,omitempty"`
	AsPerson string `json:"as_person,omitempty"`
	State    string `json:"state,omitempty"`
}

type waypostReadInput struct {
	MessageIDs  []string `json:"message_ids,omitempty"`
	DeliveryIDs []string `json:"delivery_ids,omitempty"`
	Latest      bool     `json:"latest,omitempty"`
	Addresses   []string `json:"addresses,omitempty"`
	State       string   `json:"state,omitempty"`
	Limit       *int     `json:"limit,omitempty"`
}

type waypostGroupInput struct {
	GroupAddress string `json:"group_address"`
}

type waypostGroupMemberInput struct {
	GroupAddress string `json:"group_address"`
	Person       string `json:"person"`
}

type waypostGroupSubscriberInput struct {
	GroupAddress  string `json:"group_address"`
	NotifyAddress string `json:"notify_address"`
	Person        string `json:"person"`
}

type waypostGroupSubscriberRemoveInput struct {
	GroupAddress  string `json:"group_address"`
	NotifyAddress string `json:"notify_address"`
}

type waypostAddressInspectInput struct {
	Address string `json:"address"`
}

type waypostAckInput struct {
	DeliveryID string `json:"delivery_id"`
	LeaseToken string `json:"lease_token"`
}

type waypostDeferInput struct {
	DeliveryID string `json:"delivery_id"`
	LeaseToken string `json:"lease_token"`
	Until      string `json:"until"`
}

type waypostUndeferInput struct {
	DeliveryID string `json:"delivery_id"`
}

type waypostFailInput struct {
	DeliveryID string `json:"delivery_id"`
	LeaseToken string `json:"lease_token"`
	Reason     string `json:"reason"`
}

type readLatestResult struct {
	Items   []waypost.ReadDelivery
	HasMore bool
}

func (s *Service) registerWaypostTools(server *mcp.Server) {
	addToolRequiringWaypostStatus(server, s, &mcp.Tool{
		Name:        "waypost_bind",
		Description: "Bind one or more waypost addresses into MCP server state.",
	}, s.waypostBind)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "waypost_status",
		Description: "Show the currently bound waypost addresses, default sender, and default workdir stored in this MCP server.",
	}, s.waypostStatus)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "waypost_debug",
		Description: "Show read-only MCP process and allowlisted tool session environment diagnostics without auto-binding or mutating state.",
	}, s.waypostDebug)
	addToolRequiringWaypostStatus(server, s, &mcp.Tool{
		Name:        "waypost_send",
		Description: "Send one waypost message and automatically push-notify a non-local target when the address scheme supports it. Set disable_notify_message=true to skip notify for that send.",
	}, s.waypostSend)
	addToolRequiringWaypostStatus(server, s, &mcp.Tool{
		Name:        "waypost_forward",
		Description: "Forward one stored waypost message to a new recipient. Provide exactly one of message_id or delivery_id. The forward reuses the original body, content_type, and schema_version, and sends through the normal waypost_send path.",
	}, s.waypostForward)
	addToolRequiringWaypostStatus(server, s, &mcp.Tool{
		Name:        "waypost_wait",
		Description: "Observe whether delivery is available without claiming it. Agent-managed session queue addresses typically look like agent-deck/<session-id>, codex/<session-id>, claude/<session-id>, gemini/<session-id>, or opencode/<session-id>. Optional timeout is a duration string such as 30s, 5m, 120ms, or 1m30s.",
	}, s.waypostWait)
	addToolRequiringWaypostStatus(server, s, &mcp.Tool{
		Name:        "waypost_recv",
		Description: "Receive currently available delivery immediately and claim it; recv never blocks. Use waypost_wait to wait for availability without claiming. If this MCP process already holds unacknowledged leases, recv returns a hint immediately; pass known_delivery_ids to suppress leases the caller already knows about. If addresses is omitted, receive from all bound addresses; pass addresses only to override that queue set for this call. After ack, use waypost_read to reread persisted deliveries when context is lost.",
	}, s.waypostRecv)
	addToolRequiringWaypostStatus(server, s, &mcp.Tool{
		Name:        "waypost_claim_history",
		Description: "List deliveries this MCP process has claimed during its current lifetime. By default returns active claims without lease tokens; pass delivery_id and include_lease_token=true to recover a token the agent lost.",
	}, s.waypostClaimHistory)
	addToolRequiringWaypostStatus(server, s, &mcp.Tool{
		Name:        "waypost_list",
		Description: "List persisted deliveries for one queue. Use state='acked' to find deliveries that were already received and acknowledged before rereading them with waypost_read.",
	}, s.waypostList)
	addToolRequiringWaypostStatus(server, s, &mcp.Tool{
		Name:        "waypost_read",
		Description: "Read persisted waypost messages or deliveries. Use latest=true with state='acked' to reread recently acknowledged delivery after context loss.",
	}, s.waypostRead)
	addToolRequiringWaypostStatus(server, s, &mcp.Tool{
		Name:        "waypost_ack",
		Description: "Acknowledge a claimed waypost delivery. Acked deliveries remain readable later through waypost_read.",
	}, s.waypostAck)
	addToolRequiringWaypostStatus(server, s, &mcp.Tool{
		Name:        "waypost_release",
		Description: "Release a claimed waypost delivery back to the queue.",
	}, s.waypostRelease)
	addToolRequiringWaypostStatus(server, s, &mcp.Tool{
		Name:        "waypost_defer",
		Description: "Defer a claimed waypost delivery until a later RFC3339 time.",
	}, s.waypostDefer)
	addToolRequiringWaypostStatus(server, s, &mcp.Tool{
		Name:        "waypost_undefer",
		Description: "Make a deferred queued delivery visible immediately; call waypost_recv again to claim it before acking.",
	}, s.waypostUndefer)
	addToolRequiringWaypostStatus(server, s, &mcp.Tool{
		Name:        "waypost_fail",
		Description: "Fail a claimed waypost delivery with a reason.",
	}, s.waypostFail)
	addToolRequiringWaypostStatus(server, s, &mcp.Tool{
		Name:        "waypost_group_create",
		Description: "Create a group waypost address.",
	}, s.waypostGroupCreate)
	addToolRequiringWaypostStatus(server, s, &mcp.Tool{
		Name:        "waypost_group_add_member",
		Description: "Add a person to a group waypost.",
	}, s.waypostGroupAddMember)
	addToolRequiringWaypostStatus(server, s, &mcp.Tool{
		Name:        "waypost_group_remove_member",
		Description: "Remove a person from a group waypost.",
	}, s.waypostGroupRemoveMember)
	addToolRequiringWaypostStatus(server, s, &mcp.Tool{
		Name:        "waypost_group_members",
		Description: "List group waypost memberships.",
	}, s.waypostGroupMembers)
	addToolRequiringWaypostStatus(server, s, &mcp.Tool{
		Name:        "waypost_group_add_subscriber",
		Description: "Add a best-effort notification target for group messages.",
	}, s.waypostGroupAddSubscriber)
	addToolRequiringWaypostStatus(server, s, &mcp.Tool{
		Name:        "waypost_group_remove_subscriber",
		Description: "Remove a group notification target.",
	}, s.waypostGroupRemoveSubscriber)
	addToolRequiringWaypostStatus(server, s, &mcp.Tool{
		Name:        "waypost_group_subscribers",
		Description: "List active group notification targets.",
	}, s.waypostGroupSubscribers)
	addToolRequiringWaypostStatus(server, s, &mcp.Tool{
		Name:        "waypost_address_inspect",
		Description: "Inspect whether an address is an endpoint, group, or unbound.",
	}, s.waypostAddressInspect)
}

func (s *Service) waypostBind(ctx context.Context, _ *mcp.CallToolRequest, input waypostBindInput) (*mcp.CallToolResult, map[string]any, error) {
	bound, err := s.sessions.bind(ctx, input)
	if err != nil {
		return nil, nil, err
	}
	out := boundStateMap(bound)
	out["status"] = "bound"
	return s.waypostMutationToolResult(ctx, out)
}

func (s *Service) waypostStatus(ctx context.Context, _ *mcp.CallToolRequest, _ waypostStatusInput) (*mcp.CallToolResult, map[string]any, error) {
	bound, err := s.sessions.boundState(ctx)
	if err != nil {
		return nil, nil, err
	}
	s.markWaypostStatusCalled()
	out := boundStateMap(bound)
	out["default_sender"] = orUnset(bound.DefaultSender)
	out["default_workdir"] = orUnset(bound.DefaultWorkdir)
	return s.waypostToolResult(ctx, out)
}

func (s *Service) sendWaypostMessage(ctx context.Context, input waypostSendInput) (map[string]any, error) {
	return withWaypostService(ctx, s.waypostServices, func(service waypostSender) (map[string]any, error) {
		return s.sendWaypostMessageWithService(ctx, input, service)
	})
}

func (s *Service) sendWaypostMessageWithService(ctx context.Context, input waypostSendInput, service waypostSender) (map[string]any, error) {
	toAddress, err := waypost.NormalizeAddress(input.ToAddress)
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

	sendResult, err := service.Send(ctx, waypost.SendParams{
		ToAddress:            input.ToAddress,
		FromAddress:          fromAddress,
		AsPerson:             strings.TrimSpace(input.AsPerson),
		Subject:              input.Subject,
		ContentType:          strings.TrimSpace(input.ContentType),
		SchemaVersion:        strings.TrimSpace(input.SchemaVersion),
		ForwardedMessageID:   strings.TrimSpace(input.forwardedMessageID),
		ForwardedFromAddress: strings.TrimSpace(input.forwardedFromAddress),
		Body:                 []byte(input.Body),
		Group:                input.Group,
	})
	if err != nil {
		return nil, err
	}

	notify := s.notifyWaypostSend(ctx, input, sendResult, service)
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
	if sendResult.Mode == waypost.SendModeGroup {
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

func (s *Service) notifyWaypostSend(ctx context.Context, input waypostSendInput, sendResult waypost.SendResult, service any) notificationOutcome {
	notifyCtx := context.WithoutCancel(ctx)
	if sendResult.Mode != waypost.SendModeGroup {
		if s.sessions.isLocalAddress(notifyCtx, input.ToAddress) || wakeNotifyDisabled(input.DisableNotifyMessage) {
			return s.notifications.notifyWaypostSend(notifyCtx, input)
		}
		scope, _, err := directWakeScopeForAddress(input.ToAddress)
		if err != nil {
			return notificationOutcome{Status: "failed", Err: err}
		}
		hasAgentDeckWakeTarget := scope != nil && len(scope.targetsForChannel(WakeChannelAgentDeck)) > 0
		if hasAgentDeckWakeTarget && strings.TrimSpace(sendResult.DeliveryID) != "" {
			if err := s.waitBeforeNotify(notifyCtx); err != nil {
				return notificationOutcome{Status: "failed", Scheme: "waypost", Err: err}
			}
			stillQueued, err := s.deliveryStillQueued(notifyCtx, service, sendResult.DeliveryID)
			if err != nil {
				return notificationOutcome{Status: "failed", Scheme: "waypost", Err: err}
			}
			if !stillQueued {
				return notificationOutcome{Status: "skipped_already_claimed", Scheme: "waypost"}
			}
		}
		return s.notifications.notifyWaypostSend(notifyCtx, input)
	}
	return s.notifications.notifyGroupSubscribers(notifyCtx, input, sendResult.GroupNotificationAddresses)
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

func (s *Service) deliveryStillQueued(ctx context.Context, service any, deliveryID string) (bool, error) {
	reader, ok := service.(waypostDeliveryReader)
	if !ok {
		return false, fmt.Errorf("waypost service %T does not satisfy %T", service, reader)
	}
	deliveries, err := reader.ReadDeliveries(ctx, []string{deliveryID})
	if err != nil {
		return false, err
	}
	if len(deliveries) != 1 {
		return false, fmt.Errorf("read delivery %q: got %d deliveries, want 1", deliveryID, len(deliveries))
	}
	return strings.TrimSpace(deliveries[0].State) == "queued", nil
}

func (s *Service) waypostSend(ctx context.Context, _ *mcp.CallToolRequest, input waypostSendInput) (*mcp.CallToolResult, map[string]any, error) {
	out, err := s.sendWaypostMessage(ctx, input)
	if err != nil {
		return nil, nil, err
	}
	return s.waypostMutationToolResult(ctx, out)
}

func (s *Service) waypostForward(ctx context.Context, _ *mcp.CallToolRequest, input waypostForwardInput) (*mcp.CallToolResult, map[string]any, error) {
	out, err := withWaypostService(ctx, s.waypostServices, func(service interface {
		waypost.ForwardSourceReader
		waypostSender
	}) (map[string]any, error) {
		prepared, err := waypost.PrepareForward(ctx, service, "waypost_forward", waypost.ForwardParams{
			MessageID:   input.MessageID,
			DeliveryID:  input.DeliveryID,
			ToAddress:   input.ToAddress,
			FromAddress: input.FromAddress,
			Subject:     input.Subject,
			Group:       input.Group,
		})
		if err != nil {
			return nil, err
		}

		sendInput := waypostSendInput{
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

		out, err := s.sendWaypostMessageWithService(ctx, sendInput, service)
		if err != nil {
			return nil, err
		}
		out["status"] = "forwarded"
		out["source_message_id"] = prepared.SourceMessageID
		out["source_delivery_id"] = nilIfEmpty(prepared.SourceDeliveryID)
		return out, nil
	})
	if err != nil {
		return nil, nil, err
	}
	return s.waypostMutationToolResult(ctx, out)
}

func (s *Service) waypostWait(ctx context.Context, _ *mcp.CallToolRequest, input waypostWaitInput) (*mcp.CallToolResult, map[string]any, error) {
	addresses, err := s.sessions.waypostAddresses(ctx, input.Addresses)
	if err != nil {
		return nil, nil, err
	}
	warnings := s.waypostReceiveWarnings(ctx, len(input.Addresses) > 0)

	timeoutText := strings.TrimSpace(input.Timeout)
	timeout := time.Duration(0)
	if timeoutText != "" {
		timeout, err = time.ParseDuration(timeoutText)
		if err != nil {
			return nil, nil, fmt.Errorf("parse timeout: %w", err)
		}
	}
	if person := strings.TrimSpace(input.AsPerson); person != "" {
		return s.waypostWaitGroup(ctx, addresses, person, timeout)
	}

	delivery, err := withWaypostService(ctx, s.waypostServices, func(service waypostWaiter) (waypost.ListedDelivery, error) {
		return service.Wait(ctx, waypost.WaitParams{
			Addresses: addresses,
			Timeout:   timeout,
		})
	})
	if errors.Is(err, waypost.ErrNoMessage) {
		return s.waypostToolResult(ctx, map[string]any{
			"status":    "no_message",
			"addresses": addresses,
			"warnings":  warnings,
		})
	}
	if err != nil {
		return nil, nil, err
	}
	return s.waypostToolResult(ctx, map[string]any{
		"status":    "message_available",
		"addresses": addresses,
		"delivery":  waypost.CompactListedDelivery(delivery),
		"warnings":  warnings,
	})
}

func (s *Service) waypostWaitGroup(ctx context.Context, addresses []string, person string, timeout time.Duration) (*mcp.CallToolResult, map[string]any, error) {
	address, err := singleGroupAddress(addresses, "waypost_wait")
	if err != nil {
		return nil, nil, err
	}
	message, err := withWaypostService(ctx, s.waypostServices, func(service waypostGroupMessageWaiter) (waypost.GroupListedMessage, error) {
		return service.WaitGroupMessage(ctx, waypost.GroupWaitParams{
			Address: address,
			Person:  person,
			Timeout: timeout,
		})
	})
	if errors.Is(err, waypost.ErrNoMessage) {
		return s.waypostToolResult(ctx, map[string]any{
			"status":    "no_message",
			"addresses": []string{address},
			"as_person": person,
		})
	}
	if err != nil {
		return nil, nil, err
	}
	return s.waypostToolResult(ctx, map[string]any{
		"status":    "message_available",
		"addresses": []string{address},
		"as_person": person,
		"message":   waypost.CompactGroupListedMessage(message),
	})
}

func (s *Service) waypostRecv(ctx context.Context, _ *mcp.CallToolRequest, input waypostRecvInput) (*mcp.CallToolResult, map[string]any, error) {
	addresses, err := s.sessions.waypostAddresses(ctx, input.Addresses)
	if err != nil {
		return nil, nil, err
	}
	if person := strings.TrimSpace(input.AsPerson); person != "" {
		return s.waypostRecvGroup(ctx, addresses, person)
	}
	warnings := s.waypostReceiveWarnings(ctx, len(input.Addresses) > 0)
	activeLeaseIDs := s.activeLeaseHintDeliveryIDs(addresses, input.KnownDeliveryIDs)
	if len(activeLeaseIDs) > 0 {
		return s.waypostToolResult(ctx, map[string]any{
			"status":                 "active_leases",
			"addresses":              addresses,
			"active_lease_count":     len(activeLeaseIDs),
			"claimed_delivery_ids":   activeLeaseIDs,
			"known_delivery_ids":     normalizedKnownDeliveryIDs(input.KnownDeliveryIDs),
			"claim_history_tool":     "waypost_claim_history",
			"known_delivery_id_hint": "If you are already handling these deliveries, retry waypost_recv with known_delivery_ids set to claimed_delivery_ids. If you lost the lease token, call waypost_claim_history with delivery_id and include_lease_token=true.",
			"warnings":               warnings,
		})
	}

	delivery, err := s.receivePersonalNow(ctx, addresses)
	if errors.Is(err, waypost.ErrNoMessage) {
		return s.waypostToolResult(ctx, map[string]any{
			"status":    "no_message",
			"addresses": addresses,
			"warnings":  warnings,
		})
	}
	if err != nil {
		return nil, nil, err
	}
	s.activeLeases.trackReceive(delivery, s.now().Format(time.RFC3339Nano))
	s.startLeaseRenewLoop()
	return s.waypostMutationToolResult(ctx, map[string]any{
		"status":    "received",
		"addresses": addresses,
		"delivery":  waypost.CompactReceiveResult(delivery),
		"warnings":  warnings,
	})
}

func (s *Service) activeLeaseHintDeliveryIDs(addresses []string, knownDeliveryIDs []string) []string {
	leases := s.activeLeases.snapshot()
	if len(leases) == 0 {
		return nil
	}
	sort.Slice(leases, func(i, j int) bool {
		return leases[i].DeliveryID < leases[j].DeliveryID
	})

	addressSet := make(map[string]struct{}, len(addresses))
	for _, address := range addresses {
		addressSet[address] = struct{}{}
	}
	knownSet := knownDeliveryIDSet(knownDeliveryIDs)
	deliveryIDs := make([]string, 0, len(leases))
	for _, lease := range leases {
		if _, known := knownSet[lease.DeliveryID]; known {
			continue
		}
		if _, wanted := addressSet[lease.RecipientAddress]; !wanted {
			continue
		}
		deliveryIDs = append(deliveryIDs, lease.DeliveryID)
	}
	return deliveryIDs
}

func knownDeliveryIDSet(deliveryIDs []string) map[string]struct{} {
	knownSet := make(map[string]struct{}, len(deliveryIDs))
	for _, deliveryID := range deliveryIDs {
		deliveryID = strings.TrimSpace(deliveryID)
		if deliveryID == "" {
			continue
		}
		knownSet[deliveryID] = struct{}{}
	}
	return knownSet
}

func normalizedKnownDeliveryIDs(deliveryIDs []string) []string {
	known := make([]string, 0, len(deliveryIDs))
	for deliveryID := range knownDeliveryIDSet(deliveryIDs) {
		known = append(known, deliveryID)
	}
	sort.Strings(known)
	return known
}

func (s *Service) waypostClaimHistory(ctx context.Context, _ *mcp.CallToolRequest, input waypostClaimHistoryInput) (*mcp.CallToolResult, map[string]any, error) {
	deliveryID := strings.TrimSpace(input.DeliveryID)
	if input.IncludeLeaseToken && deliveryID == "" {
		return nil, nil, errors.New("include_lease_token requires delivery_id")
	}
	leases := s.activeLeases.historySnapshot(input.IncludeTerminal || deliveryID != "")
	sort.Slice(leases, func(i, j int) bool {
		return leases[i].DeliveryID < leases[j].DeliveryID
	})

	items := make([]map[string]any, 0, len(leases))
	for _, lease := range leases {
		if deliveryID != "" && lease.DeliveryID != deliveryID {
			continue
		}
		item := map[string]any{
			"delivery_id":       lease.DeliveryID,
			"recipient_address": lease.RecipientAddress,
			"lease_expires_at":  lease.LeaseExpiresAt,
			"subject":           lease.Subject,
			"content_type":      lease.ContentType,
			"claimed_at":        lease.ClaimedAt,
			"last_renewed_at":   nilIfEmpty(lease.LastRenewedAt),
			"status":            lease.Status,
			"terminal_at":       nilIfEmpty(lease.TerminalAt),
		}
		if input.IncludeLeaseToken {
			item["lease_token"] = lease.LeaseToken
		}
		items = append(items, item)
	}
	if deliveryID != "" && len(items) == 0 {
		return s.waypostToolResult(ctx, map[string]any{
			"status":      "not_found",
			"delivery_id": deliveryID,
			"items":       items,
		})
	}
	return s.waypostToolResult(ctx, map[string]any{
		"status":                 "listed",
		"items":                  items,
		"include_terminal":       input.IncludeTerminal,
		"lease_tokens_included":  input.IncludeLeaseToken,
		"lease_token_hint":       "Pass delivery_id and include_lease_token=true only when recovering a token this MCP process previously returned.",
		"current_process_only":   true,
		"claimed_delivery_count": len(items),
	})
}

func (s *Service) waypostRecvGroup(ctx context.Context, addresses []string, person string) (*mcp.CallToolResult, map[string]any, error) {
	address, err := singleGroupAddress(addresses, "waypost_recv")
	if err != nil {
		return nil, nil, err
	}
	message, err := s.receiveGroupNow(ctx, address, person)
	if errors.Is(err, waypost.ErrNoMessage) {
		return s.waypostToolResult(ctx, map[string]any{
			"status":    "no_message",
			"addresses": []string{address},
			"as_person": person,
		})
	}
	if err != nil {
		return nil, nil, err
	}
	return s.waypostMutationToolResult(ctx, map[string]any{
		"status":    "received",
		"addresses": []string{address},
		"as_person": person,
		"message":   waypost.CompactGroupReceivedMessage(message),
	})
}

func (s *Service) receivePersonalNow(ctx context.Context, addresses []string) (waypost.ReceiveResult, error) {
	claimMetadata := s.receiveClaimMetadata(addresses)
	return withWaypostService(ctx, s.waypostServices, func(service waypostBatchReceiver) (waypost.ReceiveResult, error) {
		// MCP claims only immediately visible work. Waiting stays in waypost_wait so
		// abandoned tool calls cannot later claim delivery into an unreachable result.
		return service.ReceiveBatchWithLeaseTTL(waypost.WithClaimMetadata(ctx, claimMetadata), waypost.ReceiveBatchParams{
			Addresses: addresses,
			Max:       1,
		}, s.mcpLeaseTTL)
	})
}

func (s *Service) receiveClaimMetadata(addresses []string) waypost.ClaimMetadata {
	snapshot := s.sessions.snapshotState()
	return waypost.ClaimMetadata{
		Source:             "mcp",
		Tool:               "waypost_recv",
		BoundAddresses:     addresses,
		AgentDeckSessionID: snapshot.DetectedAgentDeckSession,
		AgentSessionID:     snapshot.DetectedToolSessions["codex"],
		Workdir:            snapshot.DefaultWorkdir,
	}
}

func (s *Service) receiveGroupNow(ctx context.Context, address, person string) (waypost.GroupReceivedMessage, error) {
	return withWaypostService(ctx, s.waypostServices, func(service waypostGroupMessageReceiver) (waypost.GroupReceivedMessage, error) {
		return service.ReceiveGroupMessage(ctx, waypost.GroupReceiveParams{
			Address: address,
			Person:  person,
		})
	})
}

func singleGroupAddress(addresses []string, toolName string) (string, error) {
	if len(addresses) != 1 {
		return "", fmt.Errorf("%s with as_person requires exactly one group address", toolName)
	}
	address := addresses[0]
	if !waypost.IsGroupAddress(address) {
		return "", fmt.Errorf("%s with as_person requires a group address", toolName)
	}
	return address, nil
}

func (s *Service) waypostReceiveWarnings(ctx context.Context, explicitAddresses bool) []string {
	if explicitAddresses {
		return nil
	}
	bound, err := s.sessions.boundState(ctx)
	if err != nil {
		return []string{"unable to verify bound agent session state: " + err.Error()}
	}
	toolSessionBound := len(bound.DetectedToolSessionAddresses) > 0 || len(boundToolSessionAddresses(bound.BoundAddresses)) > 0
	agentDeckBound := bound.DetectedAgentDeckSession != "" || len(boundAddressesByScheme(bound.BoundAddresses, "agent-deck")) > 0
	if !toolSessionBound || agentDeckBound {
		return nil
	}
	return []string{agentDeckBindRecoveryHint}
}

func (s *Service) waypostList(ctx context.Context, _ *mcp.CallToolRequest, input waypostListInput) (*mcp.CallToolResult, map[string]any, error) {
	var address string
	if strings.TrimSpace(input.Address) != "" {
		address = strings.TrimSpace(input.Address)
	} else {
		boundAddresses, err := s.sessions.waypostAddresses(ctx, nil)
		if err != nil {
			return nil, nil, err
		}
		if len(boundAddresses) != 1 {
			return nil, nil, errors.New("waypost_list requires address when multiple waypost addresses are bound")
		}
		address = boundAddresses[0]
	}
	if input.AsPerson != "" && input.State != "" {
		return nil, nil, errors.New("waypost_list does not support state together with as_person")
	}

	deliveries, err := withWaypostService(ctx, s.waypostServices, func(service interface {
		waypostLister
		waypostGroupMessageLister
	}) (any, error) {
		if input.AsPerson != "" {
			messages, err := service.ListGroupMessages(ctx, waypost.GroupListParams{
				Address: address,
				Person:  input.AsPerson,
			})
			if err != nil {
				return nil, err
			}
			summaries := make([]waypost.GroupListedMessageCompact, 0, len(messages))
			for _, message := range messages {
				summaries = append(summaries, waypost.CompactGroupListedMessage(message))
			}
			return summaries, nil
		}
		return service.List(ctx, waypost.ListParams{
			Address: address,
			State:   input.State,
		})
	})
	if err != nil {
		return nil, nil, err
	}
	return s.waypostToolResult(ctx, map[string]any{
		"status":     "listed",
		"address":    address,
		"as_person":  nilIfEmpty(input.AsPerson),
		"state":      nilIfEmpty(input.State),
		"deliveries": deliveries,
	})
}

func (s *Service) waypostRead(ctx context.Context, _ *mcp.CallToolRequest, input waypostReadInput) (*mcp.CallToolResult, map[string]any, error) {
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
		return nil, nil, errors.New("waypost_read requires exactly one mode: message_ids, delivery_ids, or latest=true")
	}

	result := map[string]any{
		"status": "read",
		"mode":   "unknown",
	}

	switch {
	case wantsLatest:
		addresses, err := s.sessions.waypostAddresses(ctx, input.Addresses)
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
			return nil, nil, errors.New("waypost_read message_ids mode does not support addresses, state, or limit")
		}
		messageIDs := dedupe(input.MessageIDs)
		result["mode"] = "message_ids"
		result["message_ids"] = messageIDs
		messages, err := withWaypostService(ctx, s.waypostServices, func(service waypostMessageReader) ([]waypost.ReadMessage, error) {
			return service.ReadMessages(ctx, messageIDs)
		})
		if err != nil {
			return nil, nil, err
		}
		result["items"] = messages
		return s.waypostToolResult(ctx, result)
	default:
		if len(input.Addresses) > 0 || input.State != "" || input.Limit != nil {
			return nil, nil, errors.New("waypost_read delivery_ids mode does not support addresses, state, or limit")
		}
		deliveryIDs := dedupe(input.DeliveryIDs)
		result["mode"] = "delivery_ids"
		result["delivery_ids"] = deliveryIDs
		deliveries, err := withWaypostService(ctx, s.waypostServices, func(service waypostDeliveryReader) ([]waypost.ReadDelivery, error) {
			return service.ReadDeliveries(ctx, deliveryIDs)
		})
		if err != nil {
			return nil, nil, err
		}
		result["items"] = deliveries
		return s.waypostToolResult(ctx, result)
	}

	latest, err := withWaypostService(ctx, s.waypostServices, func(service waypostLatestDeliveryReader) (readLatestResult, error) {
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
	if latest.HasMore {
		result["has_more"] = true
	}
	return s.waypostToolResult(ctx, result)
}

func (s *Service) waypostAck(ctx context.Context, _ *mcp.CallToolRequest, input waypostAckInput) (*mcp.CallToolResult, map[string]any, error) {
	if err := s.activeLeases.terminalMutationAllowed(input.DeliveryID); err != nil {
		return nil, nil, err
	}
	_, err := withWaypostService(ctx, s.waypostServices, func(service waypostDeliveryTransitioner) (waypost.DeliveryTransitionResult, error) {
		return service.Ack(ctx, input.DeliveryID, input.LeaseToken)
	})
	if err != nil {
		return nil, nil, err
	}
	s.activeLeases.markTerminal(input.DeliveryID, "acked", s.now().Format(time.RFC3339Nano))
	return s.waypostMutationToolResult(ctx, map[string]any{"status": "acked", "delivery_id": input.DeliveryID})
}

func (s *Service) waypostRelease(ctx context.Context, _ *mcp.CallToolRequest, input waypostAckInput) (*mcp.CallToolResult, map[string]any, error) {
	if err := s.activeLeases.terminalMutationAllowed(input.DeliveryID); err != nil {
		return nil, nil, err
	}
	_, err := withWaypostService(ctx, s.waypostServices, func(service waypostDeliveryTransitioner) (waypost.DeliveryTransitionResult, error) {
		return service.Release(ctx, input.DeliveryID, input.LeaseToken)
	})
	if err != nil {
		return nil, nil, err
	}
	s.activeLeases.markTerminal(input.DeliveryID, "released", s.now().Format(time.RFC3339Nano))
	return s.waypostMutationToolResult(ctx, map[string]any{"status": "released", "delivery_id": input.DeliveryID})
}

func (s *Service) waypostDefer(ctx context.Context, _ *mcp.CallToolRequest, input waypostDeferInput) (*mcp.CallToolResult, map[string]any, error) {
	if err := s.activeLeases.terminalMutationAllowed(input.DeliveryID); err != nil {
		return nil, nil, err
	}
	until, err := time.Parse(time.RFC3339Nano, input.Until)
	if err != nil {
		return nil, nil, fmt.Errorf("parse until: %w", err)
	}
	_, err = withWaypostService(ctx, s.waypostServices, func(service waypostDeliveryTransitioner) (waypost.DeliveryTransitionResult, error) {
		return service.Defer(ctx, input.DeliveryID, input.LeaseToken, until)
	})
	if err != nil {
		return nil, nil, err
	}
	s.activeLeases.markTerminal(input.DeliveryID, "deferred", s.now().Format(time.RFC3339Nano))
	return s.waypostMutationToolResult(ctx, map[string]any{"status": "deferred", "delivery_id": input.DeliveryID, "until": input.Until})
}

func (s *Service) waypostUndefer(ctx context.Context, _ *mcp.CallToolRequest, input waypostUndeferInput) (*mcp.CallToolResult, map[string]any, error) {
	result, err := withWaypostService(ctx, s.waypostServices, func(service waypostDeliveryTransitioner) (waypost.DeliveryTransitionResult, error) {
		return service.Undefer(ctx, input.DeliveryID)
	})
	if err != nil {
		return nil, nil, err
	}
	return s.waypostMutationToolResult(ctx, map[string]any{
		"status":      "undeferred",
		"delivery_id": result.DeliveryID,
		"visible_at":  result.VisibleAt,
	})
}

func (s *Service) waypostFail(ctx context.Context, _ *mcp.CallToolRequest, input waypostFailInput) (*mcp.CallToolResult, map[string]any, error) {
	if err := s.activeLeases.terminalMutationAllowed(input.DeliveryID); err != nil {
		return nil, nil, err
	}
	_, err := withWaypostService(ctx, s.waypostServices, func(service waypostDeliveryTransitioner) (waypost.DeliveryTransitionResult, error) {
		return service.Fail(ctx, input.DeliveryID, input.LeaseToken, input.Reason)
	})
	if err != nil {
		return nil, nil, err
	}
	s.activeLeases.markTerminal(input.DeliveryID, "failed", s.now().Format(time.RFC3339Nano))
	return s.waypostMutationToolResult(ctx, map[string]any{"status": "failed", "delivery_id": input.DeliveryID, "reason": input.Reason})
}

func (s *Service) waypostGroupCreate(ctx context.Context, _ *mcp.CallToolRequest, input waypostGroupInput) (*mcp.CallToolResult, map[string]any, error) {
	group, err := withWaypostService(ctx, s.waypostServices, func(service waypostGroupManager) (waypost.GroupRecord, error) {
		return service.CreateGroup(ctx, input.GroupAddress)
	})
	if err != nil {
		return nil, nil, err
	}
	return s.waypostMutationToolResult(ctx, map[string]any{
		"status": "created",
		"group":  group,
	})
}

func (s *Service) waypostGroupAddMember(ctx context.Context, _ *mcp.CallToolRequest, input waypostGroupMemberInput) (*mcp.CallToolResult, map[string]any, error) {
	membership, err := withWaypostService(ctx, s.waypostServices, func(service waypostGroupManager) (waypost.GroupMembershipRecord, error) {
		return service.AddGroupMember(ctx, input.GroupAddress, input.Person)
	})
	if err != nil {
		return nil, nil, err
	}
	return s.waypostMutationToolResult(ctx, map[string]any{
		"status":     "added",
		"membership": membership,
	})
}

func (s *Service) waypostGroupRemoveMember(ctx context.Context, _ *mcp.CallToolRequest, input waypostGroupMemberInput) (*mcp.CallToolResult, map[string]any, error) {
	membership, err := withWaypostService(ctx, s.waypostServices, func(service waypostGroupManager) (waypost.GroupMembershipRecord, error) {
		return service.RemoveGroupMember(ctx, input.GroupAddress, input.Person)
	})
	if err != nil {
		return nil, nil, err
	}
	return s.waypostMutationToolResult(ctx, map[string]any{
		"status":     "removed",
		"membership": membership,
	})
}

func (s *Service) waypostGroupMembers(ctx context.Context, _ *mcp.CallToolRequest, input waypostGroupInput) (*mcp.CallToolResult, map[string]any, error) {
	memberships, err := withWaypostService(ctx, s.waypostServices, func(service waypostGroupManager) ([]waypost.GroupMembershipRecord, error) {
		return service.ListGroupMembers(ctx, input.GroupAddress)
	})
	if err != nil {
		return nil, nil, err
	}
	return s.waypostToolResult(ctx, map[string]any{
		"status":      "listed",
		"group":       input.GroupAddress,
		"memberships": memberships,
	})
}

func (s *Service) waypostGroupAddSubscriber(ctx context.Context, _ *mcp.CallToolRequest, input waypostGroupSubscriberInput) (*mcp.CallToolResult, map[string]any, error) {
	subscriber, err := withWaypostService(ctx, s.waypostServices, func(service waypostGroupSubscriberManager) (waypost.GroupNotificationSubscriberRecord, error) {
		return service.AddGroupNotificationSubscriber(ctx, input.GroupAddress, input.NotifyAddress, input.Person)
	})
	if err != nil {
		return nil, nil, err
	}
	return s.waypostMutationToolResult(ctx, map[string]any{
		"status":     "added",
		"subscriber": subscriber,
	})
}

func (s *Service) waypostGroupRemoveSubscriber(ctx context.Context, _ *mcp.CallToolRequest, input waypostGroupSubscriberRemoveInput) (*mcp.CallToolResult, map[string]any, error) {
	subscriber, err := withWaypostService(ctx, s.waypostServices, func(service waypostGroupSubscriberManager) (waypost.GroupNotificationSubscriberRecord, error) {
		return service.RemoveGroupNotificationSubscriber(ctx, input.GroupAddress, input.NotifyAddress)
	})
	if err != nil {
		return nil, nil, err
	}
	return s.waypostMutationToolResult(ctx, map[string]any{
		"status":     "removed",
		"subscriber": subscriber,
	})
}

func (s *Service) waypostGroupSubscribers(ctx context.Context, _ *mcp.CallToolRequest, input waypostGroupInput) (*mcp.CallToolResult, map[string]any, error) {
	subscribers, err := withWaypostService(ctx, s.waypostServices, func(service waypostGroupSubscriberManager) ([]waypost.GroupNotificationSubscriberRecord, error) {
		return service.ListGroupNotificationSubscribers(ctx, input.GroupAddress)
	})
	if err != nil {
		return nil, nil, err
	}
	return s.waypostToolResult(ctx, map[string]any{
		"status":      "listed",
		"group":       input.GroupAddress,
		"subscribers": subscribers,
	})
}

func (s *Service) waypostAddressInspect(ctx context.Context, _ *mcp.CallToolRequest, input waypostAddressInspectInput) (*mcp.CallToolResult, map[string]any, error) {
	inspection, err := withWaypostService(ctx, s.waypostServices, func(service waypostAddressInspector) (waypost.AddressInspection, error) {
		return service.InspectAddress(ctx, input.Address)
	})
	if err != nil {
		return nil, nil, err
	}
	return s.waypostToolResult(ctx, map[string]any{
		"status":     "inspected",
		"inspection": inspection,
	})
}

func (s *Service) waypostToolResult(ctx context.Context, result map[string]any) (*mcp.CallToolResult, map[string]any, error) {
	return s.toolResult(ctx, result)
}

func (s *Service) waypostMutationToolResult(ctx context.Context, result map[string]any) (*mcp.CallToolResult, map[string]any, error) {
	s.emitWaypostOverviewUpdatedBestEffort(ctx)
	return s.toolResult(ctx, result)
}
