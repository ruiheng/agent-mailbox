//go:build windows

package mcpinstall

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteFileAtomicallyPreservesWindowsDACL(t *testing.T) {
	path := filepath.Join(t.TempDir(), configFileName)
	if err := os.WriteFile(path, []byte("old\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(config) error = %v", err)
	}
	if output, err := exec.Command("icacls.exe", path, "/inheritance:d").CombinedOutput(); err != nil {
		t.Fatalf("set config DACL: %v; output = %q", err, output)
	}
	before := windowsConfigACL(t, path)
	if err := writeFileAtomically(path, []byte("new\n"), 0o600); err != nil {
		t.Fatalf("writeFileAtomically() error = %v", err)
	}
	if after := windowsConfigACL(t, path); after != before {
		t.Fatalf("config DACL changed across replacement:\nbefore: %s\nafter:  %s", before, after)
	}
}

func windowsConfigACL(t *testing.T, path string) string {
	t.Helper()
	output, err := exec.Command("icacls.exe", path).CombinedOutput()
	if err != nil {
		t.Fatalf("read config DACL: %v; output = %q", err, output)
	}
	return strings.TrimSpace(strings.ReplaceAll(string(output), "\r\n", "\n"))
}
