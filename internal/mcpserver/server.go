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
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/ruiheng/agent-mailbox/internal/mailbox"
)

const (
	serverName                  = "agent_mailbox"
	serverVersion               = "0.4.0"
	syncCmdTimeout              = 30 * time.Second
	ensureSessionShowTimeout    = 30 * time.Second
	defaultReminderPollInterval = 30 * time.Second
	defaultReminderConfirmDelay = 2 * time.Second
	notificationDelivery        = "delivery_available"
	notificationStaleUnread     = "stale_unread"
	defaultListenerMessage      = "If agent_mailbox is not bound yet, first run agent-deck session current --json and call mailbox_bind for this session. When a wakeup message arrives, use the 'check-agent-mail' skill and execute its requested action."
	defaultNotifyMessage        = "Use the 'check-agent-mail' skill now. Receive the pending message and execute its requested action."
	mailboxRecoveryHint         = "If you forget the mailbox details or next action after ack, use `mailbox_read` on the latest `acked` delivery for this session. For older mail, use `mailbox_list` with `state: acked` and then `mailbox_read` by delivery id."
	serverInstructions          = "Bootstrap this MCP process once per agent-managed session. If it is not bound yet, run `agent-deck session current --json`, take the current session id, and call `mailbox_bind`. Use `agent-deck/<id>` as the default sender. Pass `default_workdir` when you want later `agent_deck_ensure_session` calls to create sessions in the current project. `mailbox_wait` is not recommended for normal workflow; prefer `mailbox_recv`. Later reuse the bound addresses until MCP state is lost."
	unsetValue                  = "<unset>"
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
	MailboxRunner             Runner
	CommandRunner             Runner
	StateDir                  string
	Now                       func() time.Time
	ReminderPollInterval      time.Duration
	ReminderConfirmDelay      time.Duration
	DisableActiveReminderLoop bool
}

type Service struct {
	mailboxRunner             Runner
	commandRunner             Runner
	notifiers                 map[string]managerNotifier
	state                     *serverState
	now                       func() time.Time
	reminderPollInterval      time.Duration
	reminderConfirmDelay      time.Duration
	disableActiveReminderLoop bool
	activeReminderLoopOnce    sync.Once
}

type serverState struct {
	mu                       sync.Mutex
	boundAddresses           []string
	defaultSender            string
	defaultWorkdir           string
	autoBindAttempted        bool
	detectedAgentDeckSession string
	detectedAgentSession     string
	reminderSubscriptions    map[string]reminderSubscription
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

type reminderSelector struct {
	Addresses  []string            `json:"addresses,omitempty"`
	GroupViews []reminderGroupView `json:"group_views,omitempty"`
}

type reminderGroupViewInput struct {
	GroupAddress string `json:"group_address"`
	AsPerson     string `json:"as_person"`
}

type reminderGroupView struct {
	GroupAddress string `json:"group_address"`
	AsPerson     string `json:"as_person"`
}

type reminderRouteConfig struct {
	Address string `json:"address"`
	Manager string `json:"manager"`
	Target  string `json:"target"`
}

type reminderPolicy struct {
	OlderThan    time.Duration `json:"-"`
	OlderThanRaw string        `json:"older_than"`
	ActivePush   bool          `json:"active_push,omitempty"`
}

type reminderRuntime struct {
	LastCheckedAt      string `json:"last_checked_at,omitempty"`
	LastMatchedAt      string `json:"last_matched_at,omitempty"`
	LastStaleCount     int    `json:"last_stale_count"`
	LastClaimableCount int    `json:"last_claimable_count"`
	LastOldestEligible string `json:"last_oldest_eligible_at,omitempty"`
	PendingSince       string `json:"pending_since,omitempty"`
	LastSuppressedAt   string `json:"last_suppressed_at,omitempty"`
	LastNotifiedAt     string `json:"last_notified_at,omitempty"`
}

type reminderSubscription struct {
	Key      string              `json:"subscription_key"`
	Selector reminderSelector    `json:"selector"`
	Route    reminderRouteConfig `json:"route"`
	Policy   reminderPolicy      `json:"policy"`
	Runtime  reminderRuntime     `json:"runtime"`
}

type staleAddress struct {
	Address          string `json:"address"`
	Person           string `json:"person,omitempty"`
	OldestEligibleAt string `json:"oldest_eligible_at"`
	ClaimableCount   int    `json:"claimable_count"`
}

type reminderCurrentState struct {
	Stale             bool   `json:"stale"`
	StaleAddressCount int    `json:"stale_address_count"`
	ClaimableCount    int    `json:"claimable_count"`
	OldestEligibleAt  string `json:"oldest_eligible_at,omitempty"`
}

type reminderStatusEntry struct {
	Subscription reminderSubscription `json:"subscription"`
	Current      reminderCurrentState `json:"current"`
}

type passiveReminderSubscription struct {
	SubscriptionKey    string              `json:"subscription_key"`
	SelectorAddresses  []string            `json:"selector_addresses,omitempty"`
	SelectorGroupViews []reminderGroupView `json:"selector_group_views,omitempty"`
	Route              string              `json:"route"`
	OlderThan          string              `json:"older_than"`
	StaleAddressCount  int                 `json:"stale_address_count"`
	ClaimableCount     int                 `json:"claimable_count"`
	OldestEligibleAt   string              `json:"oldest_eligible_at,omitempty"`
}

type passiveReminderPayload struct {
	ConfiguredCount int                           `json:"configured_count"`
	StaleCount      int                           `json:"stale_count"`
	Subscriptions   []passiveReminderSubscription `json:"subscriptions,omitempty"`
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

type notificationProbe struct {
	Status   string
	Scheme   string
	Wakeable bool
}

type managerNotifier interface {
	Name() string
	Probe(ctx context.Context, route notificationRoute) notificationProbe
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
		state: &serverState{
			reminderSubscriptions: map[string]reminderSubscription{},
		},
		now:                       opts.Now,
		reminderPollInterval:      opts.ReminderPollInterval,
		reminderConfirmDelay:      opts.ReminderConfirmDelay,
		disableActiveReminderLoop: opts.DisableActiveReminderLoop,
	}
	if service.now == nil {
		service.now = func() time.Time {
			return time.Now().UTC()
		}
	}
	if service.reminderPollInterval <= 0 {
		service.reminderPollInterval = defaultReminderPollInterval
	}
	if service.reminderConfirmDelay <= 0 {
		service.reminderConfirmDelay = defaultReminderConfirmDelay
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
	return s.mailboxToolResult(ctx, map[string]any{
		"bound_addresses": bound.BoundAddresses,
		"default_sender":  orUnset(bound.DefaultSender),
		"default_workdir": orUnset(bound.DefaultWorkdir),
	})
}

func (s *Service) mailboxReminderSubscribe(ctx context.Context, _ *mcp.CallToolRequest, input mailboxReminderSubscribeInput) (*mcp.CallToolResult, map[string]any, error) {
	subscription, err := buildReminderSubscription(input.Addresses, input.GroupViews, input.Route, input.OlderThan, input.ActivePush)
	if err != nil {
		return nil, nil, err
	}

	staleEntries, err := s.listStaleEntries(ctx, subscription.Selector, subscription.Policy)
	if err != nil {
		return nil, nil, err
	}
	subscription.Runtime = nextReminderRuntime(reminderRuntime{}, s.now().UTC(), staleEntries)

	s.state.mu.Lock()
	_, existed := s.state.reminderSubscriptions[subscription.Key]
	s.state.reminderSubscriptions[subscription.Key] = subscription
	s.state.mu.Unlock()
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
	subscriptionKey, err := reminderSubscriptionKey(input.Addresses, input.GroupViews, input.Route)
	if err != nil {
		return nil, nil, err
	}

	s.state.mu.Lock()
	_, existed := s.state.reminderSubscriptions[subscriptionKey]
	delete(s.state.reminderSubscriptions, subscriptionKey)
	s.state.mu.Unlock()

	return nil, map[string]any{
		"status":           "unsubscribed",
		"removed":          existed,
		"subscription_key": subscriptionKey,
	}, nil
}

func (s *Service) mailboxReminderStatus(ctx context.Context, _ *mcp.CallToolRequest, _ mailboxReminderStatusInput) (*mcp.CallToolResult, map[string]any, error) {
	subscriptions := s.reminderSubscriptionsSnapshot()
	entries := make([]reminderStatusEntry, 0, len(subscriptions))
	now := s.now().UTC()
	updatedRuntime := make(map[string]reminderRuntime, len(subscriptions))

	for _, subscription := range subscriptions {
		staleEntries, err := s.listStaleEntries(ctx, subscription.Selector, subscription.Policy)
		if err != nil {
			return nil, nil, err
		}
		subscription.Runtime = nextReminderRuntime(subscription.Runtime, now, staleEntries)
		updatedRuntime[subscription.Key] = subscription.Runtime
		entries = append(entries, reminderStatusEntry{
			Subscription: subscription,
			Current:      reminderCurrentSummary(staleEntries),
		})
	}

	s.updateReminderRuntime(updatedRuntime)
	return nil, map[string]any{
		"status":        "listed",
		"subscriptions": entries,
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

	return s.mailboxToolResult(ctx, map[string]any{
		"status":        "sent",
		"from_address":  fromAddress,
		"to_address":    input.ToAddress,
		"subject":       input.Subject,
		"delivery_id":   sendIDs.DeliveryID,
		"notify_status": notify.Status,
		"notify_scheme": notifyScheme,
		"notify_error":  notifyError,
	})
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

func (s *Service) probeRoute(ctx context.Context, route notificationRoute) notificationProbe {
	notifier, ok := s.notifiers[route.Manager]
	if !ok {
		return notificationProbe{
			Status: "unsupported",
			Scheme: route.Manager,
		}
	}
	probe := notifier.Probe(ctx, route)
	if probe.Scheme == "" {
		probe.Scheme = notifier.Name()
	}
	return probe
}

func (n agentDeckNotifier) Name() string {
	return "agent-deck"
}

func (n agentDeckNotifier) Probe(ctx context.Context, route notificationRoute) notificationProbe {
	targetSession, err := n.service.resolveSessionShowBestEffort(ctx, route.Target)
	if err != nil {
		return notificationProbe{
			Status: "failed",
			Scheme: n.Name(),
		}
	}
	if targetSession == nil {
		return notificationProbe{
			Status: "not_found",
			Scheme: n.Name(),
		}
	}

	status := strings.TrimSpace(targetSession.Status)
	switch status {
	case "waiting", "idle":
		return notificationProbe{
			Status:   "wakeable",
			Scheme:   n.Name(),
			Wakeable: true,
		}
	default:
		return notificationProbe{
			Status: "suppressed_local_activity",
			Scheme: n.Name(),
		}
	}
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

func (s *Service) startActiveReminderLoop() {
	if s.disableActiveReminderLoop {
		return
	}
	s.activeReminderLoopOnce.Do(func() {
		go s.runActiveReminderLoop()
	})
}

func (s *Service) runActiveReminderLoop() {
	ticker := time.NewTicker(s.reminderPollInterval)
	defer ticker.Stop()
	for range ticker.C {
		_ = s.processActiveReminderSubscriptions(context.Background())
	}
}

func (s *Service) processActiveReminderSubscriptions(ctx context.Context) error {
	subscriptions := s.reminderSubscriptionsSnapshot()
	if len(subscriptions) == 0 {
		return nil
	}

	now := s.now().UTC()
	updatedRuntime := make(map[string]reminderRuntime, len(subscriptions))
	for _, subscription := range subscriptions {
		if !subscription.Policy.ActivePush {
			continue
		}
		staleEntries, err := s.listStaleEntries(ctx, subscription.Selector, subscription.Policy)
		if err != nil {
			continue
		}
		updatedRuntime[subscription.Key] = s.processActiveReminderSubscription(ctx, subscription, now, staleEntries)
	}
	s.updateReminderRuntime(updatedRuntime)
	return nil
}

func (s *Service) processActiveReminderSubscription(ctx context.Context, subscription reminderSubscription, now time.Time, staleEntries []staleAddress) reminderRuntime {
	runtime := nextReminderRuntime(subscription.Runtime, now, staleEntries)
	if len(staleEntries) == 0 {
		runtime.PendingSince = ""
		return runtime
	}

	route := notificationRoute{
		Manager: subscription.Route.Manager,
		Target:  subscription.Route.Target,
	}
	probe := s.probeRoute(ctx, route)
	if !probe.Wakeable {
		runtime.PendingSince = ""
		if probe.Status == "suppressed_local_activity" {
			runtime.LastSuppressedAt = now.Format(time.RFC3339)
		}
		return runtime
	}

	if inReminderCooldown(runtime.LastNotifiedAt, now, subscription.Policy.cooldown()) {
		return runtime
	}

	if runtime.PendingSince == "" {
		runtime.PendingSince = now.Format(time.RFC3339)
		return runtime
	}

	pendingSince, err := time.Parse(time.RFC3339, runtime.PendingSince)
	if err != nil {
		runtime.PendingSince = now.Format(time.RFC3339)
		return runtime
	}
	if now.Sub(pendingSince) < s.reminderConfirmDelay {
		return runtime
	}

	outcome := s.notifyRoute(ctx, notificationEvent{
		Kind:  notificationStaleUnread,
		Route: route,
	})
	if notificationOutcomeDelivered(outcome) {
		runtime.PendingSince = ""
		runtime.LastNotifiedAt = now.Format(time.RFC3339)
	}
	return runtime
}

func notificationOutcomeDelivered(outcome notificationOutcome) bool {
	return strings.TrimSpace(outcome.Status) == "sent"
}

func inReminderCooldown(lastNotifiedAt string, now time.Time, cooldown time.Duration) bool {
	if cooldown <= 0 || strings.TrimSpace(lastNotifiedAt) == "" {
		return false
	}
	notifiedAt, err := time.Parse(time.RFC3339, lastNotifiedAt)
	if err != nil {
		return false
	}
	return now.Sub(notifiedAt) < cooldown
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
		return s.mailboxToolResult(ctx, map[string]any{
			"status":    "no_message",
			"addresses": addresses,
		})
	}

	delivery, err := parseJSONValue(result.Stdout, "agent-mailbox wait")
	if err != nil {
		return nil, nil, err
	}
	return s.mailboxToolResult(ctx, map[string]any{
		"status":    "message_available",
		"addresses": addresses,
		"delivery":  delivery,
	})
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
		return s.mailboxToolResult(ctx, map[string]any{
			"status":    "no_message",
			"addresses": addresses,
		})
	}

	delivery, err := parseJSONValue(result.Stdout, "agent-mailbox recv")
	if err != nil {
		return nil, nil, err
	}
	return s.mailboxToolResult(ctx, map[string]any{
		"status":    "received",
		"addresses": addresses,
		"delivery":  delivery,
	})
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
	return s.mailboxToolResult(ctx, result)
}

func (s *Service) mailboxAck(ctx context.Context, _ *mcp.CallToolRequest, input mailboxAckInput) (*mcp.CallToolResult, map[string]any, error) {
	_, err := runCommand(ctx, s.mailboxRunner, []string{
		"ack", "--delivery", input.DeliveryID, "--lease-token", input.LeaseToken,
	}, runOptions{})
	if err != nil {
		return nil, nil, err
	}
	return s.mailboxToolResult(ctx, map[string]any{"status": "acked", "delivery_id": input.DeliveryID})
}

func (s *Service) mailboxRelease(ctx context.Context, _ *mcp.CallToolRequest, input mailboxAckInput) (*mcp.CallToolResult, map[string]any, error) {
	_, err := runCommand(ctx, s.mailboxRunner, []string{
		"release", "--delivery", input.DeliveryID, "--lease-token", input.LeaseToken,
	}, runOptions{})
	if err != nil {
		return nil, nil, err
	}
	return s.mailboxToolResult(ctx, map[string]any{"status": "released", "delivery_id": input.DeliveryID})
}

func (s *Service) mailboxDefer(ctx context.Context, _ *mcp.CallToolRequest, input mailboxDeferInput) (*mcp.CallToolResult, map[string]any, error) {
	_, err := runCommand(ctx, s.mailboxRunner, []string{
		"defer", "--delivery", input.DeliveryID, "--lease-token", input.LeaseToken, "--until", input.Until,
	}, runOptions{})
	if err != nil {
		return nil, nil, err
	}
	return s.mailboxToolResult(ctx, map[string]any{"status": "deferred", "delivery_id": input.DeliveryID, "until": input.Until})
}

func (s *Service) mailboxFail(ctx context.Context, _ *mcp.CallToolRequest, input mailboxFailInput) (*mcp.CallToolResult, map[string]any, error) {
	_, err := runCommand(ctx, s.mailboxRunner, []string{
		"fail", "--delivery", input.DeliveryID, "--lease-token", input.LeaseToken, "--reason", input.Reason,
	}, runOptions{})
	if err != nil {
		return nil, nil, err
	}
	return s.mailboxToolResult(ctx, map[string]any{"status": "failed", "delivery_id": input.DeliveryID, "reason": input.Reason})
}

func (s *Service) mailboxToolResult(ctx context.Context, result map[string]any) (*mcp.CallToolResult, map[string]any, error) {
	return nil, s.withPassiveReminderPayloadBestEffort(ctx, result), nil
}

func (s *Service) withPassiveReminderPayloadBestEffort(ctx context.Context, result map[string]any) map[string]any {
	payload, err := s.passiveReminderPayload(ctx)
	if err != nil {
		return result
	}
	if payload != nil {
		result["reminders"] = payload
	}
	return result
}

func (s *Service) passiveReminderPayload(ctx context.Context) (map[string]any, error) {
	subscriptions := s.reminderSubscriptionsSnapshot()
	if len(subscriptions) == 0 {
		return nil, nil
	}

	now := s.now().UTC()
	updatedRuntime := make(map[string]reminderRuntime, len(subscriptions))
	staleSubscriptions := make([]passiveReminderSubscription, 0, len(subscriptions))
	for _, subscription := range subscriptions {
		staleEntries, err := s.listStaleEntries(ctx, subscription.Selector, subscription.Policy)
		if err != nil {
			return nil, err
		}
		subscription.Runtime = nextReminderRuntime(subscription.Runtime, now, staleEntries)
		updatedRuntime[subscription.Key] = subscription.Runtime
		if len(staleEntries) == 0 {
			continue
		}
		summary := reminderCurrentSummary(staleEntries)
		staleSubscriptions = append(staleSubscriptions, passiveReminderSubscription{
			SubscriptionKey:    subscription.Key,
			SelectorAddresses:  append([]string(nil), subscription.Selector.Addresses...),
			SelectorGroupViews: append([]reminderGroupView(nil), subscription.Selector.GroupViews...),
			Route:              subscription.Route.Address,
			OlderThan:          subscription.Policy.OlderThanRaw,
			StaleAddressCount:  summary.StaleAddressCount,
			ClaimableCount:     summary.ClaimableCount,
			OldestEligibleAt:   summary.OldestEligibleAt,
		})
	}

	s.updateReminderRuntime(updatedRuntime)
	sort.Slice(staleSubscriptions, func(i, j int) bool {
		left := staleSubscriptions[i]
		right := staleSubscriptions[j]
		if left.OldestEligibleAt != right.OldestEligibleAt {
			if left.OldestEligibleAt == "" {
				return false
			}
			if right.OldestEligibleAt == "" {
				return true
			}
			return left.OldestEligibleAt < right.OldestEligibleAt
		}
		if left.Route != right.Route {
			return left.Route < right.Route
		}
		return left.SubscriptionKey < right.SubscriptionKey
	})

	payload := passiveReminderPayload{
		ConfiguredCount: len(subscriptions),
		StaleCount:      len(staleSubscriptions),
		Subscriptions:   staleSubscriptions,
	}
	encoded, err := structToMap(payload)
	if err != nil {
		return nil, err
	}
	return encoded, nil
}

func (s *Service) reminderSubscriptionsSnapshot() []reminderSubscription {
	s.state.mu.Lock()
	defer s.state.mu.Unlock()

	subscriptions := make([]reminderSubscription, 0, len(s.state.reminderSubscriptions))
	for _, subscription := range s.state.reminderSubscriptions {
		subscriptions = append(subscriptions, cloneReminderSubscription(subscription))
	}
	sort.Slice(subscriptions, func(i, j int) bool {
		return subscriptions[i].Key < subscriptions[j].Key
	})
	return subscriptions
}

func (s *Service) updateReminderRuntime(runtimeByKey map[string]reminderRuntime) {
	if len(runtimeByKey) == 0 {
		return
	}

	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	for key, runtime := range runtimeByKey {
		subscription, ok := s.state.reminderSubscriptions[key]
		if !ok {
			continue
		}
		subscription.Runtime = runtime
		s.state.reminderSubscriptions[key] = subscription
	}
}

func (s *Service) listStaleEntries(ctx context.Context, selector reminderSelector, policy reminderPolicy) ([]staleAddress, error) {
	staleEntries := make([]staleAddress, 0, len(selector.Addresses)+len(selector.GroupViews))
	if len(selector.Addresses) > 0 {
		personalEntries, err := s.listStaleAddresses(ctx, selector.Addresses, "", policy)
		if err != nil {
			return nil, err
		}
		staleEntries = append(staleEntries, personalEntries...)
	}
	for _, groupView := range selector.GroupViews {
		groupEntries, err := s.listStaleAddresses(ctx, []string{groupView.GroupAddress}, groupView.AsPerson, policy)
		if err != nil {
			return nil, err
		}
		staleEntries = append(staleEntries, groupEntries...)
	}
	sort.Slice(staleEntries, func(i, j int) bool {
		if staleEntries[i].OldestEligibleAt != staleEntries[j].OldestEligibleAt {
			return staleEntries[i].OldestEligibleAt < staleEntries[j].OldestEligibleAt
		}
		if staleEntries[i].Address != staleEntries[j].Address {
			return staleEntries[i].Address < staleEntries[j].Address
		}
		return staleEntries[i].Person < staleEntries[j].Person
	})
	return staleEntries, nil
}

func (s *Service) listStaleAddresses(ctx context.Context, addresses []string, person string, policy reminderPolicy) ([]staleAddress, error) {
	args := []string{"stale", "--older-than", policy.OlderThanRaw}
	for _, address := range addresses {
		args = append(args, "--for", address)
	}
	if strings.TrimSpace(person) != "" {
		args = append(args, "--as", strings.TrimSpace(person))
	}
	args = append(args, "--json")

	value, err := parseJSONFromRunner(ctx, s.mailboxRunner, args, "agent-mailbox stale")
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var staleEntries []staleAddress
	if err := json.Unmarshal(encoded, &staleEntries); err != nil {
		return nil, fmt.Errorf("agent-mailbox stale returned invalid JSON: %w", err)
	}
	return staleEntries, nil
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

func buildReminderSubscription(addresses []string, groupViews []reminderGroupViewInput, route, olderThan string, activePush bool) (reminderSubscription, error) {
	selector, err := normalizeReminderSelector(addresses, groupViews)
	if err != nil {
		return reminderSubscription{}, err
	}
	routeConfig, err := normalizeReminderRoute(route)
	if err != nil {
		return reminderSubscription{}, err
	}
	policy, err := normalizeReminderPolicy(olderThan, activePush)
	if err != nil {
		return reminderSubscription{}, err
	}
	return reminderSubscription{
		Key:      reminderKey(selector, routeConfig.Address),
		Selector: selector,
		Route:    routeConfig,
		Policy:   policy,
	}, nil
}

func reminderSubscriptionKey(addresses []string, groupViews []reminderGroupViewInput, route string) (string, error) {
	selector, err := normalizeReminderSelector(addresses, groupViews)
	if err != nil {
		return "", err
	}
	routeConfig, err := normalizeReminderRoute(route)
	if err != nil {
		return "", err
	}
	return reminderKey(selector, routeConfig.Address), nil
}

func normalizeReminderSelector(addresses []string, groupViews []reminderGroupViewInput) (reminderSelector, error) {
	normalizedAddresses := dedupe(addresses)
	if len(normalizedAddresses) > 0 {
		sort.Strings(normalizedAddresses)
	}
	normalizedGroupViews, err := normalizeReminderGroupViews(groupViews)
	if err != nil {
		return reminderSelector{}, err
	}
	if len(normalizedAddresses) == 0 && len(normalizedGroupViews) == 0 {
		return reminderSelector{}, errors.New("mailbox reminder requires at least one selector")
	}
	return reminderSelector{
		Addresses:  normalizedAddresses,
		GroupViews: normalizedGroupViews,
	}, nil
}

func normalizeReminderGroupViews(groupViews []reminderGroupViewInput) ([]reminderGroupView, error) {
	if len(groupViews) == 0 {
		return nil, nil
	}
	normalized := make([]reminderGroupView, 0, len(groupViews))
	seen := map[string]bool{}
	for _, groupView := range groupViews {
		groupAddress := strings.TrimSpace(groupView.GroupAddress)
		asPerson := strings.TrimSpace(groupView.AsPerson)
		if groupAddress == "" {
			return nil, errors.New("mailbox reminder group view requires group_address")
		}
		if asPerson == "" {
			return nil, errors.New("mailbox reminder group view requires as_person")
		}
		key := groupAddress + "\x00" + asPerson
		if seen[key] {
			continue
		}
		seen[key] = true
		normalized = append(normalized, reminderGroupView{
			GroupAddress: groupAddress,
			AsPerson:     asPerson,
		})
	}
	sort.Slice(normalized, func(i, j int) bool {
		if normalized[i].GroupAddress != normalized[j].GroupAddress {
			return normalized[i].GroupAddress < normalized[j].GroupAddress
		}
		return normalized[i].AsPerson < normalized[j].AsPerson
	})
	return normalized, nil
}

func normalizeReminderRoute(route string) (reminderRouteConfig, error) {
	trimmed := strings.TrimSpace(route)
	if trimmed == "" {
		return reminderRouteConfig{}, errors.New("mailbox reminder requires route")
	}
	parsed, err := parseAddress(trimmed)
	if err != nil {
		return reminderRouteConfig{}, err
	}
	return reminderRouteConfig{
		Address: parsed.Scheme + "/" + parsed.ID,
		Manager: parsed.Scheme,
		Target:  parsed.ID,
	}, nil
}

func normalizeReminderPolicy(olderThan string, activePush bool) (reminderPolicy, error) {
	trimmed := strings.TrimSpace(olderThan)
	if trimmed == "" {
		return reminderPolicy{}, errors.New("mailbox reminder requires older_than")
	}
	duration, err := time.ParseDuration(trimmed)
	if err != nil {
		return reminderPolicy{}, fmt.Errorf("invalid older_than duration: %w", err)
	}
	if duration <= 0 {
		return reminderPolicy{}, errors.New("older_than must be greater than zero")
	}
	return reminderPolicy{
		OlderThan:    duration,
		OlderThanRaw: trimmed,
		ActivePush:   activePush,
	}, nil
}

func (p reminderPolicy) cooldown() time.Duration {
	return p.OlderThan
}

func reminderKey(selector reminderSelector, route string) string {
	parts := []string{route, "addresses"}
	parts = append(parts, selector.Addresses...)
	parts = append(parts, "groups")
	for _, groupView := range selector.GroupViews {
		parts = append(parts, groupView.GroupAddress, groupView.AsPerson)
	}
	return strings.Join(parts, "\x00")
}

func cloneReminderSubscription(subscription reminderSubscription) reminderSubscription {
	cloned := subscription
	cloned.Selector.Addresses = append([]string(nil), subscription.Selector.Addresses...)
	cloned.Selector.GroupViews = append([]reminderGroupView(nil), subscription.Selector.GroupViews...)
	return cloned
}

func nextReminderRuntime(previous reminderRuntime, checkedAt time.Time, staleEntries []staleAddress) reminderRuntime {
	runtime := previous
	runtime.LastCheckedAt = checkedAt.Format(time.RFC3339)

	summary := reminderCurrentSummary(staleEntries)
	runtime.LastStaleCount = summary.StaleAddressCount
	runtime.LastClaimableCount = summary.ClaimableCount
	runtime.LastOldestEligible = summary.OldestEligibleAt
	if summary.Stale {
		runtime.LastMatchedAt = checkedAt.Format(time.RFC3339)
	}
	return runtime
}

func reminderCurrentSummary(staleEntries []staleAddress) reminderCurrentState {
	summary := reminderCurrentState{
		Stale:             len(staleEntries) > 0,
		StaleAddressCount: len(staleEntries),
	}
	for _, entry := range staleEntries {
		summary.ClaimableCount += entry.ClaimableCount
		if summary.OldestEligibleAt == "" || (entry.OldestEligibleAt != "" && entry.OldestEligibleAt < summary.OldestEligibleAt) {
			summary.OldestEligibleAt = entry.OldestEligibleAt
		}
	}
	return summary
}

func structToMap(value any) (map[string]any, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(encoded, &out); err != nil {
		return nil, err
	}
	return out, nil
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
