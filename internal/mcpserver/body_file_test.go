package mcpserver

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestReadWaypostSendBodyFileEnforcesWorkspaceBoundary(t *testing.T) {
	workdir := t.TempDir()
	insideFile := filepath.Join(workdir, "inside.md")
	if err := os.WriteFile(insideFile, []byte("inside body"), 0o600); err != nil {
		t.Fatalf("WriteFile(inside) error = %v", err)
	}
	body, resolved, err := readWaypostSendBodyFile("inside.md", workdir)
	if err != nil {
		t.Fatalf("readWaypostSendBodyFile(inside) error = %v", err)
	}
	if string(body) != "inside body" {
		t.Fatalf("inside body = %q, want inside body", body)
	}
	wantResolved, err := filepath.EvalSymlinks(insideFile)
	if err != nil {
		t.Fatalf("EvalSymlinks(inside) error = %v", err)
	}
	if resolved != wantResolved {
		t.Fatalf("resolved body_file = %q, want %q", resolved, wantResolved)
	}

	outsideDir := t.TempDir()
	outsideFile := filepath.Join(outsideDir, "outside.md")
	if err := os.WriteFile(outsideFile, []byte("outside body"), 0o600); err != nil {
		t.Fatalf("WriteFile(outside) error = %v", err)
	}
	largeFile := filepath.Join(workdir, "large.md")
	large, err := os.OpenFile(largeFile, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("OpenFile(large) error = %v", err)
	}
	if err := large.Truncate(maxWaypostSendBodyFileBytes + 1); err != nil {
		_ = large.Close()
		t.Fatalf("Truncate(large) error = %v", err)
	}
	if err := large.Close(); err != nil {
		t.Fatalf("Close(large) error = %v", err)
	}

	tests := []struct {
		name            string
		bodyFile        string
		defaultWorkdir  string
		wantErrorSubstr string
	}{
		{name: "missing default workdir", bodyFile: insideFile, wantErrorSubstr: "requires a bound default_workdir"},
		{name: "relative workdir", bodyFile: insideFile, defaultWorkdir: "relative-workdir", wantErrorSubstr: "must be absolute"},
		{name: "missing workdir", bodyFile: insideFile, defaultWorkdir: filepath.Join(workdir, "missing"), wantErrorSubstr: "resolve waypost_send default_workdir"},
		{name: "absolute outside", bodyFile: outsideFile, defaultWorkdir: workdir, wantErrorSubstr: "outside bound default_workdir"},
		{name: "relative escape", bodyFile: filepath.Join("..", filepath.Base(outsideDir), filepath.Base(outsideFile)), defaultWorkdir: workdir, wantErrorSubstr: "outside bound default_workdir"},
		{name: "directory", bodyFile: workdir, defaultWorkdir: workdir, wantErrorSubstr: "is not a regular file"},
		{name: "oversized", bodyFile: largeFile, defaultWorkdir: workdir, wantErrorSubstr: "exceeds the"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := readWaypostSendBodyFile(test.bodyFile, test.defaultWorkdir)
			if err == nil || !strings.Contains(err.Error(), test.wantErrorSubstr) {
				t.Fatalf("readWaypostSendBodyFile() error = %v, want containing %q", err, test.wantErrorSubstr)
			}
		})
	}

	t.Run("symlink escape", func(t *testing.T) {
		link := filepath.Join(workdir, "outside-link.md")
		if err := os.Symlink(outsideFile, link); err != nil {
			if runtime.GOOS == "windows" {
				t.Skipf("symlink unavailable on Windows: %v", err)
			}
			t.Fatalf("Symlink(outside) error = %v", err)
		}
		_, _, err := readWaypostSendBodyFile(link, workdir)
		if err == nil || !strings.Contains(err.Error(), "resolves outside bound default_workdir") {
			t.Fatalf("readWaypostSendBodyFile(symlink escape) error = %v", err)
		}
	})

	if runtime.GOOS == "windows" {
		t.Run("alternate data stream", func(t *testing.T) {
			_, _, err := readWaypostSendBodyFile(insideFile+":hidden", workdir)
			if err == nil || !strings.Contains(err.Error(), "alternate data stream") {
				t.Fatalf("readWaypostSendBodyFile(ADS) error = %v", err)
			}
		})
	}
}
