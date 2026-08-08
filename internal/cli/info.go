package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/lroolle/bwg-cli/kiwivm"
	"github.com/lroolle/bwg-cli/pkg/output"
	"github.com/spf13/cobra"
)

func newInfoCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "info",
		Short: "Plan, location, network and quota for one server",
		Long: `Show the standing facts about a VPS, without touching the guest.

This is the fast call. For power state, load and disk usage from inside
the VPS, use 'bwg status', which is slower because it queries the
hypervisor.

JSON shape: the raw getServiceInfo payload plus a "derived" block with
the bandwidth arithmetic already done:
  {"server","info":{...},"derived":{"bandwidth":{...},"ipv4":[],"ipv6":[],
   "abusePercent","healthy"}}`,
		Example: `  bwg info
  bwg info --server tokyo
  bwg info --json --jq '.derived.bandwidth.percent'`,
		Args: cobra.NoArgs,
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

			payload := map[string]any{
				"server": s.Name,
				"info":   info,
				"derived": map[string]any{
					"bandwidth":    info.Bandwidth(),
					"ipv4":         info.IPv4(),
					"ipv6":         info.IPv6(),
					"abusePercent": info.AbusePercent(),
					"healthy":      info.Healthy(),
				},
				"hints": map[string]string{
					"live":      "bwg status",
					"snapshots": "bwg snapshot ls",
				},
			}
			return app.Emit(payload, func(w io.Writer) {
				renderInfo(w, s.Name, info)
			})
		},
	}
}

func renderInfo(w io.Writer, server string, info *kiwivm.ServiceInfo) {
	b := info.Bandwidth()

	fmt.Fprintf(w, "%s %s\n\n", output.Strong(info.Hostname), output.Dim("("+server+")"))

	rows := [][2]string{
		{"Plan", fmt.Sprintf("%s  %s RAM, %s disk, %s swap",
			info.Plan, output.Bytes(info.PlanRAM.Int64()),
			output.Bytes(info.PlanDisk.Int64()), output.Bytes(info.PlanSwap.Int64()))},
		{"OS", info.OS},
		{"Type", info.VMType},
		{"Location", locationLine(info)},
		{"Node", info.NodeAlias},
		{"IPv4", strings.Join(info.IPv4(), ", ")},
		{"IPv6", strings.Join(info.IPv6(), ", ")},
		{"Private IP", strings.Join(info.PrivateIPAddresses, ", ")},
		{"Account", info.Email},
	}
	output.Tabbed(w, rows)

	fmt.Fprintf(w, "\n%s\n", output.Dim("Bandwidth"))
	output.Tabbed(w, [][2]string{
		{"Used", fmt.Sprintf("%s %s of %s",
			output.Bar(b.Percent, 20), output.Usage(b.Percent), output.Bytes(b.Total))},
		{"Free", output.Bytes(b.Free)},
		{"Resets", bandwidthReset(b)},
		{"Multiplier", multiplierNote(b)},
	})

	if ptr := info.PTR; len(ptr) > 0 {
		fmt.Fprintf(w, "\n%s\n", output.Dim("rDNS"))
		rows := make([][2]string, 0, len(ptr))
		for _, ip := range ptr.Keys() {
			rows = append(rows, [2]string{ip, ptr[ip]})
		}
		output.Tabbed(w, rows)
	}

	if iso := info.ISO1; iso != "" {
		fmt.Fprintf(w, "\n%s booting from ISO %s\n", output.Warn("!"), output.Strong(iso))
	}

	renderHealth(w, info)
}

func locationLine(info *kiwivm.ServiceInfo) string {
	parts := []string{info.NodeLocation}
	if info.NodeDatacenter != "" && info.NodeDatacenter != info.NodeLocation {
		parts = append(parts, info.NodeDatacenter)
	}
	line := strings.Join(parts, " · ")
	if !info.LocationIPv6Ready.Bool() {
		line += output.Dim("  (no IPv6 here)")
	}
	return line
}

func bandwidthReset(b kiwivm.Bandwidth) string {
	if b.ResetsAt.IsZero() {
		return ""
	}
	return fmt.Sprintf("%s  %s", output.Time(b.ResetsAt), output.Dim("in "+output.Duration(b.ResetsIn())))
}

// multiplierNote spells out what a multiplier above 1 means, because
// the raw number is the single most misread field in the API.
func multiplierNote(b kiwivm.Bandwidth) string {
	if b.Multiplier <= 1 {
		return ""
	}
	return fmt.Sprintf("%dx %s", b.Multiplier,
		output.Dim("(expensive-bandwidth location; both the allowance and the counter are scaled)"))
}

func renderHealth(w io.Writer, info *kiwivm.ServiceInfo) {
	if info.Healthy() && info.AbusePercent() < 75 {
		return
	}
	fmt.Fprintf(w, "\n%s\n", output.Dim("Health"))

	var rows [][2]string
	if info.Suspended.Bool() {
		rows = append(rows, [2]string{"Suspended", output.Bad("yes — see: bwg abuse")})
	}
	if info.PolicyViolation.Bool() {
		rows = append(rows, [2]string{"Policy violation", output.Bad("unresolved — see: bwg abuse")})
	}
	if info.MaxAbusePoints > 0 {
		rows = append(rows, [2]string{"Abuse points", fmt.Sprintf("%d of %d (%s this year)",
			info.TotalAbusePoints.Int(), info.MaxAbusePoints.Int(),
			output.Usage(info.AbusePercent()))})
	}
	for _, ip := range sortedKeys(info.IPNullroutes) {
		nr := info.IPNullroutes[ip]
		detail := "nullrouted"
		if exp, ok := nr.ExpiresAt(); ok {
			detail = fmt.Sprintf("nullrouted, lifts %s", output.Ago(exp))
		}
		rows = append(rows, [2]string{ip, output.Bad(detail)})
	}
	output.Tabbed(w, rows)
}
