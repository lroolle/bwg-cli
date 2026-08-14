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
	"runtime/debug"
	"syscall"

	"github.com/lroolle/bwg-cli/internal/cli"
)

// version is set by the linker at release time.
var version = "dev"

// buildVersion returns the version to report.
//
// A release binary has it stamped in by the linker. A binary from
// `go install ...@latest` does not, and reporting "dev" there is worse
// than cosmetic: `bwg update` compares versions numerically, so "dev"
// parses as 0.0.0 and every check announces an update that is already
// installed. Go records the module version for install-built binaries,
// so ask it.
func buildVersion() string {
	bi, ok := debug.ReadBuildInfo()
	return resolveVersion(version, bi, ok)
}

// resolveVersion is buildVersion's decision, split out so it can be
// tested without controlling how the test binary itself was built.
func resolveVersion(stamped string, bi *debug.BuildInfo, ok bool) string {
	if stamped != "dev" {
		return stamped
	}
	if ok && bi != nil {
		if v := bi.Main.Version; v != "" && v != "(devel)" {
			return v
		}
	}
	return stamped
}

func main() {
	// Ctrl-C cancels in-flight requests rather than leaving the
	// terminal to kill the process mid-write.
	ctx, stop := signal.NotifyContext(context.Background(),
		os.Interrupt, syscall.SIGTERM)
	defer stop()

	root, _ := cli.NewRoot(buildVersion())
	if err := root.ExecuteContext(ctx); err != nil {
		code := cli.CodeFor(err)
		if !errors.Is(err, context.Canceled) && !errors.Is(err, cli.ErrDryRun) {
			fmt.Fprintf(os.Stderr, "%s %s\n", "Error:", err)
		}
		os.Exit(code)
	}
}
