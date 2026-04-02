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
		return errors.New("expected a command: send, recv, wait, watch, read, ack, renew, release, defer, fail, list, stale, group, or address")
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
		return nil, errors.New("expected a command: send, recv, wait, watch, read, ack, renew, release, defer, fail, list, stale, group, or address")
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
	case "read":
		return a.prepareReadCommand(args[1:])
	case "ack":
		return a.prepareAckCommand(args[1:])
	case "renew":
		return a.prepareRenewCommand(args[1:])
	case "release":
		return a.prepareReleaseCommand(args[1:])
	case "defer":
		return a.prepareDeferCommand(args[1:])
	case "fail":
		return a.prepareFailCommand(args[1:])
	case "list":
		return a.prepareListCommand(args[1:])
	case "stale":
		return a.prepareStaleCommand(args[1:])
	case "group":
		return a.prepareGroupCommand(args[1:])
	case "address":
		return a.prepareAddressCommand(args[1:])
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
	var groupMode bool
	var full bool
	var formats outputFlags

	fs.StringVar(&toAddress, "to", "", "recipient address")
	fs.StringVar(&fromAddress, "from", "", "sender address")
	fs.StringVar(&subject, "subject", "", "message subject")
	fs.StringVar(&contentType, "content-type", "text/plain", "message content type")
	fs.StringVar(&schemaVersion, "schema-version", "v1", "sender-defined schema version")
	fs.StringVar(&bodyFile, "body-file", "", "path to message body, or - for stdin")
	fs.BoolVar(&groupMode, "group", false, "send to a known group address")
	fs.BoolVar(&full, "full", false, "emit the full payload")
	formats.register(fs, "emit JSON", "emit YAML")

	if err := a.parseCommandFlags(fs, args, a.writeSendHelp); err != nil {
		return nil, err
	}
	if err := requireFlag(toAddress, "--to"); err != nil {
		return nil, err
	}
	format, err := formats.resolve()
	if err != nil {
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
		Group:         groupMode,
	}

	return func(ctx context.Context, store *Store) error {
		result, err := store.Send(ctx, params)
		if err != nil {
			return err
		}

		if format != outputFormatText {
			if full {
				return a.writeStructuredOutput(format, fullSendResult(result))
			}
			return a.writeStructuredOutput(format, summarizeSendResult(result))
		}
		if full {
			return a.writeSendResultFullText(fullSendResult(result))
		}
		return a.writeSendResultText(summarizeSendResult(result))
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
	var person string
	var formats outputFlags
	var state string
	fs.StringVar(&address, "for", "", "recipient address")
	fs.StringVar(&person, "as", "", "group reader identity")
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
		if strings.TrimSpace(person) != "" {
			if strings.TrimSpace(state) != "" {
				return errors.New("--state is not supported with --as")
			}
			messages, err := store.ListGroupMessages(ctx, GroupListParams{
				Address: address,
				Person:  person,
			})
			if err != nil {
				return err
			}
			if format != outputFormatText {
				summaries := make([]groupListedMessageSummary, 0, len(messages))
				for _, message := range messages {
					summaries = append(summaries, summarizeGroupListedMessage(message))
				}
				return a.writeStructuredOutput(format, summaries)
			}
			for _, message := range messages {
				if err := a.writeGroupListedMessageText(message); err != nil {
					return err
				}
			}
			return nil
		}

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

func (a *App) prepareStaleCommand(args []string) (preparedCommand, error) {
	fs := flag.NewFlagSet("agent-mailbox stale", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	var addresses stringListFlag
	var person string
	var olderThan time.Duration
	var formats outputFlags

	fs.Var(&addresses, "for", "recipient address (repeatable)")
	fs.StringVar(&person, "as", "", "group reader identity")
	fs.DurationVar(&olderThan, "older-than", 0, "staleness threshold")
	formats.register(fs, "emit JSON", "emit YAML")

	if err := a.parseCommandFlags(fs, args, a.writeStaleHelp); err != nil {
		return nil, err
	}
	normalizedAddresses, err := normalizeAddresses("", []string(addresses), "--for")
	if err != nil {
		return nil, err
	}
	if olderThan <= 0 {
		return nil, errors.New("--older-than must be greater than 0")
	}
	person = strings.TrimSpace(person)
	if person != "" && len(normalizedAddresses) != 1 {
		return nil, errors.New("--as requires exactly one --for address")
	}
	format, err := formats.resolveStructured()
	if err != nil {
		return nil, err
	}

	params := StaleAddressesParams{
		OlderThan: olderThan,
	}
	if person != "" {
		params.GroupViews = []GroupStaleView{{
			Address: normalizedAddresses[0],
			Person:  person,
		}}
	} else {
		params.Addresses = normalizedAddresses
	}

	return func(ctx context.Context, store *Store) error {
		stale, err := store.ListStaleAddresses(ctx, params)
		if err != nil {
			return err
		}
		return a.writeStructuredOutput(format, stale)
	}, nil
}

func (a *App) prepareRecvCommand(args []string) (preparedCommand, error) {
	fs := flag.NewFlagSet("agent-mailbox recv", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	var addresses stringListFlag
	var person string
	var maxMessages int
	var full bool
	var formats outputFlags

	fs.Var(&addresses, "for", "recipient address (repeatable)")
	fs.StringVar(&person, "as", "", "group reader identity")
	fs.IntVar(&maxMessages, "max", 1, "maximum number of deliveries to claim")
	fs.BoolVar(&full, "full", false, "emit the full payload")
	formats.register(fs, "emit JSON", "emit YAML")

	if err := a.parseCommandFlags(fs, args, a.writeRecvHelp); err != nil {
		return nil, err
	}
	normalizedAddresses, err := normalizeAddresses("", []string(addresses), "--for")
	if err != nil {
		return nil, err
	}
	if maxMessages < 1 || maxMessages > maxReceiveBatchSize {
		return nil, fmt.Errorf("--max must be between 1 and %d", maxReceiveBatchSize)
	}
	maxProvided := flagWasProvided(fs, "max")
	format, err := formats.resolve()
	if err != nil {
		return nil, err
	}
	person = strings.TrimSpace(person)

	params := ReceiveBatchParams{
		Addresses: normalizedAddresses,
		Max:       maxMessages,
	}

	return func(ctx context.Context, store *Store) error {
		if person != "" {
			if maxProvided {
				return errors.New("--max is not supported with --as")
			}
			if len(normalizedAddresses) != 1 {
				return errors.New("--as requires exactly one --for address")
			}

			message, err := store.ReceiveGroupMessage(ctx, GroupReceiveParams{
				Address: normalizedAddresses[0],
				Person:  person,
			})
			if err != nil {
				return err
			}
			if format != outputFormatText {
				if full {
					return a.writeStructuredOutput(format, message)
				}
				return a.writeStructuredOutput(format, summarizeGroupReceivedMessage(message))
			}
			if full {
				return a.writeGroupReceivedMessageFullText(message)
			}
			return a.writeGroupReceivedMessageText(summarizeGroupReceivedMessage(message))
		}

		if !maxProvided {
			message, err := store.Receive(ctx, ReceiveParams{Addresses: normalizedAddresses})
			if err != nil {
				return err
			}

			if format != outputFormatText {
				if full {
					return a.writeStructuredOutput(format, message)
				}
				return a.writeStructuredOutput(format, summarizeReceivedMessage(message))
			}
			if full {
				return a.writeReceivedMessageFullText(message)
			}
			return a.writeReceivedMessageText(summarizeReceivedMessage(message))
		}

		result, err := store.ReceiveBatch(ctx, params)
		if err != nil {
			return err
		}

		if format != outputFormatText {
			if full {
				return a.writeStructuredOutput(format, result)
			}
			return a.writeStructuredOutput(format, summarizeReceiveResult(result))
		}
		if full {
			return a.writeReceiveResultFullText(result)
		}

		return a.writeReceiveResultText(summarizeReceiveResult(result))
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
	var person string
	var timeout time.Duration
	var full bool
	var formats outputFlags

	fs.Var(&addresses, "for", "recipient address (repeatable)")
	fs.StringVar(&person, "as", "", "group reader identity")
	fs.DurationVar(&timeout, "timeout", 0, "maximum time to wait for a matching delivery")
	fs.BoolVar(&full, "full", false, "emit the full payload")
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
	person = strings.TrimSpace(person)

	params := WaitParams{
		Addresses: normalizedAddresses,
		Timeout:   timeout,
	}

	return func(ctx context.Context, store *Store) error {
		if person != "" {
			if len(normalizedAddresses) != 1 {
				return errors.New("--as requires exactly one --for address")
			}
			message, err := store.WaitGroupMessage(ctx, GroupWaitParams{
				Address: normalizedAddresses[0],
				Person:  person,
				Timeout: timeout,
			})
			if err != nil {
				return err
			}
			if format != outputFormatText {
				if full {
					return a.writeStructuredOutput(format, message)
				}
				return a.writeStructuredOutput(format, summarizeGroupListedMessage(message))
			}
			if full {
				return a.writeGroupListedMessageText(message)
			}
			return a.writeGroupWaitedMessageText(summarizeGroupListedMessage(message))
		}

		delivery, err := store.Wait(ctx, params)
		if err != nil {
			return err
		}
		if format != outputFormatText {
			if full {
				return a.writeStructuredOutput(format, delivery)
			}
			return a.writeStructuredOutput(format, summarizeListedDelivery(delivery))
		}
		if full {
			return a.writeListedDeliveryText(delivery)
		}
		return a.writeWaitedDeliveryText(summarizeListedDelivery(delivery))
	}, nil
}

func (a *App) prepareReadCommand(args []string) (preparedCommand, error) {
	fs := flag.NewFlagSet("agent-mailbox read", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	var deliveryIDs stringListFlag
	var messageIDs stringListFlag
	var addresses stringListFlag
	var latest bool
	var state string
	var limit int
	var formats outputFlags
	fs.Var(&deliveryIDs, "delivery", "delivery id (repeatable)")
	fs.Var(&messageIDs, "message", "message id (repeatable)")
	fs.Var(&addresses, "for", "recipient address (repeatable)")
	fs.BoolVar(&latest, "latest", false, "read the latest deliveries for one or more inboxes")
	fs.StringVar(&state, "state", "", "delivery state for --latest (defaults to acked)")
	fs.IntVar(&limit, "limit", 1, "maximum number of latest deliveries to read")
	formats.register(fs, "emit JSON", "emit YAML")

	if err := a.parseCommandFlags(fs, args, a.writeReadHelp); err != nil {
		return nil, err
	}
	normalizedDeliveryIDs, err := normalizeFlagValues([]string(deliveryIDs), "--delivery")
	if err != nil && !strings.Contains(err.Error(), "is required") {
		return nil, err
	}
	normalizedMessageIDs, err := normalizeFlagValues([]string(messageIDs), "--message")
	if err != nil && !strings.Contains(err.Error(), "is required") {
		return nil, err
	}
	normalizedAddresses, err := normalizeFlagValues([]string(addresses), "--for")
	if err != nil && !strings.Contains(err.Error(), "is required") {
		return nil, err
	}
	selectorCount := 0
	if len(normalizedDeliveryIDs) > 0 {
		selectorCount++
	}
	if len(normalizedMessageIDs) > 0 {
		selectorCount++
	}
	if latest {
		selectorCount++
	}
	switch {
	case selectorCount == 0:
		return nil, errors.New("one of --delivery, --message, or --latest is required")
	case selectorCount > 1:
		return nil, errors.New("--delivery, --message, and --latest are mutually exclusive")
	case !latest && len(normalizedAddresses) > 0:
		return nil, errors.New("--for requires --latest")
	case !latest && strings.TrimSpace(state) != "":
		return nil, errors.New("--state requires --latest")
	case !latest && flagWasProvided(fs, "limit"):
		return nil, errors.New("--limit requires --latest")
	case latest && len(normalizedAddresses) == 0:
		return nil, errors.New("--latest requires at least one --for address")
	case latest && limit <= 0:
		return nil, errors.New("--limit must be greater than 0")
	}
	format, err := formats.resolve()
	if err != nil {
		return nil, err
	}
	state = strings.TrimSpace(state)

	return func(ctx context.Context, store *Store) error {
		if len(normalizedMessageIDs) > 0 {
			messages, err := store.ReadMessages(ctx, normalizedMessageIDs)
			if err != nil {
				return err
			}
			result := readMessageResult{
				Items:   messages,
				HasMore: false,
			}
			if format != outputFormatText {
				return a.writeStructuredOutput(format, result)
			}
			return a.writeReadMessageResultText(result)
		}

		if latest {
			deliveries, hasMore, err := store.ReadLatestDeliveries(ctx, normalizedAddresses, state, limit)
			if err != nil {
				return err
			}
			result := readDeliveryResult{
				Items:   deliveries,
				HasMore: hasMore,
			}
			if format != outputFormatText {
				return a.writeStructuredOutput(format, result)
			}
			return a.writeReadDeliveryResultText(result)
		}

		deliveries, err := store.ReadDeliveries(ctx, normalizedDeliveryIDs)
		if err != nil {
			return err
		}
		result := readDeliveryResult{
			Items:   deliveries,
			HasMore: false,
		}
		if format != outputFormatText {
			return a.writeStructuredOutput(format, result)
		}
		return a.writeReadDeliveryResultText(result)
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
		return a.writeDeliveryTransitionResultText(result)
	}, nil
}

func (a *App) prepareRenewCommand(args []string) (preparedCommand, error) {
	fs := flag.NewFlagSet("agent-mailbox renew", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	var deliveryID string
	var leaseToken string
	var extendBy time.Duration
	fs.StringVar(&deliveryID, "delivery", "", "delivery id")
	fs.StringVar(&leaseToken, "lease-token", "", "lease token")
	fs.DurationVar(&extendBy, "for", 0, "lease extension duration")

	if err := a.parseCommandFlags(fs, args, a.writeRenewHelp); err != nil {
		return nil, err
	}
	if err := requireFlag(deliveryID, "--delivery"); err != nil {
		return nil, err
	}
	if err := requireFlag(leaseToken, "--lease-token"); err != nil {
		return nil, err
	}
	if extendBy <= 0 {
		return nil, errors.New("--for must be greater than 0")
	}

	return func(ctx context.Context, store *Store) error {
		result, err := store.Renew(ctx, deliveryID, leaseToken, extendBy)
		if err != nil {
			return err
		}
		return a.writeLeaseRenewResultText(result)
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
		return a.writeDeliveryTransitionResultText(result)
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
		return a.writeDeliveryTransitionResultText(result)
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
		return a.writeDeliveryTransitionResultText(result)
	}, nil
}

func requireFlag(value, name string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s is required", name)
	}
	return nil
}

func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func normalizeAddresses(address string, addresses []string, flagName string) ([]string, error) {
	values := make([]string, 0, len(addresses)+1)
	if address != "" {
		values = append(values, address)
	}
	values = append(values, addresses...)

	return normalizeFlagValues(values, flagName)
}

func normalizeFlagValues(values []string, flagName string) ([]string, error) {
	if len(values) == 0 {
		return nil, fmt.Errorf("%s is required", flagName)
	}

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
		"  read                Read one persisted personal message or delivery",
		"  list                List deliveries",
		"  stale               List stale inbox views",
		"  group               Manage group mailboxes",
		"  address             Inspect address bindings",
		"  ack                 Acknowledge a leased delivery",
		"  renew               Extend a leased delivery",
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
		"  agent-mailbox send --to ADDRESS --body-file PATH [options] [--json | --yaml] [--full]",
		"",
		"Options:",
		"  --to ADDRESS           Recipient address",
		"  --from ADDRESS         Sender address",
		"  --group                Send to a known group address",
		"  --subject TEXT         Message subject",
		"  --content-type TYPE    Message content type",
		"  --schema-version VER   Sender-defined schema version",
		"  --body-file PATH|-     Read body from a file or stdin",
		"  --json                 Emit JSON",
		"  --yaml                 Emit YAML",
		"  --full                 Emit the full payload",
	})
}

func (a *App) writeListHelp() {
	writeHelp(a.stdout, []string{
		"Usage:",
		"  agent-mailbox list --for ADDRESS [--state STATE] [--json | --yaml]",
		"  agent-mailbox list --for GROUP_ADDRESS --as PERSON [--json | --yaml]",
		"",
		"Options:",
		"  --for ADDRESS      Recipient address",
		"  --as PERSON        Group reader identity",
		"  --state STATE      Filter by delivery state (queued, leased, acked, dead_letter)",
		"  --json             Emit JSON",
		"  --yaml             Emit YAML",
	})
}

func (a *App) writeStaleHelp() {
	writeHelp(a.stdout, []string{
		"Usage:",
		"  agent-mailbox stale --for ADDRESS [--for ADDRESS ...] --older-than DURATION [--json | --yaml]",
		"  agent-mailbox stale --for GROUP_ADDRESS --as PERSON --older-than DURATION [--json | --yaml]",
		"",
		"Options:",
		"  --for ADDRESS        Recipient address (repeatable)",
		"  --as PERSON          Group reader identity",
		"  --older-than DUR     Minimum receivable age before an inbox is stale",
		"  --json               Emit JSON",
		"  --yaml               Emit YAML",
	})
}

func (a *App) writeGroupHelp() {
	writeHelp(a.stdout, []string{
		"Usage:",
		"  agent-mailbox group <subcommand> [options]",
		"",
		"Subcommands:",
		"  create              Create a group address",
		"  add-member          Add a person to a group",
		"  remove-member       Remove a person from a group",
		"  members             List current and historical memberships",
	})
}

func (a *App) writeGroupCreateHelp() {
	writeHelp(a.stdout, []string{
		"Usage:",
		"  agent-mailbox group create --group ADDRESS [--json | --yaml]",
		"",
		"Options:",
		"  --group ADDRESS     Group address",
		"  --json              Emit JSON",
		"  --yaml              Emit YAML",
	})
}

func (a *App) writeGroupAddMemberHelp() {
	writeHelp(a.stdout, []string{
		"Usage:",
		"  agent-mailbox group add-member --group ADDRESS --person PERSON [--json | --yaml]",
		"",
		"Options:",
		"  --group ADDRESS     Group address",
		"  --person PERSON     Person identity",
		"  --json              Emit JSON",
		"  --yaml              Emit YAML",
	})
}

func (a *App) writeGroupRemoveMemberHelp() {
	writeHelp(a.stdout, []string{
		"Usage:",
		"  agent-mailbox group remove-member --group ADDRESS --person PERSON [--json | --yaml]",
		"",
		"Options:",
		"  --group ADDRESS     Group address",
		"  --person PERSON     Person identity",
		"  --json              Emit JSON",
		"  --yaml              Emit YAML",
	})
}

func (a *App) writeGroupMembersHelp() {
	writeHelp(a.stdout, []string{
		"Usage:",
		"  agent-mailbox group members --group ADDRESS [--json | --yaml]",
		"",
		"Options:",
		"  --group ADDRESS     Group address",
		"  --json              Emit JSON",
		"  --yaml              Emit YAML",
	})
}

func (a *App) writeRecvHelp() {
	writeHelp(a.stdout, []string{
		"Usage:",
		"  agent-mailbox recv --for ADDRESS [--for ADDRESS ...] [--max COUNT] [--json | --yaml] [--full]",
		"  agent-mailbox recv --for GROUP_ADDRESS --as PERSON [--json | --yaml] [--full]",
		"",
		"Options:",
		"  --for ADDRESS        Recipient address (repeatable)",
		"  --as PERSON          Group reader identity",
		"  --max COUNT          Maximum number of deliveries to claim (up to 10)",
		"  --json               Emit JSON",
		"  --yaml               Emit YAML",
		"  --full               Emit the full payload",
	})
}

func (a *App) writeReadHelp() {
	writeHelp(a.stdout, []string{
		"Usage:",
		"  agent-mailbox read --message ID [--message ID ...] [--json | --yaml]",
		"  agent-mailbox read --delivery ID [--delivery ID ...] [--json | --yaml]",
		"  agent-mailbox read --latest --for ADDRESS [--for ADDRESS ...] [--state STATE] [--limit N] [--json | --yaml]",
		"",
		"Options:",
		"  --message ID        Message id to read (repeatable)",
		"  --delivery ID       Delivery id to read (repeatable)",
		"  --latest            Read the latest deliveries for one or more inboxes",
		"  --for ADDRESS       Recipient address for --latest (repeatable)",
		"  --state STATE       Optional delivery state filter for --latest (defaults to any)",
		"  --limit N           Maximum number of latest deliveries to read",
		"  --json              Emit JSON",
		"  --yaml              Emit YAML",
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
		"  --timeout DURATION   Maximum idle time before watch exits (for example 30s, 5m, 120ms, 1m30s)",
		"  --json               Emit NDJSON",
		"  --yaml               Emit a YAML document stream",
	})
}

func (a *App) writeWaitHelp() {
	writeHelp(a.stdout, []string{
		"Usage:",
		"  agent-mailbox wait --for ADDRESS [--for ADDRESS ...] [--timeout DURATION] [--json | --yaml] [--full]",
		"  agent-mailbox wait --for GROUP_ADDRESS --as PERSON [--timeout DURATION] [--json | --yaml] [--full]",
		"",
		"Options:",
		"  --for ADDRESS        Recipient address (repeatable)",
		"  --as PERSON          Group reader identity",
		"  --timeout DURATION   Maximum time to wait for a matching delivery (for example 30s, 5m, 120ms, 1m30s)",
		"  --json               Emit JSON",
		"  --yaml               Emit YAML",
		"  --full               Emit the full payload",
	})
}

func (a *App) writeAckHelp() {
	writeHelp(a.stdout, []string{
		"Usage:",
		"  agent-mailbox ack --delivery ID --lease-token TOKEN",
	})
}

func (a *App) writeRenewHelp() {
	writeHelp(a.stdout, []string{
		"Usage:",
		"  agent-mailbox renew --delivery ID --lease-token TOKEN --for DURATION",
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

func (a *App) writeAddressHelp() {
	writeHelp(a.stdout, []string{
		"Usage:",
		"  agent-mailbox address <subcommand> [options]",
		"",
		"Subcommands:",
		"  inspect             Inspect endpoint/group binding for one address",
	})
}

func (a *App) writeAddressInspectHelp() {
	writeHelp(a.stdout, []string{
		"Usage:",
		"  agent-mailbox address inspect --address ADDRESS [--json | --yaml]",
		"",
		"Options:",
		"  --address ADDRESS   Address to inspect",
		"  --json              Emit JSON",
		"  --yaml              Emit YAML",
	})
}

func writeHelp(w io.Writer, lines []string) {
	for _, line := range lines {
		fmt.Fprintln(w, line)
	}
}

func (a *App) prepareGroupCommand(args []string) (preparedCommand, error) {
	if len(args) == 0 {
		return nil, errors.New("expected a group subcommand: create, add-member, remove-member, or members")
	}
	if isHelpArg(args[0]) {
		a.writeGroupHelp()
		return nil, ErrHelpRequested
	}

	switch args[0] {
	case "create":
		return a.prepareGroupCreateCommand(args[1:])
	case "add-member":
		return a.prepareGroupAddMemberCommand(args[1:])
	case "remove-member":
		return a.prepareGroupRemoveMemberCommand(args[1:])
	case "members":
		return a.prepareGroupMembersCommand(args[1:])
	default:
		return nil, fmt.Errorf("unknown group subcommand %q", args[0])
	}
}

func (a *App) prepareGroupCreateCommand(args []string) (preparedCommand, error) {
	fs := flag.NewFlagSet("agent-mailbox group create", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	var groupAddress string
	var formats outputFlags
	fs.StringVar(&groupAddress, "group", "", "group address")
	formats.register(fs, "emit JSON", "emit YAML")

	if err := a.parseCommandFlags(fs, args, a.writeGroupCreateHelp); err != nil {
		return nil, err
	}
	if err := requireFlag(groupAddress, "--group"); err != nil {
		return nil, err
	}
	format, err := formats.resolve()
	if err != nil {
		return nil, err
	}

	return func(ctx context.Context, store *Store) error {
		group, err := store.CreateGroup(ctx, groupAddress)
		if err != nil {
			return err
		}
		if format != outputFormatText {
			return a.writeStructuredOutput(format, group)
		}
		_, err = fmt.Fprintf(a.stdout, "group_id=%s address=%s\n", group.GroupID, group.Address)
		return err
	}, nil
}

func (a *App) prepareGroupAddMemberCommand(args []string) (preparedCommand, error) {
	fs := flag.NewFlagSet("agent-mailbox group add-member", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	var groupAddress string
	var person string
	var formats outputFlags
	fs.StringVar(&groupAddress, "group", "", "group address")
	fs.StringVar(&person, "person", "", "person identity")
	formats.register(fs, "emit JSON", "emit YAML")

	if err := a.parseCommandFlags(fs, args, a.writeGroupAddMemberHelp); err != nil {
		return nil, err
	}
	if err := requireFlag(groupAddress, "--group"); err != nil {
		return nil, err
	}
	if err := requireFlag(person, "--person"); err != nil {
		return nil, err
	}
	format, err := formats.resolve()
	if err != nil {
		return nil, err
	}

	return func(ctx context.Context, store *Store) error {
		membership, err := store.AddGroupMember(ctx, groupAddress, person)
		if err != nil {
			return err
		}
		if format != outputFormatText {
			return a.writeStructuredOutput(format, membership)
		}
		_, err = fmt.Fprintf(
			a.stdout,
			"membership_id=%s group=%s person=%s active=%t joined_at=%s\n",
			membership.MembershipID,
			membership.GroupAddress,
			membership.Person,
			membership.Active,
			membership.JoinedAt,
		)
		return err
	}, nil
}

func (a *App) prepareGroupRemoveMemberCommand(args []string) (preparedCommand, error) {
	fs := flag.NewFlagSet("agent-mailbox group remove-member", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	var groupAddress string
	var person string
	var formats outputFlags
	fs.StringVar(&groupAddress, "group", "", "group address")
	fs.StringVar(&person, "person", "", "person identity")
	formats.register(fs, "emit JSON", "emit YAML")

	if err := a.parseCommandFlags(fs, args, a.writeGroupRemoveMemberHelp); err != nil {
		return nil, err
	}
	if err := requireFlag(groupAddress, "--group"); err != nil {
		return nil, err
	}
	if err := requireFlag(person, "--person"); err != nil {
		return nil, err
	}
	format, err := formats.resolve()
	if err != nil {
		return nil, err
	}

	return func(ctx context.Context, store *Store) error {
		membership, err := store.RemoveGroupMember(ctx, groupAddress, person)
		if err != nil {
			return err
		}
		if format != outputFormatText {
			return a.writeStructuredOutput(format, membership)
		}
		leftAt := ""
		if membership.LeftAt != nil {
			leftAt = *membership.LeftAt
		}
		_, err = fmt.Fprintf(
			a.stdout,
			"membership_id=%s group=%s person=%s active=%t joined_at=%s left_at=%s\n",
			membership.MembershipID,
			membership.GroupAddress,
			membership.Person,
			membership.Active,
			membership.JoinedAt,
			leftAt,
		)
		return err
	}, nil
}

func (a *App) prepareGroupMembersCommand(args []string) (preparedCommand, error) {
	fs := flag.NewFlagSet("agent-mailbox group members", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	var groupAddress string
	var formats outputFlags
	fs.StringVar(&groupAddress, "group", "", "group address")
	formats.register(fs, "emit JSON", "emit YAML")

	if err := a.parseCommandFlags(fs, args, a.writeGroupMembersHelp); err != nil {
		return nil, err
	}
	if err := requireFlag(groupAddress, "--group"); err != nil {
		return nil, err
	}
	format, err := formats.resolve()
	if err != nil {
		return nil, err
	}

	return func(ctx context.Context, store *Store) error {
		memberships, err := store.ListGroupMembers(ctx, groupAddress)
		if err != nil {
			return err
		}
		if format != outputFormatText {
			return a.writeStructuredOutput(format, memberships)
		}
		for _, membership := range memberships {
			leftAt := ""
			if membership.LeftAt != nil {
				leftAt = *membership.LeftAt
			}
			if _, err := fmt.Fprintf(
				a.stdout,
				"membership_id=%s person=%s active=%t joined_at=%s left_at=%s\n",
				membership.MembershipID,
				membership.Person,
				membership.Active,
				membership.JoinedAt,
				leftAt,
			); err != nil {
				return err
			}
		}
		return nil
	}, nil
}

func (a *App) prepareAddressCommand(args []string) (preparedCommand, error) {
	if len(args) == 0 {
		return nil, errors.New("expected an address subcommand: inspect")
	}
	if isHelpArg(args[0]) {
		a.writeAddressHelp()
		return nil, ErrHelpRequested
	}

	switch args[0] {
	case "inspect":
		return a.prepareAddressInspectCommand(args[1:])
	default:
		return nil, fmt.Errorf("unknown address subcommand %q", args[0])
	}
}

func (a *App) prepareAddressInspectCommand(args []string) (preparedCommand, error) {
	fs := flag.NewFlagSet("agent-mailbox address inspect", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	var address string
	var formats outputFlags
	fs.StringVar(&address, "address", "", "address to inspect")
	formats.register(fs, "emit JSON", "emit YAML")

	if err := a.parseCommandFlags(fs, args, a.writeAddressInspectHelp); err != nil {
		return nil, err
	}
	if err := requireFlag(address, "--address"); err != nil {
		return nil, err
	}
	format, err := formats.resolve()
	if err != nil {
		return nil, err
	}

	return func(ctx context.Context, store *Store) error {
		inspection, err := store.InspectAddress(ctx, address)
		if err != nil {
			return err
		}
		if format != outputFormatText {
			return a.writeStructuredOutput(format, inspection)
		}
		switch inspection.Kind {
		case AddressKindEndpoint:
			_, err = fmt.Fprintf(a.stdout, "address=%s kind=%s endpoint_id=%s\n", inspection.Address, inspection.Kind, valueOrEmpty(inspection.EndpointID))
		case AddressKindGroup:
			_, err = fmt.Fprintf(a.stdout, "address=%s kind=%s group_id=%s\n", inspection.Address, inspection.Kind, valueOrEmpty(inspection.GroupID))
		default:
			_, err = fmt.Fprintf(a.stdout, "address=%s kind=%s\n", inspection.Address, inspection.Kind)
		}
		return err
	}, nil
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
