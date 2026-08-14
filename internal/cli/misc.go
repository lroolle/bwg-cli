package cli

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/lroolle/bwg-cli/kiwivm"
	"github.com/lroolle/bwg-cli/pkg/output"
	"github.com/spf13/cobra"
)

// -- ISO ------------------------------------------------------------------

func newISOCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "iso",
		Short: "Mount and unmount ISO boot images",
		Long: `Boot the VPS from an ISO image instead of its disk.

Both mounting and unmounting need the VPS fully shut down, and a
restart afterwards. A VPS left on the wrong ISO is unreachable until
someone corrects it, which is why both are treated as destructive.`,
	}

	ls := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List available ISO images and what is mounted",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, s, err := app.Client()
			if err != nil {
				return err
			}
			ctx, cancel := app.Context(cmd.Context())
			defer cancel()

			info, err := c.ServiceInfo(ctx)
			if err != nil {
				return Explain(err, s.Name)
			}
			return app.Emit(
				map[string]any{"server": s.Name, "mounted": info.ISO1,
					"available": info.AvailableISOs,
					"hints":     map[string]string{"mount": "bwg iso mount <image>"}},
				func(w io.Writer) {
					mounted := info.ISO1
					if mounted == "" {
						mounted = output.Dim("none — booting from primary storage")
					} else {
						mounted = output.Warn(mounted)
					}
					output.Tabbed(w, [][2]string{{"Mounted", mounted}})
					if len(info.AvailableISOs) > 0 {
						fmt.Fprintf(w, "\n%s\n", output.Dim("Available"))
						for _, iso := range info.AvailableISOs {
							fmt.Fprintf(w, "  %s\n", iso)
						}
					}
				})
		},
	}

	mount := &cobra.Command{
		Use:   "mount <image>",
		Short: kiwivm.Ops["iso/mount"].Summary,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, s, err := app.ClientForOp(kiwivm.Ops["iso/mount"])
			if err != nil {
				return err
			}
			ctx, cancel := app.Context(cmd.Context())
			defer cancel()

			facts := [][2]string{{"Required", "the VPS must be fully shut down, then restarted"}}
			if info, err := c.ServiceInfo(ctx); err == nil {
				if !contains(info.AvailableISOs, args[0]) {
					return fmt.Errorf("%q is not an available image\n\n  Available:\n    %s",
						args[0], strings.Join(info.AvailableISOs, "\n    "))
				}
				facts = append([][2]string{{"Hostname", info.Hostname}}, facts...)
			}
			if err := app.Confirm(Consent{
				Op: kiwivm.Ops["iso/mount"], Server: s, Target: args[0], Facts: facts,
			}); err != nil {
				return err
			}
			if err := c.MountISO(ctx, args[0]); err != nil {
				return Explain(err, s.Name)
			}
			return app.Emit(
				map[string]any{"server": s.Name, "iso": args[0], "status": "mounted",
					"hints": map[string]string{"next": "bwg power restart"}},
				func(w io.Writer) {
					fmt.Fprintf(w, "%s Mounted %s — restart to boot from it: bwg power restart\n",
						output.Good("✓"), args[0])
				})
		},
	}

	unmount := &cobra.Command{
		Use:   "unmount",
		Short: kiwivm.Ops["iso/unmount"].Summary,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, s, err := app.ClientForOp(kiwivm.Ops["iso/unmount"])
			if err != nil {
				return err
			}
			ctx, cancel := app.Context(cmd.Context())
			defer cancel()

			if err := app.Confirm(Consent{
				Op: kiwivm.Ops["iso/unmount"], Server: s,
				Facts: [][2]string{{"Required", "the VPS must be fully shut down, then restarted"}},
			}); err != nil {
				return err
			}
			if err := c.UnmountISO(ctx); err != nil {
				return Explain(err, s.Name)
			}
			return app.Emit(
				map[string]any{"server": s.Name, "status": "unmounted",
					"hints": map[string]string{"next": "bwg power restart"}},
				func(w io.Writer) {
					fmt.Fprintf(w, "%s Unmounted — restart to boot from disk: bwg power restart\n",
						output.Good("✓"))
				})
		},
	}

	cmd.AddCommand(ls, mount, unmount)
	return cmd
}

func contains(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}

// -- backups ----------------------------------------------------------------

func newBackupCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "backup",
		Short: "List automatic backups and promote one to a snapshot",
		Long: `KiwiVM takes automatic backups on its own schedule.

They cannot be restored directly. Copy one into a snapshot first, then
restore that:

  bwg backup restore <token>     # becomes a snapshot
  bwg snapshot ls                # find it
  bwg snapshot restore <file>    # actually restore`,
	}

	ls := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List automatic backups, newest first",
		Long: `List automatic backups.

JSON shape: {"server","backups":[{"backupToken","size","os","md5",
"timestamp"}],"count"}`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, s, err := app.Client()
			if err != nil {
				return err
			}
			ctx, cancel := app.Context(cmd.Context())
			defer cancel()

			list, err := c.Backups(ctx)
			if err != nil {
				return Explain(err, s.Name)
			}
			backups := list.Sorted()
			return app.Emit(
				map[string]any{"server": s.Name, "backups": backups, "count": len(backups),
					"hints": map[string]string{"promote": "bwg backup restore <token>"}},
				func(w io.Writer) {
					if len(backups) == 0 {
						fmt.Fprintf(w, "No automatic backups for %s.\n", s.Name)
						return
					}
					t := output.NewTable("WHEN", "OS", "SIZE", "TOKEN").RightAlign(2)
					for _, b := range backups {
						t.Row(output.Time(b.Time()), b.OS,
							output.Bytes(b.Size.Int64()), output.Truncate(b.Token, 32))
					}
					t.Render(w)
				})
		},
	}

	restore := &cobra.Command{
		Use:     "restore <token>",
		Aliases: []string{"promote"},
		Short:   kiwivm.Ops["backup/copyToSnapshot"].Summary,
		Long: `Copy an automatic backup into a restorable snapshot.

This does NOT overwrite the VPS. It creates a snapshot you can then
restore with 'bwg snapshot restore'.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, s, err := app.ClientForOp(kiwivm.Ops["backup/copyToSnapshot"])
			if err != nil {
				return err
			}
			ctx, cancel := app.Context(cmd.Context())
			defer cancel()

			token := args[0]
			facts := [][2]string{{"Effect", "creates a snapshot; the VPS is not touched"}}
			if list, err := c.Backups(ctx); err == nil {
				resolved, err := resolveBackup(list, token)
				if err != nil {
					return err
				}
				token = resolved.Token
				facts = append([][2]string{
					{"Backup taken", output.Time(resolved.Time())},
					{"Backup OS", resolved.OS},
					{"Size", output.Bytes(resolved.Size.Int64())},
				}, facts...)
			}

			if err := app.Confirm(Consent{
				Op: kiwivm.Ops["backup/copyToSnapshot"], Server: s,
				Target: output.Truncate(token, 24), Facts: facts,
			}); err != nil {
				return err
			}
			if err := c.CopyBackupToSnapshot(ctx, token); err != nil {
				return Explain(err, s.Name)
			}
			return app.Emit(
				map[string]any{"server": s.Name, "backupToken": token, "status": "copying",
					"hints": map[string]string{
						"find":    "bwg snapshot ls",
						"restore": "bwg snapshot restore <fileName>"}},
				func(w io.Writer) {
					fmt.Fprintf(w, "%s Copying the backup into a snapshot.\n  %s\n",
						output.Good("✓"),
						output.Dim("Find it with: bwg snapshot ls, then: bwg snapshot restore <fileName>"))
				})
		},
	}

	cmd.AddCommand(ls, restore)
	return cmd
}

// resolveBackup accepts a full token or a unique prefix, because the
// tokens are long opaque strings.
func resolveBackup(list *kiwivm.BackupList, ref string) (kiwivm.Backup, error) {
	backups := list.Sorted()
	var partial []kiwivm.Backup
	for _, b := range backups {
		if b.Token == ref {
			return b, nil
		}
		if strings.HasPrefix(b.Token, ref) {
			partial = append(partial, b)
		}
	}
	switch len(partial) {
	case 1:
		return partial[0], nil
	case 0:
		var tokens []string
		for _, b := range backups {
			tokens = append(tokens, b.Token)
		}
		if len(tokens) == 0 {
			return kiwivm.Backup{}, fmt.Errorf("this server has no automatic backups")
		}
		return kiwivm.Backup{}, fmt.Errorf("no backup token starts with %q\n\n  Available:\n    %s",
			ref, strings.Join(tokens, "\n    "))
	default:
		return kiwivm.Backup{}, fmt.Errorf("%q matches %d backups — use a longer prefix", ref, len(partial))
	}
}

// -- abuse --------------------------------------------------------------------

func newAbuseCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "abuse",
		Short: "Suspensions, policy violations and abuse points",
		Long: `Show and clear abuse cases.

A policy violation left unresolved becomes a suspension at its
deadline, so this is the command to run when 'bwg ls' flags a box.

Cases marked soft can be cleared through the API. Hard cases need a
support ticket — bwg says which is which rather than letting you find
out from a failed call.`,
	}

	ls := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list", "status"},
		Short:   "Show suspensions, violations and the abuse-point balance",
		Long: `List abuse cases for a server.

JSON shape:
  {"server","abusePoints","maxAbusePoints","percent","suspensionCount",
   "suspensions":[{"record_id","flag","is_soft","abuse_points"}],
   "violations":[{"record_id","flag","suspend_at","evidence_data"}],
   "evidence":{id:text}}`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, s, err := app.Client()
			if err != nil {
				return err
			}
			ctx, cancel := app.Context(cmd.Context())
			defer cancel()

			susp, err := c.SuspensionDetails(ctx)
			if err != nil {
				return Explain(err, s.Name)
			}
			viol, err := c.PolicyViolations(ctx)
			if err != nil {
				return Explain(err, s.Name)
			}

			pct := 0.0
			if susp.MaxAbusePoints > 0 {
				pct = float64(susp.TotalAbusePoints) / float64(susp.MaxAbusePoints) * 100
			}
			payload := map[string]any{
				"server": s.Name, "abusePoints": susp.TotalAbusePoints.Int(),
				"maxAbusePoints": susp.MaxAbusePoints.Int(), "percent": pct,
				"suspensionCount": susp.SuspensionCount.Int(),
				"suspensions":     susp.Suspensions, "violations": viol.PolicyViolations,
				"evidence": susp.Evidence,
				"hints": map[string]string{
					"resolve":   "bwg abuse resolve <record-id>",
					"unsuspend": "bwg abuse unsuspend <record-id>",
				},
			}
			return app.Emit(payload, func(w io.Writer) {
				output.Tabbed(w, [][2]string{
					{"Abuse points", fmt.Sprintf("%d of %d this year (%s)",
						susp.TotalAbusePoints.Int(), susp.MaxAbusePoints.Int(), output.Usage(pct))},
					{"Suspensions", fmt.Sprint(susp.SuspensionCount.Int())},
				})

				if len(viol.PolicyViolations) > 0 {
					fmt.Fprintf(w, "\n%s\n", output.Bad("Open policy violations"))
					for _, v := range viol.PolicyViolations {
						rows := [][2]string{
							{"Case", fmt.Sprint(v.RecordID.Int())},
							{"Type", v.Flag},
							{"Points", fmt.Sprint(v.AbusePoints.Int())},
							{"Resolvable", resolvableNote(v.APIResolvable(), v.RecordID.Int(), "resolve")},
						}
						if at, ok := v.SuspendsAt(); ok {
							// How long is left is the actionable half; the
							// wall-clock time alone makes you do the maths.
							rows = append(rows, [2]string{"Suspends at",
								output.Bad(fmt.Sprintf("%s (in %s)",
									output.Time(at), output.Duration(time.Until(at))))})
						}
						if v.EvidenceData != "" {
							rows = append(rows, [2]string{"Evidence", output.Truncate(v.EvidenceData, 120)})
						}
						fmt.Fprintln(w)
						output.Tabbed(w, rows)
					}
				}

				if len(susp.Suspensions) > 0 {
					fmt.Fprintf(w, "\n%s\n", output.Bad("Active suspensions"))
					for _, sp := range susp.Suspensions {
						rows := [][2]string{
							{"Case", fmt.Sprint(sp.RecordID.Int())},
							{"Type", sp.Flag},
							{"Points", fmt.Sprint(sp.AbusePoints.Int())},
							{"Resolvable", resolvableNote(sp.APIResolvable(), sp.RecordID.Int(), "unsuspend")},
						}
						if ev := susp.Evidence[strconv.Itoa(sp.EvidenceRecordID.Int())]; ev != "" {
							rows = append(rows, [2]string{"Evidence", output.Truncate(ev, 200)})
						}
						fmt.Fprintln(w)
						output.Tabbed(w, rows)
					}
				}

				if len(viol.PolicyViolations) == 0 && len(susp.Suspensions) == 0 {
					fmt.Fprintf(w, "\n%s Nothing outstanding on %s.\n", output.Good("✓"), s.Name)
				}
			})
		},
	}

	cmd.AddCommand(ls,
		abuseAction(app, "resolve", "resolvePolicyViolation"),
		abuseAction(app, "unsuspend", "unsuspend"))
	return cmd
}

func resolvableNote(soft bool, id int, verb string) string {
	if soft {
		return output.Good(fmt.Sprintf("yes — bwg abuse %s %d", verb, id))
	}
	return output.Bad("no — this one needs a support ticket")
}

func abuseAction(app *App, name, endpoint string) *cobra.Command {
	op := kiwivm.Ops[endpoint]
	short := "Mark a policy violation resolved"
	if name == "unsuspend" {
		short = "Clear an abuse case and unsuspend the VPS"
	}

	return &cobra.Command{
		Use:   name + " <record-id>",
		Short: short,
		Long: fmt.Sprintf(`%s.

Destructive: %s.

Record IDs come from 'bwg abuse ls'. Only cases marked resolvable can
be cleared this way; the rest need a support ticket.`, op.Summary, op.Why),
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]
			if _, err := strconv.Atoi(id); err != nil {
				return fmt.Errorf("%q is not a record ID — see: bwg abuse ls", id)
			}
			c, s, err := app.ClientForOp(op)
			if err != nil {
				return err
			}
			ctx, cancel := app.Context(cmd.Context())
			defer cancel()

			facts, err := abuseFacts(cmd, c, name, id)
			if err != nil {
				return err
			}
			if err := app.Confirm(Consent{
				Op: op, Server: s, Target: "case " + id, Facts: facts,
			}); err != nil {
				return err
			}

			if name == "unsuspend" {
				err = c.Unsuspend(ctx, id)
			} else {
				err = c.ResolvePolicyViolation(ctx, id)
			}
			if err != nil {
				return Explain(err, s.Name)
			}
			return app.Emit(
				map[string]any{"server": s.Name, "recordId": id, "action": name, "status": "done",
					"hints": map[string]string{"verify": "bwg abuse ls"}},
				func(w io.Writer) {
					fmt.Fprintf(w, "%s Case %s %sd — verify with: bwg abuse ls\n",
						output.Good("✓"), id, name)
				})
		},
	}
}

// abuseFacts looks the case up so the card can state what is being
// cleared, and refuses early when the API cannot clear it at all.
func abuseFacts(cmd *cobra.Command, c *kiwivm.Client, action, id string) ([][2]string, error) {
	ctx := cmd.Context()
	want, _ := strconv.Atoi(id)

	if action == "unsuspend" {
		susp, err := c.SuspensionDetails(ctx)
		if err != nil {
			return nil, nil // best effort: the card still shows what bwg knows
		}
		for _, sp := range susp.Suspensions {
			if sp.RecordID.Int() != want {
				continue
			}
			if !sp.APIResolvable() {
				return nil, fmt.Errorf(
					"case %s is not resolvable through the API\n\n"+
						"  Type: %s. Open a support ticket in the billing portal.", id, sp.Flag)
			}
			return [][2]string{
				{"Type", sp.Flag},
				{"Abuse points", fmt.Sprint(sp.AbusePoints.Int())},
			}, nil
		}
		return nil, fmt.Errorf("no suspension with record ID %s — see: bwg abuse ls", id)
	}

	viol, err := c.PolicyViolations(ctx)
	if err != nil {
		return nil, nil
	}
	for _, v := range viol.PolicyViolations {
		if v.RecordID.Int() != want {
			continue
		}
		if !v.APIResolvable() {
			return nil, fmt.Errorf(
				"case %s is not resolvable through the API\n\n"+
					"  Type: %s. Open a support ticket in the billing portal.", id, v.Flag)
		}
		facts := [][2]string{
			{"Type", v.Flag},
			{"Abuse points", fmt.Sprint(v.AbusePoints.Int())},
		}
		if v.EvidenceData != "" {
			facts = append(facts, [2]string{"Evidence", output.Truncate(v.EvidenceData, 160)})
		}
		return facts, nil
	}
	return nil, fmt.Errorf("no policy violation with record ID %s — see: bwg abuse ls", id)
}

// -- rate limit ------------------------------------------------------------------

func newRateLimitCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:     "ratelimit",
		Aliases: []string{"rate"},
		Short:   "Remaining KiwiVM API budget",
		Long: `Show how much API budget is left.

KiwiVM meters requests in points over a 15-minute and a 24-hour
window, and silently drops requests once a budget is spent. This call
costs a point itself, so polling it in a loop is self-defeating.

JSON shape: {"server","remaining15min","remaining24h"}`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, s, err := app.Client()
			if err != nil {
				return err
			}
			ctx, cancel := app.Context(cmd.Context())
			defer cancel()

			rl, err := c.RateLimitStatus(ctx)
			if err != nil {
				return Explain(err, s.Name)
			}
			return app.Emit(
				map[string]any{"server": s.Name,
					"remaining15min": rl.Remaining15Min.Int(), "remaining24h": rl.Remaining24H.Int()},
				func(w io.Writer) {
					output.Tabbed(w, [][2]string{
						{"Next 15 minutes", budgetCell(rl.Remaining15Min.Int())},
						{"Next 24 hours", budgetCell(rl.Remaining24H.Int())},
					})
				})
		},
	}
}

func budgetCell(points int) string {
	s := output.Count(points, "point")
	switch {
	case points <= 0:
		return output.Bad(s + " — requests are being dropped")
	case points < 10:
		return output.Warn(s)
	}
	return output.Good(s)
}

// -- migration -----------------------------------------------------------------

func newMigrateCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "List migration locations and move a VPS",
		Long: `Move a VPS to another datacenter.

Migration replaces every IPv4 address — the old ones are not
recoverable, and anything pointing at them (DNS, firewall allowlists,
license keys) breaks until updated.

Locations differ in bandwidth multiplier, so a move can change the
effective monthly allowance. 'bwg migrate ls' shows each one's.`,
	}

	ls := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list", "locations"},
		Short:   "List locations this VPS can move to",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, s, err := app.Client()
			if err != nil {
				return err
			}
			ctx, cancel := app.Context(cmd.Context())
			defer cancel()

			locs, err := c.MigrateLocations(ctx)
			if err != nil {
				return Explain(err, s.Name)
			}
			return app.Emit(
				map[string]any{"server": s.Name, "current": locs.CurrentLocation,
					"locations": locs.Locations, "descriptions": locs.Descriptions,
					"multipliers": locs.DataTransferMultipliers,
					"hints":       map[string]string{"move": "bwg migrate start <location-id>"}},
				func(w io.Writer) {
					t := output.NewTable("ID", "LOCATION", "BANDWIDTH", "")
					ids := append([]string{}, locs.Locations...)
					sort.Strings(ids)
					for _, id := range ids {
						mult := locs.DataTransferMultipliers[id]
						bw := "1x"
						if mult > 0 {
							bw = fmt.Sprintf("%dx", mult.Int())
						}
						marker := ""
						if id == locs.CurrentLocation {
							marker = output.Good("← current")
						}
						t.Row(id, locs.Descriptions[id], bw, marker)
					}
					t.Render(w)
				})
		},
	}

	start := &cobra.Command{
		Use:   "start <location-id>",
		Short: kiwivm.Ops["migrate/start"].Summary,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, s, err := app.ClientForOp(kiwivm.Ops["migrate/start"])
			if err != nil {
				return err
			}
			ctx, cancel := app.Context(cmd.Context())
			defer cancel()

			locs, err := c.MigrateLocations(ctx)
			if err != nil {
				return Explain(err, s.Name)
			}
			target := args[0]
			if !contains(locs.Locations, target) {
				return fmt.Errorf("%q is not an available location\n\n  See: bwg migrate ls", target)
			}

			facts, _ := app.identifyFacts(ctx, c)
			facts = append(facts,
				[2]string{"From", locs.Descriptions[locs.CurrentLocation]},
				[2]string{"To", locs.Descriptions[target]},
				[2]string{"Bandwidth multiplier", multiplierChange(locs, target)},
				[2]string{"Addresses", output.Bad("every IPv4 address is replaced")})

			if err := app.Confirm(Consent{
				Op: kiwivm.Ops["migrate/start"], Server: s, Target: target, Facts: facts,
			}); err != nil {
				return err
			}
			res, err := c.StartMigration(ctx, target)
			if err != nil {
				return Explain(err, s.Name)
			}
			return app.Emit(
				map[string]any{"server": s.Name, "location": target, "status": "migrating",
					"newIps": res.NewIPs, "notificationEmail": res.NotificationEmail,
					"hints": map[string]string{"dns": "update DNS and firewall rules for the new addresses"}},
				func(w io.Writer) {
					fmt.Fprintf(w, "%s Migrating %s to %s\n\n",
						output.Good("✓"), output.Strong(s.Name), locs.Descriptions[target])
					if len(res.NewIPs) > 0 {
						output.Tabbed(w, [][2]string{
							{"New addresses", strings.Join(res.NewIPs, ", ")},
							{"Notify", res.NotificationEmail},
						})
						fmt.Fprintf(w, "\n%s\n", output.Warn("Update DNS and any firewall allowlists now."))
					}
				})
		},
	}

	cmd.AddCommand(ls, start)
	return cmd
}

func multiplierChange(locs *kiwivm.MigrateLocations, target string) string {
	from := locs.DataTransferMultipliers[locs.CurrentLocation]
	to := locs.DataTransferMultipliers[target]
	if from == to {
		return fmt.Sprintf("%dx (unchanged)", orOne(to))
	}
	return fmt.Sprintf("%dx -> %dx", orOne(from), orOne(to))
}

func orOne(v kiwivm.Int) int {
	if v <= 0 {
		return 1
	}
	return v.Int()
}

// -- notifications ------------------------------------------------------------

func newNotifyCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "notify",
		Short: "Show and change KiwiVM email notification settings",
	}

	ls := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List notification preferences and their state",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, s, err := app.Client()
			if err != nil {
				return err
			}
			ctx, cancel := app.Context(cmd.Context())
			defer cancel()

			prefs, err := c.NotificationPreferences(ctx)
			if err != nil {
				return Explain(err, s.Name)
			}
			flat := prefs.Flat()
			return app.Emit(
				map[string]any{"server": s.Name, "email": prefs.NotificationEmail,
					"preferences": flat, "grouped": prefs.EmailPreferences,
					"hints": map[string]string{"enable": "bwg notify set <id>=on"}},
				func(w io.Writer) {
					fmt.Fprintf(w, "%s %s\n\n", output.Dim("Notifications go to"), prefs.NotificationEmail)
					ids := make([]string, 0, len(flat))
					for id := range flat {
						ids = append(ids, id)
					}
					sort.Strings(ids)

					t := output.NewTable("ID", "STATE", "DESCRIPTION")
					for _, id := range ids {
						p := flat[id]
						state := output.Dim("off")
						if p.IsEnabled.Bool() {
							state = output.Good("on")
						}
						t.Row(id, state, p.FriendlyDescription)
					}
					t.Render(w)
				})
		},
	}

	set := &cobra.Command{
		Use:   "set <id>=on|off [<id>=on|off...]",
		Short: kiwivm.Ops["kiwivm/setNotificationPreferences"].Summary,
		Long: `Enable or disable notification preferences by ID.

IDs come from 'bwg notify ls'. KiwiVM silently ignores unknown IDs, so
bwg compares what came back against what it sent and tells you if
something did not take.`,
		Example: `  bwg notify set snapshot_done=on
  bwg notify set bandwidth_80=on suspension=on`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			prefs := map[string]bool{}
			for _, arg := range args {
				id, val, ok := strings.Cut(arg, "=")
				if !ok {
					return fmt.Errorf("%q must be <id>=on or <id>=off — see: bwg notify ls", arg)
				}
				switch strings.ToLower(val) {
				case "on", "1", "true", "yes":
					prefs[id] = true
				case "off", "0", "false", "no":
					prefs[id] = false
				default:
					return fmt.Errorf("%q: %q is not on or off", arg, val)
				}
			}

			c, s, err := app.ClientForOp(kiwivm.Ops["kiwivm/setNotificationPreferences"])
			if err != nil {
				return err
			}
			ctx, cancel := app.Context(cmd.Context())
			defer cancel()

			var facts [][2]string
			for _, id := range sortedBoolKeys(prefs) {
				facts = append(facts, [2]string{id, boolWord(prefs[id])})
			}
			if err := app.Confirm(Consent{
				Op: kiwivm.Ops["kiwivm/setNotificationPreferences"], Server: s, Facts: facts,
			}); err != nil {
				return err
			}

			res, err := c.SetNotificationPreferences(ctx, prefs)
			if err != nil {
				return Explain(err, s.Name)
			}

			// KiwiVM accepts unknown IDs without complaint, so the only
			// way to know a change took is to check what came back.
			var ignored []string
			for id := range prefs {
				if _, ok := res.Updated[id]; !ok {
					if _, wasSubmitted := res.Submitted[id]; !wasSubmitted {
						ignored = append(ignored, id)
					}
				}
			}
			sort.Strings(ignored)

			return app.Emit(
				map[string]any{"server": s.Name, "submitted": res.Submitted,
					"updated": res.Updated, "ignored": ignored},
				func(w io.Writer) {
					fmt.Fprintf(w, "%s %s updated.\n", output.Good("✓"), output.Count(len(res.Updated), "preference"))
					if len(ignored) > 0 {
						fmt.Fprintf(w, "%s KiwiVM ignored: %s\n  %s\n",
							output.Warn("!"), strings.Join(ignored, ", "),
							output.Dim("Check the IDs with: bwg notify ls"))
					}
				})
		},
	}

	cmd.AddCommand(ls, set)
	return cmd
}

func sortedBoolKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func boolWord(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// -- shell --------------------------------------------------------------------

func newExecCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "exec <command>",
		Short: kiwivm.Ops["basicShell/exec"].Summary,
		Long: `Run a command inside the VPS as root and wait for the output.

This runs through KiwiVM's out-of-band shell, not SSH: it works even
when the network inside the guest is broken, which is exactly when you
need it. It is also arbitrary root code execution, which is why it is
classified destructive regardless of the command.

The command's exit status becomes bwg's exit status, so this composes
in a shell the way you would expect.

For anything long-running, use 'bwg run' — this call blocks.`,
		Example: `  bwg exec 'df -h'
  bwg exec 'systemctl status nginx'
  bwg exec 'ip a' --server tokyo`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, s, err := app.ClientForOp(kiwivm.Ops["basicShell/exec"])
			if err != nil {
				return err
			}
			ctx, cancel := app.Context(cmd.Context())
			defer cancel()

			facts, _ := app.identifyFacts(ctx, c)
			facts = append(facts, [2]string{"Command", output.Strong(args[0])})
			if err := app.Confirm(Consent{
				Op: kiwivm.Ops["basicShell/exec"], Server: s, Facts: facts,
			}); err != nil {
				return err
			}

			res, err := c.ShellExec(ctx, args[0])
			if err != nil {
				return Explain(err, s.Name)
			}

			err = app.Emit(
				map[string]any{"server": s.Name, "command": args[0],
					"exitStatus": res.ExitStatus, "output": res.Output},
				func(w io.Writer) {
					if res.Output != "" {
						fmt.Fprintln(w, strings.TrimRight(res.Output, "\n"))
					}
				})
			if err != nil {
				return err
			}
			// The guest command's status is the meaningful result; pass
			// it through rather than reporting bwg's own success.
			if res.ExitStatus != 0 {
				return &ExitCodeError{Code: res.ExitStatus,
					Err: fmt.Errorf("command exited %d", res.ExitStatus)}
			}
			return nil
		},
	}
}

func newRunCmd(app *App) *cobra.Command {
	var file string
	cmd := &cobra.Command{
		Use:   "run [script]",
		Short: kiwivm.Ops["shellScript/exec"].Summary,
		Long: `Run a shell script inside the VPS as root, detached.

The call returns as soon as the script is queued and gives back the
name of the log file it writes to inside the VPS. There is no way to
recall a script once it starts.

Give the script as an argument, with --file, or on stdin.`,
		Example: `  bwg run 'apt-get update && apt-get -y upgrade'
  bwg run --file ./bootstrap.sh
  cat bootstrap.sh | bwg run`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			script, err := readScript(app, args, file)
			if err != nil {
				return err
			}

			c, s, err := app.ClientForOp(kiwivm.Ops["shellScript/exec"])
			if err != nil {
				return err
			}
			ctx, cancel := app.Context(cmd.Context())
			defer cancel()

			facts, _ := app.identifyFacts(ctx, c)
			facts = append(facts,
				[2]string{"Lines", fmt.Sprint(len(strings.Split(strings.TrimSpace(script), "\n")))},
				[2]string{"Starts with", output.Truncate(firstScriptLine(script), 60)})
			if err := app.Confirm(Consent{
				Op: kiwivm.Ops["shellScript/exec"], Server: s, Facts: facts,
			}); err != nil {
				return err
			}

			res, err := c.ScriptExec(ctx, script)
			if err != nil {
				return Explain(err, s.Name)
			}
			return app.Emit(
				map[string]any{"server": s.Name, "log": res.Log, "status": "running",
					"hints": map[string]string{
						"output": fmt.Sprintf("bwg exec 'cat %s'", res.Log)}},
				func(w io.Writer) {
					fmt.Fprintf(w, "%s Script started on %s.\n  %s\n",
						output.Good("✓"), output.Strong(s.Name),
						output.Dim(fmt.Sprintf("Read its output: bwg exec 'cat %s'", res.Log)))
				})
		},
	}
	cmd.Flags().StringVarP(&file, "file", "f", "", "Read the script from a file")
	return cmd
}

func readScript(app *App, args []string, file string) (string, error) {
	switch {
	case file != "" && len(args) > 0:
		return "", fmt.Errorf("give the script as an argument or with --file, not both")
	case file != "":
		data, err := os.ReadFile(file)
		if err != nil {
			return "", fmt.Errorf("reading %s: %w", file, err)
		}
		return string(data), nil
	case len(args) > 0:
		return args[0], nil
	}

	// Check the stream that is actually about to be read, not os.Stdin:
	// they are the same file in production, and only the former is
	// true in a test or when a caller supplies its own input.
	if f, ok := app.In.(*os.File); ok && output.IsTerminal(f) {
		return "", fmt.Errorf("no script given\n\n" +
			"  bwg run 'apt-get update'\n" +
			"  bwg run --file ./bootstrap.sh\n" +
			"  cat bootstrap.sh | bwg run")
	}
	data, err := io.ReadAll(app.In)
	if err != nil {
		return "", fmt.Errorf("reading the script from stdin: %w", err)
	}
	if strings.TrimSpace(string(data)) == "" {
		return "", fmt.Errorf("the script on stdin is empty")
	}
	return string(data), nil
}

// firstScriptLine returns the first line that actually does something,
// so the consent card shows the command rather than a shebang.
func firstScriptLine(script string) string {
	for _, line := range strings.Split(script, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		return line
	}
	return strings.TrimSpace(script)
}
