package cli

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/lroolle/bwg-cli/kiwivm"
	"github.com/lroolle/bwg-cli/pkg/output"
	"github.com/spf13/cobra"
)

func newSnapshotCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "snapshot",
		Aliases: []string{"snap"},
		Short:   "List, create, restore, delete and transfer snapshots",
		Long: `Snapshots are KiwiVM's manual restore points.

Unprotected snapshots are purged automatically; 'sticky' ones are not.
'bwg snapshot ls' shows how long each one has left.`,
	}
	cmd.AddCommand(
		snapshotLs(app),
		snapshotCreate(app),
		snapshotDelete(app),
		snapshotRestore(app),
		snapshotSticky(app),
		snapshotExport(app),
		snapshotImport(app),
	)
	return cmd
}

func snapshotLs(app *App) *cobra.Command {
	return &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List snapshots",
		Long: `List the snapshots stored for a server.

JSON shape:
  {"server","snapshots":[{"fileName","os","description","size",
   "uncompressed","md5","sticky","purgesIn","downloadLink",
   "downloadLinkSSL"}],"count"}`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, s, err := app.Client()
			if err != nil {
				return err
			}
			ctx, cancel := app.Context(cmd.Context())
			defer cancel()

			list, err := c.Snapshots(ctx)
			if err != nil {
				return Explain(err, s.Name)
			}
			payload := map[string]any{
				"server": s.Name, "snapshots": list.Snapshots, "count": len(list.Snapshots),
				"hints": map[string]string{
					"create":  "bwg snapshot create --description <text>",
					"protect": "bwg snapshot sticky <fileName>",
				},
			}
			return app.Emit(payload, func(w io.Writer) {
				if len(list.Snapshots) == 0 {
					fmt.Fprintf(w, "No snapshots on %s. Create one: bwg snapshot create\n", s.Name)
					return
				}
				t := output.NewTable("FILE", "OS", "SIZE", "DESCRIPTION", "PURGES", "STICKY").
					RightAlign(2)
				for _, sn := range list.Snapshots {
					t.Row(
						output.Truncate(sn.FileName, 34),
						sn.OS,
						output.Bytes(sn.Size.Int64()),
						output.Truncate(sn.Description, 24),
						purgeCell(sn),
						stickyCell(sn),
					)
				}
				t.Render(w)
			})
		},
	}
}

func purgeCell(sn kiwivm.Snapshot) string {
	if _, ok := sn.PurgesAt(); !ok {
		return output.Dim("never")
	}
	left := time.Duration(sn.PurgesIn.Int64()) * time.Second
	label := output.Duration(left)
	// A snapshot about to age out is the one you might want to make
	// sticky, so it is worth flagging in the listing.
	if left < 3*24*time.Hour {
		return output.Warn(label)
	}
	return label
}

func stickyCell(sn kiwivm.Snapshot) string {
	if sn.Sticky.Bool() {
		return output.Good("yes")
	}
	return ""
}

func snapshotCreate(app *App) *cobra.Command {
	var description string
	cmd := &cobra.Command{
		Use:   "create",
		Short: kiwivm.Ops["snapshot/create"].Summary,
		Long: `Create a snapshot.

KiwiVM locks the VPS while the snapshot runs and emails when it
finishes; the command returns as soon as the task is queued. Watch
progress with 'bwg snapshot ls' — a locked VPS reports its progress in
the error from any other call.`,
		Example: `  bwg snapshot create --description "before the upgrade"`,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, s, err := app.ClientForOp(kiwivm.Ops["snapshot/create"])
			if err != nil {
				return err
			}
			ctx, cancel := app.Context(cmd.Context())
			defer cancel()

			if err := app.Confirm(Consent{
				Op: kiwivm.Ops["snapshot/create"], Server: s, Target: description,
				Facts: [][2]string{{"Effect", "the VPS is locked while the snapshot runs"}},
			}); err != nil {
				return err
			}

			res, err := c.CreateSnapshot(ctx, description)
			if err != nil {
				return Explain(err, s.Name)
			}
			payload := map[string]any{
				"server": s.Name, "status": "queued",
				"description": description, "notificationEmail": res.NotificationEmail,
				"hints": map[string]string{"check": "bwg snapshot ls"},
			}
			return app.Emit(payload, func(w io.Writer) {
				fmt.Fprintf(w, "%s Snapshot queued on %s.\n", output.Good("✓"), output.Strong(s.Name))
				if res.NotificationEmail != "" {
					fmt.Fprintf(w, "  %s\n", output.Dim("Notification goes to "+res.NotificationEmail))
				}
				fmt.Fprintf(w, "  %s\n", output.Dim("Check with: bwg snapshot ls"))
			})
		},
	}
	cmd.Flags().StringVarP(&description, "description", "d", "", "Snapshot description")
	return cmd
}

func snapshotDelete(app *App) *cobra.Command {
	return &cobra.Command{
		Use:     "rm <fileName>",
		Aliases: []string{"delete"},
		Short:   kiwivm.Ops["snapshot/delete"].Summary,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, s, err := app.ClientForOp(kiwivm.Ops["snapshot/delete"])
			if err != nil {
				return err
			}
			ctx, cancel := app.Context(cmd.Context())
			defer cancel()

			sn, err := app.findSnapshot(ctx, c, args[0])
			if err != nil {
				return Explain(err, s.Name)
			}
			if err := app.Confirm(Consent{
				Op: kiwivm.Ops["snapshot/delete"], Server: s, Target: sn.FileName,
				Facts: snapshotFacts(sn),
			}); err != nil {
				return err
			}
			if err := c.DeleteSnapshot(ctx, sn.FileName); err != nil {
				return Explain(err, s.Name)
			}
			return app.Emit(
				map[string]any{"server": s.Name, "snapshot": sn.FileName, "status": "deleted"},
				func(w io.Writer) {
					fmt.Fprintf(w, "%s Deleted %s\n", output.Good("✓"), sn.FileName)
				})
		},
	}
}

func snapshotRestore(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "restore <fileName>",
		Short: kiwivm.Ops["snapshot/restore"].Summary,
		Long: `Restore a snapshot over the VPS.

Everything currently on the disk is replaced by the snapshot. This
prompt asks for the server name rather than y/N: the mistake worth
catching here is restoring onto the wrong box.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, s, err := app.ClientForOp(kiwivm.Ops["snapshot/restore"])
			if err != nil {
				return err
			}
			ctx, cancel := app.Context(cmd.Context())
			defer cancel()

			sn, err := app.findSnapshot(ctx, c, args[0])
			if err != nil {
				return Explain(err, s.Name)
			}
			facts := snapshotFacts(sn)
			if hostFacts, err := app.identifyFacts(ctx, c); err == nil {
				facts = append(hostFacts, facts...)
			}
			if err := app.Confirm(Consent{
				Op: kiwivm.Ops["snapshot/restore"], Server: s, Target: sn.FileName, Facts: facts,
			}); err != nil {
				return err
			}
			if err := c.RestoreSnapshot(ctx, sn.FileName); err != nil {
				return Explain(err, s.Name)
			}
			return app.Emit(
				map[string]any{"server": s.Name, "snapshot": sn.FileName, "status": "restoring",
					"hints": map[string]string{"check": "bwg status"}},
				func(w io.Writer) {
					fmt.Fprintf(w, "%s Restoring %s on %s — watch with: bwg status\n",
						output.Good("✓"), sn.FileName, output.Strong(s.Name))
				})
		},
	}
}

func snapshotSticky(app *App) *cobra.Command {
	var off bool
	cmd := &cobra.Command{
		Use:   "sticky <fileName>",
		Short: "Protect a snapshot from automatic purge (or stop protecting it)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, s, err := app.ClientForOp(kiwivm.Ops["snapshot/toggleSticky"])
			if err != nil {
				return err
			}
			ctx, cancel := app.Context(cmd.Context())
			defer cancel()

			sn, err := app.findSnapshot(ctx, c, args[0])
			if err != nil {
				return Explain(err, s.Name)
			}
			want := !off
			if err := app.Confirm(Consent{
				Op: kiwivm.Ops["snapshot/toggleSticky"], Server: s, Target: sn.FileName,
				Facts: append(snapshotFacts(sn),
					[2]string{"Change", stickyChange(sn.Sticky.Bool(), want)}),
			}); err != nil {
				return err
			}
			if err := c.SetSnapshotSticky(ctx, sn.FileName, want); err != nil {
				return Explain(err, s.Name)
			}
			return app.Emit(
				map[string]any{"server": s.Name, "snapshot": sn.FileName, "sticky": want},
				func(w io.Writer) {
					verb := "protected from purge"
					if !want {
						verb = "no longer protected"
					}
					fmt.Fprintf(w, "%s %s is %s\n", output.Good("✓"), sn.FileName, verb)
				})
		},
	}
	cmd.Flags().BoolVar(&off, "off", false, "Remove protection instead of adding it")
	return cmd
}

func stickyChange(from, to bool) string {
	label := func(b bool) string {
		if b {
			return "protected"
		}
		return "purgeable"
	}
	if from == to {
		return "already " + label(to)
	}
	return label(from) + " -> " + label(to)
}

func snapshotExport(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "export <fileName>",
		Short: "Mint a transfer token so another instance can import this snapshot",
		Long: `Generate a transfer token for a snapshot.

Run this on the SOURCE server, then import on the destination:

  bwg snapshot export snap.tar.gz --server source
  bwg snapshot import --from-veid <source-veid> --token <token> --server dest`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, s, err := app.ClientForOp(kiwivm.Ops["snapshot/export"])
			if err != nil {
				return err
			}
			ctx, cancel := app.Context(cmd.Context())
			defer cancel()

			sn, err := app.findSnapshot(ctx, c, args[0])
			if err != nil {
				return Explain(err, s.Name)
			}
			if err := app.Confirm(Consent{
				Op: kiwivm.Ops["snapshot/export"], Server: s, Target: sn.FileName,
				Facts: snapshotFacts(sn),
			}); err != nil {
				return err
			}
			res, err := c.ExportSnapshot(ctx, sn.FileName)
			if err != nil {
				return Explain(err, s.Name)
			}
			return app.Emit(
				map[string]any{"server": s.Name, "snapshot": sn.FileName,
					"sourceVeid": s.VEID, "token": res.Token,
					"hints": map[string]string{"import": fmt.Sprintf(
						"bwg snapshot import --from-veid %s --token %s --server <dest>",
						s.VEID, res.Token)}},
				func(w io.Writer) {
					fmt.Fprintf(w, "%s Transfer token for %s:\n\n  %s\n\n%s\n",
						output.Good("✓"), sn.FileName, output.Strong(res.Token),
						output.Dim(fmt.Sprintf(
							"Import it: bwg snapshot import --from-veid %s --token %s --server <dest>",
							s.VEID, res.Token)))
				})
		},
	}
}

func snapshotImport(app *App) *cobra.Command {
	var fromVeid, token string
	cmd := &cobra.Command{
		Use:   "import",
		Short: "Import a snapshot exported from another instance",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if fromVeid == "" || token == "" {
				return fmt.Errorf("--from-veid and --token are both required\n\n" +
					"  Get them by running on the source server:\n" +
					"    bwg snapshot export <fileName> --server <source>")
			}
			c, s, err := app.ClientForOp(kiwivm.Ops["snapshot/import"])
			if err != nil {
				return err
			}
			ctx, cancel := app.Context(cmd.Context())
			defer cancel()

			if err := app.Confirm(Consent{
				Op: kiwivm.Ops["snapshot/import"], Server: s,
				Target: "snapshot from veid " + fromVeid,
			}); err != nil {
				return err
			}
			if err := c.ImportSnapshot(ctx, fromVeid, token); err != nil {
				return Explain(err, s.Name)
			}
			return app.Emit(
				map[string]any{"server": s.Name, "sourceVeid": fromVeid, "status": "imported",
					"hints": map[string]string{"list": "bwg snapshot ls"}},
				func(w io.Writer) {
					fmt.Fprintf(w, "%s Imported from veid %s — see: bwg snapshot ls\n",
						output.Good("✓"), fromVeid)
				})
		},
	}
	cmd.Flags().StringVar(&fromVeid, "from-veid", "", "Source VPS ID (required)")
	cmd.Flags().StringVar(&token, "token", "", "Token from 'bwg snapshot export' (required)")
	return cmd
}

// findSnapshot resolves a snapshot reference to a real snapshot.
//
// Exact fileName wins; otherwise a unique substring of the fileName or
// description is accepted, because the real names are unguessable
// strings like "1347645_20260807_a1b2.tar.gz" and nobody types those.
func (a *App) findSnapshot(ctx context.Context, c *kiwivm.Client, ref string) (kiwivm.Snapshot, error) {
	list, err := c.Snapshots(ctx)
	if err != nil {
		return kiwivm.Snapshot{}, err
	}
	if len(list.Snapshots) == 0 {
		return kiwivm.Snapshot{}, fmt.Errorf("this server has no snapshots")
	}

	var partial []kiwivm.Snapshot
	for _, sn := range list.Snapshots {
		if sn.FileName == ref {
			return sn, nil
		}
		if strings.Contains(sn.FileName, ref) ||
			(sn.Description != "" && strings.Contains(
				strings.ToLower(sn.Description), strings.ToLower(ref))) {
			partial = append(partial, sn)
		}
	}
	switch len(partial) {
	case 1:
		return partial[0], nil
	case 0:
		var names []string
		for _, sn := range list.Snapshots {
			names = append(names, sn.FileName)
		}
		return kiwivm.Snapshot{}, fmt.Errorf(
			"no snapshot matches %q\n\n  Available:\n    %s",
			ref, strings.Join(names, "\n    "))
	default:
		var names []string
		for _, sn := range partial {
			names = append(names, sn.FileName)
		}
		return kiwivm.Snapshot{}, fmt.Errorf(
			"%q matches %d snapshots — be more specific:\n    %s",
			ref, len(partial), strings.Join(names, "\n    "))
	}
}

// snapshotFacts is what someone needs to know to answer "restore this
// one?" — chiefly how old it is and what was on it.
func snapshotFacts(sn kiwivm.Snapshot) [][2]string {
	facts := [][2]string{
		{"Snapshot OS", sn.OS},
		{"Size", output.Bytes(sn.Size.Int64())},
	}
	if sn.Description != "" {
		facts = append(facts, [2]string{"Description", sn.Description})
	}
	if at, ok := sn.PurgesAt(); ok {
		facts = append(facts, [2]string{"Purges", output.Time(at)})
	} else {
		facts = append(facts, [2]string{"Purges", "never (sticky)"})
	}
	return facts
}
