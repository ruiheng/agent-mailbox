package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/ruiheng/waypost/internal/rootcmd"
	"github.com/ruiheng/waypost/internal/waypost"
)

func main() {
	os.Exit(runCommand(context.Background(), os.Args[1:], os.Stdin, os.Stdout, os.Stderr, nil))
}

func runCommand(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, run func(context.Context, []string) error) int {
	if run == nil {
		app := rootcmd.New(stdin, stdout, stderr)
		run = app.Run
	}
	if err := run(ctx, args); err != nil {
		if errors.Is(err, waypost.ErrHelpRequested) {
			return 0
		}
		if errors.Is(err, waypost.ErrNoMessage) {
			return 2
		}
		if !waypost.WriteCLIJSONError(stderr, args, err) {
			fmt.Fprintln(stderr, err)
		}
		return 1
	}
	return 0
}
