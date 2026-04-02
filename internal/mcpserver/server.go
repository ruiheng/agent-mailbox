package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/ruiheng/agent-mailbox/internal/mailbox"
)

const (
	serverName               = "agent_mailbox"
	serverVersion            = "0.4.0"
	syncCmdTimeout           = 30 * time.Second
	ensureSessionShowTimeout = 30 * time.Second
	notificationDelivery     = "delivery_available"
	notificationStaleUnread  = "stale_unread"
	defaultListenerMessage   = "If agent_mailbox is not bound yet, first run agent-deck session current --json and call mailbox_bind for this session. When a wakeup message arrives, use the 'check-agent-mail' skill and execute its requested action."
	defaultNotifyMessage     = "Use the 'check-agent-mail' skill now. Receive the pending message and execute its requested action."
	mailboxRecoveryHint      = "If you forget the mailbox details or next action after ack, use `mailbox_read` on the latest `acked` delivery for this session. For older mail, use `mailbox_list` with `state: acked` and then `mailbox_read` by delivery id."
	serverInstructions       = "Bootstrap this MCP process once per agent-managed session. If it is not bound yet, run `agent-deck session current --json`, take the current session id, and call `mailbox_bind`. Use `agent-deck/<id>` as the default sender. Pass `default_workdir` when you want later `agent_deck_ensure_session` calls to create sessions in the current project. `mailbox_wait` is not recommended for normal workflow; prefer `mailbox_recv`. Later reuse the bound addresses until MCP state is lost."
	unsetValue               = "<unset>"
)

var (
	activeSessionStatuses = map[string]bool{
		"running": true,
		"waiting": true,
		"idle":    true,
	}
	codexResumePattern      = regexp.MustCompile(`\bresume\s+([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})\b`)
	codexSessionFilePattern = regexp.MustCompile(`/\.codex/sessions/.*-([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})\.jsonl$`)
	codexCommandPattern     = regexp.MustCompile(`(^|/)codex(\s|$)`)
)

type Runner interface {
	Run(ctx context.Context, args []string, input string) (RunResult, error)
}

type RunResult struct {
	ExitCode int
	Stdout   string
	Stderr   string
}

type Options struct {
	MailboxRunner Runner
	CommandRunner Runner
	StateDir      string
}

type Service struct {
	mailboxRunner Runner
	commandRunner Runner
	notifiers     map[string]managerNotifier
	state         *serverState
}

type serverState struct {
	mu                       sync.Mutex
	boundAddresses           []string
	defaultSender            string
	defaultWorkdir           string
	autoBindAttempted        bool
	detectedAgentDeckSession string
	detectedAgentSession     string
}

type stateSnapshot struct {
	BoundAddresses           []string
	DefaultSender            string
	DefaultWorkdir           string
	AutoBindAttempted        bool
	DetectedAgentDeckSession string
	DetectedAgentSession     string
}

type boundState struct {
	BoundAddresses           []string `json:"bound_addresses"`
	DefaultSender            string   `json:"default_sender"`
	DefaultWorkdir           string   `json:"default_workdir"`
	DetectedAgentDeckSession string   `json:"detected_agent_deck_session_id"`
	DetectedAgentSession     string   `json:"detected_agent_session_id"`
	Warnings                 []string `json:"warnings"`
}

type runOptions struct {
	input   string
	okCodes []int
	timeout time.Duration
}

type mailboxBindInput struct {
	Addresses      []string `json:"addresses"`
	DefaultSender  string   `json:"default_sender,omitempty"`
	DefaultWorkdir string   `json:"default_workdir,omitempty"`
}

type mailboxStatusInput struct{}

type mailboxSendInput struct {
	ToAddress     string  `json:"to_address"`
	FromAddress   string  `json:"from_address,omitempty"`
	Subject       string  `json:"subject"`
	Body          string  `json:"body"`
	ContentType   string  `json:"content_type,omitempty"`
	SchemaVersion string  `json:"schema_version,omitempty"`
	NotifyMessage *string `json:"notify_message,omitempty"`
}

type mailboxWaitInput struct {
	Addresses []string `json:"addresses,omitempty"`
	Timeout   string   `json:"timeout,omitempty"`
}

type mailboxRecvInput struct {
	Addresses []string `json:"addresses,omitempty"`
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

type agentDeckResolveSessionInput struct {
	Session string `json:"session"`
}

type agentDeckEnsureSessionInput struct {
	SessionID       string `json:"session_id,omitempty"`
	SessionRef      string `json:"session_ref,omitempty"`
	EnsureTitle     string `json:"ensure_title,omitempty"`
	EnsureCmd       string `json:"ensure_cmd,omitempty"`
	ParentSessionID string `json:"parent_session_id,omitempty"`
	Workdir         string `json:"workdir,omitempty"`
	ListenerMessage string `json:"listener_message,omitempty"`
}

type sessionData struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Status  string `json:"status"`
	Path    string `json:"path"`
	Success *bool  `json:"success,omitempty"`
}

type parsedAddress struct {
	Scheme string
	ID     string
}

type notificationRoute struct {
	Manager string
	Target  string
}

type notificationEvent struct {
	Kind            string
	Route           notificationRoute
	Subject         string
	Body            string
	MessageOverride *string
}

type notificationOutcome struct {
	Status string
	Scheme string
	Err    error
}

type managerNotifier interface {
	Name() string
	Notify(ctx context.Context, event notificationEvent) notificationOutcome
}

type psRow struct {
	PID  int
	PPID int
	Comm string
	Args string
}

type osCommandRunner struct {
	cwd string
}

type mailboxAppRunner struct {
	prefixArgs []string
}

type agentDeckNotifier struct {
	service *Service
}

func New(opts Options) *mcp.Server {
	return newService(opts).Server()
}

func newService(opts Options) *Service {
	if opts.MailboxRunner == nil {
		opts.MailboxRunner = mailboxAppRunner{prefixArgs: stateDirArgs(opts.StateDir)}
	}
	if opts.CommandRunner == nil {
		opts.CommandRunner = osCommandRunner{cwd: currentWorkingDir()}
	}
	service := &Service{
		mailboxRunner: opts.MailboxRunner,
		commandRunner: opts.CommandRunner,
		state:         &serverState{},
	}
	service.notifiers = map[string]managerNotifier{
		"agent-deck": agentDeckNotifier{service: service},
	}
	return service
}

func (s *Service) Server() *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: serverName, Version: serverVersion}, &mcp.ServerOptions{
		Instructions: serverInstructions,
	})

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
		Description: "Send one mailbox message and automatically push-notify a non-local target when the address scheme supports it. Pass an empty notify_message to disable notify for that send.",
	}, s.mailboxSend)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "mailbox_wait",
		Description: "Observe whether mail is available without claiming it. Not recommended for normal workflow; prefer mailbox_recv. Use this only for manual diagnostics or observation. Agent-managed session inbox addresses typically look like agent-deck/<session-id> or codex/<session-id>. Optional timeout is a duration string such as 30s, 5m, 120ms, or 1m30s.",
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
		Name:        "agent_deck_resolve_session",
		Description: "Resolve an agent-deck session ref or id and return its canonical session id, status, and mailbox addresses.",
	}, s.agentDeckResolveSession)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "agent_deck_ensure_session",
		Description: "Resolve or create an agent-deck session. If the target exists but is not active, start it; if it is already active, return notify_needed=true.",
	}, s.agentDeckEnsureSession)

	return server
}

func currentWorkingDir() string {
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return wd
}

func (r osCommandRunner) Run(ctx context.Context, args []string, input string) (RunResult, error) {
	if len(args) == 0 {
		return RunResult{}, errors.New("missing command")
	}
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	if r.cwd != "" {
		cmd.Dir = r.cwd
	}
	if input != "" {
		cmd.Stdin = strings.NewReader(input)
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err == nil {
		return RunResult{ExitCode: 0, Stdout: stdout.String(), Stderr: stderr.String()}, nil
	}
	if ctx.Err() != nil {
		return RunResult{}, ctx.Err()
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return RunResult{ExitCode: exitErr.ExitCode(), Stdout: stdout.String(), Stderr: stderr.String()}, nil
	}
	return RunResult{}, err
}

func (r mailboxAppRunner) Run(ctx context.Context, args []string, input string) (RunResult, error) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	app := mailbox.NewApp(strings.NewReader(input), &stdout, &stderr)
	forwarded := append([]string(nil), r.prefixArgs...)
	forwarded = append(forwarded, args...)
	err := app.Run(ctx, forwarded)
	switch {
	case err == nil, errors.Is(err, mailbox.ErrHelpRequested):
		return RunResult{ExitCode: 0, Stdout: stdout.String(), Stderr: stderr.String()}, nil
	case errors.Is(err, mailbox.ErrNoMessage):
		return RunResult{ExitCode: 2, Stdout: stdout.String(), Stderr: stderr.String()}, nil
	default:
		if stderr.Len() == 0 && err != nil {
			stderr.WriteString(err.Error())
		}
		return RunResult{ExitCode: 1, Stdout: stdout.String(), Stderr: stderr.String()}, nil
	}
}

func stateDirArgs(stateDir string) []string {
	trimmed := strings.TrimSpace(stateDir)
	if trimmed == "" {
		return nil
	}
	return []string{"--state-dir", trimmed}
}

func (s *Service) mailboxBind(ctx context.Context, _ *mcp.CallToolRequest, input mailboxBindInput) (*mcp.CallToolResult, map[string]any, error) {
	boundAddresses := dedupe(input.Addresses)
	defaultSender := strings.TrimSpace(input.DefaultSender)
	if defaultSender == "" && len(boundAddresses) > 0 {
		defaultSender = boundAddresses[0]
	}

	s.state.mu.Lock()
	s.state.boundAddresses = boundAddresses
	s.state.defaultSender = defaultSender
	s.state.defaultWorkdir = strings.TrimSpace(input.DefaultWorkdir)
	s.state.autoBindAttempted = true
	s.state.mu.Unlock()

	bound, err := s.getBoundState(ctx)
	if err != nil {
		return nil, nil, err
	}
	out := boundStateMap(bound)
	out["status"] = "bound"
	return nil, out, nil
}

func (s *Service) mailboxStatus(ctx context.Context, _ *mcp.CallToolRequest, _ mailboxStatusInput) (*mcp.CallToolResult, map[string]any, error) {
	bound, err := s.getBoundState(ctx)
	if err != nil {
		return nil, nil, err
	}
	return nil, map[string]any{
		"bound_addresses": bound.BoundAddresses,
		"default_sender":  orUnset(bound.DefaultSender),
		"default_workdir": orUnset(bound.DefaultWorkdir),
	}, nil
}

func (s *Service) mailboxSend(ctx context.Context, _ *mcp.CallToolRequest, input mailboxSendInput) (*mcp.CallToolResult, map[string]any, error) {
	fromAddress, err := s.senderAddress(ctx, input.FromAddress)
	if err != nil {
		return nil, nil, err
	}

	sendArgs := []string{
		"send",
		"--to", input.ToAddress,
		"--from", fromAddress,
		"--subject", input.Subject,
		"--body-file", "-",
	}
	if contentType := strings.TrimSpace(input.ContentType); contentType != "" {
		sendArgs = append(sendArgs, "--content-type", contentType)
	}
	if schemaVersion := strings.TrimSpace(input.SchemaVersion); schemaVersion != "" {
		sendArgs = append(sendArgs, "--schema-version", schemaVersion)
	}

	sendResult, err := runCommand(ctx, s.mailboxRunner, sendArgs, runOptions{input: input.Body})
	if err != nil {
		return nil, nil, err
	}

	sendIDs, err := validateSendReceipt(parseSendTokens(sendResult.Stdout), sendResult.Stdout)
	if err != nil {
		return nil, nil, err
	}

	notify := s.notifyMailboxSend(ctx, input)
	var notifyScheme any
	if notify.Scheme != "" {
		notifyScheme = notify.Scheme
	}
	var notifyError any
	if notify.Err != nil {
		notifyError = notify.Err.Error()
	}

	return nil, map[string]any{
		"status":        "sent",
		"from_address":  fromAddress,
		"to_address":    input.ToAddress,
		"subject":       input.Subject,
		"delivery_id":   sendIDs.DeliveryID,
		"notify_status": notify.Status,
		"notify_scheme": notifyScheme,
		"notify_error":  notifyError,
	}, nil
}

func (s *Service) notifyMailboxSend(ctx context.Context, input mailboxSendInput) notificationOutcome {
	if s.isLocalAddress(ctx, input.ToAddress) {
		return notificationOutcome{Status: "skipped_local"}
	}

	route, err := notificationRouteForAddress(input.ToAddress)
	if err != nil {
		return notificationOutcome{Status: "failed", Err: err}
	}

	return s.notifyRoute(ctx, notificationEvent{
		Kind:            notificationDelivery,
		Route:           route,
		Subject:         input.Subject,
		Body:            input.Body,
		MessageOverride: input.NotifyMessage,
	})
}

func (s *Service) notifyRoute(ctx context.Context, event notificationEvent) notificationOutcome {
	notifier, ok := s.notifiers[event.Route.Manager]
	if !ok {
		return notificationOutcome{
			Status: "unsupported",
			Scheme: event.Route.Manager,
		}
	}
	outcome := notifier.Notify(ctx, event)
	if outcome.Scheme == "" {
		outcome.Scheme = notifier.Name()
	}
	return outcome
}

func (n agentDeckNotifier) Name() string {
	return "agent-deck"
}

func (n agentDeckNotifier) Notify(ctx context.Context, event notificationEvent) notificationOutcome {
	if event.Kind != notificationDelivery && event.Kind != notificationStaleUnread {
		return notificationOutcome{
			Status: "unsupported",
			Scheme: n.Name(),
		}
	}
	if event.MessageOverride != nil && strings.TrimSpace(*event.MessageOverride) == "" {
		return notificationOutcome{
			Status: "skipped_disabled",
			Scheme: n.Name(),
		}
	}

	targetLabel := event.Route.Target
	targetSession, err := n.service.resolveSessionShowBestEffort(ctx, event.Route.Target)
	if err != nil {
		return notificationOutcome{
			Status: "failed",
			Scheme: n.Name(),
			Err:    err,
		}
	}
	if targetSession != nil && strings.TrimSpace(targetSession.Title) != "" {
		targetLabel = strings.TrimSpace(targetSession.Title)
	}

	notifyMessage := defaultNotifyMessage
	if event.MessageOverride != nil && strings.TrimSpace(*event.MessageOverride) != "" {
		notifyMessage = *event.MessageOverride
	}
	notifyMessage = ensureReceiverWorkflowHint(notifyMessage, defaultNotifyMessage, targetLabel)
	_, err = runCommand(ctx, n.service.commandRunner, []string{
		"agent-deck", "session", "send", "--no-wait", event.Route.Target, notifyMessage,
	}, runOptions{timeout: syncCmdTimeout})
	if err != nil {
		return notificationOutcome{
			Status: "failed",
			Scheme: n.Name(),
			Err:    err,
		}
	}

	return notificationOutcome{
		Status: "sent",
		Scheme: n.Name(),
	}
}

func (s *Service) mailboxWait(ctx context.Context, _ *mcp.CallToolRequest, input mailboxWaitInput) (*mcp.CallToolResult, map[string]any, error) {
	addresses, err := s.mailboxAddresses(ctx, input.Addresses)
	if err != nil {
		return nil, nil, err
	}

	args := []string{"wait"}
	for _, address := range addresses {
		args = append(args, "--for", address)
	}
	if strings.TrimSpace(input.Timeout) != "" {
		args = append(args, "--timeout", strings.TrimSpace(input.Timeout))
	}
	args = append(args, "--json")

	result, err := runCommand(ctx, s.mailboxRunner, args, runOptions{okCodes: []int{0, 2}})
	if err != nil {
		return nil, nil, err
	}
	if result.ExitCode == 2 {
		return nil, map[string]any{
			"status":    "no_message",
			"addresses": addresses,
		}, nil
	}

	delivery, err := parseJSONValue(result.Stdout, "agent-mailbox wait")
	if err != nil {
		return nil, nil, err
	}
	return nil, map[string]any{
		"status":    "message_available",
		"addresses": addresses,
		"delivery":  delivery,
	}, nil
}

func (s *Service) mailboxRecv(ctx context.Context, _ *mcp.CallToolRequest, input mailboxRecvInput) (*mcp.CallToolResult, map[string]any, error) {
	addresses, err := s.mailboxAddresses(ctx, input.Addresses)
	if err != nil {
		return nil, nil, err
	}

	args := []string{"recv"}
	for _, address := range addresses {
		args = append(args, "--for", address)
	}
	args = append(args, "--max", "1", "--json")

	result, err := runCommand(ctx, s.mailboxRunner, args, runOptions{okCodes: []int{0, 2}})
	if err != nil {
		return nil, nil, err
	}
	if result.ExitCode == 2 {
		return nil, map[string]any{
			"status":    "no_message",
			"addresses": addresses,
		}, nil
	}

	delivery, err := parseJSONValue(result.Stdout, "agent-mailbox recv")
	if err != nil {
		return nil, nil, err
	}
	return nil, map[string]any{
		"status":    "received",
		"addresses": addresses,
		"delivery":  delivery,
	}, nil
}

func (s *Service) mailboxList(ctx context.Context, _ *mcp.CallToolRequest, input mailboxListInput) (*mcp.CallToolResult, map[string]any, error) {
	var address string
	if strings.TrimSpace(input.Address) != "" {
		address = strings.TrimSpace(input.Address)
	} else {
		boundAddresses, err := s.mailboxAddresses(ctx, nil)
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

	args := []string{"list", "--for", address}
	if input.AsPerson != "" {
		args = append(args, "--as", input.AsPerson)
	}
	if input.State != "" {
		args = append(args, "--state", input.State)
	}
	args = append(args, "--json")

	deliveries, err := parseJSONFromRunner(ctx, s.mailboxRunner, args, "agent-mailbox list")
	if err != nil {
		return nil, nil, err
	}
	return nil, map[string]any{
		"status":     "listed",
		"address":    address,
		"as_person":  nilIfEmpty(input.AsPerson),
		"state":      nilIfEmpty(input.State),
		"deliveries": deliveries,
	}, nil
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

	args := []string{"read"}
	result := map[string]any{
		"status": "read",
		"mode":   "unknown",
	}

	switch {
	case wantsLatest:
		addresses, err := s.mailboxAddresses(ctx, input.Addresses)
		if err != nil {
			return nil, nil, err
		}
		args = append(args, "--latest")
		for _, address := range addresses {
			args = append(args, "--for", address)
		}
		if input.State != "" {
			args = append(args, "--state", input.State)
		}
		if input.Limit != nil {
			args = append(args, "--limit", strconv.Itoa(*input.Limit))
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
		for _, messageID := range messageIDs {
			args = append(args, "--message", messageID)
		}
		result["mode"] = "message_ids"
		result["message_ids"] = messageIDs
	default:
		if len(input.Addresses) > 0 || input.State != "" || input.Limit != nil {
			return nil, nil, errors.New("mailbox_read delivery_ids mode does not support addresses, state, or limit")
		}
		deliveryIDs := dedupe(input.DeliveryIDs)
		for _, deliveryID := range deliveryIDs {
			args = append(args, "--delivery", deliveryID)
		}
		result["mode"] = "delivery_ids"
		result["delivery_ids"] = deliveryIDs
	}

	args = append(args, "--json")
	readResult, err := parseJSONFromRunner(ctx, s.mailboxRunner, args, "agent-mailbox read")
	if err != nil {
		return nil, nil, err
	}
	for key, value := range readResult.(map[string]any) {
		result[key] = value
	}
	return nil, result, nil
}

func (s *Service) mailboxAck(ctx context.Context, _ *mcp.CallToolRequest, input mailboxAckInput) (*mcp.CallToolResult, map[string]any, error) {
	_, err := runCommand(ctx, s.mailboxRunner, []string{
		"ack", "--delivery", input.DeliveryID, "--lease-token", input.LeaseToken,
	}, runOptions{})
	if err != nil {
		return nil, nil, err
	}
	return nil, map[string]any{"status": "acked", "delivery_id": input.DeliveryID}, nil
}

func (s *Service) mailboxRelease(ctx context.Context, _ *mcp.CallToolRequest, input mailboxAckInput) (*mcp.CallToolResult, map[string]any, error) {
	_, err := runCommand(ctx, s.mailboxRunner, []string{
		"release", "--delivery", input.DeliveryID, "--lease-token", input.LeaseToken,
	}, runOptions{})
	if err != nil {
		return nil, nil, err
	}
	return nil, map[string]any{"status": "released", "delivery_id": input.DeliveryID}, nil
}

func (s *Service) mailboxDefer(ctx context.Context, _ *mcp.CallToolRequest, input mailboxDeferInput) (*mcp.CallToolResult, map[string]any, error) {
	_, err := runCommand(ctx, s.mailboxRunner, []string{
		"defer", "--delivery", input.DeliveryID, "--lease-token", input.LeaseToken, "--until", input.Until,
	}, runOptions{})
	if err != nil {
		return nil, nil, err
	}
	return nil, map[string]any{"status": "deferred", "delivery_id": input.DeliveryID, "until": input.Until}, nil
}

func (s *Service) mailboxFail(ctx context.Context, _ *mcp.CallToolRequest, input mailboxFailInput) (*mcp.CallToolResult, map[string]any, error) {
	_, err := runCommand(ctx, s.mailboxRunner, []string{
		"fail", "--delivery", input.DeliveryID, "--lease-token", input.LeaseToken, "--reason", input.Reason,
	}, runOptions{})
	if err != nil {
		return nil, nil, err
	}
	return nil, map[string]any{"status": "failed", "delivery_id": input.DeliveryID, "reason": input.Reason}, nil
}

func (s *Service) agentDeckResolveSession(ctx context.Context, _ *mcp.CallToolRequest, input agentDeckResolveSessionInput) (*mcp.CallToolResult, map[string]any, error) {
	data, err := s.resolveSessionShow(ctx, input.Session, syncCmdTimeout)
	if err != nil {
		return nil, nil, err
	}
	if data == nil {
		return nil, map[string]any{
			"status":      "not_found",
			"session_ref": input.Session,
		}, nil
	}
	out := sessionInfoMap(data, input.Session)
	out["status"] = "found"
	return nil, out, nil
}

func (s *Service) agentDeckEnsureSession(ctx context.Context, _ *mcp.CallToolRequest, input agentDeckEnsureSessionInput) (*mcp.CallToolResult, map[string]any, error) {
	bound, err := s.getBoundState(ctx)
	if err != nil {
		return nil, nil, err
	}

	identifier := firstNonEmpty(input.SessionID, input.SessionRef)
	workdir := firstNonEmpty(input.Workdir, bound.DefaultWorkdir)
	var data *sessionData
	if identifier != "" {
		data, err = s.resolveSessionShow(ctx, identifier, ensureSessionShowTimeout)
		if err != nil {
			return nil, nil, err
		}
	}

	createdTarget := false
	startedSession := false
	notifyNeeded := false
	listenerStatus := "not_needed"

	if data == nil {
		if input.EnsureTitle == "" {
			return nil, nil, errors.New("target session missing: provide session_id, session_ref, or ensure_title")
		}
		if input.EnsureCmd == "" {
			return nil, nil, errors.New("ensure_cmd is required when creating a target session")
		}
		if input.ParentSessionID == "" {
			return nil, nil, errors.New("parent_session_id is required when creating a target session")
		}
		if workdir == "" {
			return nil, nil, errors.New("workdir is required when creating a target session")
		}
		info, statErr := os.Stat(workdir)
		if statErr != nil || !info.IsDir() {
			return nil, nil, fmt.Errorf("workdir does not exist: %s", workdir)
		}

		targetLabel := firstNonEmpty(input.EnsureTitle, input.SessionRef, identifier)
		listenerMessage := ensureReceiverWorkflowHint(firstNonEmpty(input.ListenerMessage, defaultListenerMessage), defaultListenerMessage, targetLabel)
		launchResult, err := runCommand(ctx, s.commandRunner, []string{
			"agent-deck", "launch", "--json",
			"--title", input.EnsureTitle,
			"--parent", input.ParentSessionID,
			"--cmd", input.EnsureCmd,
			"--message", listenerMessage,
			workdir,
		}, runOptions{})
		if err != nil {
			return nil, nil, err
		}
		data, err = parseSessionData(launchResult.Stdout, "agent-deck launch")
		if err != nil {
			return nil, nil, err
		}
		createdTarget = true
		startedSession = true
		listenerStatus = "started_waiting"
	} else {
		targetLabel := firstNonEmpty(data.Title, input.SessionRef, identifier, data.ID)
		listenerMessage := ensureReceiverWorkflowHint(firstNonEmpty(input.ListenerMessage, defaultListenerMessage), defaultListenerMessage, targetLabel)
		if activeSessionStatuses[strings.TrimSpace(data.Status)] {
			notifyNeeded = true
			listenerStatus = "not_needed_existing_session"
		} else {
			startArgs := []string{"agent-deck", "session", "start", "--json"}
			if listenerMessage != "" {
				startArgs = append(startArgs, "-m", listenerMessage)
			}
			startArgs = append(startArgs, data.ID)
			if _, err := runCommand(ctx, s.commandRunner, startArgs, runOptions{}); err != nil {
				return nil, nil, err
			}
			refreshed, err := s.resolveSessionShow(ctx, data.ID, ensureSessionShowTimeout)
			if err != nil {
				return nil, nil, err
			}
			if refreshed != nil {
				data = refreshed
			}
			startedSession = true
			if listenerMessage != "" {
				listenerStatus = "started_waiting"
			} else {
				listenerStatus = "started"
			}
		}
	}

	out := sessionInfoMap(data, firstNonEmpty(input.SessionRef, input.EnsureTitle, identifier))
	out["status"] = "ready"
	out["created_target"] = createdTarget
	out["started_session"] = startedSession
	out["notify_needed"] = notifyNeeded
	out["listener_status"] = listenerStatus
	return nil, out, nil
}

func (s *Service) getBoundState(ctx context.Context) (boundState, error) {
	if err := s.tryAutoBindCurrentSession(ctx); err != nil {
		return boundState{}, err
	}
	snapshot := s.snapshotState()

	warnings := make([]string, 0, 3)
	if snapshot.DetectedAgentDeckSession == "" {
		warnings = append(warnings, "unable to determine current agent-deck session id")
	}
	if snapshot.DetectedAgentSession == "" {
		warnings = append(warnings, "unable to determine current AI agent session id")
	}
	if len(snapshot.BoundAddresses) == 0 {
		warnings = append(warnings, "no mailbox addresses are currently bound")
	}

	return boundState{
		BoundAddresses:           snapshot.BoundAddresses,
		DefaultSender:            snapshot.DefaultSender,
		DefaultWorkdir:           snapshot.DefaultWorkdir,
		DetectedAgentDeckSession: snapshot.DetectedAgentDeckSession,
		DetectedAgentSession:     snapshot.DetectedAgentSession,
		Warnings:                 warnings,
	}, nil
}

func (s *Service) snapshotState() stateSnapshot {
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	return stateSnapshot{
		BoundAddresses:           append([]string(nil), s.state.boundAddresses...),
		DefaultSender:            s.state.defaultSender,
		DefaultWorkdir:           s.state.defaultWorkdir,
		AutoBindAttempted:        s.state.autoBindAttempted,
		DetectedAgentDeckSession: s.state.detectedAgentDeckSession,
		DetectedAgentSession:     s.state.detectedAgentSession,
	}
}

func (s *Service) tryAutoBindCurrentSession(ctx context.Context) error {
	snapshot := s.snapshotState()
	if len(snapshot.BoundAddresses) > 0 || snapshot.AutoBindAttempted {
		return nil
	}

	envAgentDeckID := strings.TrimSpace(os.Getenv("AGENTDECK_INSTANCE_ID"))
	agentDeckSessionID := envAgentDeckID
	probeCompleted := envAgentDeckID != ""

	if agentDeckSessionID == "" {
		result, err := runProbe(ctx, s.commandRunner, []string{"agent-deck", "session", "current", "--json"}, runOptions{timeout: syncCmdTimeout}, false)
		if err != nil {
			return err
		}
		if result != nil {
			probeCompleted = true
			if result.ExitCode == 0 {
				var current struct {
					ID string `json:"id"`
				}
				if err := json.Unmarshal([]byte(result.Stdout), &current); err != nil {
					return fmt.Errorf("agent-deck session current returned invalid JSON: %w", err)
				}
				agentDeckSessionID = strings.TrimSpace(current.ID)
			}
		}
	}

	addresses := make([]string, 0, 2)
	detectedAgentDeckSession := ""
	defaultWorkdir := snapshot.DefaultWorkdir
	if agentDeckSessionID != "" {
		detectedAgentDeckSession = agentDeckSessionID
		addresses = append(addresses, agentDeckAddress(agentDeckSessionID))
		data, err := s.resolveSessionShowBestEffort(ctx, agentDeckSessionID)
		if err != nil {
			return err
		}
		if data != nil && strings.TrimSpace(data.Path) != "" {
			defaultWorkdir = strings.TrimSpace(data.Path)
		}
	}

	codexSessionID, err := s.detectCurrentCodexSessionID(ctx)
	if err != nil {
		return err
	}
	detectedAgentSession := ""
	if codexSessionID != "" {
		detectedAgentSession = codexSessionID
		addresses = append(addresses, codexAddress(codexSessionID))
	}

	if !probeCompleted && codexSessionID == "" && len(addresses) == 0 {
		return nil
	}

	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	if len(s.state.boundAddresses) > 0 {
		return nil
	}
	s.state.boundAddresses = dedupe(addresses)
	s.state.detectedAgentDeckSession = detectedAgentDeckSession
	s.state.detectedAgentSession = detectedAgentSession
	s.state.defaultWorkdir = defaultWorkdir
	switch {
	case detectedAgentDeckSession != "":
		s.state.defaultSender = agentDeckAddress(detectedAgentDeckSession)
	case detectedAgentSession != "":
		s.state.defaultSender = codexAddress(detectedAgentSession)
	}
	s.state.autoBindAttempted = true
	return nil
}

func (s *Service) detectCurrentCodexSessionID(ctx context.Context) (string, error) {
	if sessionID := strings.TrimSpace(os.Getenv("CODEX_SESSION_ID")); sessionID != "" {
		return sessionID, nil
	}

	seen := map[int]bool{}
	pid := os.Getppid()
	for pid > 1 && !seen[pid] {
		seen[pid] = true
		row, err := s.getProcessRow(ctx, pid)
		if err != nil {
			return "", err
		}
		if row == nil {
			break
		}
		looksLikeCodex := row.Comm == "codex" || codexCommandPattern.MatchString(row.Args) || strings.Contains(row.Args, "@openai/codex")
		if looksLikeCodex {
			if fromArgs := extractCodexSessionIDFromArgs(row.Args); fromArgs != "" {
				return fromArgs, nil
			}
			if fromLsof, err := s.extractCodexSessionIDFromLsof(ctx, row.PID); err != nil {
				return "", err
			} else if fromLsof != "" {
				return fromLsof, nil
			}
			return "", nil
		}
		pid = row.PPID
	}
	return "", nil
}

func (s *Service) getProcessRow(ctx context.Context, pid int) (*psRow, error) {
	if pid <= 1 {
		return nil, nil
	}
	result, err := runProbe(ctx, s.commandRunner, []string{"ps", "-p", strconv.Itoa(pid), "-o", "pid=,ppid=,comm=,args="}, runOptions{timeout: syncCmdTimeout}, false)
	if err != nil {
		return nil, err
	}
	if result == nil || result.ExitCode != 0 {
		return nil, nil
	}
	return parsePSRow(result.Stdout), nil
}

func (s *Service) extractCodexSessionIDFromLsof(ctx context.Context, pid int) (string, error) {
	if pid <= 1 {
		return "", nil
	}
	result, err := runProbe(ctx, s.commandRunner, []string{"lsof", "-p", strconv.Itoa(pid)}, runOptions{timeout: syncCmdTimeout}, false)
	if err != nil {
		return "", err
	}
	if result == nil || result.ExitCode != 0 {
		return "", nil
	}
	for _, line := range strings.Split(result.Stdout, "\n") {
		match := codexSessionFilePattern.FindStringSubmatch(line)
		if len(match) == 2 {
			return match[1], nil
		}
	}
	return "", nil
}

func (s *Service) mailboxAddresses(ctx context.Context, addresses []string) ([]string, error) {
	if len(addresses) > 0 {
		return dedupe(addresses), nil
	}
	bound, err := s.getBoundState(ctx)
	if err != nil {
		return nil, err
	}
	if len(bound.BoundAddresses) == 0 {
		return nil, errors.New("no mailbox addresses provided and no mailbox addresses are bound")
	}
	return append([]string(nil), bound.BoundAddresses...), nil
}

func (s *Service) senderAddress(ctx context.Context, override string) (string, error) {
	if strings.TrimSpace(override) != "" {
		return strings.TrimSpace(override), nil
	}
	bound, err := s.getBoundState(ctx)
	if err != nil {
		return "", err
	}
	switch {
	case bound.DefaultSender != "":
		return bound.DefaultSender, nil
	case len(bound.BoundAddresses) > 0:
		return bound.BoundAddresses[0], nil
	default:
		return "", errors.New("mailbox_send requires from_address or a bound default_sender")
	}
}

func (s *Service) isLocalAddress(ctx context.Context, address string) bool {
	bound, err := s.getBoundState(ctx)
	if err != nil {
		return false
	}
	for _, candidate := range bound.BoundAddresses {
		if candidate == address {
			return true
		}
	}
	return false
}

func (s *Service) resolveSessionShow(ctx context.Context, identifier string, timeout time.Duration) (*sessionData, error) {
	result, err := runProbe(ctx, s.commandRunner, []string{"agent-deck", "session", "show", identifier, "--json"}, runOptions{timeout: timeout}, true)
	if err != nil {
		return nil, err
	}
	if result == nil || result.ExitCode != 0 {
		return nil, nil
	}
	data, err := parseSessionData(result.Stdout, "agent-deck session show")
	if err != nil {
		return nil, err
	}
	if data.Success != nil && !*data.Success {
		return nil, nil
	}
	return data, nil
}

func (s *Service) resolveSessionShowBestEffort(ctx context.Context, identifier string) (*sessionData, error) {
	result, err := runProbe(ctx, s.commandRunner, []string{"agent-deck", "session", "show", identifier, "--json"}, runOptions{}, false)
	if err != nil {
		return nil, err
	}
	if result == nil || result.ExitCode != 0 {
		return nil, nil
	}
	data, err := parseSessionData(result.Stdout, "agent-deck session show")
	if err != nil {
		return nil, err
	}
	if data.Success != nil && !*data.Success {
		return nil, nil
	}
	return data, nil
}

func parseSessionData(text, context string) (*sessionData, error) {
	var data sessionData
	if err := json.Unmarshal([]byte(text), &data); err != nil {
		return nil, fmt.Errorf("%s returned invalid JSON: %w", context, err)
	}
	return &data, nil
}

func parseJSONFromRunner(ctx context.Context, runner Runner, args []string, contextLabel string) (any, error) {
	result, err := runCommand(ctx, runner, args, runOptions{})
	if err != nil {
		return nil, err
	}
	return parseJSONValue(result.Stdout, contextLabel)
}

func parseJSONValue(text, contextLabel string) (any, error) {
	var value any
	if err := json.Unmarshal([]byte(text), &value); err != nil {
		return nil, fmt.Errorf("%s returned invalid JSON: %w", contextLabel, err)
	}
	return value, nil
}

func runCommand(ctx context.Context, runner Runner, args []string, opts runOptions) (RunResult, error) {
	runCtx := ctx
	var cancel context.CancelFunc
	if opts.timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, opts.timeout)
		defer cancel()
	}

	result, err := runner.Run(runCtx, args, opts.input)
	if err != nil {
		detail := err.Error()
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			detail = fmt.Sprintf("timed out after %dms", opts.timeout.Milliseconds())
		}
		return RunResult{}, fmt.Errorf("command failed: %s :: %s", strings.Join(args, " "), detail)
	}

	okCodes := opts.okCodes
	if len(okCodes) == 0 {
		okCodes = []int{0}
	}
	if containsInt(okCodes, result.ExitCode) {
		return result, nil
	}

	detail := strings.TrimSpace(result.Stderr)
	if detail == "" {
		detail = strings.TrimSpace(result.Stdout)
	}
	if detail == "" {
		detail = fmt.Sprintf("exit code %d", result.ExitCode)
	}
	return RunResult{}, fmt.Errorf("command failed: %s :: %s", strings.Join(args, " "), detail)
}

func runProbe(ctx context.Context, runner Runner, args []string, opts runOptions, failOnError bool) (*RunResult, error) {
	runCtx := ctx
	var cancel context.CancelFunc
	if opts.timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, opts.timeout)
		defer cancel()
	}

	result, err := runner.Run(runCtx, args, opts.input)
	if err != nil {
		if !failOnError {
			return nil, nil
		}
		detail := err.Error()
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			detail = fmt.Sprintf("timed out after %dms", opts.timeout.Milliseconds())
		}
		return nil, fmt.Errorf("command failed: %s :: %s", strings.Join(args, " "), detail)
	}
	return &result, nil
}

func dedupe(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" || seen[trimmed] {
			continue
		}
		seen[trimmed] = true
		out = append(out, trimmed)
	}
	return out
}

func parseAddress(address string) (parsedAddress, error) {
	scheme, id, ok := strings.Cut(address, "/")
	if !ok || scheme == "" || id == "" {
		return parsedAddress{}, fmt.Errorf("invalid address: %s", address)
	}
	return parsedAddress{Scheme: scheme, ID: id}, nil
}

func notificationRouteForAddress(address string) (notificationRoute, error) {
	parsed, err := parseAddress(address)
	if err != nil {
		return notificationRoute{}, err
	}
	return notificationRoute{
		Manager: parsed.Scheme,
		Target:  parsed.ID,
	}, nil
}

func agentDeckAddress(sessionID string) string {
	return "agent-deck/" + sessionID
}

func codexAddress(sessionID string) string {
	return "codex/" + sessionID
}

func parsePSRow(text string) *psRow {
	fields := strings.Fields(strings.TrimSpace(text))
	if len(fields) < 3 {
		return nil
	}
	pid, err := strconv.Atoi(fields[0])
	if err != nil {
		return nil
	}
	ppid, err := strconv.Atoi(fields[1])
	if err != nil {
		return nil
	}
	args := ""
	if len(fields) > 3 {
		args = strings.Join(fields[3:], " ")
	}
	return &psRow{
		PID:  pid,
		PPID: ppid,
		Comm: fields[2],
		Args: args,
	}
}

func extractCodexSessionIDFromArgs(args string) string {
	match := codexResumePattern.FindStringSubmatch(args)
	if len(match) != 2 {
		return ""
	}
	return match[1]
}

type sendReceipt struct {
	DeliveryID string
}

func parseSendTokens(output string) sendReceipt {
	var receipt sendReceipt
	for _, token := range strings.Fields(output) {
		if strings.HasPrefix(token, "delivery_id=") {
			receipt.DeliveryID = strings.TrimPrefix(token, "delivery_id=")
		}
	}
	return receipt
}

func validateSendReceipt(ids sendReceipt, output string) (sendReceipt, error) {
	if ids.DeliveryID != "" {
		return ids, nil
	}
	detail := strings.TrimSpace(output)
	if detail == "" {
		detail = "<empty stdout>"
	}
	return sendReceipt{}, fmt.Errorf("agent-mailbox send returned incomplete receipt: missing delivery_id :: %s", detail)
}

func ensureCheckAgentMailHint(message, defaultMessage string) string {
	trimmed := strings.TrimSpace(message)
	if trimmed == "" {
		return defaultMessage
	}
	if strings.Contains(trimmed, "check-agent-mail") {
		return trimmed
	}
	return trimmed + "\nWhen a wakeup message arrives, use the 'check-agent-mail' skill and execute its requested action."
}

func ensureMailboxRecoveryHint(message string) string {
	trimmed := strings.TrimSpace(message)
	if trimmed == "" {
		return mailboxRecoveryHint
	}
	if strings.Contains(trimmed, "mailbox_read") && strings.Contains(trimmed, "acked") {
		return trimmed
	}
	return trimmed + "\n" + mailboxRecoveryHint
}

func ensureReceiverWorkflowHint(message, defaultMessage, targetLabel string) string {
	withWakeHint := ensureCheckAgentMailHint(message, defaultMessage)
	if !isWorkerSessionLabel(targetLabel) {
		return withWakeHint
	}
	return ensureMailboxRecoveryHint(withWakeHint)
}

func isWorkerSessionLabel(label string) bool {
	trimmed := strings.ToLower(strings.TrimSpace(label))
	return trimmed == "coder" || strings.HasPrefix(trimmed, "coder-") ||
		trimmed == "reviewer" || strings.HasPrefix(trimmed, "reviewer-") ||
		trimmed == "architect" || strings.HasPrefix(trimmed, "architect-")
}

func boundStateMap(bound boundState) map[string]any {
	return map[string]any{
		"bound_addresses":                bound.BoundAddresses,
		"default_sender":                 nilIfEmpty(bound.DefaultSender),
		"default_workdir":                nilIfEmpty(bound.DefaultWorkdir),
		"detected_agent_deck_session_id": nilIfEmpty(bound.DetectedAgentDeckSession),
		"detected_agent_session_id":      nilIfEmpty(bound.DetectedAgentSession),
		"warnings":                       bound.Warnings,
	}
}

func sessionInfoMap(data *sessionData, sessionRef string) map[string]any {
	return map[string]any{
		"session_id":     data.ID,
		"session_ref":    firstNonEmpty(sessionRef, data.Title, data.ID),
		"title":          nilIfEmpty(data.Title),
		"session_status": nilIfEmpty(data.Status),
		"addresses":      []string{agentDeckAddress(data.ID)},
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func nilIfEmpty(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func orUnset(value string) string {
	if strings.TrimSpace(value) == "" {
		return unsetValue
	}
	return value
}

func containsInt(values []int, want int) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
