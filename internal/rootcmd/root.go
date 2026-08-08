package rootcmd

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/ruiheng/waypost/internal/mcpserver"
	"github.com/ruiheng/waypost/internal/waypost"
	"github.com/ruiheng/waypost/internal/webui"
)

type App struct {
	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer
	runMCP func(context.Context, mcpserver.Options) error
	runWeb func(context.Context, webui.Options) error
}

func New(stdin io.Reader, stdout, stderr io.Writer) *App {
	return &App{
		stdin:  stdin,
		stdout: stdout,
		stderr: stderr,
		runMCP: func(ctx context.Context, options mcpserver.Options) error {
			service := mcpserver.NewService(options)
			defer service.Close()
			return service.Server().Run(ctx, &mcp.StdioTransport{})
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
		return waypost.ErrHelpRequested
	}
	if len(rest) == 0 {
		return errors.New("expected a command: mcp, migrate, doc, send, forward, recv, wait, watch, read, ack, renew, release, defer, undefer, fail, list, stale, group, or address")
	}
	if rest[0] == "migrate" {
		return a.runMigrateCommand(stateDir, rest[1:])
	}
	if rest[0] == "mcp" {
		return a.runMCPCommand(ctx, stateDir, rest[1:])
	}
	if rest[0] == "group" && len(rest) > 1 && rest[1] == "web" {
		return a.runGroupWebCommand(ctx, stateDir, rest[2:])
	}

	forwarded := append([]string(nil), rest...)
	return waypost.NewApp(a.stdin, a.stdout, a.stderr).RunWithStateDir(ctx, stateDir, forwarded)
}

func parseGlobalArgs(args []string) (string, []string, bool, error) {
	fs := flag.NewFlagSet("waypost", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	var stateDir string
	fs.StringVar(&stateDir, "state-dir", "", "override waypost state directory")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return "", nil, true, nil
		}
		return "", nil, false, err
	}
	return stateDir, fs.Args(), false, nil
}

func (a *App) runMCPCommand(ctx context.Context, stateDir string, args []string) error {
	fs := flag.NewFlagSet("waypost mcp", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	// Keep the historical launcher flag as inert syntax compatibility. The
	// value is deliberately discarded and never opened, statted, parsed, or
	// passed into the MCP service.
	fs.Func("session-host-config", "deprecated; accepted and ignored", func(string) error { return nil })
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			a.writeMCPHelp()
			return waypost.ErrHelpRequested
		}
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("mcp does not accept positional arguments")
	}

	return a.runMCP(ctx, mcpserver.Options{
		StateDir: stateDir,
	})
}

func (a *App) runMigrateCommand(stateDir string, args []string) error {
	fs := flag.NewFlagSet("waypost migrate", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	var sourceDir string
	fs.StringVar(&sourceDir, "from", "", "legacy state directory to migrate")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			a.writeMigrateHelp()
			return waypost.ErrHelpRequested
		}
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("migrate does not accept positional arguments")
	}

	result, err := waypost.MigrateLegacyState(stateDir, sourceDir)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(a.stdout, "migrated legacy state: %s -> %s\n", result.Source, result.Destination); err != nil {
		return err
	}
	if result.SourceRetained {
		_, err = fmt.Fprintf(a.stdout, "legacy source retained as a recovery copy: %s\n", result.Source)
	}
	return err
}

func (a *App) runGroupWebCommand(ctx context.Context, stateDir string, args []string) error {
	fs := flag.NewFlagSet("waypost group web", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	var listen string
	var group string
	fs.StringVar(&listen, "listen", "127.0.0.1:0", "HTTP listen address")
	fs.StringVar(&group, "group", "", "initial group address")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			a.writeGroupWebHelp()
			return waypost.ErrHelpRequested
		}
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("group web does not accept positional arguments")
	}
	return a.runWeb(ctx, webui.Options{
		StateDir:    stateDir,
		Listen:      listen,
		Group:       group,
		Stdin:       a.stdin,
		Stdout:      a.stdout,
		Interactive: isInteractiveTerminal(a.stdin, a.stdout),
	})
}

func isHelpArg(value string) bool {
	return value == "-h" || value == "--help"
}

func isInteractiveTerminal(stdin io.Reader, stdout io.Writer) bool {
	return isTerminalFile(stdin) && isTerminalFile(stdout)
}

func isTerminalFile(value any) bool {
	file, ok := value.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

func (a *App) writeRootHelp() {
	writeHelp(a.stdout, []string{
		"Usage:",
		"  waypost [--state-dir PATH] <command> [options]",
		"",
		"Commands:",
		"  mcp                 Run the built-in stdio MCP server",
		"  migrate             Move state from the previous default directory",
		"  doc                 Show concise agent workflow guidance",
		"  send                Send a message to an address",
		"  forward             Forward a stored message or delivery",
		"  recv                Claim the next delivery",
		"  wait                Wait for one delivery without claiming",
		"  watch               Observe deliveries without claiming",
		"  read                Read one persisted personal message or delivery",
		"  list                List deliveries",
		"  stale               List stale personal queues",
		"  group               Manage group wayposts",
		"  address             Inspect address bindings",
		"  ack                 Acknowledge a leased delivery",
		"  renew               Extend a leased delivery",
		"  release             Return a leased delivery to the queue",
		"  defer               Hide a leased delivery until a future time",
		"  undefer             Make a deferred queued delivery visible now",
		"  fail                Record a failed delivery attempt",
		"",
		"Global options:",
		"  --state-dir PATH    Override waypost state directory",
		"  --help              Show help",
		"",
		"Use \"waypost <command> --help\" for command-specific details.",
	})
}

func (a *App) writeMCPHelp() {
	writeHelp(a.stdout, []string{
		"Usage:",
		"  waypost mcp [--session-host-config ABSOLUTE_PATH]",
		"",
		"Run the built-in stdio MCP server using the main waypost binary.",
		"",
		"Options:",
		"  --session-host-config ABSOLUTE_PATH    deprecated; accepted and ignored",
	})
}

func (a *App) writeMigrateHelp() {
	writeHelp(a.stdout, []string{
		"Usage:",
		"  waypost [--state-dir PATH] migrate [--from LEGACY_PATH]",
		"",
		"Migrate one legacy local state directory to the current Waypost state path.",
		"Stop all previous-version processes before running this command.",
	})
}

func (a *App) writeGroupWebHelp() {
	writeHelp(a.stdout, []string{
		"Usage:",
		"  waypost [--state-dir PATH] group web [--listen ADDRESS] [--group ADDRESS]",
		"",
		"Options:",
		"  --listen ADDRESS    HTTP listen address (default 127.0.0.1:0)",
		"  --group ADDRESS     Initial group address",
		"  --help              Show help",
	})
}

func writeHelp(w io.Writer, lines []string) {
	for _, line := range lines {
		fmt.Fprintln(w, line)
	}
}
