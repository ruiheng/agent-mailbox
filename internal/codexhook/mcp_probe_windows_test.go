//go:build windows

package codexhook

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCurrentDirectoryWaypostMCPAvailableRunsWindowsCommandShim(t *testing.T) {
	writeWindowsCodexProbe(t, `@echo off
if not "%~1"=="mcp" exit /b 9
if not "%~2"=="get" exit /b 9
if not "%~3"=="waypost" exit /b 9
if not "%~4"=="--json" exit /b 9
echo {"name":"waypost","enabled":true}
`)

	available, err := CurrentDirectoryWaypostMCPAvailable(context.Background())
	if err != nil || !available {
		t.Fatalf("CurrentDirectoryWaypostMCPAvailable() = %v, %v; want available", available, err)
	}
}

func TestCurrentDirectoryWaypostMCPAvailableTreatsMissingServerAsUnavailableOnWindows(t *testing.T) {
	writeWindowsCodexProbe(t, `@echo off
1>&2 echo Error: No MCP server named 'waypost' found.
exit /b 1
`)

	available, err := CurrentDirectoryWaypostMCPAvailable(context.Background())
	if err != nil || available {
		t.Fatalf("CurrentDirectoryWaypostMCPAvailable() = %v, %v; want unavailable", available, err)
	}
}

func TestCurrentDirectoryWaypostMCPAvailableReportsCommandStderrOnWindows(t *testing.T) {
	writeWindowsCodexProbe(t, `@echo off
1>&2 echo invalid Codex MCP configuration
exit /b 9
`)

	available, err := CurrentDirectoryWaypostMCPAvailable(context.Background())
	if err == nil || !strings.Contains(err.Error(), "invalid Codex MCP configuration") {
		t.Fatalf("CurrentDirectoryWaypostMCPAvailable() = %v, %v; want stderr detail", available, err)
	}
}

func writeWindowsCodexProbe(t *testing.T, contents string) {
	t.Helper()
	binDir := t.TempDir()
	path := filepath.Join(binDir, "codex.cmd")
	if err := os.WriteFile(path, []byte(strings.ReplaceAll(contents, "\n", "\r\n")), 0o600); err != nil {
		t.Fatalf("WriteFile(codex.cmd) error = %v", err)
	}
	t.Setenv("PATH", binDir)
}
