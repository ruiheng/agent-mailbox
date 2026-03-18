package main

import (
	"context"
	"fmt"
	"os"

	"github.com/ruiheng/agent-mailbox/internal/mailbox"
)

func main() {
	app := mailbox.NewApp(os.Stdin, os.Stdout, os.Stderr)
	if err := app.Run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
