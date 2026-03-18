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
		return errors.New("expected a command: endpoint, send, or list")
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
		return nil, errors.New("expected a command: endpoint, send, or list")
	}

	switch args[0] {
	case "endpoint":
		return a.prepareEndpointCommand(args[1:])
	case "send":
		return a.prepareSendCommand(args[1:])
	case "list":
		return a.prepareListCommand(args[1:])
	default:
		return nil, fmt.Errorf("unknown command %q", args[0])
	}
}

func parseGlobalArgs(args []string) (string, []string, error) {
	fs := flag.NewFlagSet("mailbox", flag.ContinueOnError)
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
	fs := flag.NewFlagSet("mailbox endpoint register", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	var alias string
	var kind string
	fs.StringVar(&alias, "alias", "", "endpoint alias")
	fs.StringVar(&kind, "kind", "", "endpoint kind")

	if err := fs.Parse(args); err != nil {
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
	fs := flag.NewFlagSet("mailbox send", flag.ContinueOnError)
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
	fs := flag.NewFlagSet("mailbox list", flag.ContinueOnError)
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
