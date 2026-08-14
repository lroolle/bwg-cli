package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/lroolle/bwg-cli/kiwivm"
	"github.com/lroolle/bwg-cli/pkg/output"
	"github.com/spf13/cobra"
)

func newNetCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "net",
		Short: "Addresses, rDNS, IPv6 subnets and private networking",
	}
	cmd.AddCommand(netLs(app), netPTR(app), netIPv6(app), netPrivate(app))
	return cmd
}

func netLs(app *App) *cobra.Command {
	return &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "Show every address on this server, with rDNS and nullroute state",
		Long: `List the VPS's addresses.

JSON shape:
  {"server","ipv4":[],"ipv6":[],"private":[],"ptr":{ip:name},
   "nullrouted":{ip:{...}},"capabilities":{"rdnsApi","maxIpv6",
   "privateNetwork","ipv6Ready"}}`,
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
				"ipv4":   info.IPv4(), "ipv6": info.IPv6(),
				"private": info.PrivateIPAddresses, "ptr": info.PTR,
				"nullrouted": info.IPNullroutes,
				"capabilities": map[string]any{
					"rdnsApi":        info.RDNSAPIAvailable.Bool(),
					"maxIpv6":        info.PlanMaxIPv6s.Int(),
					"privateNetwork": info.PlanPrivateNetworkAvailable.Bool() && info.LocationPrivateNetworkAvailable.Bool(),
					"ipv6Ready":      info.LocationIPv6Ready.Bool(),
				},
			}
			return app.Emit(payload, func(w io.Writer) {
				t := output.NewTable("ADDRESS", "KIND", "RDNS", "STATE")
				for _, ip := range info.IPv4() {
					t.Row(ip, "ipv4", info.PTR[ip], nullrouteCell(info, ip))
				}
				for _, ip := range info.IPv6() {
					t.Row(ip, "ipv6", info.PTR[ip], nullrouteCell(info, ip))
				}
				for _, ip := range info.PrivateIPAddresses {
					t.Row(ip, "private", "", "")
				}
				t.Render(w)

				fmt.Fprintf(w, "\n%s\n", output.Dim("Capabilities"))
				output.Tabbed(w, [][2]string{
					{"rDNS via API", yesNo(info.RDNSAPIAvailable.Bool())},
					{"IPv6 subnets", fmt.Sprintf("%d of %d used",
						len(info.IPv6()), info.PlanMaxIPv6s.Int())},
					{"Private network", privateNetNote(info)},
				})
			})
		},
	}
}

func nullrouteCell(info *kiwivm.ServiceInfo, ip string) string {
	nr, ok := info.IPNullroutes[ip]
	if !ok {
		return output.Good("ok")
	}
	if exp, ok := nr.ExpiresAt(); ok {
		return output.Bad("nullrouted, lifts " + output.Ago(exp))
	}
	return output.Bad("nullrouted")
}

func yesNo(b bool) string {
	if b {
		return output.Good("yes")
	}
	return output.Dim("no")
}

func privateNetNote(info *kiwivm.ServiceInfo) string {
	switch {
	case !info.PlanPrivateNetworkAvailable.Bool():
		return output.Dim("not available on this plan")
	case !info.LocationPrivateNetworkAvailable.Bool():
		return output.Dim("not available at this location")
	}
	return output.Good("available")
}

func netPTR(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "ptr <ip> <hostname>",
		Short: kiwivm.Ops["setPTR"].Summary,
		Long: `Set the reverse DNS record for one of the VPS's IPs.

Mail delivery generally requires the PTR to match the forward record,
so set the A/AAAA record first.

Pass an empty hostname ("") to clear the record.`,
		Example: `  bwg net ptr 1.2.3.4 mail.example.com
  bwg net ptr 1.2.3.4 ""              # clear it`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ip, ptr := args[0], args[1]

			c, s, err := app.ClientForOp(kiwivm.Ops["setPTR"])
			if err != nil {
				return err
			}
			ctx, cancel := app.Context(cmd.Context())
			defer cancel()

			info, err := c.ServiceInfo(ctx)
			if err != nil {
				return Explain(err, s.Name)
			}
			if !info.RDNSAPIAvailable.Bool() {
				return fmt.Errorf(
					"this plan cannot set rDNS through the API\n\n" +
						"  Use the KiwiVM panel, or contact support.")
			}
			if !ownsIP(info, ip) {
				return fmt.Errorf("%s is not assigned to %s\n\n  Its addresses: %s\n  List them: bwg net ls",
					ip, s.Name, strings.Join(append(info.IPv4(), info.IPv6()...), ", "))
			}

			facts := [][2]string{{"Current rDNS", orNone(info.PTR[ip])}, {"New rDNS", orNone(ptr)}}
			if err := app.Confirm(Consent{
				Op: kiwivm.Ops["setPTR"], Server: s, Target: ip, Facts: facts,
			}); err != nil {
				return err
			}
			if err := c.SetPTR(ctx, ip, ptr); err != nil {
				return Explain(err, s.Name)
			}
			return app.Emit(
				map[string]any{"server": s.Name, "ip": ip, "ptr": ptr,
					"hints": map[string]string{"verify": "bwg net ls"}},
				func(w io.Writer) {
					if ptr == "" {
						fmt.Fprintf(w, "%s Cleared rDNS for %s\n", output.Good("✓"), ip)
						return
					}
					fmt.Fprintf(w, "%s %s -> %s\n", output.Good("✓"), ip, ptr)
				})
		},
	}
}

func ownsIP(info *kiwivm.ServiceInfo, ip string) bool {
	for _, a := range append(info.IPv4(), info.IPv6()...) {
		if a == ip || strings.HasPrefix(a, ip+"/") {
			return true
		}
	}
	return false
}

func orNone(s string) string {
	if s == "" {
		return output.Dim("(none)")
	}
	return s
}

func netIPv6(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ipv6",
		Short: "Allocate and release IPv6 /64 subnets",
	}

	add := &cobra.Command{
		Use:   "add",
		Short: kiwivm.Ops["ipv6/add"].Summary,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, s, err := app.ClientForOp(kiwivm.Ops["ipv6/add"])
			if err != nil {
				return err
			}
			ctx, cancel := app.Context(cmd.Context())
			defer cancel()

			info, err := c.ServiceInfo(ctx)
			if err != nil {
				return Explain(err, s.Name)
			}
			if !info.LocationIPv6Ready.Bool() {
				return fmt.Errorf("this location does not offer IPv6\n\n" +
					"  Check other locations: bwg migrate ls")
			}
			have, max := len(info.IPv6()), info.PlanMaxIPv6s.Int()
			if max > 0 && have >= max {
				return fmt.Errorf("this plan allows %d IPv6 /64 subnets and %d are assigned\n\n"+
					"  Release one first: bwg net ipv6 rm <subnet>", max, have)
			}

			if err := app.Confirm(Consent{
				Op: kiwivm.Ops["ipv6/add"], Server: s,
				Facts: [][2]string{{"Currently", fmt.Sprintf("%d of %s", have, output.Count(max, "subnet"))}},
			}); err != nil {
				return err
			}
			res, err := c.AddIPv6(ctx)
			if err != nil {
				return Explain(err, s.Name)
			}
			return app.Emit(
				map[string]any{"server": s.Name, "assignedSubnet": res.AssignedSubnet},
				func(w io.Writer) {
					fmt.Fprintf(w, "%s Assigned %s\n", output.Good("✓"), output.Strong(res.AssignedSubnet))
				})
		},
	}

	rm := &cobra.Command{
		Use:     "rm <subnet>",
		Aliases: []string{"delete"},
		Short:   kiwivm.Ops["ipv6/delete"].Summary,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, s, err := app.ClientForOp(kiwivm.Ops["ipv6/delete"])
			if err != nil {
				return err
			}
			ctx, cancel := app.Context(cmd.Context())
			defer cancel()

			if err := app.Confirm(Consent{
				Op: kiwivm.Ops["ipv6/delete"], Server: s, Target: args[0],
			}); err != nil {
				return err
			}
			if err := c.DeleteIPv6(ctx, args[0]); err != nil {
				return Explain(err, s.Name)
			}
			return app.Emit(
				map[string]any{"server": s.Name, "subnet": args[0], "status": "released"},
				func(w io.Writer) {
					fmt.Fprintf(w, "%s Released %s\n", output.Good("✓"), args[0])
				})
		},
	}

	cmd.AddCommand(add, rm)
	return cmd
}

func netPrivate(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "private",
		Short: "Assign and remove private IPv4 addresses",
	}

	ls := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "Show assigned and assignable private IPv4 addresses",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, s, err := app.Client()
			if err != nil {
				return err
			}
			ctx, cancel := app.Context(cmd.Context())
			defer cancel()

			avail, err := c.AvailablePrivateIPs(ctx)
			if err != nil {
				return Explain(err, s.Name)
			}
			info, err := c.ServiceInfo(ctx)
			if err != nil {
				return Explain(err, s.Name)
			}
			return app.Emit(
				map[string]any{"server": s.Name,
					"assigned": info.PrivateIPAddresses, "available": avail.AvailableIPs,
					"hints": map[string]string{"assign": "bwg net private add [ip]"}},
				func(w io.Writer) {
					output.Tabbed(w, [][2]string{
						{"Assigned", strings.Join(info.PrivateIPAddresses, ", ")},
						{"Available", strings.Join(avail.AvailableIPs, ", ")},
					})
				})
		},
	}

	add := &cobra.Command{
		Use:   "add [ip]",
		Short: kiwivm.Ops["privateIp/assign"].Summary,
		Long:  "Assign a private IPv4 address. With no argument, KiwiVM picks one.",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var ip string
			if len(args) == 1 {
				ip = args[0]
			}
			c, s, err := app.ClientForOp(kiwivm.Ops["privateIp/assign"])
			if err != nil {
				return err
			}
			ctx, cancel := app.Context(cmd.Context())
			defer cancel()

			target := ip
			if target == "" {
				target = "an address chosen by KiwiVM"
			}
			if err := app.Confirm(Consent{
				Op: kiwivm.Ops["privateIp/assign"], Server: s, Target: target,
			}); err != nil {
				return err
			}
			res, err := c.AssignPrivateIP(ctx, ip)
			if err != nil {
				return Explain(err, s.Name)
			}
			return app.Emit(
				map[string]any{"server": s.Name, "assigned": res.AssignedIPs},
				func(w io.Writer) {
					fmt.Fprintf(w, "%s Assigned %s\n", output.Good("✓"),
						strings.Join(res.AssignedIPs, ", "))
				})
		},
	}

	rm := &cobra.Command{
		Use:     "rm <ip>",
		Aliases: []string{"delete"},
		Short:   kiwivm.Ops["privateIp/delete"].Summary,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, s, err := app.ClientForOp(kiwivm.Ops["privateIp/delete"])
			if err != nil {
				return err
			}
			ctx, cancel := app.Context(cmd.Context())
			defer cancel()

			if err := app.Confirm(Consent{
				Op: kiwivm.Ops["privateIp/delete"], Server: s, Target: args[0],
			}); err != nil {
				return err
			}
			if err := c.DeletePrivateIP(ctx, args[0]); err != nil {
				return Explain(err, s.Name)
			}
			return app.Emit(
				map[string]any{"server": s.Name, "ip": args[0], "status": "removed"},
				func(w io.Writer) {
					fmt.Fprintf(w, "%s Removed %s\n", output.Good("✓"), args[0])
				})
		},
	}

	cmd.AddCommand(ls, add, rm)
	return cmd
}

func newHostCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "host <hostname>",
		Short: kiwivm.Ops["setHostname"].Summary,
		Long: `Set the hostname KiwiVM records for this VPS.

This is the panel's label and what appears in KiwiVM notifications. It
does not change the hostname inside a running guest — for that, use
hostnamectl over SSH.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, s, err := app.ClientForOp(kiwivm.Ops["setHostname"])
			if err != nil {
				return err
			}
			ctx, cancel := app.Context(cmd.Context())
			defer cancel()

			facts := [][2]string{}
			if info, err := c.ServiceInfo(ctx); err == nil {
				facts = append(facts, [2]string{"Current", info.Hostname})
			}
			facts = append(facts,
				[2]string{"New", args[0]},
				[2]string{"Scope", "the KiwiVM record only; the running guest is unchanged"})

			if err := app.Confirm(Consent{
				Op: kiwivm.Ops["setHostname"], Server: s, Target: args[0], Facts: facts,
			}); err != nil {
				return err
			}
			if err := c.SetHostname(ctx, args[0]); err != nil {
				return Explain(err, s.Name)
			}
			return app.Emit(
				map[string]any{"server": s.Name, "hostname": args[0]},
				func(w io.Writer) {
					fmt.Fprintf(w, "%s Hostname set to %s\n", output.Good("✓"), output.Strong(args[0]))
				})
		},
	}
}
