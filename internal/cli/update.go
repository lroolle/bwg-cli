package cli

import (
	"fmt"
	"io"
	"os"

	"github.com/lroolle/bwg-cli/internal/updater"
	"github.com/lroolle/bwg-cli/pkg/output"
	"github.com/spf13/cobra"
)

func newUpdateCmd(app *App) *cobra.Command {
	var checkOnly bool

	cmd := &cobra.Command{
		Use:   "update",
		Short: "Check for updates and install the latest version",
		Long: `Check GitHub releases for a newer version and install it.

The binary is replaced atomically. The old version is kept as
<binary>.old until the next update, so a bad release can be recovered
by renaming it back.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := app.Context(cmd.Context())
			defer cancel()

			app.Notef("%s Checking for updates...", output.Dim("→"))

			rel, err := updater.CheckLatest(ctx, app.Version)
			if err != nil {
				return fmt.Errorf("checking for updates: %w", err)
			}

			if !rel.HasUpdate {
				return app.Emit(
					map[string]any{"current": app.Version, "latest": rel.Version, "upToDate": true},
					func(w io.Writer) {
						fmt.Fprintf(w, "%s bwg %s is the latest version.\n",
							output.Good("✓"), app.Version)
					})
			}

			if checkOnly {
				return app.Emit(
					map[string]any{"current": app.Version, "latest": rel.Version,
						"upToDate": false, "url": rel.URL},
					func(w io.Writer) {
						fmt.Fprintf(w, "%s bwg %s is available (current: %s)\n\n"+
							"  Run 'bwg update' to install.\n",
							output.Warn("!"), rel.Version, app.Version)
					})
			}

			app.Notef("%s Downloading bwg %s...", output.Dim("→"), rel.Version)

			bin, err := os.Executable()
			if err != nil {
				return fmt.Errorf("finding current binary: %w", err)
			}

			tmpPath, err := updater.Download(ctx, rel)
			if err != nil {
				return fmt.Errorf("downloading update: %w", err)
			}
			defer os.Remove(tmpPath)

			if err := updater.Replace(bin, tmpPath); err != nil {
				return fmt.Errorf("installing update: %w\n\n"+
					"  The downloaded binary is at %s — install it manually.", err, tmpPath)
			}

			return app.Emit(
				map[string]any{"previous": app.Version, "installed": rel.Version, "binary": bin},
				func(w io.Writer) {
					fmt.Fprintf(w, "%s Updated bwg %s -> %s\n",
						output.Good("✓"), app.Version, output.Strong(rel.Version))
				})
		},
	}

	cmd.Flags().BoolVar(&checkOnly, "check", false, "Only check, do not install")
	return cmd
}
