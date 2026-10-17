package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/zela-dev/zela/internal/cli"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	r := cli.Runner{In: os.Stdin, Out: os.Stdout, Err: os.Stderr}
	if err := r.Run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "zela:", err)
		if ctx.Err() != nil {
			os.Exit(130)
		}
		os.Exit(1)
	}
}
