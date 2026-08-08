package cli

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const statusFeed = `<?xml version="1.0" encoding="utf-8"?>
<feed xmlns="http://www.w3.org/2005/Atom">
  <title>BandwagonHost Status</title>
  <entry>
    <id>https://bwhstatus.com/issue.php?id=1785907793</id>
    <title>[Resolved] Osaka upstream maintenance</title>
    <updated>2026-08-05T18:26:03-07:00</updated>
    <published>2026-08-04T22:29:53-07:00</published>
    <link rel="alternate" href="https://bwhstatus.com/issue.php?id=1785907793" />
    <content type="text">Location: Osaka, Japan. This is going to impact network
connectivity for VMs hosted on nodes v31xx, v32xx, v33xx.</content>
  </entry>
  <entry>
    <id>https://bwhstatus.com/issue.php?id=1785900000</id>
    <title>Packet loss in Tokyo</title>
    <updated>2026-08-06T10:00:00-07:00</updated>
    <published>2026-08-06T09:00:00-07:00</published>
    <link rel="alternate" href="https://bwhstatus.com/issue.php?id=1785900000" />
    <content type="text">Investigating elevated packet loss in Tokyo, JP.</content>
  </entry>
</feed>`

// statusHarness is a CLI whose fleet reports a Tokyo box on node
// v3105, plus a stand-in status page.
func statusHarness(t *testing.T, feed string, feedStatus int) *harness {
	t.Helper()

	statusSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if feedStatus != 200 {
			w.WriteHeader(feedStatus)
			return
		}
		w.Header().Set("Content-Type", "application/atom+xml")
		fmt.Fprint(w, feed)
	}))
	t.Cleanup(statusSrv.Close)
	t.Setenv("BWG_STATUS_FEED", statusSrv.URL)

	bodies := map[string]string{
		"getServiceInfo": `{"error":0,"vm_type":"kvm","hostname":"tokyo.example.com",
		  "plan":"micro128","os":"debian-12","node_location":"JP, Tokyo",
		  "node_alias":"v3105","node_datacenter":"Tokyo DC1",
		  "plan_monthly_data":1000,"data_counter":250,"monthly_data_multiplier":1,
		  "ip_addresses":["203.0.113.10"],"ip_nullroutes":[],"suspended":0,
		  "policy_violation":0,"total_abuse_points":0,"max_abuse_points":1500}`,
	}
	return newHarness(t, bodies)
}

// The feature's whole claim: an incident naming "nodes v31xx" reaches
// the box on v3105, and says why.
func TestIncidentsCorrelateWithTheFleet(t *testing.T) {
	h := statusHarness(t, statusFeed, 200)

	payload := h.runJSON(t, "incidents")
	incidents := payload["incidents"].([]any)
	if len(incidents) != 2 {
		t.Fatalf("got %d incidents, want 2", len(incidents))
	}

	var osaka map[string]any
	for _, raw := range incidents {
		inc := raw.(map[string]any)
		if inc["number"] == "1785907793" {
			osaka = inc
		}
	}
	if osaka == nil {
		t.Fatal("the Osaka incident is missing")
	}

	affects, _ := osaka["affects"].([]any)
	if len(affects) == 0 {
		t.Fatalf("the Osaka incident named nodes v31xx but matched nothing:\n%v", osaka)
	}
	first := affects[0].(map[string]any)
	reasons := fmt.Sprint(first["reasons"])
	if !strings.Contains(reasons, "v31") {
		t.Errorf("the node-group reason is missing: %v", reasons)
	}
	// The reason must be legible, not a bare "affected" badge.
	if !strings.Contains(reasons, "v3105") {
		t.Errorf("the reason does not name the server's own node: %v", reasons)
	}
}

func TestIncidentsHumanOutputExplainsItself(t *testing.T) {
	h := statusHarness(t, statusFeed, 200)
	if err := h.run("incidents"); err != nil {
		t.Fatal(err)
	}
	out := h.stdout.String()

	if !strings.Contains(out, "Osaka upstream maintenance") {
		t.Errorf("the incident is missing:\n%s", out)
	}
	if !strings.Contains(out, "resolved") || !strings.Contains(out, "ongoing") {
		t.Errorf("incident states are missing:\n%s", out)
	}
	if !strings.Contains(out, "affects") {
		t.Errorf("the correlation is missing:\n%s", out)
	}
	// The honesty footer is load-bearing: a heuristic must not be read
	// as a guarantee in either direction.
	if !strings.Contains(out, "heuristic") || !strings.Contains(out, "all-clear") {
		t.Errorf("the output does not qualify its own matching:\n%s", out)
	}
}

func TestIncidentsOngoingFilter(t *testing.T) {
	h := statusHarness(t, statusFeed, 200)

	payload := h.runJSON(t, "incidents", "--ongoing")
	incidents := payload["incidents"].([]any)
	if len(incidents) != 1 {
		t.Fatalf("--ongoing returned %d incidents, want 1", len(incidents))
	}
	if incidents[0].(map[string]any)["resolved"] == true {
		t.Error("--ongoing returned a resolved incident")
	}

	summary := payload["summary"].(map[string]any)
	if summary["operational"] == true {
		t.Error("operational is true with an open incident")
	}
}

// --all is the escape from spending a getServiceInfo per server.
func TestIncidentsAllSkipsTheFleetLookup(t *testing.T) {
	h := statusHarness(t, statusFeed, 200)

	payload := h.runJSON(t, "incidents", "--all")
	for _, raw := range payload["incidents"].([]any) {
		if _, ok := raw.(map[string]any)["affects"]; ok {
			t.Error("--all still correlated against the fleet")
		}
	}
	for _, req := range h.seen() {
		if strings.Contains(req, "getServiceInfo") {
			t.Errorf("--all called the KiwiVM API anyway: %s", req)
		}
	}
}

func TestIncidentsShowOne(t *testing.T) {
	h := statusHarness(t, statusFeed, 200)

	if err := h.run("incidents", "1785907793"); err != nil {
		t.Fatal(err)
	}
	out := h.stdout.String()
	if !strings.Contains(out, "nodes v31xx") {
		t.Errorf("the full incident text is missing:\n%s", out)
	}

	err := h.run("incidents", "9999999")
	if err == nil {
		t.Fatal("an unknown incident id succeeded")
	}
	if !strings.Contains(err.Error(), "1785907793") {
		t.Errorf("the error does not list the known ids: %v", err)
	}
}

// The status page going down must not look like the fleet going down.
func TestIncidentsFeedFailureIsNotAFleetFailure(t *testing.T) {
	h := statusHarness(t, "", 503)

	err := h.run("incidents")
	if err == nil {
		t.Fatal("a 503 from the status page was accepted")
	}
	if !strings.Contains(err.Error(), "bwg ls") {
		t.Errorf("the error does not point at checking the servers directly: %v", err)
	}
	if !strings.Contains(err.Error(), "may be fine") {
		t.Errorf("the error implies the servers are down: %v", err)
	}
}

// A server that cannot be reached is unchecked, not unaffected.
func TestIncidentsReportUncheckedServers(t *testing.T) {
	statusSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, statusFeed)
	}))
	defer statusSrv.Close()
	t.Setenv("BWG_STATUS_FEED", statusSrv.URL)

	h := newHarness(t, map[string]string{
		"getServiceInfo": `{"error":700005,"message":"Authentication failure"}`,
	})

	payload := h.runJSON(t, "incidents")
	unchecked, _ := payload["unchecked"].([]any)
	if len(unchecked) == 0 {
		t.Fatalf("servers that did not answer were not reported as unchecked:\n%v", payload)
	}
}
