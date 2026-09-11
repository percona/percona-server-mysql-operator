package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

var log = logf.Log.WithName("orc-handler")

type command struct {
	name  string
	usage string
	run   func(ctx context.Context, args []string) error
}

var commands = []command{
	{
		name:  "set-primary-label",
		usage: "label the pod of the new primary and unlabel the old one",
		run:   runSetPrimaryLabel,
	},
	{
		name:  "failover",
		usage: "apply the binary logs the new primary is missing from the old one",
		run:   runFailover,
	},
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()

	opts := zap.Options{
		Development: true,
		DestWriter:  os.Stdout,
	}
	logf.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	if len(os.Args) < 2 {
		printUsage()
		os.Exit(2)
	}

	name := os.Args[1]
	for _, cmd := range commands {
		if cmd.name != name {
			continue
		}

		if err := cmd.run(ctx, os.Args[2:]); err != nil {
			log.Error(err, "command failed", "command", cmd.name)
			os.Exit(1)
		}

		return
	}

	fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", name)
	printUsage()
	os.Exit(2)
}

func printUsage() {
	fmt.Fprint(os.Stderr, "Usage: orc-handler <command> [flags]\n\nCommands:\n")
	for _, cmd := range commands {
		fmt.Fprintf(os.Stderr, "  %-20s %s\n", cmd.name, cmd.usage)
	}
}
