package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, os.Args[1:])
	stop()
	if err != nil {
		// Package errors and local validation messages never include token values.
		fmt.Fprintln(os.Stderr, "sofa:", err)
		os.Exit(1)
	}
}
