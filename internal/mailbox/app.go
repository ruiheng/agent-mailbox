package mailbox

import (
	"context"
	"errors"
	"fmt"
	"io"
)

var ErrHelpRequested = errors.New("help requested")

type App struct {
	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer
}

func NewApp(stdin io.Reader, stdout, stderr io.Writer) *App {
	return &App{
		stdin:  stdin,
		stdout: stdout,
		stderr: stderr,
	}
}

func (a *App) Run(ctx context.Context, args []string) error {
	return a.RunWithStateDir(ctx, "", args)
}

func (a *App) RunWithStateDir(ctx context.Context, stateDir string, args []string) error {
	if len(args) == 0 {
		return errors.New("expected a command: send, recv, wait, watch, read, ack, renew, release, defer, fail, list, stale, group, or address")
	}

	command, err := a.prepareCommand(args)
	if err != nil {
		return err
	}

	runtime, err := OpenRuntime(ctx, stateDir)
	if err != nil {
		return err
	}
	defer runtime.Close()

	return command(ctx, runtime.Store())
}

func (a *App) prepareCommand(args []string) (preparedCommand, error) {
	if len(args) == 0 {
		return nil, errors.New("expected a command: send, recv, wait, watch, read, ack, renew, release, defer, fail, list, stale, group, or address")
	}

	switch args[0] {
	case "send":
		return a.prepareSendCommand(args[1:])
	case "recv":
		return a.prepareRecvCommand(args[1:])
	case "wait":
		return a.prepareWaitCommand(args[1:])
	case "watch":
		return a.prepareWatchCommand(args[1:])
	case "read":
		return a.prepareReadCommand(args[1:])
	case "ack":
		return a.prepareAckCommand(args[1:])
	case "renew":
		return a.prepareRenewCommand(args[1:])
	case "release":
		return a.prepareReleaseCommand(args[1:])
	case "defer":
		return a.prepareDeferCommand(args[1:])
	case "fail":
		return a.prepareFailCommand(args[1:])
	case "list":
		return a.prepareListCommand(args[1:])
	case "stale":
		return a.prepareStaleCommand(args[1:])
	case "group":
		return a.prepareGroupCommand(args[1:])
	case "address":
		return a.prepareAddressCommand(args[1:])
	default:
		return nil, fmt.Errorf("unknown command %q", args[0])
	}
}
