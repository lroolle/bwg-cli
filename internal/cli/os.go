package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/lroolle/bwg-cli/kiwivm"
	"github.com/lroolle/bwg-cli/pkg/output"
	"github.com/spf13/cobra"
)

func newOSCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "os",
		Short: "List OS templates and reinstall the operating system",
	}
	cmd.AddCommand(osLs(app), osReinstall(app))
	return cmd
}

func osLs(app *App) *cobra.Command {
	return &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List installable OS templates",
		Long: `Show the OS templates available for this VPS.

JSON shape: {"server","installed","templates":[...],"count"}`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, s, err := app.Client()
			if err != nil {
				return err
			}
			ctx, cancel := app.Context(cmd.Context())
			defer cancel()

			list, err := c.AvailableOS(ctx)
			if err != nil {
				return Explain(err, s.Name)
			}
			payload := map[string]any{
				"server": s.Name, "installed": list.Installed,
				"templates": list.Templates, "count": len(list.Templates),
				"hints": map[string]string{"reinstall": "bwg os reinstall <template>"},
			}
			return app.Emit(payload, func(w io.Writer) {
				fmt.Fprintf(w, "%s %s\n\n", output.Dim("Installed:"), output.Strong(list.Installed))
				t := output.NewTable("TEMPLATE", "")
				for _, tpl := range list.Templates {
					marker := ""
					if tpl == list.Installed {
						marker = output.Good("← installed")
					}
					t.Row(tpl, marker)
				}
				t.Render(w)
			})
		},
	}
}

func osReinstall(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "reinstall <template>",
		Short: kiwivm.Ops["reinstallOS"].Summary,
		Long: `Reinstall the operating system.

Every byte on the VPS disk is erased. The new root password is
returned once and cannot be retrieved afterwards, so it is printed
immediately — capture it.

The template must be one of those from 'bwg os ls'. A unique
substring is accepted.`,
		Example: `  bwg os reinstall debian-12-x86_64
  bwg os reinstall debian-12          # unique substring is enough`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, s, err := app.ClientForOp(kiwivm.Ops["reinstallOS"])
			if err != nil {
				return err
			}
			ctx, cancel := app.Context(cmd.Context())
			defer cancel()

			list, err := c.AvailableOS(ctx)
			if err != nil {
				return Explain(err, s.Name)
			}
			template, err := matchTemplate(args[0], list)
			if err != nil {
				return err
			}

			facts := [][2]string{}
			if hostFacts, err := app.identifyFacts(ctx, c); err == nil {
				facts = hostFacts
			}
			facts = append(facts,
				[2]string{"Current OS", list.Installed},
				[2]string{"New OS", output.Strong(template)})

			// Keys matter here: a reinstall with no keys registered
			// leaves password-only access, which is worth knowing BEFORE
			// the disk is erased, not after.
			if keys, err := c.SSHKeys(ctx); err == nil {
				n := len(keys.PreferredSlice())
				if n == 0 {
					facts = append(facts, [2]string{"SSH keys",
						output.Warn("none registered — access will be by password only")})
				} else {
					facts = append(facts, [2]string{"SSH keys",
						fmt.Sprintf("%d will be installed", n)})
				}
			}

			if err := app.Confirm(Consent{
				Op: kiwivm.Ops["reinstallOS"], Server: s, Target: template, Facts: facts,
			}); err != nil {
				return err
			}

			res, err := c.ReinstallOS(ctx, template)
			if err != nil {
				return Explain(err, s.Name)
			}

			payload := map[string]any{
				"server": s.Name, "os": template, "status": "reinstalling",
				"rootPassword": res.RootPassword, "sshPort": res.SSHPort.Int(),
				"sshKeys": res.SSHKeysBrief, "notificationEmail": res.NotificationEmail,
				"hints": map[string]string{
					"check": "bwg status",
					"note":  "the root password is shown once and cannot be retrieved later",
				},
			}
			return app.Emit(payload, func(w io.Writer) {
				fmt.Fprintf(w, "%s Reinstalling %s on %s\n\n",
					output.Good("✓"), template, output.Strong(s.Name))
				output.Tabbed(w, [][2]string{
					{"Root password", output.Strong(res.RootPassword)},
					{"SSH port", fmt.Sprint(res.SSHPort.Int())},
					{"SSH keys", strings.Join(res.SSHKeysBrief, ", ")},
					{"Notify", res.NotificationEmail},
				})
				fmt.Fprintf(w, "\n%s\n",
					output.Warn("Save the password now — KiwiVM will not show it again."))
			})
		},
	}
}

// matchTemplate resolves a template reference. Exact wins; otherwise a
// unique substring, so nobody has to type "ubuntu-24.04-x86_64" in full
// to be sure they got the right one.
func matchTemplate(ref string, list *kiwivm.AvailableOS) (string, error) {
	var partial []string
	for _, tpl := range list.Templates {
		if tpl == ref {
			return tpl, nil
		}
		if strings.Contains(strings.ToLower(tpl), strings.ToLower(ref)) {
			partial = append(partial, tpl)
		}
	}
	switch len(partial) {
	case 1:
		return partial[0], nil
	case 0:
		return "", fmt.Errorf("no OS template matches %q\n\n  Available:\n    %s",
			ref, strings.Join(list.Templates, "\n    "))
	default:
		return "", fmt.Errorf("%q matches %d templates — be more specific:\n    %s",
			ref, len(partial), strings.Join(partial, "\n    "))
	}
}
