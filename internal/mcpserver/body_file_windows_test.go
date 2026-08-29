//go:build windows

package mcpserver

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadWaypostSendBodyFileRejectsWindowsJunctionEscape(t *testing.T) {
	workdir := t.TempDir()
	outsideDir := t.TempDir()
	outsideFile := filepath.Join(outsideDir, "outside.md")
	if err := os.WriteFile(outsideFile, []byte("outside body"), 0o600); err != nil {
		t.Fatalf("WriteFile(outside) error = %v", err)
	}

	junction := filepath.Join(workdir, "outside-junction")
	output, err := exec.Command("cmd.exe", "/c", "mklink", "/J", junction, outsideDir).CombinedOutput()
	if err != nil {
		t.Fatalf("create junction: %v; output = %q", err, output)
	}
	t.Cleanup(func() {
		if err := os.Remove(junction); err != nil && !os.IsNotExist(err) {
			t.Errorf("remove junction: %v", err)
		}
	})
	junctionFile := filepath.Join(junction, filepath.Base(outsideFile))
	if _, err := os.Stat(junctionFile); err != nil {
		t.Fatalf("stat file through junction: %v; mklink output = %q", err, output)
	}

	_, _, err = readWaypostSendBodyFile(junctionFile, workdir)
	if err == nil || !strings.Contains(err.Error(), "waypost_send body_file") {
		t.Fatalf("readWaypostSendBodyFile(junction escape) error = %v", err)
	}
}
