package cli

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// fleetBox describes one VPS in the rendering fixture.
type fleetBox struct {
	name, host, plan, loc string
	veid                  string
	capGB, usedGB, mult   int64
	suspended             bool
	abuse                 int
}

const gib = 1024 * 1024 * 1024

// TestFleetTableRendering pins how `bwg ls` actually looks with a
// realistic mixed fleet. The README shows this output, so a change in
// layout or in the alert thresholds shows up as a diff here rather
// than quietly making the documentation wrong.
func TestFleetTableRendering(t *testing.T) {
	boxes := []fleetBox{
		{"tokyo", "tokyo.example.com", "micro128", "JP, Tokyo", "1001", 1000, 470, 1, false, 0},
		{"osaka", "osaka.example.com", "speed-2g", "JP, Osaka", "1002", 500, 472, 2, false, 300},
		{"la", "la.example.com", "kvm-2g", "US, Los Angeles", "1003", 2000, 380, 1, false, 0},
	}
	byVEID := map[string]fleetBox{}
	for _, b := range boxes {
		byVEID[b.veid] = b
	}

	// Six days out, so the RESETS column reads like a real billing
	// cycle rather than a number from the next century.
	reset := time.Now().Add(6*24*time.Hour + 4*time.Hour).Unix()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		b, ok := byVEID[r.Form.Get("veid")]
		if !ok {
			http.Error(w, "unknown veid", 404)
			return
		}
		fmt.Fprintf(w, `{"error":0,"vm_type":"kvm","hostname":%q,"plan":%q,
		  "os":"debian-12-x86_64","node_location":%q,"plan_ram":2147483648,
		  "plan_disk":21474836480,"plan_monthly_data":%d,"data_counter":%d,
		  "monthly_data_multiplier":%d,"data_next_reset":%d,
		  "ip_addresses":["203.0.113.1"],"private_ip_addresses":[],
		  "ip_nullroutes":[],"ptr":{},"available_isos":[],"plan_max_ipv6s":1,
		  "rdns_api_available":1,"location_ipv6_ready":1,"suspended":%d,
		  "policy_violation":0,"suspension_count":0,"total_abuse_points":%d,
		  "max_abuse_points":1500}`,
			b.host, b.plan, b.loc, b.capGB*gib, b.usedGB*gib, b.mult, reset,
			boolInt(b.suspended), b.abuse)
	}))
	defer srv.Close()

	h := newHarness(t, nil)
	var cfg strings.Builder
	cfg.WriteString("default: tokyo\nservers:\n")
	for _, b := range boxes {
		fmt.Fprintf(&cfg, "  %s: {veid: %q, api_key: private_%sxxxxxxxxxxxxxxxx, endpoint: %s, tags: [prod]}\n",
			b.name, b.veid, b.name, srv.URL)
	}
	if err := os.WriteFile(h.config, []byte(cfg.String()), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := h.run("ls"); err != nil {
		t.Fatal(err)
	}
	table := h.stdout.String()
	t.Logf("bwg ls\n%s", table)

	// Worst headroom first: the row you need is the top one.
	lines := strings.Split(strings.TrimSpace(table), "\n")
	if !strings.HasPrefix(lines[1], "osaka") {
		t.Errorf("the fullest box is not first:\n%s", table)
	}
	// The multiplier scales both sides, so osaka is 472/500 = 94%.
	if !strings.Contains(table, "94%") {
		t.Errorf("osaka should read 94%%:\n%s", table)
	}
	if !strings.Contains(table, "attention") {
		t.Errorf("a box over 90%% should be flagged for attention:\n%s", table)
	}
	if !strings.Contains(table, "bandwidth at 94%") {
		t.Errorf("the alert line is missing:\n%s", table)
	}
	if !strings.Contains(table, "Total:") {
		t.Errorf("the fleet total is missing:\n%s", table)
	}

	// --alerting narrows to the box that needs it.
	if err := h.run("ls", "--alerting"); err != nil {
		t.Fatal(err)
	}
	alerting := h.stdout.String()
	t.Logf("bwg ls --alerting\n%s", alerting)
	if !strings.Contains(alerting, "osaka") {
		t.Errorf("--alerting dropped the alerting box:\n%s", alerting)
	}
	if strings.Contains(alerting, "la.example.com") {
		t.Errorf("--alerting kept a healthy box:\n%s", alerting)
	}
}

// A clean fleet should say so, not report an empty filter result.
func TestAlertingOnAHealthyFleetSaysSo(t *testing.T) {
	h := newHarness(t, apiBodies)
	if err := h.run("ls", "--alerting"); err != nil {
		t.Fatal(err)
	}
	out := h.stdout.String()
	if strings.Contains(out, "No servers matched") {
		t.Errorf("a healthy fleet reads like a failed filter: %q", out)
	}
	if !strings.Contains(strings.ToLower(out), "attention") {
		t.Errorf("the all-clear does not say what was checked: %q", out)
	}
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// KiwiVM returns one PTR entry per address whether or not a record is
// set, so `bwg info` used to print an "rDNS" heading with nothing
// under it — which reads as data that failed to load.
func TestInfoOmitsTheRDNSHeadingWhenNothingIsSet(t *testing.T) {
	unset := strings.Replace(serviceInfoBody,
		`"ptr":{"203.0.113.10":"tokyo.example.com"}`,
		`"ptr":{"203.0.113.10":"","2001:db8::":""}`, 1)
	if unset == serviceInfoBody {
		t.Fatal("the fixture no longer contains the ptr field")
	}

	h := newHarness(t, map[string]string{"getServiceInfo": unset})
	if err := h.run("info"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(h.stdout.String(), "rDNS") {
		t.Errorf("an empty rDNS section was printed:\n%s", h.stdout)
	}

	// With a record set, the section is real and must appear.
	h = newHarness(t, map[string]string{"getServiceInfo": serviceInfoBody})
	if err := h.run("info"); err != nil {
		t.Fatal(err)
	}
	out := h.stdout.String()
	if !strings.Contains(out, "rDNS") || !strings.Contains(out, "tokyo.example.com") {
		t.Errorf("a set PTR record did not render:\n%s", out)
	}
}

// The health block is the one part of `bwg info` people act on, so it
// appears exactly when something is wrong and stays quiet otherwise.
func TestInfoHealthSectionAppearsOnlyWhenItHasSomethingToSay(t *testing.T) {
	h := newHarness(t, map[string]string{"getServiceInfo": serviceInfoBody})
	if err := h.run("info"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(h.stdout.String(), "Health") {
		t.Errorf("a healthy box printed a health section:\n%s", h.stdout)
	}

	suspended := strings.Replace(serviceInfoBody, `"suspended":0`, `"suspended":1`, 1)
	h = newHarness(t, map[string]string{"getServiceInfo": suspended})
	if err := h.run("info"); err != nil {
		t.Fatal(err)
	}
	out := h.stdout.String()
	if !strings.Contains(out, "Health") || !strings.Contains(out, "Suspended") {
		t.Errorf("a suspended box hid the reason:\n%s", out)
	}
	if !strings.Contains(out, "bwg abuse") {
		t.Errorf("the health section does not name the command that explains it:\n%s", out)
	}
}

// The dead end that prompted this: a box suspended at 100% bandwidth
// with no abuse case was told to run `bwg abuse`, which answered
// "nothing outstanding".
func TestSuspensionNamesTheLikelyCause(t *testing.T) {
	exhausted := strings.NewReplacer(
		`"suspended":0`, `"suspended":1`,
		`"data_counter":536870912000`, `"data_counter":1073741824000`,
		`"total_abuse_points":100`, `"total_abuse_points":0`,
	).Replace(serviceInfoBody)

	h := newHarness(t, map[string]string{"getServiceInfo": exhausted})
	if err := h.run("info"); err != nil {
		t.Fatal(err)
	}
	out := h.stdout.String()
	if !strings.Contains(out, "transfer quota exhausted") {
		t.Errorf("a bandwidth suspension does not name the quota:\n%s", out)
	}
	if !strings.Contains(out, "resets in") {
		t.Errorf("the note does not say when it lifts:\n%s", out)
	}
	if strings.Contains(out, "see: bwg abuse") {
		t.Errorf("still sending people to an empty abuse page:\n%s", out)
	}

	// With abuse points on the record, the abuse page is the right
	// place and the guess would be wrong.
	withAbuse := strings.Replace(serviceInfoBody, `"suspended":0`, `"suspended":1`, 1)
	h = newHarness(t, map[string]string{"getServiceInfo": withAbuse})
	if err := h.run("info"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(h.stdout.String(), "see: bwg abuse") {
		t.Errorf("an abuse suspension does not point at the abuse log:\n%s", h.stdout)
	}
}
