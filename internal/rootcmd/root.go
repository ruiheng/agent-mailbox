package rootcmd

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/ruiheng/agent-mailbox/internal/mailbox"
	"github.com/ruiheng/agent-mailbox/internal/mcpserver"
)

type App struct {
	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer
	runMCP func(context.Context, string) error
}

func New(stdin io.Reader, stdout, stderr io.Writer) *App {
	return &App{
		stdin:  stdin,
		stdout: stdout,
		stderr: stderr,
		runMCP: func(ctx context.Context, stateDir string) error {
			server := mcpserver.New(mcpserver.Options{StateDir: stateDir})
			return server.Run(ctx, &mcp.StdioTransport{})
		},
	}
}

func (a *App) Run(ctx context.Context, args []string) error {
	stateDir, rest, helpRequested, err := parseGlobalArgs(args)
	if err != nil {
		return err
	}
	if helpRequested {
		a.writeRootHelp()
		return mailbox.ErrHelpRequested
	}
	if len(rest) == 0 {
		return errors.New("expected a command: mcp, send, recv, wait, watch, read, ack, renew, release, defer, fail, list, stale, group, or address")
	}
	if rest[0] == "mcp" {
		return a.runMCPCommand(ctx, stateDir, rest[1:])
	}

	forwarded := append([]string(nil), rest...)
	if stateDir != "" {
		forwarded = append([]string{"--state-dir", stateDir}, forwarded...)
	}
	return mailbox.NewApp(a.stdin, a.stdout, a.stderr).Run(ctx, forwarded)
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

func (a *App) runMCPCommand(ctx context.Context, stateDir string, args []string) error {
	if len(args) > 0 {
		if len(args) == 1 && isHelpArg(args[0]) {
			a.writeMCPHelp()
			return mailbox.ErrHelpRequested
		}
		return fmt.Errorf("mcp does not accept arguments")
	}
	return a.runMCP(ctx, stateDir)
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
		"  mcp                 Run the built-in stdio MCP server",
		"  send                Send a message to an address",
		"  recv                Claim the next delivery",
		"  wait                Wait for one delivery without claiming",
		"  watch               Observe deliveries without claiming",
		"  read                Read one persisted personal message or delivery",
		"  list                List deliveries",
		"  stale               List stale personal inboxes",
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

func (a *App) writeMCPHelp() {
	writeHelp(a.stdout, []string{
		"Usage:",
		"  agent-mailbox mcp",
		"",
		"Run the built-in stdio MCP server using the main agent-mailbox binary.",
	})
}

func writeHelp(w io.Writer, lines []string) {
	for _, line := range lines {
		fmt.Fprintln(w, line)
	}
}
