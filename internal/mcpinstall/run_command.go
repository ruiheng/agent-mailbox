package mcpinstall

import (
	"bytes"
	"context"
	"os/exec"
)

func runCommand(ctx context.Context, name string, args ...string) (commandOutput, error) {
	commandName, commandArgs := commandInvocation(name, args...)
	command := exec.CommandContext(ctx, commandName, commandArgs...)
	configureCommand(command, name, commandName, commandArgs)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	stdout, err := command.Output()
	return commandOutput{stdout: stdout, stderr: stderr.Bytes()}, err
}
