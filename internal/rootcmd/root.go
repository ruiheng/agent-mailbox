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
	"github.com/ruiheng/agent-mailbox/internal/webui"
)

type App struct {
	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer
	runMCP func(context.Context, string) error
	runWeb func(context.Context, webui.Options) error
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
		runWeb: webui.Run,
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
		return errors.New("expected a command: mcp, web, send, forward, recv, wait, watch, read, ack, renew, release, defer, fail, list, stale, group, or address")
	}
	if rest[0] == "mcp" {
		return a.runMCPCommand(ctx, stateDir, rest[1:])
	}
	if rest[0] == "web" {
		return a.runWebCommand(ctx, stateDir, rest[1:])
	}

	forwarded := append([]string(nil), rest...)
	return mailbox.NewApp(a.stdin, a.stdout, a.stderr).RunWithStateDir(ctx, stateDir, forwarded)
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

func (a *App) runWebCommand(ctx context.Context, stateDir string, args []string) error {
	fs := flag.NewFlagSet("agent-mailbox web", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	var listen string
	var group string
	fs.StringVar(&listen, "listen", "127.0.0.1:8765", "HTTP listen address")
	fs.StringVar(&group, "group", "", "initial group address")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			a.writeWebHelp()
			return mailbox.ErrHelpRequested
		}
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("web does not accept positional arguments")
	}
	return a.runWeb(ctx, webui.Options{
		StateDir: stateDir,
		Listen:   listen,
		Group:    group,
		Stdout:   a.stdout,
	})
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
		"  web                 Run the local read-only group transcript UI",
		"  send                Send a message to an address",
		"  forward             Forward a stored message or delivery",
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

func (a *App) writeWebHelp() {
	writeHelp(a.stdout, []string{
		"Usage:",
		"  agent-mailbox [--state-dir PATH] web [--listen ADDRESS] [--group ADDRESS]",
		"",
		"Options:",
		"  --listen ADDRESS    HTTP listen address (default 127.0.0.1:8765)",
		"  --group ADDRESS     Initial group address",
		"  --help              Show help",
	})
}

func writeHelp(w io.Writer, lines []string) {
	for _, line := range lines {
		fmt.Fprintln(w, line)
	}
}
