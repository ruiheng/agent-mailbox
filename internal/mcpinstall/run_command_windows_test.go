//go:build windows

package mcpinstall

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestCommandInvocationUsesComSpecForBatchShim(t *testing.T) {
	t.Setenv("ComSpec", `C:\Windows\System32\cmd.exe`)

	name, args := commandInvocation(`C:\Program Files\node\codex.cmd`, "mcp", "get", "waypost")
	if name != `C:\Windows\System32\cmd.exe` {
		t.Fatalf("commandInvocation() name = %q, want ComSpec", name)
	}
	want := []string{"/d", "/s", "/c", `C:\Program Files\node\codex.cmd`, "mcp", "get", "waypost"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("commandInvocation() args = %#v, want %#v", args, want)
	}
	if got := windowsCommandLine(name, args); got != `C:\Windows\System32\cmd.exe /d /s /c ""C:\Program Files\node\codex.cmd" "mcp" "get" "waypost""` {
		t.Fatalf("windowsCommandLine() = %q, want nested quoted command", got)
	}
}

func TestCommandInvocationLeavesNativeExecutableUnchanged(t *testing.T) {
	name, args := commandInvocation(`C:\Program Files\node\codex.exe`, "mcp", "get")
	if name != `C:\Program Files\node\codex.exe` {
		t.Fatalf("commandInvocation() name = %q, want native executable", name)
	}
	want := []string{"mcp", "get"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("commandInvocation() args = %#v, want %#v", args, want)
	}
}

func TestRunCommandExecutesBatchShimWithSpaces(t *testing.T) {
	binDir := filepath.Join(t.TempDir(), "node installation with spaces")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	path := filepath.Join(binDir, "codex.cmd")
	contents := "@echo off\r\necho %~1 %~2 %~3 %~4\r\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("WriteFile(codex.cmd) error = %v", err)
	}

	output, err := runCommand(context.Background(), path, "mcp", "get", "waypost", "--json")
	if err != nil {
		t.Fatalf("runCommand() error = %v; stderr = %q", err, output.stderr)
	}
	if got := strings.TrimSpace(string(output.stdout)); got != "mcp get waypost --json" {
		t.Fatalf("runCommand() stdout = %q, want batch arguments", got)
	}
}
