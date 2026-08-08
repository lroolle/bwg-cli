package cli

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/lroolle/bwg-cli/internal/config"
	"github.com/lroolle/bwg-cli/internal/fleet"
	"github.com/lroolle/bwg-cli/kiwivm"
	"github.com/lroolle/bwg-cli/pkg/output"
	"github.com/spf13/cobra"
)

// FleetEntry is one row of `bwg ls`: the standing facts about a VPS
// plus the numbers that decide whether it needs attention.
type FleetEntry struct {
	Server    string   `json:"server"`
	VEID      string   `json:"veid"`
	Hostname  string   `json:"hostname"`
	Plan      string   `json:"plan"`
	Location  string   `json:"location"`
	OS        string   `json:"os"`
	VMType    string   `json:"vmType"`
	IPv4      []string `json:"ipv4,omitempty"`
	IPv6      []string `json:"ipv6,omitempty"`
	Tags      []string `json:"tags,omitempty"`
	Note      string   `json:"note,omitempty"`
	Suspended bool     `json:"suspended"`

	Bandwidth kiwivm.Bandwidth `json:"bandwidth"`
	Abuse     AbuseSummary     `json:"abuse"`
	// Alerts are the things wrong with this box right now, in plain
	// words. Empty means nothing needs attention.
	Alerts []string `json:"alerts,omitempty"`
}

// AbuseSummary is the yearly abuse-point standing for a VPS.
type AbuseSummary struct {
	Points          int     `json:"points"`
	Max             int     `json:"max"`
	Percent         float64 `json:"percent"`
	Suspensions     int     `json:"suspensions"`
	PolicyViolation bool    `json:"policyViolation"`
}

// FleetReport is the full `bwg ls --json` payload.
type FleetReport struct {
	Servers []FleetEntry      `json:"servers"`
	Failed  []FleetFailure    `json:"failed,omitempty"`
	Totals  FleetTotals       `json:"totals"`
	Hints   map[string]string `json:"hints,omitempty"`
}

// FleetFailure records a box that did not answer. It is reported
// alongside the data rather than instead of it: an unreachable server
// must not hide the state of the ones that did answer.
type FleetFailure struct {
	Server string `json:"server"`
	VEID   string `json:"veid"`
	Error  string `json:"error"`
}

// FleetTotals aggregates the fleet.
type FleetTotals struct {
	Servers        int     `json:"servers"`
	Reachable      int     `json:"reachable"`
	BandwidthUsed  int64   `json:"bandwidthUsed"`
	BandwidthTotal int64   `json:"bandwidthTotal"`
	Percent        float64 `json:"percent"`
	NeedsAttention int     `json:"needsAttention"`
}

func newLsCmd(app *App) *cobra.Command {
	var (
		tags     []string
		sortBy   string
		live     bool
		alerting bool
	)

	cmd := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list", "fleet"},
		Short:   "Show every server: bandwidth, quota headroom, and what needs attention",
		Long: `Sweep the fleet concurrently and print one row per VPS.

This is the answer to the question a BandwagonHost account actually
raises day to day: which box is close to its monthly transfer cap, and
is anything suspended or accruing abuse points.

Bandwidth already has the location multiplier applied. A server that
does not answer is listed under "failed" rather than dropping out, so
a missing row always means a missing server, never a hidden error.

JSON shape:
  {"servers":[{"server","veid","hostname","plan","location","bandwidth":
   {"used","total","free","percent","multiplier","resetsAt"},
   "abuse":{"points","max","percent"},"alerts":[...]}],
   "failed":[{"server","error"}],
   "totals":{"servers","reachable","bandwidthUsed","percent","needsAttention"}}`,
		Example: `  bwg ls                        # the whole fleet
  bwg ls --tag prod             # only servers tagged prod
  bwg ls --alerting             # only servers that need attention
  bwg ls --sort bandwidth       # worst quota headroom first
  bwg ls --json --jq '.servers[] | select(.bandwidth.percent > 80) | .server'`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			servers, err := app.Servers(tags)
			if err != nil {
				return err
			}
			ctx, cancel := app.Context(cmd.Context())
			defer cancel()

			results := fleet.Map(ctx, servers, app.Concurrency,
				func(ctx context.Context, s *config.Server) (*FleetEntry, error) {
					return app.fleetEntry(ctx, s, live)
				})

			report := buildReport(results, sortBy, alerting)
			return app.Emit(report, func(w io.Writer) { renderFleet(w, report, alerting) })
		},
	}

	f := cmd.Flags()
	f.StringSliceVar(&tags, "tag", nil, "Only servers carrying every given tag")
	f.StringVar(&sortBy, "sort", "bandwidth",
		"Sort by: bandwidth, name, abuse, plan, location")
	f.BoolVar(&live, "live", false,
		"Also fetch live guest state (power, load). Adds up to 15s per server.")
	f.BoolVar(&alerting, "alerting", false, "Only servers that need attention")
	return cmd
}

// fleetEntry gathers one server's row. It uses getServiceInfo, which
// carries the bandwidth and abuse counters, and only reaches for the
// slow live call when asked.
func (a *App) fleetEntry(ctx context.Context, s *config.Server, live bool) (*FleetEntry, error) {
	c := a.ClientFor(s)

	var info *kiwivm.ServiceInfo
	var state string
	if live {
		l, err := c.LiveServiceInfo(ctx)
		if err != nil {
			return nil, err
		}
		info, state = &l.ServiceInfo, l.State()
	} else {
		var err error
		if info, err = c.ServiceInfo(ctx); err != nil {
			return nil, err
		}
	}

	e := &FleetEntry{
		Server: s.Name, VEID: s.VEID, Hostname: info.Hostname,
		Plan: info.Plan, Location: info.NodeLocation, OS: info.OS,
		VMType: info.VMType, IPv4: info.IPv4(), IPv6: info.IPv6(),
		Tags: s.Tags, Note: s.Note, Suspended: info.Suspended.Bool(),
		Bandwidth: info.Bandwidth(),
		Abuse: AbuseSummary{
			Points: info.TotalAbusePoints.Int(), Max: info.MaxAbusePoints.Int(),
			Percent: info.AbusePercent(), Suspensions: info.SuspensionCount.Int(),
			PolicyViolation: info.PolicyViolation.Bool(),
		},
	}
	e.Alerts = alertsFor(info, state)
	return e, nil
}

// alertsFor states, in words a person can act on, what is wrong with a
// box. Anything that would make someone open the KiwiVM panel belongs
// here; nothing else does.
func alertsFor(info *kiwivm.ServiceInfo, state string) []string {
	var out []string
	if info.Suspended.Bool() {
		out = append(out, "suspended")
	}
	if info.PolicyViolation.Bool() {
		out = append(out, "policy violation unresolved")
	}
	for _, ip := range sortedKeys(info.IPNullroutes) {
		out = append(out, "nullrouted: "+ip)
	}
	if b := info.Bandwidth(); b.Total > 0 {
		switch {
		case b.Percent >= 100:
			out = append(out, "bandwidth exhausted")
		case b.Percent >= 90:
			out = append(out, fmt.Sprintf("bandwidth at %s", output.Percent(b.Percent)))
		}
	}
	if p := info.AbusePercent(); p >= 75 {
		out = append(out, fmt.Sprintf("abuse points at %s of the yearly limit", output.Percent(p)))
	}
	if state == "stopped" {
		out = append(out, "stopped")
	}
	return out
}

func sortedKeys(n kiwivm.Nullroutes) []string {
	out := make([]string, 0, len(n))
	for ip := range n {
		out = append(out, ip)
	}
	sort.Strings(out)
	return out
}

func buildReport(results []fleet.Result[*FleetEntry], sortBy string, alerting bool) FleetReport {
	rep := FleetReport{Hints: map[string]string{
		"detail": "bwg status --server <name>",
		"usage":  "bwg usage --server <name>",
	}}
	rep.Totals.Servers = len(results)

	ok, failed := fleet.Split(results)
	for _, r := range failed {
		rep.Failed = append(rep.Failed, FleetFailure{
			Server: r.Server.Name, VEID: r.Server.VEID, Error: r.Error,
		})
	}
	for _, r := range ok {
		if r.Value == nil {
			continue
		}
		rep.Totals.Reachable++
		rep.Totals.BandwidthUsed += r.Value.Bandwidth.Used
		rep.Totals.BandwidthTotal += r.Value.Bandwidth.Total
		if len(r.Value.Alerts) > 0 {
			rep.Totals.NeedsAttention++
		}
		if alerting && len(r.Value.Alerts) == 0 {
			continue
		}
		rep.Servers = append(rep.Servers, *r.Value)
	}
	if rep.Totals.BandwidthTotal > 0 {
		rep.Totals.Percent = float64(rep.Totals.BandwidthUsed) /
			float64(rep.Totals.BandwidthTotal) * 100
	}
	sortEntries(rep.Servers, sortBy)
	return rep
}

func sortEntries(entries []FleetEntry, by string) {
	switch strings.ToLower(by) {
	case "name", "server":
		sort.SliceStable(entries, func(i, j int) bool { return entries[i].Server < entries[j].Server })
	case "abuse":
		sort.SliceStable(entries, func(i, j int) bool {
			return entries[i].Abuse.Percent > entries[j].Abuse.Percent
		})
	case "plan":
		sort.SliceStable(entries, func(i, j int) bool { return entries[i].Plan < entries[j].Plan })
	case "location":
		sort.SliceStable(entries, func(i, j int) bool { return entries[i].Location < entries[j].Location })
	default:
		// Worst headroom first: the row you need to see is the top one.
		sort.SliceStable(entries, func(i, j int) bool {
			return entries[i].Bandwidth.Percent > entries[j].Bandwidth.Percent
		})
	}
}

func renderFleet(w io.Writer, rep FleetReport, alerting bool) {
	if len(rep.Servers) == 0 && len(rep.Failed) == 0 {
		// Under --alerting an empty result is the good outcome, not a
		// filter that found nothing. Say which it is.
		if alerting {
			fmt.Fprintf(w, "%s Nothing needs attention across %d server(s).\n",
				output.Good("✓"), rep.Totals.Reachable)
			return
		}
		fmt.Fprintln(w, "No servers matched.")
		return
	}

	t := output.NewTable("SERVER", "HOSTNAME", "PLAN", "LOCATION", "BANDWIDTH", "USED", "RESETS", "STATE").
		RightAlign(5)
	for _, e := range rep.Servers {
		t.Row(
			e.Server,
			output.Truncate(e.Hostname, 28),
			e.Plan,
			output.Truncate(e.Location, 18),
			output.Bar(e.Bandwidth.Percent, 10),
			output.Usage(e.Bandwidth.Percent),
			resetsIn(e.Bandwidth),
			stateCell(e),
		)
	}
	t.Render(w)

	if len(rep.Servers) > 1 {
		fmt.Fprintf(w, "\n%s %s of %s across %d servers (%s)\n",
			output.Dim("Total:"),
			output.Bytes(rep.Totals.BandwidthUsed),
			output.Bytes(rep.Totals.BandwidthTotal),
			rep.Totals.Reachable,
			output.Usage(rep.Totals.Percent))
	}

	// Alerts are printed in full underneath rather than squeezed into a
	// column, because the whole point of an alert is that it is legible.
	var flagged []FleetEntry
	for _, e := range rep.Servers {
		if len(e.Alerts) > 0 {
			flagged = append(flagged, e)
		}
	}
	if len(flagged) > 0 {
		fmt.Fprintln(w)
		for _, e := range flagged {
			fmt.Fprintf(w, "%s %s: %s\n",
				output.Bad("!"), output.Strong(e.Server), strings.Join(e.Alerts, ", "))
		}
	}

	if len(rep.Failed) > 0 {
		fmt.Fprintln(w)
		for _, f := range rep.Failed {
			fmt.Fprintf(w, "%s %s did not answer: %s\n",
				output.Warn("?"), output.Strong(f.Server), f.Error)
		}
	}
}

func resetsIn(b kiwivm.Bandwidth) string {
	if b.ResetsAt.IsZero() {
		return ""
	}
	d := b.ResetsIn()
	if d == 0 {
		return output.Dim("due")
	}
	return output.Duration(d)
}

func stateCell(e FleetEntry) string {
	switch {
	case e.Suspended:
		return output.Bad("suspended")
	case e.Abuse.PolicyViolation:
		return output.Bad("violation")
	case len(e.Alerts) > 0:
		return output.Warn("attention")
	}
	return output.Good("ok")
}
