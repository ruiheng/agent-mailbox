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

func (a *App) Run(ctx context.Context, args []string) error {
	stateDir, rest, err := parseGlobalArgs(args)
	if err != nil {
		return err
	}
	if len(rest) == 0 {
		return errors.New("expected a command: endpoint, send, or list")
	}

	runtime, err := OpenRuntime(ctx, stateDir)
	if err != nil {
		return err
	}
	defer runtime.Close()

	switch rest[0] {
	case "endpoint":
		return a.runEndpoint(ctx, runtime.Store(), rest[1:])
	case "send":
		return a.runSend(ctx, runtime.Store(), rest[1:])
	case "list":
		return a.runList(ctx, runtime.Store(), rest[1:])
	default:
		return fmt.Errorf("unknown command %q", rest[0])
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

func (a *App) runEndpoint(ctx context.Context, store *Store, args []string) error {
	if len(args) == 0 {
		return errors.New("expected endpoint subcommand")
	}
	switch args[0] {
	case "register":
		return a.runEndpointRegister(ctx, store, args[1:])
	default:
		return fmt.Errorf("unknown endpoint subcommand %q", args[0])
	}
}

func (a *App) runEndpointRegister(ctx context.Context, store *Store, args []string) error {
	fs := flag.NewFlagSet("mailbox endpoint register", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	var alias string
	var kind string
	fs.StringVar(&alias, "alias", "", "endpoint alias")
	fs.StringVar(&kind, "kind", "", "endpoint kind")

	if err := fs.Parse(args); err != nil {
		return err
	}

	result, err := store.RegisterEndpoint(ctx, alias, kind)
	if err != nil {
		return err
	}

	fmt.Fprintf(a.stdout, "endpoint_id=%s alias=%s kind=%s created=%t\n", result.EndpointID, result.Alias, result.Kind, result.Created)
	return nil
}

func (a *App) runSend(ctx context.Context, store *Store, args []string) error {
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
		return err
	}

	body, err := a.readBody(bodyFile)
	if err != nil {
		return err
	}

	result, err := store.Send(ctx, SendParams{
		ToAlias:       toAlias,
		FromAlias:     fromAlias,
		Subject:       subject,
		ContentType:   contentType,
		SchemaVersion: schemaVersion,
		Body:          body,
	})
	if err != nil {
		return err
	}

	fmt.Fprintf(a.stdout, "message_id=%s delivery_id=%s blob_id=%s\n", result.MessageID, result.DeliveryID, result.BodyBlobRef)
	return nil
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

func (a *App) runList(ctx context.Context, store *Store, args []string) error {
	fs := flag.NewFlagSet("mailbox list", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	var alias string
	var jsonOutput bool
	var state string
	fs.StringVar(&alias, "for", "", "recipient alias")
	fs.BoolVar(&jsonOutput, "json", false, "emit JSON")
	fs.StringVar(&state, "state", "", "filter by delivery state")

	if err := fs.Parse(args); err != nil {
		return err
	}

	deliveries, err := store.List(ctx, ListParams{
		Alias: alias,
		State: state,
	})
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
}
