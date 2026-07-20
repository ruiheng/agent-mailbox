package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/ruiheng/waypost/internal/rootcmd"
	"github.com/ruiheng/waypost/internal/waypost"
)

func main() {
	app := rootcmd.New(os.Stdin, os.Stdout, os.Stderr)
	if err := app.Run(context.Background(), os.Args[1:]); err != nil {
		if errors.Is(err, waypost.ErrHelpRequested) {
			return
		}
		if errors.Is(err, waypost.ErrNoMessage) {
			os.Exit(2)
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
