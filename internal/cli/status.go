package cli

import (
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/lroolle/bwg-cli/kiwivm"
	"github.com/lroolle/bwg-cli/pkg/output"
	"github.com/spf13/cobra"
)

func newStatusCmd(app *App) *cobra.Command {
	var screenshot string

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Live state of one server: power, load, memory, disk, SSH port",
		Long: `Query the hypervisor for what the VPS is doing right now.

KiwiVM documents this call as taking up to 15 seconds — it inspects the
running guest. For plan and quota facts alone, 'bwg info' is instant.

Reported fields depend on the hypervisor. KVM gives power state, load,
memory and a VGA screenshot; OpenVZ gives beancounters and quotas.
bwg normalizes what it can, and both are in the JSON.

JSON shape:
  {"server","state","running","sshPort","hostname",
   "resources":{"memUsed","memTotal","diskUsed","diskTotal","loadAverage"},
   "throttled":{"cpu","disk"},"live":{...raw payload...}}`,
		Example: `  bwg status
  bwg status --server tokyo --json --jq '.state'
  bwg status --screenshot console.png   # KVM only`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, s, err := app.Client()
			if err != nil {
				return err
			}
			ctx, cancel := app.Context(cmd.Context())
			defer cancel()

			live, err := c.LiveServiceInfo(ctx)
			if err != nil {
				return Explain(err, s.Name)
			}

			if screenshot != "" {
				if err := saveScreenshot(live, screenshot); err != nil {
					return err
				}
				app.Notef("Console screenshot written to %s", screenshot)
			}

			payload := buildStatus(s.Name, live)
			return app.Emit(payload, func(w io.Writer) { renderStatus(w, s.Name, live) })
		},
	}
	cmd.Flags().StringVar(&screenshot, "screenshot", "",
		"Write the VGA console screenshot to this PNG file (KVM only)")
	return cmd
}

func buildStatus(server string, l *kiwivm.LiveServiceInfo) map[string]any {
	res := map[string]any{}
	if used, ok := l.MemUsedBytes(); ok {
		res["memUsed"] = used
	}
	if l.PlanRAM > 0 {
		res["memTotal"] = l.PlanRAM.Int64()
	}
	if used, ok := l.DiskUsedBytes(); ok {
		res["diskUsed"] = used
	}
	if total, ok := l.DiskTotalBytes(); ok {
		res["diskTotal"] = total
	}
	if l.LoadAverage != "" {
		res["loadAverage"] = l.LoadAverage
	}

	// The screenshot is hundreds of kilobytes of base64. It is available
	// through --screenshot; putting it in every --json payload would
	// make the output unusable in a terminal or a log.
	redacted := *l
	hasShot := redacted.ScreendumpPNGBase64 != ""
	redacted.ScreendumpPNGBase64 = ""

	return map[string]any{
		"server":    server,
		"state":     l.State(),
		"running":   l.Running(),
		"sshPort":   l.SSHPort.Int(),
		"hostname":  firstNonEmpty(l.LiveHostname, l.Hostname),
		"resources": res,
		"throttled": map[string]bool{
			"cpu":  l.IsCPUThrottled.Bool(),
			"disk": l.IsDiskThrottled.Bool(),
		},
		"screenshotAvailable": hasShot,
		"live":                redacted,
		"hints": map[string]string{
			"facts": "bwg info",
			"shell": "bwg ssh",
		},
	}
}

func renderStatus(w io.Writer, server string, l *kiwivm.LiveServiceInfo) {
	fmt.Fprintf(w, "%s %s  %s\n\n",
		output.Strong(firstNonEmpty(l.LiveHostname, l.Hostname)),
		output.Dim("("+server+")"),
		stateBadge(l.State()))

	rows := [][2]string{}
	if l.SSHPort > 0 {
		addr := l.PrimaryIP()
		if addr != "" {
			rows = append(rows, [2]string{"SSH", fmt.Sprintf("%s port %d", addr, l.SSHPort.Int())})
		} else {
			rows = append(rows, [2]string{"SSH port", fmt.Sprint(l.SSHPort.Int())})
		}
	}
	if l.LoadAverage != "" {
		rows = append(rows, [2]string{"Load", l.LoadAverage})
	}
	if used, ok := l.MemUsedBytes(); ok && l.PlanRAM > 0 {
		rows = append(rows, [2]string{"Memory", gauge(used, l.PlanRAM.Int64())})
	}
	if used, ok := l.DiskUsedBytes(); ok {
		if total, ok2 := l.DiskTotalBytes(); ok2 {
			rows = append(rows, [2]string{"Disk", gauge(used, total)})
		}
	}
	if l.SwapTotalKB > 0 {
		used := (l.SwapTotalKB.Int64() - l.SwapAvailableKB.Int64()) * 1024
		rows = append(rows, [2]string{"Swap", gauge(used, l.SwapTotalKB.Int64()*1024)})
	}
	if l.VeMac1 != "" {
		rows = append(rows, [2]string{"MAC", l.VeMac1})
	}
	output.Tabbed(w, rows)

	// Throttling is invisible from inside the guest and explains most
	// "why is this box slow" questions, so it is called out rather than
	// left as a field to notice.
	if l.IsCPUThrottled.Bool() {
		fmt.Fprintf(w, "\n%s CPU is throttled for sustained high usage; it clears on its own within ~2 hours.\n",
			output.Warn("!"))
	}
	if l.IsDiskThrottled.Bool() {
		fmt.Fprintf(w, "%s Disk I/O is throttled for sustained high usage; it clears within 15-180 minutes.\n",
			output.Warn("!"))
	}

	b := l.Bandwidth()
	fmt.Fprintf(w, "\n%s %s %s of %s, resets in %s\n",
		output.Dim("Bandwidth"), output.Bar(b.Percent, 12),
		output.Usage(b.Percent), output.Bytes(b.Total), output.Duration(b.ResetsIn()))

	if l.ScreendumpPNGBase64 != "" {
		fmt.Fprintf(w, "\n%s\n", output.Dim("A console screenshot is available: bwg status --screenshot console.png"))
	}
	renderHealth(w, &l.ServiceInfo)
}

func stateBadge(state string) string {
	switch state {
	case "running":
		return output.Good("● running")
	case "stopped":
		return output.Bad("● stopped")
	case "starting":
		return output.Warn("● starting")
	}
	return output.Dim("● " + state)
}

func gauge(used, total int64) string {
	if total <= 0 {
		return output.Bytes(used)
	}
	pct := float64(used) / float64(total) * 100
	return fmt.Sprintf("%s %s of %s (%s)",
		output.Bar(pct, 12), output.Bytes(used), output.Bytes(total), output.Usage(pct))
}

func saveScreenshot(l *kiwivm.LiveServiceInfo, path string) error {
	if l.ScreendumpPNGBase64 == "" {
		return fmt.Errorf("this VPS returned no console screenshot\n\n" +
			"  Screenshots come from the KVM hypervisor's VGA console.\n" +
			"  OpenVZ containers have no console to capture.")
	}
	data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(l.ScreendumpPNGBase64))
	if err != nil {
		return fmt.Errorf("decoding the screenshot: %w", err)
	}
	return os.WriteFile(path, data, 0o644)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
