package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/lroolle/bwg-cli/pkg/output"
	"github.com/spf13/cobra"
)

func newAuditCmd(app *App) *cobra.Command {
	var (
		limit  int
		filter string
	)

	cmd := &cobra.Command{
		Use:   "audit",
		Short: "KiwiVM control-panel audit log",
		Long: `Show what has been done to this VPS through the panel and the API.

This is the record to check after an unexpected reboot or reinstall:
it carries the requesting IP for each event.

JSON shape:
  {"server","entries":[{"timestamp","time","requestorIp","type",
   "summary"}],"count"}`,
		Example: `  bwg audit
  bwg audit --limit 10
  bwg audit --grep reinstall`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, s, err := app.Client()
			if err != nil {
				return err
			}
			ctx, cancel := app.Context(cmd.Context())
			defer cancel()

			log, err := c.AuditLog(ctx)
			if err != nil {
				return Explain(err, s.Name)
			}

			type entry struct {
				Timestamp   int64  `json:"timestamp"`
				Time        string `json:"time"`
				RequestorIP string `json:"requestorIp,omitempty"`
				Type        int    `json:"type"`
				Summary     string `json:"summary"`

				display string
			}
			var entries []entry
			for _, e := range log.LogEntries {
				if filter != "" && !strings.Contains(
					strings.ToLower(e.Summary), strings.ToLower(filter)) {
					continue
				}
				entries = append(entries, entry{
					Timestamp: e.Timestamp.Int64(), Time: e.Time().Format("2006-01-02T15:04:05Z07:00"),
					RequestorIP: e.RequestorIP(), Type: e.Type.Int(), Summary: e.Summary,
					display: e.Time().Format("2006-01-02 15:04"),
				})
			}
			// Newest first: the reason you opened the audit log is
			// almost always the most recent thing in it.
			for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
				entries[i], entries[j] = entries[j], entries[i]
			}
			if limit > 0 && len(entries) > limit {
				entries = entries[:limit]
			}

			payload := map[string]any{
				"server": s.Name, "entries": entries, "count": len(entries),
			}
			return app.Emit(payload, func(w io.Writer) {
				if len(entries) == 0 {
					if filter != "" {
						fmt.Fprintf(w, "No audit entries match %q.\n", filter)
					} else {
						fmt.Fprintf(w, "No audit entries for %s.\n", s.Name)
					}
					return
				}
				t := output.NewTable("WHEN", "FROM", "EVENT")
				for _, e := range entries {
					t.Row(e.display, e.RequestorIP, output.Truncate(e.Summary, 70))
				}
				t.Render(w)
			})
		},
	}

	cmd.Flags().IntVarP(&limit, "limit", "n", 0, "Show only the most recent N entries")
	cmd.Flags().StringVar(&filter, "grep", "", "Only entries whose summary contains this text")
	return cmd
}
