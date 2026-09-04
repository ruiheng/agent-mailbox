//go:build !windows

package mcpinstall

import "os/exec"

func commandInvocation(name string, args ...string) (string, []string) {
	return name, args
}

func configureCommand(_ *exec.Cmd, _, _ string, _ []string) {}
