package cli

import (
	"fmt"
	"io"
	"sort"

	"github.com/lroolle/bwg-cli/kiwivm"
	"github.com/lroolle/bwg-cli/pkg/output"
	"github.com/spf13/cobra"
)

// DayUsage is one calendar day of the usage series.
type DayUsage struct {
	Date       string `json:"date"`
	NetworkIn  int64  `json:"networkIn"`
	NetworkOut int64  `json:"networkOut"`
	DiskRead   int64  `json:"diskRead"`
	DiskWrite  int64  `json:"diskWrite"`
	CPUAvg     int    `json:"cpuAvg"`
	Samples    int    `json:"samples"`
}

func newUsageCmd(app *App) *cobra.Command {
	var (
		raw  bool
		days int
	)

	cmd := &cobra.Command{
		Use:   "usage",
		Short: "CPU, network and disk usage over time, plus quota headroom",
		Long: `Show resource usage from KiwiVM's sampled statistics.

Samples are aggregated per calendar day, which is the granularity that
answers "when did the traffic spike". Use --raw for the individual
samples.

KiwiVM keeps roughly two years of samples. The default window is the
last 30 days, which is the billing cycle the quota below is measured
against; --days 0 prints everything it kept. The window applies to the
whole output — table, totals and JSON all describe the same span.

The quota summary at the bottom is the same arithmetic 'bwg info'
shows: transfer counters with the location multiplier applied.

JSON shape:
  {"server","vmType","days":[{"date","networkIn","networkOut",
   "diskRead","diskWrite","cpuAvg","samples"}],
   "totals":{"networkIn","networkOut","diskRead","diskWrite"},
   "window":{"days","available"},
   "bandwidth":{...},"samples":[...] (only with --raw)}`,
		Example: `  bwg usage
  bwg usage --days 7
  bwg usage --days 0                    # everything KiwiVM kept
  bwg usage --json --jq '.totals.networkOut'`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, s, err := app.Client()
			if err != nil {
				return err
			}
			ctx, cancel := app.Context(cmd.Context())
			defer cancel()

			stats, err := c.RawUsageStats(ctx)
			if err != nil {
				return Explain(err, s.Name)
			}
			// The quota picture belongs next to the traffic that consumed
			// it; fetching it separately is one extra cheap call.
			info, infoErr := c.ServiceInfo(ctx)

			shown, available := window(stats, days)
			byDay := aggregateByDay(shown)
			in, out, dr, dw := shown.Totals()

			payload := map[string]any{
				"server": s.Name, "vmType": stats.VMType, "days": byDay,
				"totals": map[string]int64{
					"networkIn": in, "networkOut": out, "diskRead": dr, "diskWrite": dw,
				},
				"window": map[string]int{"days": len(byDay), "available": available},
			}
			if infoErr == nil {
				payload["bandwidth"] = info.Bandwidth()
			}
			if raw {
				payload["samples"] = shown.Data
			}

			return app.Emit(payload, func(w io.Writer) {
				renderUsage(w, s.Name, shown, byDay, available, raw)
				if infoErr == nil {
					b := info.Bandwidth()
					fmt.Fprintf(w, "\n%s %s %s of %s used, %s free, resets in %s\n",
						output.Dim("Quota"), output.Bar(b.Percent, 12),
						output.Usage(b.Percent), output.Bytes(b.Total),
						output.Bytes(b.Free), output.Duration(b.ResetsIn()))
				}
			})
		},
	}

	cmd.Flags().BoolVar(&raw, "raw", false, "Show individual samples instead of daily totals")
	cmd.Flags().IntVar(&days, "days", defaultUsageDays,
		"Only the most recent N days (0 = everything KiwiVM kept)")
	return cmd
}

// defaultUsageDays is the billing cycle, which is the span the quota
// line underneath the table is measured over. Printing two years of
// rows by default put the interesting end of the series off-screen and
// the summary out of reach.
const defaultUsageDays = 30

// window trims the series to the most recent n calendar days and
// reports how many days the full series covered.
//
// Everything downstream reads this one slice — the daily table, --raw,
// the totals line and the JSON payload — so no two parts of the output
// can end up describing different spans. Before this, --days 7 printed
// seven rows above a total covering two years.
func window(stats *kiwivm.UsageStats, days int) (*kiwivm.UsageStats, int) {
	dates := map[string]bool{}
	for _, s := range stats.Data {
		dates[s.Time().Format("2006-01-02")] = true
	}
	available := len(dates)
	if days <= 0 || available <= days {
		return stats, available
	}

	keep := make([]string, 0, len(dates))
	for d := range dates {
		keep = append(keep, d)
	}
	sort.Strings(keep)
	// ISO dates sort lexically, so the cutoff is a string compare.
	cutoff := keep[len(keep)-days]

	trimmed := &kiwivm.UsageStats{VMType: stats.VMType}
	for _, s := range stats.Data {
		if s.Time().Format("2006-01-02") >= cutoff {
			trimmed.Data = append(trimmed.Data, s)
		}
	}
	return trimmed, available
}

// aggregateByDay rolls samples up per calendar day in the local zone.
func aggregateByDay(stats *kiwivm.UsageStats) []DayUsage {
	type acc struct {
		DayUsage
		cpuSum int
	}
	byDate := map[string]*acc{}

	for _, s := range stats.Data {
		key := s.Time().Format("2006-01-02")
		a := byDate[key]
		if a == nil {
			a = &acc{DayUsage: DayUsage{Date: key}}
			byDate[key] = a
		}
		a.NetworkIn += s.NetworkInBytes.Int64()
		a.NetworkOut += s.NetworkOutBytes.Int64()
		a.DiskRead += s.DiskReadBytes.Int64()
		a.DiskWrite += s.DiskWriteBytes.Int64()
		a.cpuSum += s.CPUUsage.Int()
		a.Samples++
	}

	out := make([]DayUsage, 0, len(byDate))
	for _, a := range byDate {
		d := a.DayUsage
		if a.Samples > 0 {
			d.CPUAvg = a.cpuSum / a.Samples
		}
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Date < out[j].Date })
	return out
}

func renderUsage(w io.Writer, server string, stats *kiwivm.UsageStats, days []DayUsage, available int, raw bool) {
	if len(stats.Data) == 0 {
		fmt.Fprintf(w, "KiwiVM has no usage samples for %s yet.\n", server)
		return
	}

	if raw {
		t := output.NewTable("TIME", "CPU", "NET IN", "NET OUT", "DISK R", "DISK W").
			RightAlign(1, 2, 3, 4, 5)
		for _, s := range stats.Data {
			t.Row(
				s.Time().Format("Jan 02 15:04"),
				fmt.Sprintf("%d%%", s.CPUUsage.Int()),
				output.Bytes(s.NetworkInBytes.Int64()),
				output.Bytes(s.NetworkOutBytes.Int64()),
				output.Bytes(s.DiskReadBytes.Int64()),
				output.Bytes(s.DiskWriteBytes.Int64()),
			)
		}
		t.Render(w)
	} else {
		// Scale the sparkline to the busiest day so the shape is readable
		// regardless of absolute volume.
		var peak int64
		for _, d := range days {
			if total := d.NetworkIn + d.NetworkOut; total > peak {
				peak = total
			}
		}

		t := output.NewTable("DATE", "TRAFFIC", "NET IN", "NET OUT", "DISK R", "DISK W", "CPU").
			RightAlign(2, 3, 4, 5, 6)
		for _, d := range days {
			pct := 0.0
			if peak > 0 {
				pct = float64(d.NetworkIn+d.NetworkOut) / float64(peak) * 100
			}
			t.Row(
				d.Date,
				output.Colorize(bars(pct, 10), output.Blue),
				output.Bytes(d.NetworkIn),
				output.Bytes(d.NetworkOut),
				output.Bytes(d.DiskRead),
				output.Bytes(d.DiskWrite),
				fmt.Sprintf("%d%%", d.CPUAvg),
			)
		}
		t.Render(w)
	}

	in, out, dr, dw := stats.Totals()
	fmt.Fprintf(w, "\n%s %s in, %s out, %s read, %s written over %s\n",
		output.Dim("Total:"), output.Bytes(in), output.Bytes(out),
		output.Bytes(dr), output.Bytes(dw), output.Count(len(days), "day"))

	// Say what was left out. A table that silently stops at 30 rows
	// reads like a server with no history before last month.
	if available > len(days) {
		fmt.Fprintf(w, "%s\n", output.Dim(fmt.Sprintf(
			"Showing %d of the %d days KiwiVM kept — for all of it: bwg usage --days 0",
			len(days), available)))
	}
}

// bars renders a plain proportional bar. Unlike output.Bar it carries
// no severity colour: a busy day is information, not a problem.
func bars(percent float64, width int) string {
	filled := int(percent / 100 * float64(width))
	if filled < 0 {
		filled = 0
	}
	if filled > width {
		filled = width
	}
	out := make([]rune, width)
	for i := range out {
		if i < filled {
			out[i] = '▇'
		} else {
			out[i] = '·'
		}
	}
	return string(out)
}
