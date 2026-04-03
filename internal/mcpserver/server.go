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
	serverName                  = "agent_mailbox"
	serverVersion               = "0.4.0"
	syncCmdTimeout              = 30 * time.Second
	ensureSessionShowTimeout    = 30 * time.Second
	defaultMCPLeaseTTL          = 30 * time.Second
	defaultLeaseRenewInterval   = 10 * time.Second
	notificationDelivery        = "delivery_available"
	defaultListenerMessage      = ""
	defaultNotifyMessage        = "Use the check-agent-mail skill now. Receive the pending message for your current agent-deck session and execute its requested action."
	defaultMailHint             = "mailbox_recv"
	mailboxRecoveryHint         = "If you forget the mailbox details or next action after ack, use `mailbox_read` on the latest `acked` delivery for this session. For older mail, use `mailbox_list` with `state: acked` and then `mailbox_read` by delivery id."
	serverInstructions          = "Bootstrap this MCP process once per agent-managed session. If it is not bound yet, run `agent-deck session current --json`, take the current session id, and call `mailbox_bind`. Use `agent-deck/<id>` as the default sender. Pass `default_workdir` when you want later `agent_deck_ensure_session` calls to create sessions in the current project. `mailbox_wait` is not recommended for normal workflow; prefer `mailbox_recv`. Later reuse the bound addresses until MCP state is lost."
	unsetValue                  = "<unset>"
)

type Runner interface {
	Run(ctx context.Context, args []string, input string) (RunResult, error)
}

type RunResult struct {
	ExitCode int
	Stdout   string
	Stderr   string
}

type mailboxService interface {
	Send(context.Context, mailbox.SendParams) (mailbox.SendResult, error)
	List(context.Context, mailbox.ListParams) ([]mailbox.ListedDelivery, error)
	ListGroupMessages(context.Context, mailbox.GroupListParams) ([]mailbox.GroupListedMessage, error)
	ReceiveBatch(context.Context, mailbox.ReceiveBatchParams) (mailbox.ReceiveResult, error)
	ReceiveBatchWithLeaseTTL(context.Context, mailbox.ReceiveBatchParams, time.Duration) (mailbox.ReceiveResult, error)
	Wait(context.Context, mailbox.WaitParams) (mailbox.ListedDelivery, error)
	HasVisibleDelivery(context.Context, mailbox.WaitParams) (bool, error)
	ReadMessages(context.Context, []string) ([]mailbox.ReadMessage, error)
	ReadLatestDeliveries(context.Context, []string, string, int) ([]mailbox.ReadDelivery, bool, error)
	ReadDeliveries(context.Context, []string) ([]mailbox.ReadDelivery, error)
	Ack(context.Context, string, string) (mailbox.DeliveryTransitionResult, error)
	Renew(context.Context, string, string, time.Duration) (mailbox.LeaseRenewResult, error)
	Release(context.Context, string, string) (mailbox.DeliveryTransitionResult, error)
	Defer(context.Context, string, string, time.Time) (mailbox.DeliveryTransitionResult, error)
	Fail(context.Context, string, string, string) (mailbox.DeliveryTransitionResult, error)
}

type mailboxServiceFactory interface {
	Open(context.Context) (mailboxService, func() error, error)
}

type runtimeMailboxServiceFactory struct {
	stateDir    string
	openRuntime func(context.Context, string) (*mailbox.Runtime, error)
}

func (f runtimeMailboxServiceFactory) Open(ctx context.Context) (mailboxService, func() error, error) {
	runtime, err := f.openRuntime(ctx, f.stateDir)
	if err != nil {
		return nil, nil, err
	}
	return mailbox.NewOperations(runtime.Store()), runtime.Close, nil
}

type Options struct {
	MailboxServiceFactory     mailboxServiceFactory
	CommandRunner             Runner
	StateDir                  string
	Now                       func() time.Time
	MCPLeaseTTL               time.Duration
	LeaseRenewInterval        time.Duration
	DisableLeaseRenewLoop     bool
}

type Service struct {
	mailboxServices           mailboxServiceFactory
	commandRunner             Runner
	sessions                  *sessionManager
	notifications             *notificationManager
	activeLeases              *activeLeaseManager
	state                     *serverState
	now                       func() time.Time
	mcpLeaseTTL               time.Duration
	leaseRenewInterval        time.Duration
	disableLeaseRenewLoop     bool
	leaseRenewLoopOnce        sync.Once
}

type runOptions struct {
	input   string
	okCodes []int
	timeout time.Duration
}

type parsedAddress struct {
	Scheme string
	ID     string
}

type osCommandRunner struct {
	cwd string
}

func New(opts Options) *mcp.Server {
	return newService(opts).Server()
}

func newService(opts Options) *Service {
	if opts.MailboxServiceFactory == nil {
		opts.MailboxServiceFactory = runtimeMailboxServiceFactory{
			stateDir:    opts.StateDir,
			openRuntime: mailbox.OpenRuntime,
		}
	}
	if opts.CommandRunner == nil {
		opts.CommandRunner = osCommandRunner{cwd: currentWorkingDir()}
	}
	state := &serverState{}
	sessions := newSessionManager(opts.CommandRunner, state)
	service := &Service{
		mailboxServices:           opts.MailboxServiceFactory,
		commandRunner:             opts.CommandRunner,
		sessions:                  sessions,
		state:                     state,
		now:                       opts.Now,
		mcpLeaseTTL:               opts.MCPLeaseTTL,
		leaseRenewInterval:        opts.LeaseRenewInterval,
		disableLeaseRenewLoop:     opts.DisableLeaseRenewLoop,
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
	service.notifications = newNotificationManager(service.commandRunner, service.sessions)
	service.activeLeases = newActiveLeaseManager()
	return service
}

func (s *Service) Server() *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: serverName, Version: serverVersion}, &mcp.ServerOptions{
		Instructions: serverInstructions,
	})

	s.registerMailboxTools(server)
	s.registerSessionTools(server)

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

func withMailboxService[T any](ctx context.Context, factory mailboxServiceFactory, fn func(mailboxService) (T, error)) (T, error) {
	var zero T
	service, closeFunc, err := factory.Open(ctx)
	if err != nil {
		return zero, err
	}
	defer closeFunc()
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
