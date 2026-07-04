package mcpserver

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/ruiheng/agent-mailbox/internal/mailbox"
)

const (
	serverName                   = "agent_mailbox"
	serverVersion                = "0.4.0"
	syncCmdTimeout               = 30 * time.Second
	ensureSessionShowTimeout     = 30 * time.Second
	defaultMCPLeaseTTL           = 30 * time.Second
	defaultLeaseRenewInterval    = 10 * time.Second
	notificationDelivery         = "delivery_available"
	notificationFallbackWake     = "fallback_wake"
	mailboxOverviewURI           = "mailbox://bound/overview"
	defaultWakePollInterval      = 30 * time.Second
	defaultWakeInterChannelGap   = 1 * time.Minute
	defaultMCPHintInitialDelay   = 1 * time.Minute
	defaultMCPHintCooldown       = 2 * time.Minute
	defaultAgentDeckInitialDelay = 3 * time.Minute
	defaultAgentDeckCooldown     = 5 * time.Minute
	defaultNotifyDelay           = 2 * time.Second
	defaultStartupInstruction    = ""
	defaultNotifyMessage         = "NOTICE: There might be new mail in agent-mailbox."
	agentDeckBindRecoveryHint    = "agent-deck address auto-bind did not find your current session; run `agent-deck session current --json` to find your `agent-deck/<session-id>` address, then call `mailbox_bind` with that address."
	toolSessionBindRecoveryHint  = "AI tool session auto-bind did not find codex/..., claude/..., gemini/..., or opencode/...; expose CODEX_THREAD_ID, CLAUDE_CODE_SESSION_ID, GEMINI_SESSION_ID, or OPENCODE_SESSION_ID, wait for agent-deck state sync for the current session, then call `mailbox_status` again or call `mailbox_bind` manually."
	serverInstructions           = "Bootstrap this MCP process once per agent-managed session. The first tool call must be `mailbox_status`; it auto-binds any detectable agent-deck/codex/claude/gemini/opencode address and reports warnings. All other tools fail until `mailbox_status` has been called."
	unsetValue                   = "<unset>"
)

type Runner interface {
	Run(ctx context.Context, args []string, input string) (RunResult, error)
}

type RunResult struct {
	ExitCode int
	Stdout   string
	Stderr   string
}

type mailboxServiceFactory interface {
	Open(context.Context) (any, func() error, error)
}

type mailboxSender interface {
	Send(context.Context, mailbox.SendParams) (mailbox.SendResult, error)
}

type mailboxLister interface {
	List(context.Context, mailbox.ListParams) ([]mailbox.ListedDelivery, error)
}

type mailboxGroupMessageLister interface {
	ListGroupMessages(context.Context, mailbox.GroupListParams) ([]mailbox.GroupListedMessage, error)
}

type mailboxGroupMessageWaiter interface {
	WaitGroupMessage(context.Context, mailbox.GroupWaitParams) (mailbox.GroupListedMessage, error)
}

type mailboxGroupMessageReceiver interface {
	ReceiveGroupMessage(context.Context, mailbox.GroupReceiveParams) (mailbox.GroupReceivedMessage, error)
}

type mailboxGroupManager interface {
	CreateGroup(context.Context, string) (mailbox.GroupRecord, error)
	AddGroupMember(context.Context, string, string) (mailbox.GroupMembershipRecord, error)
	RemoveGroupMember(context.Context, string, string) (mailbox.GroupMembershipRecord, error)
	ListGroupMembers(context.Context, string) ([]mailbox.GroupMembershipRecord, error)
}

type mailboxGroupSubscriberManager interface {
	AddGroupNotificationSubscriber(context.Context, string, string, string) (mailbox.GroupNotificationSubscriberRecord, error)
	RemoveGroupNotificationSubscriber(context.Context, string, string) (mailbox.GroupNotificationSubscriberRecord, error)
	ListGroupNotificationSubscribers(context.Context, string) ([]mailbox.GroupNotificationSubscriberRecord, error)
}

type mailboxAddressInspector interface {
	InspectAddress(context.Context, string) (mailbox.AddressInspection, error)
}

type mailboxClaimableLister interface {
	ListClaimableAddresses(context.Context, []string) ([]mailbox.ClaimableAddress, error)
}

type mailboxBatchReceiver interface {
	ReceiveBatchWithLeaseTTL(context.Context, mailbox.ReceiveBatchParams, time.Duration) (mailbox.ReceiveResult, error)
}

type mailboxWaiter interface {
	Wait(context.Context, mailbox.WaitParams) (mailbox.ListedDelivery, error)
}

type mailboxDeliveryReader interface {
	ReadDeliveries(context.Context, []string) ([]mailbox.ReadDelivery, error)
}

type mailboxMessageReader interface {
	ReadMessages(context.Context, []string) ([]mailbox.ReadMessage, error)
}

type mailboxLatestDeliveryReader interface {
	ReadLatestDeliveries(context.Context, []string, string, int) ([]mailbox.ReadDelivery, bool, error)
}

type mailboxDeliveryTransitioner interface {
	Ack(context.Context, string, string) (mailbox.DeliveryTransitionResult, error)
	Release(context.Context, string, string) (mailbox.DeliveryTransitionResult, error)
	Defer(context.Context, string, string, time.Time) (mailbox.DeliveryTransitionResult, error)
	Undefer(context.Context, string) (mailbox.DeliveryTransitionResult, error)
	Fail(context.Context, string, string, string) (mailbox.DeliveryTransitionResult, error)
}

type mailboxLeaseRenewer interface {
	Renew(context.Context, string, string, time.Duration) (mailbox.LeaseRenewResult, error)
}

type runtimeMailboxServiceFactory struct {
	stateDir     string
	openRuntime  func(context.Context, string) (*mailbox.Runtime, error)
	closeRuntime func(*mailbox.Runtime) error

	mu      sync.Mutex
	service any
	runtime *mailbox.Runtime
	closed  bool
}

func (f *runtimeMailboxServiceFactory) Open(ctx context.Context) (any, func() error, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return nil, nil, errors.New("mailbox runtime is closed")
	}
	if f.service != nil {
		return f.service, func() error { return nil }, nil
	}
	runtime, err := f.openRuntime(ctx, f.stateDir)
	if err != nil {
		return nil, nil, err
	}
	f.runtime = runtime
	f.service = mailbox.NewOperations(runtime.Store())
	return f.service, func() error { return nil }, nil
}

func (f *runtimeMailboxServiceFactory) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return nil
	}
	f.closed = true
	if f.runtime == nil {
		return nil
	}
	closeRuntime := f.closeRuntime
	if closeRuntime == nil {
		closeRuntime = func(runtime *mailbox.Runtime) error {
			return runtime.Close()
		}
	}
	err := closeRuntime(f.runtime)
	f.runtime = nil
	f.service = nil
	return err
}

type Options struct {
	MailboxServiceFactory mailboxServiceFactory
	CommandRunner         Runner
	StateDir              string
	Now                   func() time.Time
	MCPLeaseTTL           time.Duration
	LeaseRenewInterval    time.Duration
	DisableLeaseRenewLoop bool
	WakePollInterval      time.Duration
	NotifyDelay           time.Duration
	DisableWakeScheduler  bool
}

type Service struct {
	ctx                    context.Context
	cancel                 context.CancelFunc
	mailboxServices        mailboxServiceFactory
	closeMailboxServices   func() error
	commandRunner          Runner
	sessions               *sessionManager
	notifications          *notificationManager
	activeLeases           *activeLeaseManager
	state                  *serverState
	now                    func() time.Time
	mcpLeaseTTL            time.Duration
	leaseRenewInterval     time.Duration
	disableLeaseRenewLoop  bool
	wakePollInterval       time.Duration
	notifyDelay            time.Duration
	disableWakeScheduler   bool
	wakeSchedulerState     *wakeSchedulerState
	overviewSubscriptions  *resourceSubscriptionState
	mailboxOverviewEmitter func(context.Context) notificationOutcome
	leaseRenewLoopOnce     sync.Once
	wakeSchedulerLoopOnce  sync.Once
	backgroundMu           sync.Mutex
	backgroundLoops        sync.WaitGroup
	closeOnce              sync.Once
	closeMailboxOnce       sync.Once
	serverMu               sync.Mutex
	server                 *mcp.Server
}

type runOptions struct {
	input   string
	okCodes []int
	timeout time.Duration
}

type osCommandRunner struct {
	cwd string
}

var legacyServerServices sync.Map

// New returns a legacy MCP server handle. Call CloseServer with the returned
// server when done so service-owned background loops and runtimes are released.
// Prefer NewService when the caller can own the Service lifecycle directly.
func New(opts Options) *mcp.Server {
	service := NewService(opts)
	server := service.Server()
	legacyServerServices.Store(server, service)
	return server
}

// CloseServer closes the Service created by New. It is safe to call more than
// once for the same server.
func CloseServer(server *mcp.Server) {
	if server == nil {
		return
	}
	service, ok := legacyServerServices.LoadAndDelete(server)
	if !ok {
		return
	}
	service.(*Service).Close()
}

func NewService(opts Options) *Service {
	if opts.NotifyDelay == 0 {
		opts.NotifyDelay = defaultNotifyDelay
	}
	return newService(opts)
}

func newService(opts Options) *Service {
	var closeMailboxServices func() error
	if opts.MailboxServiceFactory == nil {
		factory := &runtimeMailboxServiceFactory{
			stateDir:    opts.StateDir,
			openRuntime: mailbox.OpenRuntime,
		}
		opts.MailboxServiceFactory = factory
		closeMailboxServices = factory.Close
	}
	if opts.CommandRunner == nil {
		opts.CommandRunner = osCommandRunner{cwd: currentWorkingDir()}
	}
	state := &serverState{}
	sessions := newSessionManager(opts.CommandRunner, state)
	ctx, cancel := context.WithCancel(context.Background())
	service := &Service{
		ctx:                   ctx,
		cancel:                cancel,
		mailboxServices:       opts.MailboxServiceFactory,
		closeMailboxServices:  closeMailboxServices,
		commandRunner:         opts.CommandRunner,
		sessions:              sessions,
		state:                 state,
		now:                   opts.Now,
		mcpLeaseTTL:           opts.MCPLeaseTTL,
		leaseRenewInterval:    opts.LeaseRenewInterval,
		disableLeaseRenewLoop: opts.DisableLeaseRenewLoop,
		wakePollInterval:      opts.WakePollInterval,
		notifyDelay:           opts.NotifyDelay,
		disableWakeScheduler:  opts.DisableWakeScheduler,
	}
	if service.now == nil {
		service.now = func() time.Time {
			return time.Now().UTC()
		}
	}
	if service.mcpLeaseTTL <= 0 {
		service.mcpLeaseTTL = defaultMCPLeaseTTL
	}
	if service.leaseRenewInterval <= 0 {
		service.leaseRenewInterval = defaultLeaseRenewInterval
	}
	if service.wakePollInterval <= 0 {
		service.wakePollInterval = defaultWakePollInterval
	}
	if service.notifyDelay < 0 {
		service.notifyDelay = 0
	}
	service.notifications = newNotificationManager(service.commandRunner, service.sessions)
	service.activeLeases = newActiveLeaseManager()
	service.wakeSchedulerState = newWakeSchedulerState()
	service.overviewSubscriptions = newResourceSubscriptionState()
	service.mailboxOverviewEmitter = service.emitMailboxOverviewUpdated
	return service
}

// Close stops service-owned background loops and closes the service-owned
// mailbox runtime. It does not wait for in-flight MCP handlers; callers that
// need handler quiescence should close and drain their MCP sessions separately.
func (s *Service) Close() {
	s.closeOnce.Do(func() {
		s.backgroundMu.Lock()
		s.cancel()
		s.backgroundMu.Unlock()
	})
	s.backgroundLoops.Wait()
	s.closeMailboxOnce.Do(func() {
		if s.closeMailboxServices != nil {
			_ = s.closeMailboxServices()
		}
	})
}

func (s *Service) startBackgroundLoop(once *sync.Once, run func()) {
	once.Do(func() {
		s.backgroundMu.Lock()
		defer s.backgroundMu.Unlock()
		select {
		case <-s.ctx.Done():
			return
		default:
		}
		s.backgroundLoops.Add(1)
		go func() {
			defer s.backgroundLoops.Done()
			run()
		}()
	})
}

func (s *Service) Server() *mcp.Server {
	s.serverMu.Lock()
	defer s.serverMu.Unlock()
	if s.server != nil {
		return s.server
	}

	server := mcp.NewServer(&mcp.Implementation{Name: serverName, Version: serverVersion}, &mcp.ServerOptions{
		Instructions:       serverInstructions,
		SubscribeHandler:   s.subscribeResource,
		UnsubscribeHandler: s.unsubscribeResource,
	})

	s.registerMailboxTools(server)
	s.registerSessionTools(server)
	s.registerMailboxOverviewResource(server)
	s.server = server
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

func withMailboxService[T any, S any](ctx context.Context, factory mailboxServiceFactory, fn func(S) (T, error)) (T, error) {
	var zero T
	rawService, closeFunc, err := factory.Open(ctx)
	if err != nil {
		return zero, err
	}
	defer closeFunc()
	service, ok := rawService.(S)
	if !ok {
		return zero, fmt.Errorf("mailbox service %T does not satisfy %T", rawService, service)
	}
	return fn(service)
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

func parseAddress(address string) (mailbox.ParsedAddress, error) {
	return mailbox.ParseAddress(address)
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

func toolSessionAddress(scheme, sessionID string) string {
	return scheme + "/" + sessionID
}

func boundStateMap(bound boundState) map[string]any {
	out := map[string]any{
		"bound_addresses":                 bound.BoundAddresses,
		"default_sender":                  nilIfEmpty(bound.DefaultSender),
		"default_workdir":                 nilIfEmpty(bound.DefaultWorkdir),
		"detected_agent_deck_session_id":  nilIfEmpty(bound.DetectedAgentDeckSession),
		"detected_tool_session_addresses": bound.DetectedToolSessionAddresses,
		"warnings":                        bound.Warnings,
	}
	for key, value := range detectedToolSessionOutputFields(bound.DetectedToolSessions, nilIfEmpty) {
		out[key] = value
	}
	return out
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
