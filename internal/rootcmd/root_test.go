package rootcmd

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ruiheng/agent-mailbox/internal/mailbox"
)

func TestRunRootHelpIncludesMCP(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	app := New(strings.NewReader(""), &stdout, &bytes.Buffer{})

	err := app.Run(context.Background(), []string{"--help"})
	if !errors.Is(err, mailbox.ErrHelpRequested) {
		t.Fatalf("Run(--help) error = %v, want ErrHelpRequested", err)
	}
	if !strings.Contains(stdout.String(), "mcp") {
		t.Fatalf("root help = %q, want mcp command", stdout.String())
	}
}

func TestRunMCPHelp(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	app := New(strings.NewReader(""), &stdout, &bytes.Buffer{})

	err := app.Run(context.Background(), []string{"mcp", "--help"})
	if !errors.Is(err, mailbox.ErrHelpRequested) {
		t.Fatalf("Run(mcp --help) error = %v, want ErrHelpRequested", err)
	}
	if !strings.Contains(stdout.String(), "agent-mailbox mcp") {
		t.Fatalf("mcp help = %q, want usage text", stdout.String())
	}
}

func TestRunMCPInvokesRunner(t *testing.T) {
	t.Parallel()

	called := false
	app := &App{
		stdin:  strings.NewReader(""),
		stdout: &bytes.Buffer{},
		stderr: &bytes.Buffer{},
		runMCP: func(context.Context, string) error {
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

	var gotStateDir string
	app := &App{
		stdin:  strings.NewReader(""),
		stdout: &bytes.Buffer{},
		stderr: &bytes.Buffer{},
		runMCP: func(_ context.Context, stateDir string) error {
			gotStateDir = stateDir
			return nil
		},
	}

	stateDir := t.TempDir()
	if err := app.Run(context.Background(), []string{"--state-dir", stateDir, "mcp"}); err != nil {
		t.Fatalf("Run(--state-dir mcp) error = %v", err)
	}
	if gotStateDir != stateDir {
		t.Fatalf("mcp state dir = %q, want %q", gotStateDir, stateDir)
	}
}

func TestRunDelegatesMailboxCommandsWithStateDir(t *testing.T) {
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
	if stdout.String() != "[]\n" {
		t.Fatalf("list output = %q, want empty JSON array", stdout.String())
	}
}
