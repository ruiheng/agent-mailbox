package rootcmd

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ruiheng/waypost/internal/mcpserver"
	"github.com/ruiheng/waypost/internal/waypost"
	"github.com/ruiheng/waypost/internal/webui"
)

func TestRunRootHelpIncludesMCP(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	app := New(strings.NewReader(""), &stdout, &bytes.Buffer{})

	err := app.Run(context.Background(), []string{"--help"})
	if !errors.Is(err, waypost.ErrHelpRequested) {
		t.Fatalf("Run(--help) error = %v, want ErrHelpRequested", err)
	}
	if !strings.Contains(stdout.String(), "mcp") {
		t.Fatalf("root help = %q, want mcp command", stdout.String())
	}
	if strings.Contains(stdout.String(), "  web") {
		t.Fatalf("root help = %q, want no top-level web command", stdout.String())
	}
	if !strings.Contains(stdout.String(), "--version") {
		t.Fatalf("root help = %q, want version option", stdout.String())
	}
}

func TestRunVersion(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	app := New(strings.NewReader(""), &stdout, &bytes.Buffer{})

	if err := app.Run(context.Background(), []string{"--version"}); err != nil {
		t.Fatalf("Run(--version) error = %v", err)
	}
	if got, want := stdout.String(), "waypost 0.5.0\n"; got != want {
		t.Fatalf("Run(--version) output = %q, want %q", got, want)
	}
}

func TestRunRootHelpIncludesForward(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	app := New(strings.NewReader(""), &stdout, &bytes.Buffer{})

	err := app.Run(context.Background(), []string{"--help"})
	if !errors.Is(err, waypost.ErrHelpRequested) {
		t.Fatalf("Run(--help) error = %v, want ErrHelpRequested", err)
	}
	if !strings.Contains(stdout.String(), "  forward             Forward a stored message or delivery") {
		t.Fatalf("root help = %q, want forward command", stdout.String())
	}
}

func TestRunWithoutCommandMentionsForward(t *testing.T) {
	t.Parallel()

	app := New(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})

	err := app.Run(context.Background(), nil)
	if err == nil {
		t.Fatal("Run() error = nil, want missing command error")
	}
	if !strings.Contains(err.Error(), "forward") {
		t.Fatalf("Run() error = %q, want forward in command list", err.Error())
	}
}

func TestRunMCPHelp(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	app := New(strings.NewReader(""), &stdout, &bytes.Buffer{})

	err := app.Run(context.Background(), []string{"mcp", "--help"})
	if !errors.Is(err, waypost.ErrHelpRequested) {
		t.Fatalf("Run(mcp --help) error = %v, want ErrHelpRequested", err)
	}
	if !strings.Contains(stdout.String(), "waypost mcp") {
		t.Fatalf("mcp help = %q, want usage text", stdout.String())
	}
	if !strings.Contains(stdout.String(), "--session-host-config") {
		t.Fatalf("mcp help = %q, want session-host configuration flag", stdout.String())
	}
	if !strings.Contains(stdout.String(), "deprecated; accepted and ignored") {
		t.Fatalf("mcp help = %q, want deprecated no-op wording", stdout.String())
	}
}

func TestRunMigrateMovesLegacyState(t *testing.T) {
	source := filepath.Join(t.TempDir(), "legacy-state")
	destination := filepath.Join(t.TempDir(), "waypost-state")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatalf("MkdirAll(source) error = %v", err)
	}

	var stdout bytes.Buffer
	app := New(strings.NewReader(""), &stdout, &bytes.Buffer{})
	if err := app.Run(context.Background(), []string{
		"--state-dir", destination,
		"migrate",
		"--from", source,
	}); err != nil {
		t.Fatalf("Run(migrate) error = %v", err)
	}
	if _, err := os.Lstat(destination); err != nil {
		t.Fatalf("migrated destination = %v, want directory", err)
	}
	if !strings.Contains(stdout.String(), "migrated legacy state") {
		t.Fatalf("migrate output = %q, want migration summary", stdout.String())
	}
}

func TestRunMCPInvokesRunner(t *testing.T) {
	t.Parallel()

	called := false
	app := &App{
		stdin:  strings.NewReader(""),
		stdout: &bytes.Buffer{},
		stderr: &bytes.Buffer{},
		runMCP: func(context.Context, mcpserver.Options) error {
			called = true
			return nil
		},
	}

	if err := app.Run(context.Background(), []string{"mcp"}); err != nil {
		t.Fatalf("Run(mcp) error = %v", err)
	}
	if !called {
		t.Fatal("Run(mcp) did not invoke MCP runner")
	}
}

func TestRunMCPForwardsStateDir(t *testing.T) {
	t.Parallel()

	var gotOptions mcpserver.Options
	app := &App{
		stdin:  strings.NewReader(""),
		stdout: &bytes.Buffer{},
		stderr: &bytes.Buffer{},
		runMCP: func(_ context.Context, options mcpserver.Options) error {
			gotOptions = options
			return nil
		},
	}

	stateDir := t.TempDir()
	if err := app.Run(context.Background(), []string{"--state-dir", stateDir, "mcp"}); err != nil {
		t.Fatalf("Run(--state-dir mcp) error = %v", err)
	}
	if gotOptions.StateDir != stateDir {
		t.Fatalf("mcp state dir = %q, want %q", gotOptions.StateDir, stateDir)
	}
}

func TestRunMCPAcceptsDeprecatedSessionHostConfigWithoutReading(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "does-not-exist.json")
	started := false
	var gotOptions mcpserver.Options
	app := &App{
		stdin:  strings.NewReader(""),
		stdout: &bytes.Buffer{},
		stderr: &bytes.Buffer{},
		runMCP: func(_ context.Context, options mcpserver.Options) error {
			started = true
			gotOptions = options
			return nil
		},
	}
	if err := app.Run(context.Background(), []string{"mcp", "--session-host-config", configPath}); err != nil {
		t.Fatalf("Run(mcp --session-host-config) error = %v", err)
	}
	if !started {
		t.Fatal("MCP server did not start with deprecated session-host flag")
	}
	if gotOptions.StateDir != "" {
		t.Fatalf("deprecated flag altered MCP options: %#v", gotOptions)
	}
}

func TestRunGroupWebForwardsOptions(t *testing.T) {
	t.Parallel()

	var got webui.Options
	app := &App{
		stdin:  strings.NewReader(""),
		stdout: &bytes.Buffer{},
		stderr: &bytes.Buffer{},
		runWeb: func(_ context.Context, opts webui.Options) error {
			got = opts
			return nil
		},
	}

	stateDir := t.TempDir()
	if err := app.Run(context.Background(), []string{
		"--state-dir", stateDir,
		"group",
		"web",
		"--listen", "127.0.0.1:0",
		"--group", "group/review",
	}); err != nil {
		t.Fatalf("Run(group web) error = %v", err)
	}
	if got.StateDir != stateDir {
		t.Fatalf("web state dir = %q, want %q", got.StateDir, stateDir)
	}
	if got.Listen != "127.0.0.1:0" {
		t.Fatalf("web listen = %q, want 127.0.0.1:0", got.Listen)
	}
	if got.Group != "group/review" {
		t.Fatalf("web group = %q, want group/review", got.Group)
	}
	if got.Stdin == nil {
		t.Fatal("web stdin = nil, want forwarded stdin")
	}
	if got.Interactive {
		t.Fatal("web interactive = true for non-terminal test stdin, want false")
	}
}

func TestInteractiveTerminalRequiresTerminalOutput(t *testing.T) {
	t.Parallel()

	input, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open dev null error = %v", err)
	}
	defer input.Close()

	if isInteractiveTerminal(input, &bytes.Buffer{}) {
		t.Fatal("interactive terminal = true with non-terminal stdout, want false")
	}
}

func TestRunGroupWebHelp(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	app := New(strings.NewReader(""), &stdout, &bytes.Buffer{})

	err := app.Run(context.Background(), []string{"group", "web", "--help"})
	if !errors.Is(err, waypost.ErrHelpRequested) {
		t.Fatalf("Run(group web --help) error = %v, want ErrHelpRequested", err)
	}
	if !strings.Contains(stdout.String(), "waypost [--state-dir PATH] group web") {
		t.Fatalf("group web help = %q, want usage text", stdout.String())
	}
}

func TestRunDelegatesWaypostCommandsWithStateDir(t *testing.T) {
	t.Parallel()

	stateDir := t.TempDir()
	var stdout bytes.Buffer
	app := New(strings.NewReader(""), &stdout, &bytes.Buffer{})

	err := app.Run(context.Background(), []string{
		"--state-dir", stateDir,
		"list",
		"--for", "workflow/reviewer/task-123",
		"--json",
	})
	if err != nil {
		t.Fatalf("Run(list) error = %v", err)
	}
	if stdout.String() != "{\n  \"items\": []\n}\n" {
		t.Fatalf("list output = %q, want empty paginated JSON object", stdout.String())
	}
}

func TestRunSendNotifyUsesConfiguredNotifier(t *testing.T) {
	t.Parallel()

	stateDir := t.TempDir()
	var stdout bytes.Buffer
	app := New(strings.NewReader("body\n"), &stdout, &bytes.Buffer{})
	app.notifyWaypostSend = func(_ context.Context, _ *waypost.Store, request waypost.SendNotificationRequest) waypost.SendNotificationOutcome {
		if request.Params.ToAddress != "agent-deck/coder" || request.Params.FromAddress != "agent-deck/supervisor" {
			t.Fatalf("notify request params = %+v", request.Params)
		}
		return waypost.SendNotificationOutcome{Status: "sent", Scheme: "agent-deck"}
	}

	err := app.Run(context.Background(), []string{
		"--state-dir", stateDir,
		"send",
		"--to", "agent-deck/coder",
		"--from", "agent-deck/supervisor",
		"--body-file", "-",
		"--notify",
		"--json",
	})
	if err != nil {
		t.Fatalf("Run(send --notify) error = %v", err)
	}
	if !strings.Contains(stdout.String(), `"notify_status": "sent"`) || !strings.Contains(stdout.String(), `"notify_scheme": "agent-deck"`) || !strings.Contains(stdout.String(), `"notify_error": null`) {
		t.Fatalf("send --notify output = %q", stdout.String())
	}
}
