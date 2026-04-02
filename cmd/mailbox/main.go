package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/ruiheng/agent-mailbox/internal/mailbox"
	"github.com/ruiheng/agent-mailbox/internal/rootcmd"
)

func main() {
	app := rootcmd.New(os.Stdin, os.Stdout, os.Stderr)
	if err := app.Run(context.Background(), os.Args[1:]); err != nil {
		if errors.Is(err, mailbox.ErrHelpRequested) {
			return
		}
		if errors.Is(err, mailbox.ErrNoMessage) {
			os.Exit(2)
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
