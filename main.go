// Command fathom estimates how a service scales by fitting throughput
// measured at several levels of parallelism to the Universal Scalability Law.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"

	"github.com/urfave/cli/v3"
)

var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	cmd := &cli.Command{
		Name:    "fathom",
		Usage:   "estimate how a service scales by fitting throughput measurements to the USL",
		Version: version,
		Commands: []*cli.Command{
			runCommand(),
			fitCommand(),
		},
	}

	if err := cmd.Run(ctx, os.Args); err != nil {
		fmt.Fprintln(os.Stderr, "fathom:", err)
		os.Exit(1)
	}
}
