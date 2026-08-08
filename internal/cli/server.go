package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/lroolle/bwg-cli/internal/config"
	"github.com/lroolle/bwg-cli/internal/fleet"
	"github.com/lroolle/bwg-cli/pkg/output"
	"github.com/spf13/cobra"
)

func newServerCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "server",
		Aliases: []string{"servers", "config"},
		Short:   "Manage the fleet: add, list, import and remove servers",
		Long: `Manage which VPS instances bwg knows about.

KiwiVM authenticates per VPS, so each entry is a (veid, api_key) pair
from that VPS's KiwiVM > API page. The config lives at
~/.config/bwg/config.yaml with mode 0600.

For a single box, or for CI, no config is needed at all:

  export BWG_VEID=1347645
  export BWG_KIWIVM_API_KEY=private_...

Those credentials appear as a server named "env" and take precedence
over the stored default.`,
	}
	cmd.AddCommand(
		serverLs(app), serverAdd(app), serverRm(app),
		serverDefault(app), serverShow(app), serverImport(app),
		serverSet(app), serverCheck(app),
	)
	return cmd
}

func serverLs(app *App) *cobra.Command {
	return &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List configured servers",
		Long: `List the fleet without contacting the API.

API keys are always masked. JSON shape:
  {"servers":[{"name","veid","apiKey" (masked),"note","tags",
   "sshUser","sshPort","fromEnv"}],"default","configPath"}`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			servers := app.Cfg.List()
			payload := map[string]any{
				"servers": servers, "default": app.Cfg.Default,
				"configPath": app.Cfg.Path(),
			}
			return app.Emit(payload, func(w io.Writer) {
				if len(servers) == 0 {
					fmt.Fprintf(w, "No servers configured.\n\n"+
						"  Add one:      bwg server add <name> --veid <id> --key <api-key>\n"+
						"  Import a CSV: bwg server import keys.csv\n\n"+
						"%s\n", output.Dim("Both values come from KiwiVM > API for each VPS."))
					return
				}
				t := output.NewTable("NAME", "VEID", "KEY", "TAGS", "NOTE", "")
				for _, s := range servers {
					marker := ""
					switch {
					case s.FromEnv:
						marker = output.Dim("← environment")
					case s.Name == app.Cfg.Default:
						marker = output.Good("← default")
					}
					t.Row(s.Name, s.VEID, config.MaskKey(s.APIKey),
						strings.Join(s.Tags, ","), output.Truncate(s.Note, 30), marker)
				}
				t.Render(w)
				fmt.Fprintf(w, "\n%s %s\n", output.Dim("Config:"), app.Cfg.Path())
			})
		},
	}
}

func serverAdd(app *App) *cobra.Command {
	var (
		veid, key, note, sshUser string
		tags                     []string
		sshPort                  int
		makeDefault, noVerify    bool
	)

	cmd := &cobra.Command{
		Use:   "add <name>",
		Short: "Add a server to the fleet",
		Long: `Add a VPS to the fleet.

VEID is the VPS ID number — the number in the KiwiVM panel URL after ?veid=.
The API key is under the API tab on the same page (looks like private_xxxxxxxx).

The pair is verified against the API before it is saved, so a typo is
caught now rather than on the next command. Use --no-verify to skip
that check.`,
		Example: `  bwg server add tokyo --veid 1347645 --key private_xxx
  bwg server add tokyo --veid 1347645 --key private_xxx --tag prod --tag jp
  bwg server add tokyo --veid 1347645 --key private_xxx --ssh-user admin`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if veid == "" || key == "" {
				return &ExitCodeError{Code: ExitConfig, Err: fmt.Errorf(
					"--veid and --key are both required\n\n"+
						"  bwg server add %s --veid <id> --key <api-key>\n\n"+
						"  VEID is the VPS ID number (in the panel URL after ?veid=).\n"+
						"  API key is under the API tab (looks like private_xxxxxxxx).", name)}
			}

			s := &config.Server{
				VEID: strings.TrimSpace(veid), APIKey: strings.TrimSpace(key),
				Note: note, Tags: tags, SSHUser: sshUser, SSHPort: sshPort,
			}
			if err := s.Validate(); err != nil {
				return &ExitCodeError{Code: ExitConfig, Err: err}
			}

			if !noVerify {
				ctx, cancel := app.Context(cmd.Context())
				defer cancel()
				info, err := app.ClientFor(s).ServiceInfo(ctx)
				if err != nil {
					return Explain(fmt.Errorf("could not reach this VPS: %w", err), "")
				}
				if s.Note == "" {
					s.Note = info.Hostname
				}
				app.Notef("%s %s (%s, %s)", output.Good("✓"),
					info.Hostname, info.Plan, info.NodeLocation)
			}

			if err := app.Cfg.Add(name, s, makeDefault); err != nil {
				return &ExitCodeError{Code: ExitConfig, Err: err}
			}
			if err := app.Cfg.Save(); err != nil {
				return err
			}
			return app.Emit(
				map[string]any{"server": s, "default": app.Cfg.Default,
					"hints": map[string]string{"check": "bwg info --server " + name}},
				func(w io.Writer) {
					fmt.Fprintf(w, "%s Added %s\n", output.Good("✓"), output.Strong(name))
					if app.Cfg.Default == name {
						fmt.Fprintf(w, "  %s\n", output.Dim("It is now the default server."))
					}
				})
		},
	}

	f := cmd.Flags()
	f.StringVar(&veid, "veid", "", "VPS ID from KiwiVM (required)")
	f.StringVar(&key, "key", "", "KiwiVM API key for that VPS (required)")
	f.StringVar(&note, "note", "", "A human label (defaults to the hostname)")
	f.StringSliceVar(&tags, "tag", nil, "Tag for fleet filtering (repeatable)")
	f.StringVar(&sshUser, "ssh-user", "", "Login for 'bwg ssh' (default: root)")
	f.IntVar(&sshPort, "ssh-port", 0, "Pin the SSH port (default: ask the API)")
	f.BoolVar(&makeDefault, "default", false, "Make this the default server")
	f.BoolVar(&noVerify, "no-verify", false, "Skip the API check before saving")
	return cmd
}

func serverSet(app *App) *cobra.Command {
	var (
		note, sshUser, key string
		tags               []string
		sshPort            int
	)
	cmd := &cobra.Command{
		Use:   "set <name>",
		Short: "Change a stored server's settings",
		Long: `Update fields on a configured server.

Only the flags you pass are changed. This edits local configuration
and never contacts the API.`,
		Example: `  bwg server set tokyo --tag prod --tag jp
  bwg server set tokyo --ssh-port 2222
  bwg server set tokyo --key private_newkey`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, ok := app.Cfg.Servers[args[0]]
			if !ok {
				return &ExitCodeError{Code: ExitConfig, Err: fmt.Errorf(
					"%w: %s\n\n  Known servers: %s",
					config.ErrNotFound, args[0], strings.Join(app.Cfg.Names(), ", "))}
			}
			f := cmd.Flags()
			if f.Changed("note") {
				s.Note = note
			}
			if f.Changed("tag") {
				s.Tags = tags
			}
			if f.Changed("ssh-user") {
				s.SSHUser = sshUser
			}
			if f.Changed("ssh-port") {
				s.SSHPort = sshPort
			}
			if f.Changed("key") {
				s.APIKey = strings.TrimSpace(key)
			}
			if err := s.Validate(); err != nil {
				return &ExitCodeError{Code: ExitConfig, Err: err}
			}
			if err := app.Cfg.Save(); err != nil {
				return err
			}
			return app.Emit(map[string]any{"server": s}, func(w io.Writer) {
				fmt.Fprintf(w, "%s Updated %s\n", output.Good("✓"), output.Strong(args[0]))
			})
		},
	}
	f := cmd.Flags()
	f.StringVar(&note, "note", "", "Set the note")
	f.StringSliceVar(&tags, "tag", nil, "Replace the tags")
	f.StringVar(&sshUser, "ssh-user", "", "Set the SSH login")
	f.IntVar(&sshPort, "ssh-port", 0, "Pin the SSH port (0 = ask the API)")
	f.StringVar(&key, "key", "", "Replace the API key")
	return cmd
}

func serverRm(app *App) *cobra.Command {
	return &cobra.Command{
		Use:     "rm <name>",
		Aliases: []string{"remove", "delete"},
		Short:   "Remove a server from the fleet",
		Long: `Forget a server.

This edits local configuration only. The VPS itself is untouched and
still exists in your BandwagonHost account.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if _, ok := app.Cfg.Servers[name]; !ok {
				return &ExitCodeError{Code: ExitConfig, Err: fmt.Errorf(
					"%w: %s\n\n  Known servers: %s",
					config.ErrNotFound, name, strings.Join(app.Cfg.Names(), ", "))}
			}
			// Forgetting a credential is local and reversible by
			// re-adding it, so this is a plain y/N and not a card.
			if !app.Yes && output.Interactive() {
				fmt.Fprintf(app.ErrOut,
					"Remove %s from the local config? The VPS itself is untouched. %s ",
					output.Strong(name), output.Dim("[y/N]"))
				line, _ := app.readLine()
				if l := strings.ToLower(strings.TrimSpace(line)); l != "y" && l != "yes" {
					return &ExitCodeError{Code: ExitRefused, Err: ErrDeclined}
				}
			}
			if err := app.Cfg.Remove(name); err != nil {
				return err
			}
			if err := app.Cfg.Save(); err != nil {
				return err
			}
			return app.Emit(
				map[string]any{"removed": name, "default": app.Cfg.Default},
				func(w io.Writer) {
					fmt.Fprintf(w, "%s Removed %s\n", output.Good("✓"), name)
				})
		},
	}
}

func serverDefault(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "default [name]",
		Short: "Show or set the default server",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return app.Emit(
					map[string]any{"default": app.Cfg.Default},
					func(w io.Writer) {
						if app.Cfg.Default == "" {
							fmt.Fprintf(w, "No default set. Choose one: bwg server default <name>\n")
							return
						}
						fmt.Fprintln(w, app.Cfg.Default)
					})
			}
			if err := app.Cfg.SetDefault(args[0]); err != nil {
				return &ExitCodeError{Code: ExitConfig, Err: fmt.Errorf(
					"%w\n\n  Known servers: %s", err, strings.Join(app.Cfg.Names(), ", "))}
			}
			if err := app.Cfg.Save(); err != nil {
				return err
			}
			return app.Emit(map[string]any{"default": args[0]}, func(w io.Writer) {
				fmt.Fprintf(w, "%s Default is now %s\n", output.Good("✓"), output.Strong(args[0]))
			})
		},
	}
}

func serverShow(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "show [name]",
		Short: "Show one server's stored settings",
		Long: `Show what bwg has stored for a server.

The API key is masked. There is deliberately no way to print it back
out: if you need the key, it is in the KiwiVM panel.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := app.ServerName
			if len(args) == 1 {
				name = args[0]
			}
			s, err := app.Cfg.Resolve(name)
			if err != nil {
				app.ServerName = name
				if _, err2 := app.Server(); err2 != nil {
					return err2
				}
				return err
			}
			return app.Emit(map[string]any{"server": s}, func(w io.Writer) {
				output.Tabbed(w, [][2]string{
					{"Name", s.Name},
					{"VEID", s.VEID},
					{"API key", config.MaskKey(s.APIKey)},
					{"Note", s.Note},
					{"Tags", strings.Join(s.Tags, ", ")},
					{"SSH user", s.User()},
					{"SSH port", sshPortNote(s.SSHPort)},
					{"Endpoint", s.Endpoint},
					{"Source", sourceNote(s, app.Cfg)},
				})
			})
		},
	}
}

func sshPortNote(port int) string {
	if port == 0 {
		return output.Dim("from the API")
	}
	return fmt.Sprint(port)
}

func sourceNote(s *config.Server, cfg *config.Config) string {
	if s.FromEnv {
		return "environment (" + config.EnvVEID + ", " + config.EnvAPIKeyAlt + ")"
	}
	return cfg.Path()
}

func serverImport(app *App) *cobra.Command {
	var (
		makeDefault bool
		tags        []string
		dryRun      bool
	)
	cmd := &cobra.Command{
		Use:   "import <file.csv>",
		Short: "Import every VPS from a billing-portal API key export",
		Long: `Bulk-add servers from BandwagonHost's CSV export.

The billing portal can export a CSV of API keys for every instance on
the account ("CSV export" in the billing portal). This reads it.

Column names have changed over the years, so columns are matched by
name where a header exists and by content where it does not: a numeric
VEID column and a private_... key column. Rows already in the config
are skipped, so re-importing after buying a box is safe.

Use - to read the CSV from stdin.`,
		Example: `  bwg server import keys.csv
  bwg server import keys.csv --tag prod
  bwg server import keys.csv --dry-run
  curl -s "$EXPORT_URL" | bwg server import -`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var r io.Reader = app.In
			if args[0] != "-" {
				f, err := os.Open(args[0])
				if err != nil {
					return fmt.Errorf("opening %s: %w", args[0], err)
				}
				defer f.Close()
				r = f
			}

			imported, err := config.ParseCSV(r)
			if err != nil {
				return &ExitCodeError{Code: ExitConfig, Err: err}
			}

			type record struct {
				Name   string `json:"name"`
				VEID   string `json:"veid"`
				Status string `json:"status"`
			}
			var added, skipped []record

			existing := map[string]bool{}
			for _, s := range app.Cfg.Servers {
				existing[s.VEID] = true
			}

			for _, im := range imported {
				if existing[im.Server.VEID] {
					skipped = append(skipped, record{im.Name, im.Server.VEID, "already configured"})
					continue
				}
				im.Server.Tags = tags
				if !dryRun {
					if err := app.Cfg.Add(im.Name, im.Server, false); err != nil {
						skipped = append(skipped, record{im.Name, im.Server.VEID, err.Error()})
						continue
					}
				}
				existing[im.Server.VEID] = true
				added = append(added, record{im.Name, im.Server.VEID, "added"})
			}

			if !dryRun && len(added) > 0 {
				if makeDefault && len(added) == 1 {
					app.Cfg.SetDefault(added[0].Name)
				}
				if err := app.Cfg.Save(); err != nil {
					return err
				}
			}

			return app.Emit(
				map[string]any{"added": added, "skipped": skipped,
					"dryRun": dryRun, "configPath": app.Cfg.Path(),
					"hints": map[string]string{"next": "bwg ls"}},
				func(w io.Writer) {
					verb := "Added"
					if dryRun {
						verb = "Would add"
					}
					t := output.NewTable("NAME", "VEID", "RESULT")
					for _, r := range added {
						t.Row(r.Name, r.VEID, output.Good(strings.ToLower(verb)))
					}
					for _, r := range skipped {
						t.Row(r.Name, r.VEID, output.Dim(r.Status))
					}
					t.Render(w)
					fmt.Fprintf(w, "\n%s %d server(s), %d skipped.\n",
						verb, len(added), len(skipped))
					if !dryRun && len(added) > 0 {
						fmt.Fprintf(w, "%s\n", output.Dim("See them all: bwg ls"))
					}
				})
		},
	}
	cmd.Flags().BoolVar(&makeDefault, "default", false, "Make the imported server the default (single row only)")
	cmd.Flags().StringSliceVar(&tags, "tag", nil, "Tag every imported server")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show what would be imported without saving")
	return cmd
}

func serverCheck(app *App) *cobra.Command {
	var tags []string
	cmd := &cobra.Command{
		Use:   "check",
		Short: "Verify every server's credentials against the API",
		Long: `Check that each stored (veid, api_key) pair still authenticates.

Useful after importing a CSV or rotating keys. Read-only: it calls
getServiceInfo and nothing else.

JSON shape: {"results":[{"server","veid","ok","hostname","error"}],
"ok","failed"}`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			servers, err := app.Servers(tags)
			if err != nil {
				return err
			}
			ctx, cancel := app.Context(cmd.Context())
			defer cancel()

			results := fleet.Map(ctx, servers, app.Concurrency,
				func(ctx context.Context, s *config.Server) (string, error) {
					info, err := app.ClientFor(s).ServiceInfo(ctx)
					if err != nil {
						return "", err
					}
					return info.Hostname, nil
				})

			type check struct {
				Server   string `json:"server"`
				VEID     string `json:"veid"`
				OK       bool   `json:"ok"`
				Hostname string `json:"hostname,omitempty"`
				Error    string `json:"error,omitempty"`
			}
			var checks []check
			var okCount, failCount int
			for _, r := range results {
				c := check{Server: r.Server.Name, VEID: r.Server.VEID, OK: r.OK()}
				if r.OK() {
					c.Hostname = r.Value
					okCount++
				} else {
					c.Error = r.Error
					failCount++
				}
				checks = append(checks, c)
			}

			emitErr := app.Emit(
				map[string]any{"results": checks, "ok": okCount, "failed": failCount},
				func(w io.Writer) {
					t := output.NewTable("SERVER", "VEID", "RESULT")
					for _, c := range checks {
						if c.OK {
							t.Row(c.Server, c.VEID, output.Good("ok — "+c.Hostname))
						} else {
							t.Row(c.Server, c.VEID, output.Bad(output.Truncate(c.Error, 60)))
						}
					}
					t.Render(w)
				})
			if emitErr != nil {
				return emitErr
			}
			if failCount > 0 {
				return &ExitCodeError{Code: ExitAuth,
					Err: fmt.Errorf("%d of %d servers did not authenticate", failCount, len(checks))}
			}
			return nil
		},
	}
	cmd.Flags().StringSliceVar(&tags, "tag", nil, "Only servers carrying every given tag")
	return cmd
}
