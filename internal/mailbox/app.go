package mailbox

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

var ErrHelpRequested = errors.New("help requested")

type App struct {
	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer
}

func NewApp(stdin io.Reader, stdout, stderr io.Writer) *App {
	return &App{
		stdin:  stdin,
		stdout: stdout,
		stderr: stderr,
	}
}

type preparedCommand func(context.Context, *Store) error

type stringListFlag []string

func (f *stringListFlag) String() string {
	return strings.Join(*f, ",")
}

func (f *stringListFlag) Set(value string) error {
	*f = append(*f, value)
	return nil
}

func (a *App) Run(ctx context.Context, args []string) error {
	stateDir, rest, helpRequested, err := parseGlobalArgs(args)
	if err != nil {
		return err
	}
	if helpRequested {
		a.writeRootHelp()
		return ErrHelpRequested
	}
	if len(rest) == 0 {
		return errors.New("expected a command: send, recv, wait, watch, ack, release, defer, fail, or list")
	}

	command, err := a.prepareCommand(rest)
	if err != nil {
		return err
	}

	runtime, err := OpenRuntime(ctx, stateDir)
	if err != nil {
		return err
	}
	defer runtime.Close()

	return command(ctx, runtime.Store())
}

func (a *App) prepareCommand(args []string) (preparedCommand, error) {
	if len(args) == 0 {
		return nil, errors.New("expected a command: send, recv, wait, watch, ack, release, defer, fail, or list")
	}

	switch args[0] {
	case "send":
		return a.prepareSendCommand(args[1:])
	case "recv":
		return a.prepareRecvCommand(args[1:])
	case "wait":
		return a.prepareWaitCommand(args[1:])
	case "watch":
		return a.prepareWatchCommand(args[1:])
	case "ack":
		return a.prepareAckCommand(args[1:])
	case "release":
		return a.prepareReleaseCommand(args[1:])
	case "defer":
		return a.prepareDeferCommand(args[1:])
	case "fail":
		return a.prepareFailCommand(args[1:])
	case "list":
		return a.prepareListCommand(args[1:])
	default:
		return nil, fmt.Errorf("unknown command %q", args[0])
	}
}

func parseGlobalArgs(args []string) (string, []string, bool, error) {
	fs := flag.NewFlagSet("agent-mailbox", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	var stateDir string
	fs.StringVar(&stateDir, "state-dir", "", "override mailbox state directory")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return "", nil, true, nil
		}
		return "", nil, false, err
	}
	return stateDir, fs.Args(), false, nil
}

func (a *App) prepareSendCommand(args []string) (preparedCommand, error) {
	fs := flag.NewFlagSet("agent-mailbox send", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	var toAddress string
	var fromAddress string
	var subject string
	var contentType string
	var schemaVersion string
	var bodyFile string

	fs.StringVar(&toAddress, "to", "", "recipient address")
	fs.StringVar(&fromAddress, "from", "", "sender address")
	fs.StringVar(&subject, "subject", "", "message subject")
	fs.StringVar(&contentType, "content-type", "text/plain", "message content type")
	fs.StringVar(&schemaVersion, "schema-version", "v1", "sender-defined schema version")
	fs.StringVar(&bodyFile, "body-file", "", "path to message body, or - for stdin")

	if err := a.parseCommandFlags(fs, args, a.writeSendHelp); err != nil {
		return nil, err
	}
	if err := requireFlag(toAddress, "--to"); err != nil {
		return nil, err
	}

	body, err := a.readBody(bodyFile)
	if err != nil {
		return nil, err
	}

	params := SendParams{
		ToAddress:     toAddress,
		FromAddress:   fromAddress,
		Subject:       subject,
		ContentType:   contentType,
		SchemaVersion: schemaVersion,
		Body:          body,
	}

	return func(ctx context.Context, store *Store) error {
		result, err := store.Send(ctx, params)
		if err != nil {
			return err
		}

		fmt.Fprintf(a.stdout, "message_id=%s delivery_id=%s blob_id=%s\n", result.MessageID, result.DeliveryID, result.BodyBlobRef)
		return nil
	}, nil
}

func (a *App) readBody(bodyFile string) ([]byte, error) {
	switch strings.TrimSpace(bodyFile) {
	case "":
		return nil, errors.New("--body-file is required")
	case "-":
		body, err := io.ReadAll(a.stdin)
		if err != nil {
			return nil, fmt.Errorf("read stdin body: %w", err)
		}
		return body, nil
	default:
		body, err := os.ReadFile(bodyFile)
		if err != nil {
			return nil, fmt.Errorf("read body file: %w", err)
		}
		return body, nil
	}
}

func (a *App) prepareListCommand(args []string) (preparedCommand, error) {
	fs := flag.NewFlagSet("agent-mailbox list", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	var address string
	var formats outputFlags
	var state string
	fs.StringVar(&address, "for", "", "recipient address")
	formats.register(fs, "emit JSON", "emit YAML")
	fs.StringVar(&state, "state", "", "filter by delivery state")

	if err := a.parseCommandFlags(fs, args, a.writeListHelp); err != nil {
		return nil, err
	}
	if err := requireFlag(address, "--for"); err != nil {
		return nil, err
	}
	format, err := formats.resolve()
	if err != nil {
		return nil, err
	}

	params := ListParams{
		Address: address,
		State:   state,
	}

	return func(ctx context.Context, store *Store) error {
		deliveries, err := store.List(ctx, params)
		if err != nil {
			return err
		}

		if format != outputFormatText {
			return a.writeStructuredOutput(format, deliveries)
		}

		for _, delivery := range deliveries {
			fmt.Fprintf(a.stdout, "%s %s %s %s\n", delivery.DeliveryID, delivery.State, delivery.VisibleAt, delivery.Subject)
		}
		return nil
	}, nil
}

func (a *App) prepareRecvCommand(args []string) (preparedCommand, error) {
	fs := flag.NewFlagSet("agent-mailbox recv", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	var addresses stringListFlag
	var wait bool
	var timeout time.Duration
	var formats outputFlags

	fs.Var(&addresses, "for", "recipient address (repeatable)")
	fs.BoolVar(&wait, "wait", false, "wait for a claimable delivery")
	fs.DurationVar(&timeout, "timeout", 0, "maximum time to wait when --wait is set")
	formats.register(fs, "emit JSON", "emit YAML")

	if err := a.parseCommandFlags(fs, args, a.writeRecvHelp); err != nil {
		return nil, err
	}
	normalizedAddresses, err := normalizeAddresses("", []string(addresses), "--for")
	if err != nil {
		return nil, err
	}
	if timeout < 0 {
		return nil, errors.New("--timeout must be greater than or equal to 0")
	}
	if flagWasProvided(fs, "timeout") && !wait {
		return nil, errors.New("--timeout requires --wait")
	}
	format, err := formats.resolve()
	if err != nil {
		return nil, err
	}

	params := ReceiveParams{
		Addresses: normalizedAddresses,
		Wait:      wait,
		Timeout:   timeout,
	}

	return func(ctx context.Context, store *Store) error {
		message, err := store.Receive(ctx, params)
		if err != nil {
			return err
		}

		if format != outputFormatText {
			return a.writeStructuredOutput(format, message)
		}

		fmt.Fprintf(a.stdout, "delivery_id=%s message_id=%s recipient_address=%s lease_token=%s lease_expires_at=%s subject=%q\n", message.DeliveryID, message.MessageID, message.RecipientAddress, message.LeaseToken, message.LeaseExpiresAt, message.Subject)
		fmt.Fprint(a.stdout, message.Body)
		if !strings.HasSuffix(message.Body, "\n") {
			fmt.Fprintln(a.stdout)
		}
		return nil
	}, nil
}

func (a *App) prepareWatchCommand(args []string) (preparedCommand, error) {
	fs := flag.NewFlagSet("agent-mailbox watch", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	var addresses stringListFlag
	var timeout time.Duration
	var formats outputFlags
	var state string

	fs.Var(&addresses, "for", "recipient address (repeatable)")
	fs.DurationVar(&timeout, "timeout", 0, "maximum idle time before watch exits")
	formats.register(fs, "emit NDJSON", "emit a YAML document stream")
	fs.StringVar(&state, "state", "", "filter by delivery state")

	if err := a.parseCommandFlags(fs, args, a.writeWatchHelp); err != nil {
		return nil, err
	}
	normalizedAddresses, err := normalizeAddresses("", []string(addresses), "--for")
	if err != nil {
		return nil, err
	}
	if timeout < 0 {
		return nil, errors.New("--timeout must be greater than or equal to 0")
	}
	format, err := formats.resolve()
	if err != nil {
		return nil, err
	}

	params := WatchParams{
		Addresses: normalizedAddresses,
		State:     state,
		Timeout:   timeout,
	}

	return func(ctx context.Context, store *Store) error {
		emit, err := a.newWatchEmitter(format)
		if err != nil {
			return err
		}

		return store.Watch(ctx, params, func(delivery ListedDelivery) error {
			return emit(delivery)
		})
	}, nil
}

func (a *App) prepareWaitCommand(args []string) (preparedCommand, error) {
	fs := flag.NewFlagSet("agent-mailbox wait", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	var addresses stringListFlag
	var timeout time.Duration
	var formats outputFlags

	fs.Var(&addresses, "for", "recipient address (repeatable)")
	fs.DurationVar(&timeout, "timeout", 0, "maximum time to wait for a matching delivery")
	formats.register(fs, "emit JSON", "emit YAML")

	if err := a.parseCommandFlags(fs, args, a.writeWaitHelp); err != nil {
		return nil, err
	}
	normalizedAddresses, err := normalizeAddresses("", []string(addresses), "--for")
	if err != nil {
		return nil, err
	}
	if timeout < 0 {
		return nil, errors.New("--timeout must be greater than or equal to 0")
	}
	format, err := formats.resolve()
	if err != nil {
		return nil, err
	}

	params := WaitParams{
		Addresses: normalizedAddresses,
		Timeout:   timeout,
	}

	return func(ctx context.Context, store *Store) error {
		delivery, err := store.Wait(ctx, params)
		if err != nil {
			return err
		}
		if format != outputFormatText {
			return a.writeStructuredOutput(format, delivery)
		}
		return a.writeListedDeliveryText(delivery)
	}, nil
}

func (a *App) prepareAckCommand(args []string) (preparedCommand, error) {
	fs := flag.NewFlagSet("agent-mailbox ack", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	var deliveryID string
	var leaseToken string
	fs.StringVar(&deliveryID, "delivery", "", "delivery id")
	fs.StringVar(&leaseToken, "lease-token", "", "lease token")

	if err := a.parseCommandFlags(fs, args, a.writeAckHelp); err != nil {
		return nil, err
	}
	if err := requireFlag(deliveryID, "--delivery"); err != nil {
		return nil, err
	}
	if err := requireFlag(leaseToken, "--lease-token"); err != nil {
		return nil, err
	}

	return func(ctx context.Context, store *Store) error {
		result, err := store.Ack(ctx, deliveryID, leaseToken)
		if err != nil {
			return err
		}
		fmt.Fprintf(a.stdout, "delivery_id=%s state=%s acked_at=%s attempt_count=%d\n", result.DeliveryID, result.State, result.AckedAt, result.AttemptCount)
		return nil
	}, nil
}

func (a *App) prepareReleaseCommand(args []string) (preparedCommand, error) {
	fs := flag.NewFlagSet("agent-mailbox release", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	var deliveryID string
	var leaseToken string
	fs.StringVar(&deliveryID, "delivery", "", "delivery id")
	fs.StringVar(&leaseToken, "lease-token", "", "lease token")

	if err := a.parseCommandFlags(fs, args, a.writeReleaseHelp); err != nil {
		return nil, err
	}
	if err := requireFlag(deliveryID, "--delivery"); err != nil {
		return nil, err
	}
	if err := requireFlag(leaseToken, "--lease-token"); err != nil {
		return nil, err
	}

	return func(ctx context.Context, store *Store) error {
		result, err := store.Release(ctx, deliveryID, leaseToken)
		if err != nil {
			return err
		}
		fmt.Fprintf(a.stdout, "delivery_id=%s state=%s visible_at=%s attempt_count=%d\n", result.DeliveryID, result.State, result.VisibleAt, result.AttemptCount)
		return nil
	}, nil
}

func (a *App) prepareDeferCommand(args []string) (preparedCommand, error) {
	fs := flag.NewFlagSet("agent-mailbox defer", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	var deliveryID string
	var leaseToken string
	var untilText string
	fs.StringVar(&deliveryID, "delivery", "", "delivery id")
	fs.StringVar(&leaseToken, "lease-token", "", "lease token")
	fs.StringVar(&untilText, "until", "", "future RFC3339 timestamp")

	if err := a.parseCommandFlags(fs, args, a.writeDeferHelp); err != nil {
		return nil, err
	}
	if err := requireFlag(deliveryID, "--delivery"); err != nil {
		return nil, err
	}
	if err := requireFlag(leaseToken, "--lease-token"); err != nil {
		return nil, err
	}
	if strings.TrimSpace(untilText) == "" {
		return nil, errors.New("--until is required")
	}

	until, err := time.Parse(time.RFC3339Nano, untilText)
	if err != nil {
		return nil, fmt.Errorf("parse --until: %w", err)
	}

	return func(ctx context.Context, store *Store) error {
		result, err := store.Defer(ctx, deliveryID, leaseToken, until)
		if err != nil {
			return err
		}
		fmt.Fprintf(a.stdout, "delivery_id=%s state=%s visible_at=%s attempt_count=%d\n", result.DeliveryID, result.State, result.VisibleAt, result.AttemptCount)
		return nil
	}, nil
}

func (a *App) prepareFailCommand(args []string) (preparedCommand, error) {
	fs := flag.NewFlagSet("agent-mailbox fail", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	var deliveryID string
	var leaseToken string
	var reason string
	fs.StringVar(&deliveryID, "delivery", "", "delivery id")
	fs.StringVar(&leaseToken, "lease-token", "", "lease token")
	fs.StringVar(&reason, "reason", "", "failure reason")

	if err := a.parseCommandFlags(fs, args, a.writeFailHelp); err != nil {
		return nil, err
	}
	if err := requireFlag(deliveryID, "--delivery"); err != nil {
		return nil, err
	}
	if err := requireFlag(leaseToken, "--lease-token"); err != nil {
		return nil, err
	}
	if err := requireFlag(reason, "--reason"); err != nil {
		return nil, err
	}

	return func(ctx context.Context, store *Store) error {
		result, err := store.Fail(ctx, deliveryID, leaseToken, reason)
		if err != nil {
			return err
		}
		fmt.Fprintf(a.stdout, "delivery_id=%s state=%s visible_at=%s attempt_count=%d\n", result.DeliveryID, result.State, result.VisibleAt, result.AttemptCount)
		return nil
	}, nil
}

func requireFlag(value, name string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s is required", name)
	}
	return nil
}

func normalizeAddresses(address string, addresses []string, flagName string) ([]string, error) {
	values := make([]string, 0, len(addresses)+1)
	if address != "" {
		values = append(values, address)
	}
	values = append(values, addresses...)

	normalized := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			return nil, fmt.Errorf("%s must not be empty", flagName)
		}
		if _, exists := seen[trimmed]; exists {
			continue
		}
		seen[trimmed] = struct{}{}
		normalized = append(normalized, trimmed)
	}
	if len(normalized) == 0 {
		return nil, fmt.Errorf("%s is required", flagName)
	}
	return normalized, nil
}

func (a *App) parseCommandFlags(fs *flag.FlagSet, args []string, writeHelp func()) error {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			writeHelp()
			return ErrHelpRequested
		}
		return err
	}
	return nil
}

func isHelpArg(value string) bool {
	return value == "-h" || value == "--help"
}

func (a *App) writeRootHelp() {
	writeHelp(a.stdout, []string{
		"Usage:",
		"  agent-mailbox [--state-dir PATH] <command> [options]",
		"",
		"Commands:",
		"  send                Send a message to an address",
		"  recv                Claim the next delivery",
		"  wait                Wait for one delivery without claiming",
		"  watch               Observe deliveries without claiming",
		"  list                List deliveries",
		"  ack                 Acknowledge a leased delivery",
		"  release             Return a leased delivery to the queue",
		"  defer               Hide a leased delivery until a future time",
		"  fail                Record a failed delivery attempt",
		"",
		"Global options:",
		"  --state-dir PATH    Override mailbox state directory",
		"  --help              Show help",
		"",
		"Use \"agent-mailbox <command> --help\" for command-specific details.",
	})
}

func (a *App) writeSendHelp() {
	writeHelp(a.stdout, []string{
		"Usage:",
		"  agent-mailbox send --to ADDRESS --body-file PATH [options]",
		"",
		"Options:",
		"  --to ADDRESS           Recipient address",
		"  --from ADDRESS         Sender address",
		"  --subject TEXT         Message subject",
		"  --content-type TYPE    Message content type",
		"  --schema-version VER   Sender-defined schema version",
		"  --body-file PATH|-     Read body from a file or stdin",
	})
}

func (a *App) writeListHelp() {
	writeHelp(a.stdout, []string{
		"Usage:",
		"  agent-mailbox list --for ADDRESS [--state STATE] [--json | --yaml]",
		"",
		"Options:",
		"  --for ADDRESS      Recipient address",
		"  --state STATE      Filter by delivery state",
		"  --json             Emit JSON",
		"  --yaml             Emit YAML",
	})
}

func (a *App) writeRecvHelp() {
	writeHelp(a.stdout, []string{
		"Usage:",
		"  agent-mailbox recv --for ADDRESS [--for ADDRESS ...] [--wait] [--timeout DURATION] [--json | --yaml]",
		"",
		"Options:",
		"  --for ADDRESS        Recipient address (repeatable)",
		"  --wait               Wait for a claimable delivery",
		"  --timeout DURATION   Maximum time to wait when --wait is set",
		"  --json               Emit JSON",
		"  --yaml               Emit YAML",
	})
}

func (a *App) writeWatchHelp() {
	writeHelp(a.stdout, []string{
		"Usage:",
		"  agent-mailbox watch --for ADDRESS [--for ADDRESS ...] [--state STATE] [--timeout DURATION] [--json | --yaml]",
		"",
		"Options:",
		"  --for ADDRESS        Recipient address (repeatable)",
		"  --state STATE        Filter by delivery state",
		"  --timeout DURATION   Maximum idle time before watch exits",
		"  --json               Emit NDJSON",
		"  --yaml               Emit a YAML document stream",
	})
}

func (a *App) writeWaitHelp() {
	writeHelp(a.stdout, []string{
		"Usage:",
		"  agent-mailbox wait --for ADDRESS [--for ADDRESS ...] [--timeout DURATION] [--json | --yaml]",
		"",
		"Options:",
		"  --for ADDRESS        Recipient address (repeatable)",
		"  --timeout DURATION   Maximum time to wait for a matching delivery",
		"  --json               Emit JSON",
		"  --yaml               Emit YAML",
	})
}

func (a *App) writeAckHelp() {
	writeHelp(a.stdout, []string{
		"Usage:",
		"  agent-mailbox ack --delivery ID --lease-token TOKEN",
	})
}

func (a *App) writeReleaseHelp() {
	writeHelp(a.stdout, []string{
		"Usage:",
		"  agent-mailbox release --delivery ID --lease-token TOKEN",
	})
}

func (a *App) writeDeferHelp() {
	writeHelp(a.stdout, []string{
		"Usage:",
		"  agent-mailbox defer --delivery ID --lease-token TOKEN --until RFC3339",
	})
}

func (a *App) writeFailHelp() {
	writeHelp(a.stdout, []string{
		"Usage:",
		"  agent-mailbox fail --delivery ID --lease-token TOKEN --reason TEXT",
	})
}

func writeHelp(w io.Writer, lines []string) {
	for _, line := range lines {
		fmt.Fprintln(w, line)
	}
}

func flagWasProvided(fs *flag.FlagSet, name string) bool {
	provided := false
	fs.Visit(func(current *flag.Flag) {
		if current.Name == name {
			provided = true
		}
	})
	return provided
}
