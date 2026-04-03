package mailbox

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"
)

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
		ops := NewOperations(store)
		if strings.TrimSpace(person) != "" {
			if strings.TrimSpace(state) != "" {
				return errors.New("--state is not supported with --as")
			}
			messages, err := ops.ListGroupMessages(ctx, GroupListParams{
				Address: address,
				Person:  person,
			})
			if err != nil {
				return err
			}
			if format != outputFormatText {
				summaries := make([]GroupListedMessageCompact, 0, len(messages))
				for _, message := range messages {
					summaries = append(summaries, CompactGroupListedMessage(message))
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

		deliveries, err := ops.List(ctx, params)
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
		ops := NewOperations(store)
		stale, err := ops.ListStaleAddresses(ctx, params)
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
		ops := NewOperations(store)
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
			return a.writeGroupReceiveOutput(format, full, message)
		}

		if !maxProvided {
			message, err := store.Receive(ctx, ReceiveParams{Addresses: normalizedAddresses})
			if err != nil {
				return err
			}
			return a.writeReceiveOutput(format, full, message)
		}

		result, err := ops.ReceiveBatch(ctx, params)
		if err != nil {
			return err
		}
		return a.writeReceiveBatchOutput(format, full, result)
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
		ops := NewOperations(store)
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
			return a.writeGroupWaitOutput(format, full, message)
		}

		delivery, err := ops.Wait(ctx, params)
		if err != nil {
			return err
		}
		return a.writeWaitOutput(format, full, delivery)
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
		ops := NewOperations(store)
		if len(normalizedMessageIDs) > 0 {
			messages, err := ops.ReadMessages(ctx, normalizedMessageIDs)
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
			deliveries, hasMore, err := ops.ReadLatestDeliveries(ctx, normalizedAddresses, state, limit)
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

		deliveries, err := ops.ReadDeliveries(ctx, normalizedDeliveryIDs)
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
