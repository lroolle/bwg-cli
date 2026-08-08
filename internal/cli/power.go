package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/lroolle/bwg-cli/kiwivm"
	"github.com/lroolle/bwg-cli/pkg/output"
	"github.com/spf13/cobra"
)

// newPowerCmds returns start/stop/restart/kill as top-level commands.
// They are the ones typed most often, and `bwg restart` beats
// `bwg power restart` for something you run in a hurry.
func newPowerCmds(app *App) *cobra.Command {
	power := &cobra.Command{
		Use:   "power",
		Short: "Start, stop, restart or force-stop a server",
		Long: `Power operations.

start, stop and restart are writes: Start undoes any of them.
kill is destructive — it force-stops a VPS that will not stop
normally, and unsaved data in the guest is lost.`,
	}
	for _, name := range []string{"start", "stop", "restart", "kill"} {
		power.AddCommand(newPowerCmd(app, name))
	}
	return power
}

func newPowerCmd(app *App, action string) *cobra.Command {
	endpoint := action
	op := kiwivm.Ops[endpoint]

	cmd := &cobra.Command{
		Use:   action,
		Short: op.Summary,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, s, err := app.ClientForOp(op)
			if err != nil {
				return err
			}
			ctx, cancel := app.Context(cmd.Context())
			defer cancel()

			// The card names the box being power-cycled. Getting that
			// wrong is the mistake worth spending a round trip to prevent.
			facts, _ := app.identifyFacts(ctx, c)
			if err := app.Confirm(Consent{Op: op, Server: s, Facts: facts}); err != nil {
				return err
			}

			switch action {
			case "start":
				err = c.Start(ctx)
			case "stop":
				err = c.Stop(ctx)
			case "restart":
				err = c.Restart(ctx)
			case "kill":
				err = c.Kill(ctx)
			}
			if err != nil {
				return Explain(err, s.Name)
			}

			payload := map[string]any{
				"server": s.Name, "action": action, "status": "accepted",
				"hints": map[string]string{"check": "bwg status"},
			}
			return app.Emit(payload, func(w io.Writer) {
				fmt.Fprintf(w, "%s %s accepted on %s — check with: bwg status\n",
					output.Good("✓"), action, output.Strong(s.Name))
			})
		},
	}
	if op.Why != "" {
		cmd.Long = fmt.Sprintf("%s.\n\nDestructive: %s.", op.Summary, op.Why)
	}
	return cmd
}

// identifyFacts fetches the hostname and address of the target box for
// a consent card. It is best-effort: if the lookup fails, the card
// still appears with what bwg already knows, because losing the prompt
// entirely would be worse than losing a line of context.
func (a *App) identifyFacts(ctx context.Context, c *kiwivm.Client) ([][2]string, error) {
	info, err := c.ServiceInfo(ctx)
	if err != nil {
		return nil, err
	}
	facts := [][2]string{
		{"Hostname", info.Hostname},
		{"Location", info.NodeLocation},
	}
	if ip := info.PrimaryIP(); ip != "" {
		facts = append(facts, [2]string{"Address", ip})
	}
	return facts, nil
}

// topLevelPowerAliases exposes the three common power verbs at the top
// level too. Registered by NewRoot via newPowerCmds' siblings.
func newTopLevelPower(app *App, action string) *cobra.Command {
	cmd := newPowerCmd(app, action)
	cmd.Short = kiwivm.Ops[action].Summary + " (alias for 'bwg power " + action + "')"
	return cmd
}
