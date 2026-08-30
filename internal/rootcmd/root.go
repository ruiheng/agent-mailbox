package rootcmd

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/ruiheng/waypost/internal/codexhook"
	"github.com/ruiheng/waypost/internal/mcpserver"
	"github.com/ruiheng/waypost/internal/version"
	"github.com/ruiheng/waypost/internal/waypost"
	"github.com/ruiheng/waypost/internal/webui"
)

type App struct {
	stdin                               io.Reader
	stdout                              io.Writer
	stderr                              io.Writer
	runMCP                              func(context.Context, mcpserver.Options) error
	runWeb                              func(context.Context, webui.Options) error
	currentDirectoryWaypostMCPAvailable func(context.Context) (bool, error)
	notifyWaypostSend                   waypost.SendNotifier
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
		runWeb:                              webui.Run,
		currentDirectoryWaypostMCPAvailable: codexhook.CurrentDirectoryWaypostMCPAvailable,
		notifyWaypostSend:                   mcpserver.NotifyWaypostSend,
	}
}

func (a *App) Run(ctx context.Context, args []string) error {
	stateDir, rest, helpRequested, versionRequested, err := parseGlobalArgs(args)
	if err != nil {
		return err
	}
	if helpRequested {
		a.writeRootHelp()
		return waypost.ErrHelpRequested
	}
	if versionRequested {
		_, err := fmt.Fprintf(a.stdout, "waypost %s\n", version.Version)
		return err
	}
	if len(rest) == 0 {
		return errors.New("expected a command: mcp, codex-hook, install, doctor, migrate, doc, send, forward, recv, wait, watch, read, ack, renew, release, defer, undefer, fail, dead-letter, list, stale, group, or address")
	}
	if rest[0] == "codex-hook" {
		return a.runCodexHookCommand(ctx, rest[1:])
	}
	if rest[0] == "install" {
		return a.runInstallCommand(rest[1:])
	}
	if rest[0] == "doctor" {
		return a.runDoctorCommand(ctx, rest[1:])
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
	return waypost.NewAppWithOptions(a.stdin, a.stdout, a.stderr, waypost.AppOptions{
		SendNotifier: a.notifyWaypostSend,
	}).RunWithStateDir(ctx, stateDir, forwarded)
}

func (a *App) runCodexHookCommand(ctx context.Context, args []string) error {
	if len(args) == 1 && isHelpArg(args[0]) {
		a.writeCodexHookHelp()
		return waypost.ErrHelpRequested
	}
	if len(args) != 0 {
		return fmt.Errorf("codex-hook does not accept arguments")
	}
	return codexhook.Run(ctx, a.stdin, a.stdout)
}

func (a *App) runInstallCommand(args []string) error {
	if len(args) == 0 || isHelpArg(args[0]) {
		a.writeInstallHelp()
		return waypost.ErrHelpRequested
	}
	if args[0] != "codex-hook" {
		return fmt.Errorf("unknown install target %q; expected codex-hook", args[0])
	}
	if len(args) == 2 && isHelpArg(args[1]) {
		a.writeInstallCodexHookHelp()
		return waypost.ErrHelpRequested
	}
	if len(args) != 1 {
		return fmt.Errorf("install codex-hook does not accept arguments")
	}

	home, err := codexhook.DefaultHome()
	if err != nil {
		return err
	}
	command, err := codexhook.CurrentCommand()
	if err != nil {
		return err
	}
	result, err := codexhook.Install(home, command)
	if err != nil {
		return err
	}
	status := "installed"
	if !result.Changed {
		status = "already installed"
	}
	_, err = fmt.Fprintf(a.stdout, "Codex hooks %s: %s\nCodex hook trust: review the Waypost hooks with `/hooks` in Codex before use\n", status, result.Path)
	return err
}

func (a *App) runDoctorCommand(ctx context.Context, args []string) error {
	if len(args) == 0 || isHelpArg(args[0]) {
		a.writeDoctorHelp()
		return waypost.ErrHelpRequested
	}
	if args[0] != "codex-hook" {
		return fmt.Errorf("unknown doctor target %q; expected codex-hook", args[0])
	}
	if len(args) == 2 && isHelpArg(args[1]) {
		a.writeDoctorCodexHookHelp()
		return waypost.ErrHelpRequested
	}
	if len(args) != 1 {
		return fmt.Errorf("doctor codex-hook does not accept arguments")
	}

	home, err := codexhook.DefaultHome()
	if err != nil {
		return err
	}
	command, err := codexhook.CurrentCommand()
	if err != nil {
		return err
	}
	result, err := codexhook.Doctor(home, command)
	if err != nil {
		return err
	}
	mcpStatus := "not available to a new Codex process in the current directory (an already-running session, profile, or `-c` override may differ)"
	available, probeErr := a.currentDirectoryWaypostMCPAvailable(ctx)
	if probeErr != nil {
		mcpStatus = fmt.Sprintf("availability to a new Codex process in the current directory is unknown: %v (an already-running session, profile, or `-c` override may differ)", probeErr)
	} else if available {
		mcpStatus = "available to a new Codex process in the current directory (an already-running session, profile, or `-c` override may differ)"
	}
	_, err = fmt.Fprintf(a.stdout, "Codex compact hook: configured\nCodex nudge hook: configured\nCodex wait polling guard: configured\nCodex receive completion tracker: configured\nCodex nudge state cleanup: configured\nCodex hook trust: not checked; verify with `/hooks` in Codex\nWaypost MCP: %s\nHooks file: %s\nCommand: %s\n", mcpStatus, result.Path, result.Command)
	return err
}

func parseGlobalArgs(args []string) (string, []string, bool, bool, error) {
	fs := flag.NewFlagSet("waypost", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	var stateDir string
	var versionRequested bool
	fs.StringVar(&stateDir, "state-dir", "", "override waypost state directory")
	fs.BoolVar(&versionRequested, "version", false, "show version")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return "", nil, true, false, nil
		}
		return "", nil, false, false, waypost.MarkInvalidArgument(err)
	}
	return stateDir, fs.Args(), false, versionRequested, nil
}

func (a *App) runMCPCommand(ctx context.Context, stateDir string, args []string) error {
	fs := flag.NewFlagSet("waypost mcp", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	// Keep the historical launcher flag as inert syntax compatibility. The
	// value is deliberately discarded and never opened, statted, parsed, or
	// passed into the MCP service.
	fs.Func("session-host-config", "deprecated; accepted and ignored", func(string) error { return nil })
	var includeDebugTool bool
	fs.BoolVar(&includeDebugTool, "include-debug-tool", false, "register the optional waypost_debug diagnostic tool")
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
		StateDir:         stateDir,
		IncludeDebugTool: includeDebugTool,
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
		return waypost.MarkInvalidArgument(err)
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
		"  codex-hook          Emit Codex Waypost hook context",
		"  install             Install an optional integration",
		"  doctor              Diagnose an optional integration",
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
		"  dead-letter         Stop retrying a leased delivery",
		"",
		"Global options:",
		"  --state-dir PATH    Override waypost state directory",
		"  --version           Show version",
		"  --help              Show help",
		"",
		"Use \"waypost <command> --help\" for command-specific details.",
	})
}

func (a *App) writeCodexHookHelp() {
	writeHelp(a.stdout, []string{
		"Usage:",
		"  waypost codex-hook",
		"",
		"Track pending and consumed Waypost nudges for the current Codex session.",
		"For a Waypost nudge on UserPromptSubmit, probe `codex mcp get waypost --json` and",
		"emit one receive instruction: waypost_recv when available, CLI otherwise.",
		"After a successful receive, SessionStart compact emits an anti-repeat guard.",
		"For PreToolUse Bash calls, warn before waypost wait; when MCP is available,",
		"deny waypost status, recv, receive, and send in favor of Waypost MCP tools.",
	})
}

func (a *App) writeInstallHelp() {
	writeHelp(a.stdout, []string{
		"Usage:",
		"  waypost install codex-hook",
		"",
		"Install optional Waypost integrations.",
	})
}

func (a *App) writeInstallCodexHookHelp() {
	writeHelp(a.stdout, []string{
		"Usage:",
		"  waypost install codex-hook",
		"",
		"Merge Codex nudge lifecycle, compact guard, receive tracking, and wait hooks",
		"into $CODEX_HOME/hooks.json.",
		"The command is idempotent and preserves unrelated hooks.",
		"Review new or changed non-managed hooks with `/hooks` in Codex before use.",
	})
}

func (a *App) writeDoctorHelp() {
	writeHelp(a.stdout, []string{
		"Usage:",
		"  waypost doctor codex-hook",
		"",
		"Diagnose optional Waypost integrations.",
	})
}

func (a *App) writeDoctorCodexHookHelp() {
	writeHelp(a.stdout, []string{
		"Usage:",
		"  waypost doctor codex-hook",
		"",
		"Verify all Codex hook definitions and report MCP availability for a new Codex process in the current directory.",
		"Codex hook trust must be verified interactively with `/hooks`.",
	})
}

func (a *App) writeMCPHelp() {
	writeHelp(a.stdout, []string{
		"Usage:",
		"  waypost mcp [--session-host-config ABSOLUTE_PATH] [--include-debug-tool]",
		"",
		"Run the built-in stdio MCP server using the main waypost binary.",
		"",
		"Options:",
		"  --session-host-config ABSOLUTE_PATH    deprecated; accepted and ignored",
		"  --include-debug-tool                    register the optional waypost_debug diagnostic tool",
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
