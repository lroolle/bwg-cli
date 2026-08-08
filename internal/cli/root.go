package cli

import (
	"fmt"
	"io"

	"github.com/lroolle/bwg-cli/kiwivm"
	"github.com/spf13/cobra"
)

// NewRoot builds the command tree.
func NewRoot(version string) (*cobra.Command, *App) {
	app := NewApp(version)

	cmd := &cobra.Command{
		Use:   "bwg",
		Short: "BandwagonHost / KiwiVM fleet control for humans and agents",
		Long: `bwg manages BandwagonHost (KiwiVM) VPS instances from the command line.

KiwiVM authenticates per VPS, so bwg holds a fleet: one (veid, api_key)
pair per box. Fleet-wide commands sweep them all concurrently; the rest
act on one server chosen by --server, $BWG_SERVER, or the default.

Every command supports --json and --jq. Data goes to stdout and
diagnostics to stderr, so output is safe to pipe.

Safety is a client capability, not a prompt: --read-only (or
BWG_READ_ONLY=1) makes writes impossible below the CLI, in the SDK
itself. Run 'bwg api ops' to see how every endpoint is classified.`,
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			return app.Init()
		},
		RunE: func(cmd *cobra.Command, args []string) error { return cmd.Help() },
	}

	f := cmd.PersistentFlags()
	f.StringVarP(&app.ServerName, "server", "s", "",
		"Server to act on (default: $BWG_SERVER, then the configured default)")
	f.BoolVar(&app.JSON, "json", false, "Output JSON")
	f.StringVar(&app.JQ, "jq", "", "Filter JSON output through a jq expression")
	f.BoolVar(&app.ReadOnly, "read-only", false,
		"Refuse every write, in the client itself (also: BWG_READ_ONLY=1)")
	f.BoolVar(&app.DryRun, "dry-run", false,
		"Show what a write would do without doing it")
	f.BoolVarP(&app.Yes, "yes", "y", false, "Skip confirmation prompts")
	f.BoolVarP(&app.Verbose, "verbose", "v", false, "Log HTTP requests to stderr")
	f.BoolVar(&app.NoColor, "no-color", false, "Disable colour")
	f.StringVar(&app.ConfigPath, "config", "", "Config file (default: ~/.config/bwg/config.yaml)")
	f.DurationVar(&app.Timeout, "timeout", app.Timeout, "Per-request timeout")
	f.IntVar(&app.Concurrency, "concurrency", app.Concurrency,
		"Parallel API calls for fleet-wide commands")

	cmd.AddCommand(
		newLsCmd(app),
		newStatusCmd(app),
		newInfoCmd(app),
		newUsageCmd(app),
		newAuditCmd(app),
		newIncidentsCmd(app),
		newPowerCmds(app),
		// The three common verbs are also top-level: `bwg restart` is
		// what people reach for under pressure.
		newTopLevelPower(app, "start"),
		newTopLevelPower(app, "stop"),
		newTopLevelPower(app, "restart"),
		newSnapshotCmd(app),
		newBackupCmd(app),
		newOSCmd(app),
		newSSHCmd(app),
		newKeysCmd(app),
		newPasswdCmd(app),
		newHostCmd(app),
		newNetCmd(app),
		newISOCmd(app),
		newAbuseCmd(app),
		newExecCmd(app),
		newRunCmd(app),
		newMigrateCmd(app),
		newNotifyCmd(app),
		newRateLimitCmd(app),
		newServerCmd(app),
		newAPICmd(app),
		newMCPCmd(app),
		newUpdateCmd(app),
		newCompletionCmd(),
		newVersionCmd(app),
	)

	registerCompletions(cmd, app)

	return cmd, app
}

func newCompletionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "completion [bash|zsh|fish|powershell]",
		Short: "Generate shell completion script",
		Long: `Generate a completion script for the given shell.

  bash:       bwg completion bash > /etc/bash_completion.d/bwg
  zsh:        bwg completion zsh > "${fpath[1]}/_bwg"
  fish:       bwg completion fish > ~/.config/fish/completions/bwg.fish
  powershell: bwg completion powershell | Out-String | Invoke-Expression`,
		DisableFlagsInUseLine: true,
		ValidArgs:             []string{"bash", "zsh", "fish", "powershell"},
		Args:                  cobra.MatchAll(cobra.ExactArgs(1), cobra.OnlyValidArgs),
		RunE: func(cmd *cobra.Command, args []string) error {
			switch args[0] {
			case "bash":
				return cmd.Root().GenBashCompletionV2(cmd.OutOrStdout(), true)
			case "zsh":
				return cmd.Root().GenZshCompletion(cmd.OutOrStdout())
			case "fish":
				return cmd.Root().GenFishCompletion(cmd.OutOrStdout(), true)
			case "powershell":
				return cmd.Root().GenPowerShellCompletionWithDesc(cmd.OutOrStdout())
			}
			return nil
		},
	}
}

func registerCompletions(root *cobra.Command, app *App) {
	root.RegisterFlagCompletionFunc("server", func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if app.Cfg == nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		return app.Cfg.Names(), cobra.ShellCompDirectiveNoFileComp
	})
}

func newVersionCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.Emit(
				map[string]string{"version": app.Version, "apiBase": kiwivm.DefaultBaseURL},
				func(w io.Writer) { fmt.Fprintf(w, "bwg %s\n", app.Version) })
		},
	}
}
