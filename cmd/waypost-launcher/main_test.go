package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ruiheng/waypost/internal/launchpath"
)

func TestNewChildCommandPublishesStableLauncherPath(t *testing.T) {
	t.Setenv(launchpath.StableExecutableEnv, "stale-value")

	cmd, err := newChildCommand("versioned-waypost", []string{"codex-hook"})
	if err != nil {
		t.Fatalf("newChildCommand() error = %v", err)
	}
	current, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable() error = %v", err)
	}
	current, err = filepath.Abs(current)
	if err != nil {
		t.Fatalf("filepath.Abs(current executable) error = %v", err)
	}

	var got string
	prefix := launchpath.StableExecutableEnv + "="
	for _, item := range cmd.Env {
		if strings.HasPrefix(item, prefix) {
			got = strings.TrimPrefix(item, prefix)
		}
	}
	if got != current {
		t.Fatalf("%s = %q, want current launcher %q", launchpath.StableExecutableEnv, got, current)
	}
}
