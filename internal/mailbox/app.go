package mailbox

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

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

func (a *App) Run(ctx context.Context, args []string) error {
	stateDir, rest, err := parseGlobalArgs(args)
	if err != nil {
		return err
	}
	if len(rest) == 0 {
		return errors.New("expected a command: endpoint, send, recv, ack, release, defer, fail, or list")
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
		return nil, errors.New("expected a command: endpoint, send, recv, ack, release, defer, fail, or list")
	}

	switch args[0] {
	case "endpoint":
		return a.prepareEndpointCommand(args[1:])
	case "send":
		return a.prepareSendCommand(args[1:])
	case "recv":
		return a.prepareRecvCommand(args[1:])
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

func parseGlobalArgs(args []string) (string, []string, error) {
	fs := flag.NewFlagSet("agent-mailbox", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	var stateDir string
	fs.StringVar(&stateDir, "state-dir", "", "override mailbox state directory")

	if err := fs.Parse(args); err != nil {
		return "", nil, err
	}
	return stateDir, fs.Args(), nil
}

func (a *App) prepareEndpointCommand(args []string) (preparedCommand, error) {
	if len(args) == 0 {
		return nil, errors.New("expected endpoint subcommand")
	}
	switch args[0] {
	case "register":
		return a.prepareEndpointRegister(args[1:])
	default:
		return nil, fmt.Errorf("unknown endpoint subcommand %q", args[0])
	}
}

func (a *App) prepareEndpointRegister(args []string) (preparedCommand, error) {
	fs := flag.NewFlagSet("agent-mailbox endpoint register", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	var alias string
	var kind string
	fs.StringVar(&alias, "alias", "", "endpoint alias")
	fs.StringVar(&kind, "kind", "", "endpoint kind")

	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if err := requireFlag(alias, "--alias"); err != nil {
		return nil, err
	}
	if err := requireFlag(kind, "--kind"); err != nil {
		return nil, err
	}

	return func(ctx context.Context, store *Store) error {
		result, err := store.RegisterEndpoint(ctx, alias, kind)
		if err != nil {
			return err
		}

		fmt.Fprintf(a.stdout, "endpoint_id=%s alias=%s kind=%s created=%t\n", result.EndpointID, result.Alias, result.Kind, result.Created)
		return nil
	}, nil
}

func (a *App) prepareSendCommand(args []string) (preparedCommand, error) {
	fs := flag.NewFlagSet("agent-mailbox send", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	var toAlias string
	var fromAlias string
	var subject string
	var contentType string
	var schemaVersion string
	var bodyFile string

	fs.StringVar(&toAlias, "to", "", "recipient alias")
	fs.StringVar(&fromAlias, "from", "", "sender alias")
	fs.StringVar(&subject, "subject", "", "message subject")
	fs.StringVar(&contentType, "content-type", "text/plain", "message content type")
	fs.StringVar(&schemaVersion, "schema-version", "v1", "sender-defined schema version")
	fs.StringVar(&bodyFile, "body-file", "", "path to message body, or - for stdin")

	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if err := requireFlag(toAlias, "--to"); err != nil {
		return nil, err
	}

	body, err := a.readBody(bodyFile)
	if err != nil {
		return nil, err
	}

	params := SendParams{
		ToAlias:       toAlias,
		FromAlias:     fromAlias,
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

	var alias string
	var jsonOutput bool
	var state string
	fs.StringVar(&alias, "for", "", "recipient alias")
	fs.BoolVar(&jsonOutput, "json", false, "emit JSON")
	fs.StringVar(&state, "state", "", "filter by delivery state")

	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if err := requireFlag(alias, "--for"); err != nil {
		return nil, err
	}

	params := ListParams{
		Alias: alias,
		State: state,
	}

	return func(ctx context.Context, store *Store) error {
		deliveries, err := store.List(ctx, params)
		if err != nil {
			return err
		}

		if jsonOutput {
			encoder := json.NewEncoder(a.stdout)
			encoder.SetIndent("", "  ")
			return encoder.Encode(deliveries)
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

	var alias string
	var wait bool
	var timeout time.Duration
	var jsonOutput bool

	fs.StringVar(&alias, "for", "", "recipient alias")
	fs.BoolVar(&wait, "wait", false, "wait for a claimable delivery")
	fs.DurationVar(&timeout, "timeout", 0, "maximum time to wait when --wait is set")
	fs.BoolVar(&jsonOutput, "json", false, "emit JSON")

	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if err := requireFlag(alias, "--for"); err != nil {
		return nil, err
	}
	if timeout < 0 {
		return nil, errors.New("--timeout must be greater than or equal to 0")
	}
	if flagWasProvided(fs, "timeout") && !wait {
		return nil, errors.New("--timeout requires --wait")
	}

	params := ReceiveParams{
		Alias:   alias,
		Wait:    wait,
		Timeout: timeout,
	}

	return func(ctx context.Context, store *Store) error {
		message, err := store.Receive(ctx, params)
		if err != nil {
			return err
		}

		if jsonOutput {
			encoder := json.NewEncoder(a.stdout)
			encoder.SetIndent("", "  ")
			return encoder.Encode(message)
		}

		fmt.Fprintf(a.stdout, "delivery_id=%s message_id=%s lease_token=%s lease_expires_at=%s subject=%q\n", message.DeliveryID, message.MessageID, message.LeaseToken, message.LeaseExpiresAt, message.Subject)
		fmt.Fprint(a.stdout, message.Body)
		if !strings.HasSuffix(message.Body, "\n") {
			fmt.Fprintln(a.stdout)
		}
		return nil
	}, nil
}

func (a *App) prepareAckCommand(args []string) (preparedCommand, error) {
	fs := flag.NewFlagSet("agent-mailbox ack", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	var deliveryID string
	var leaseToken string
	fs.StringVar(&deliveryID, "delivery", "", "delivery id")
	fs.StringVar(&leaseToken, "lease-token", "", "lease token")

	if err := fs.Parse(args); err != nil {
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

	if err := fs.Parse(args); err != nil {
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

	if err := fs.Parse(args); err != nil {
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

	if err := fs.Parse(args); err != nil {
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

func flagWasProvided(fs *flag.FlagSet, name string) bool {
	provided := false
	fs.Visit(func(current *flag.Flag) {
		if current.Name == name {
			provided = true
		}
	})
	return provided
}
