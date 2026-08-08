// Command bwg manages BandwagonHost (KiwiVM) VPS instances.
//
// See https://github.com/lroolle/bwg-cli for documentation.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/lroolle/bwg-cli/internal/cli"
)

// version is set by the linker at release time.
var version = "dev"

func main() {
	// Ctrl-C cancels in-flight requests rather than leaving the
	// terminal to kill the process mid-write.
	ctx, stop := signal.NotifyContext(context.Background(),
		os.Interrupt, syscall.SIGTERM)
	defer stop()

	root, _ := cli.NewRoot(version)
	if err := root.ExecuteContext(ctx); err != nil {
		code := cli.CodeFor(err)
		if !errors.Is(err, context.Canceled) && !errors.Is(err, cli.ErrDryRun) {
			fmt.Fprintf(os.Stderr, "%s %s\n", "Error:", err)
		}
		os.Exit(code)
	}
}
