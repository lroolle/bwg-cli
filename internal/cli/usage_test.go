package cli

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/lroolle/bwg-cli/kiwivm"
)

// usageStats builds a series with samplesPerDay samples on each of the
// last n calendar days, each carrying 1 GiB in and 2 GiB out. Noon
// local time, so the calendar day a sample lands on does not depend on
// the zone the tests run in.
func usageStats(n, samplesPerDay int) *kiwivm.UsageStats {
	stats := &kiwivm.UsageStats{VMType: "kvm"}
	day := time.Now().AddDate(0, 0, -(n - 1))
	for i := 0; i < n; i++ {
		d := day.AddDate(0, 0, i)
		for j := 0; j < samplesPerDay; j++ {
			ts := time.Date(d.Year(), d.Month(), d.Day(), 12, j, 0, 0, time.Local).Unix()
			stats.Data = append(stats.Data, kiwivm.UsageSample{
				Timestamp:       kiwivm.Int(ts),
				CPUUsage:        kiwivm.Int(10),
				NetworkInBytes:  kiwivm.Int(gib),
				NetworkOutBytes: kiwivm.Int(2 * gib),
				DiskReadBytes:   kiwivm.Int(gib / 2),
				DiskWriteBytes:  kiwivm.Int(gib / 4),
			})
		}
	}
	return stats
}

// usageBody renders that series as the JSON KiwiVM would return.
func usageBody(n, samplesPerDay int) string {
	var rows []string
	for _, s := range usageStats(n, samplesPerDay).Data {
		rows = append(rows, fmt.Sprintf(
			`{"timestamp":%d,"cpu_usage":%d,"network_in_bytes":%d,"network_out_bytes":%d,`+
				`"disk_read_bytes":%d,"disk_write_bytes":%d}`,
			s.Timestamp.Int64(), s.CPUUsage.Int(), s.NetworkInBytes.Int64(),
			s.NetworkOutBytes.Int64(), s.DiskReadBytes.Int64(), s.DiskWriteBytes.Int64()))
	}
	return `{"error":0,"vm_type":"kvm","data":[` + strings.Join(rows, ",") + `]}`
}

// window trims by calendar day, not by sample count: KiwiVM's sampling
// interval is its own business and a "day" has to mean a day.
func TestUsageWindowTrimsByCalendarDay(t *testing.T) {
	stats := usageStats(10, 4)

	trimmed, available := window(stats, 3)
	if available != 10 {
		t.Errorf("available = %d, want 10", available)
	}
	if got := len(aggregateByDay(trimmed)); got != 3 {
		t.Errorf("kept %d days, want 3", got)
	}
	if got := len(trimmed.Data); got != 12 {
		t.Errorf("kept %d samples, want 4 per day for 3 days", got)
	}

	// The kept days must be the most recent ones.
	days := aggregateByDay(trimmed)
	want := time.Now().Format("2006-01-02")
	if days[len(days)-1].Date != want {
		t.Errorf("last day = %s, want today (%s)", days[len(days)-1].Date, want)
	}
}

func TestUsageWindowEdges(t *testing.T) {
	stats := usageStats(5, 1)

	for _, days := range []int{0, -1, 5, 99} {
		trimmed, available := window(stats, days)
		if available != 5 {
			t.Errorf("--days %d: available = %d, want 5", days, available)
		}
		if len(trimmed.Data) != len(stats.Data) {
			t.Errorf("--days %d trimmed a series it should have left alone", days)
		}
	}

	empty, available := window(&kiwivm.UsageStats{}, 7)
	if available != 0 || len(empty.Data) != 0 {
		t.Errorf("an empty series produced %d days / %d samples", available, len(empty.Data))
	}
}

// The window applies to the whole output. Before this, --days 7 printed
// seven rows above a total covering two years of traffic.
func TestUsageTotalsCoverTheWindowThatWasPrinted(t *testing.T) {
	h := newHarness(t, map[string]string{
		"getRawUsageStats": usageBody(40, 2),
		"getServiceInfo":   serviceInfoBody,
	})

	payload := h.runJSON(t, "usage", "--days", "5")
	win, ok := payload["window"].(map[string]any)
	if !ok {
		t.Fatalf("no window in the payload: %v", payload)
	}
	if win["days"] != 5.0 || win["available"] != 40.0 {
		t.Errorf("window = %v, want 5 of 40", win)
	}
	if got := len(payload["days"].([]any)); got != 5 {
		t.Errorf("days array holds %d entries, want 5", got)
	}
	// 5 days x 2 samples x 1 GiB in.
	totals := payload["totals"].(map[string]any)
	if want := 10.0 * gib; totals["networkIn"] != want {
		t.Errorf("networkIn = %v, want %v — the total does not match the window",
			totals["networkIn"], want)
	}
}

// A table that silently stops at 30 rows reads like a box with no
// history before last month.
func TestUsageSaysWhatItLeftOut(t *testing.T) {
	h := newHarness(t, map[string]string{
		"getRawUsageStats": usageBody(40, 1),
		"getServiceInfo":   serviceInfoBody,
	})

	if err := h.run("usage"); err != nil {
		t.Fatal(err)
	}
	out := h.stdout.String()
	if strings.Count(out, "\n") < defaultUsageDays {
		t.Errorf("the default window printed fewer than %d rows:\n%s", defaultUsageDays, out)
	}
	if !strings.Contains(out, "Showing 30 of the 40 days") {
		t.Errorf("the truncation is not disclosed:\n%s", out)
	}
	if !strings.Contains(out, "--days 0") {
		t.Errorf("the note does not name the flag that shows everything:\n%s", out)
	}
	if !strings.Contains(out, "over 30 days") {
		t.Errorf("the total does not state its span:\n%s", out)
	}

	// Nothing withheld, nothing to disclose.
	if err := h.run("usage", "--days", "0"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(h.stdout.String(), "Showing") {
		t.Errorf("--days 0 claimed to have withheld something:\n%s", h.stdout)
	}
}

// --raw shows individual samples, and has to respect the same window;
// otherwise "the last 7 days, raw" quietly means two years.
func TestUsageRawHonoursTheWindow(t *testing.T) {
	h := newHarness(t, map[string]string{
		"getRawUsageStats": usageBody(40, 3),
		"getServiceInfo":   serviceInfoBody,
	})

	payload := h.runJSON(t, "usage", "--raw", "--days", "2")
	samples, ok := payload["samples"].([]any)
	if !ok {
		t.Fatalf("no samples in --raw output: %v", payload)
	}
	if len(samples) != 6 {
		t.Errorf("--raw --days 2 returned %d samples, want 2 days x 3", len(samples))
	}

	if err := h.run("usage", "--raw", "--days", "2"); err != nil {
		t.Fatal(err)
	}
	out := h.stdout.String()
	// Header + 6 sample rows + blank + total + note.
	if strings.Count(out, "\n") > 12 {
		t.Errorf("--raw printed more than the window:\n%s", out)
	}
	if !strings.Contains(out, "Showing 2 of the 40 days") {
		t.Errorf("--raw does not disclose the window:\n%s", out)
	}
}

// KiwiVM keeps a couple of years. The default has to be readable at a
// glance and aligned with the quota line printed underneath it.
func TestUsageDefaultWindowIsTheBillingCycle(t *testing.T) {
	if defaultUsageDays != 30 {
		t.Errorf("default window = %d days; the quota below the table is monthly", defaultUsageDays)
	}
	h := newHarness(t, map[string]string{
		"getRawUsageStats": usageBody(40, 1),
		"getServiceInfo":   serviceInfoBody,
	})
	payload := h.runJSON(t, "usage")
	if got := len(payload["days"].([]any)); got != defaultUsageDays {
		t.Errorf("the default returned %d days, want %d", got, defaultUsageDays)
	}
}
