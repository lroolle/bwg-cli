package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lroolle/bwg-cli/internal/config"
	"github.com/lroolle/bwg-cli/kiwivm"
)

const infoBody = `{"error":0,"hostname":"tokyo.example.com","plan":"micro128",
"node_location":"JP, Tokyo","os":"debian-12","plan_monthly_data":1000,
"data_counter":250,"monthly_data_multiplier":1,"ip_addresses":["203.0.113.10"],
"ip_nullroutes":[],"suspended":0,"policy_violation":0,"total_abuse_points":0,
"max_abuse_points":1500}`

// fakeKiwi stands in for the API and records what was asked of it.
func fakeKiwi(t *testing.T) (*httptest.Server, *[]string) {
	t.Helper()
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		endpoint := strings.TrimPrefix(r.URL.Path, "/")
		seen = append(seen, r.Method+" "+endpoint)
		switch endpoint {
		case "getServiceInfo":
			fmt.Fprint(w, infoBody)
		default:
			fmt.Fprint(w, `{"error":0}`)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

func testConfig(t *testing.T, endpoint string) *config.Config {
	t.Helper()
	for _, k := range []string{"BWG_VEID", "BWG_API_KEY", "BWG_KIWIVM_API_KEY", "BWG_SERVER"} {
		t.Setenv(k, "")
	}
	cfg, err := config.Load(t.TempDir() + "/config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Add("tokyo", &config.Server{
		VEID: "1347645", APIKey: "private_aaaaaaaaaaaaaaaaaaaaaa", Endpoint: endpoint,
	}, true)
	return cfg
}

// exchange runs one batch of requests through the server and returns
// the decoded responses.
func exchange(t *testing.T, s *Server, in *bytes.Buffer, out *bytes.Buffer, reqs ...string) []map[string]any {
	t.Helper()
	for _, r := range reqs {
		in.WriteString(r + "\n")
	}
	if err := s.Serve(context.Background()); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	var got []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("response is not JSON: %v\n%s", err, line)
		}
		got = append(got, m)
	}
	return got
}

func newTestServer(t *testing.T, readOnly bool) (*Server, *bytes.Buffer, *bytes.Buffer, *[]string) {
	t.Helper()
	srv, seen := fakeKiwi(t)
	in, out := &bytes.Buffer{}, &bytes.Buffer{}
	return New(testConfig(t, srv.URL), readOnly, "test", in, out), in, out, seen
}

func TestInitializeHandshake(t *testing.T) {
	s, in, out, _ := newTestServer(t, false)
	got := exchange(t, s, in, out, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)

	if len(got) != 1 {
		t.Fatalf("got %d responses, want 1", len(got))
	}
	if got[0]["jsonrpc"] != "2.0" {
		t.Errorf("missing jsonrpc version: %v", got[0])
	}
	result := got[0]["result"].(map[string]any)
	if result["protocolVersion"] != protocolVersion {
		t.Errorf("protocolVersion = %v", result["protocolVersion"])
	}
	if _, ok := result["capabilities"].(map[string]any)["tools"]; !ok {
		t.Error("the server does not advertise tool support")
	}
	// The instructions are the agent's only orientation; they must name
	// the servers it can act on.
	if !strings.Contains(result["instructions"].(string), "tokyo") {
		t.Errorf("instructions do not name the fleet: %v", result["instructions"])
	}
}

// A notification has no id, and the spec says answer nothing at all.
func TestNotificationsGetNoResponse(t *testing.T) {
	s, in, out, _ := newTestServer(t, false)
	got := exchange(t, s, in, out,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":1,"method":"ping"}`)

	if len(got) != 1 {
		t.Fatalf("got %d responses, want only the ping's", len(got))
	}
	if got[0]["id"].(float64) != 1 {
		t.Errorf("responded to the wrong request: %v", got[0])
	}
}

// The gate's whole point in MCP: an agent host that grants a call gets
// it, so read-only must be enforced by what exists, not by asking.
func TestReadOnlyHidesWriteTools(t *testing.T) {
	ro, in, out, _ := newTestServer(t, true)
	got := exchange(t, ro, in, out, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	roTools := toolNames(t, got[0])

	rw, in2, out2, _ := newTestServer(t, false)
	got2 := exchange(t, rw, in2, out2, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	rwTools := toolNames(t, got2[0])

	if len(roTools) >= len(rwTools) {
		t.Fatalf("read-only advertised %d tools, read-write %d — nothing was withheld",
			len(roTools), len(rwTools))
	}
	for _, name := range roTools {
		if strings.Contains(name, "power") || strings.Contains(name, "create") ||
			strings.Contains(name, "set_") {
			t.Errorf("read-only advertises a mutating tool: %s", name)
		}
	}
	// Every advertised tool must map to a read operation.
	for _, name := range roTools {
		for _, tl := range ro.toolset() {
			if tl.Name != name {
				continue
			}
			if op, ok := kiwivm.LookupOp(tl.endpoint); ok && op.Risk != kiwivm.RiskRead {
				t.Errorf("read-only advertises %s, which is %s", name, op.Risk)
			}
		}
	}
}

func toolNames(t *testing.T, resp map[string]any) []string {
	t.Helper()
	result, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("no result in %v", resp)
	}
	var names []string
	for _, tl := range result["tools"].([]any) {
		names = append(names, tl.(map[string]any)["name"].(string))
	}
	return names
}

// A client can call anything it likes; hiding a tool is not enough.
func TestReadOnlyRefusesAHiddenToolThatIsCalledAnyway(t *testing.T) {
	s, in, out, seen := newTestServer(t, true)
	got := exchange(t, s, in, out,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"bwg_power","arguments":{"action":"restart"}}}`)

	result := got[0]["result"].(map[string]any)
	if result["isError"] != true {
		t.Fatalf("a hidden write tool was executed: %v", result)
	}
	text := result["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "read-only") {
		t.Errorf("the refusal does not say why: %q", text)
	}
	for _, req := range *seen {
		if strings.HasPrefix(req, "POST") {
			t.Errorf("read-only mode still sent a write: %s", req)
		}
	}
}

func TestToolCallReturnsData(t *testing.T) {
	s, in, out, _ := newTestServer(t, true)
	got := exchange(t, s, in, out,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"bwg_info","arguments":{}}}`)

	result := got[0]["result"].(map[string]any)
	if result["isError"] == true {
		t.Fatalf("bwg_info failed: %v", result)
	}
	text := result["content"].([]any)[0].(map[string]any)["text"].(string)

	var payload map[string]any
	if err := json.Unmarshal([]byte(text), &payload); err != nil {
		t.Fatalf("tool text is not JSON: %v\n%s", err, text)
	}
	if payload["server"] != "tokyo" {
		t.Errorf("server = %v", payload["server"])
	}
	res := payload["result"].(map[string]any)
	bw := res["bandwidth"].(map[string]any)
	if pct := bw["percent"].(float64); pct < 24.9 || pct > 25.1 {
		t.Errorf("bandwidth percent = %v, want 25", pct)
	}
}

func TestFleetToolSweepsAndSorts(t *testing.T) {
	s, in, out, _ := newTestServer(t, true)
	got := exchange(t, s, in, out,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"bwg_fleet","arguments":{}}}`)

	result := got[0]["result"].(map[string]any)
	text := result["content"].([]any)[0].(map[string]any)["text"].(string)

	var payload map[string]any
	json.Unmarshal([]byte(text), &payload)
	servers := payload["servers"].([]any)
	if len(servers) != 1 {
		t.Fatalf("swept %d servers", len(servers))
	}
	if servers[0].(map[string]any)["hostname"] != "tokyo.example.com" {
		t.Errorf("entry = %v", servers[0])
	}
}

// A failed KiwiVM call is a tool result the agent can read and adapt
// to, not a transport error it cannot interpret.
func TestAPIFailureBecomesAToolError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"error":700005,"message":"Authentication failure"}`)
	}))
	defer srv.Close()

	in, out := &bytes.Buffer{}, &bytes.Buffer{}
	s := New(testConfig(t, srv.URL), true, "test", in, out)

	got := exchange(t, s, in, out,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"bwg_info","arguments":{}}}`)

	if got[0]["error"] != nil {
		t.Errorf("an API failure became a protocol error: %v", got[0]["error"])
	}
	result := got[0]["result"].(map[string]any)
	if result["isError"] != true {
		t.Fatalf("an auth failure was reported as success: %v", result)
	}
	text := result["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "Authentication failure") {
		t.Errorf("the reason is missing: %q", text)
	}
}

func TestProtocolErrors(t *testing.T) {
	s, in, out, _ := newTestServer(t, false)
	got := exchange(t, s, in, out,
		`not json at all`,
		`{"jsonrpc":"2.0","id":2,"method":"no/such/method"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"bwg_nonexistent"}}`)

	if len(got) != 3 {
		t.Fatalf("got %d responses, want 3", len(got))
	}
	if code := got[0]["error"].(map[string]any)["code"].(float64); int(code) != codeParseError {
		t.Errorf("bad JSON produced code %v, want %d", code, codeParseError)
	}
	if code := got[1]["error"].(map[string]any)["code"].(float64); int(code) != codeMethodNotFound {
		t.Errorf("unknown method produced code %v, want %d", code, codeMethodNotFound)
	}
	if got[2]["error"] == nil {
		t.Error("an unknown tool was accepted")
	}
}

// notKiwiVM lists the tools that legitimately have no endpoint in
// kiwivm.Ops because they do not call KiwiVM. It is an explicit
// allowlist so that a typo in an endpoint string cannot quietly create
// an ungated tool.
var notKiwiVM = map[string]bool{
	"bwg_incidents": true, // reads the public status feed, no credentials
}

// fleetScoped lists the tools that sweep the whole fleet instead of
// acting on one server, and so are dispatched by name rather than
// through tool.run.
var fleetScoped = map[string]bool{
	"bwg_fleet":     true,
	"bwg_incidents": true,
}

func TestEveryToolIsClassified(t *testing.T) {
	s, _, _, _ := newTestServer(t, false)
	for _, tl := range s.toolset() {
		if tl.Name == "" || tl.Description == "" {
			t.Errorf("tool %q is missing a name or description", tl.Name)
		}
		if _, ok := kiwivm.LookupOp(tl.endpoint); !ok && !notKiwiVM[tl.Name] {
			t.Errorf("tool %s maps to unregistered endpoint %q — it would escape the gate",
				tl.Name, tl.endpoint)
		}
		if notKiwiVM[tl.Name] && tl.endpoint != "" {
			t.Errorf("tool %s is listed as non-KiwiVM but names endpoint %q",
				tl.Name, tl.endpoint)
		}
		if tl.InputSchema["type"] != "object" {
			t.Errorf("tool %s has no object input schema", tl.Name)
		}
		if !fleetScoped[tl.Name] && tl.run == nil {
			t.Errorf("tool %s has no implementation", tl.Name)
		}
	}
}

// An unclassified endpoint must count as mutating. Failing open here
// would mean a typo in an endpoint string silently produces a tool a
// read-only server happily advertises.
func TestUnknownEndpointCountsAsMutating(t *testing.T) {
	if !mutating(tool{Name: "bwg_typo", endpoint: "snapsho/list"}) {
		t.Error("a tool with an unrecognised endpoint was treated as safe")
	}
	if mutating(tool{Name: "bwg_incidents", endpoint: ""}) {
		t.Error("the status-feed tool was treated as mutating")
	}
	if mutating(tool{Name: "bwg_info", endpoint: "getServiceInfo"}) {
		t.Error("a read endpoint was treated as mutating")
	}
	if !mutating(tool{Name: "bwg_power", endpoint: "restart"}) {
		t.Error("a write endpoint was not treated as mutating")
	}
}

// The status tool carries no credentials and cannot change anything,
// so a read-only server must still offer it — that is exactly when a
// user most wants to know whether the provider is at fault.
func TestReadOnlyStillOffersIncidents(t *testing.T) {
	ro, in, out, _ := newTestServer(t, true)
	got := exchange(t, ro, in, out, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)

	var found bool
	for _, name := range toolNames(t, got[0]) {
		if name == "bwg_incidents" {
			found = true
		}
	}
	if !found {
		t.Error("read-only mode hid the status tool, which cannot change anything")
	}
}

func TestEmptyLinesAreIgnored(t *testing.T) {
	s, in, out, _ := newTestServer(t, false)
	got := exchange(t, s, in, out, "", "   ", `{"jsonrpc":"2.0","id":1,"method":"ping"}`)
	if len(got) != 1 {
		t.Fatalf("blank lines produced %d responses", len(got))
	}
}

// Breadth, the same way internal/cli tests every command: call every
// advertised tool once against a full fixture set. It catches the
// panics and the malformed payloads that only appear when a run
// closure is actually executed — most of them never were.
func TestEveryToolRuns(t *testing.T) {
	// A stand-in status page, so bwg_incidents does not reach the real
	// bwhstatus.com from a test.
	feed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/atom+xml")
		fmt.Fprint(w, `<?xml version="1.0" encoding="utf-8"?>
<feed xmlns="http://www.w3.org/2005/Atom"><title>BandwagonHost Status</title>
<entry><id>https://bwhstatus.com/issue.php?id=1</id>
<title>Packet loss in Tokyo</title>
<updated>2026-08-06T10:00:00-07:00</updated>
<published>2026-08-06T09:00:00-07:00</published>
<content type="text">Investigating packet loss in Tokyo, JP.</content></entry></feed>`)
	}))
	defer feed.Close()
	t.Setenv("BWG_STATUS_FEED", feed.URL)

	// Plausible arguments for the tools that need them. A tool missing
	// from this map is called with none, which is what an agent that
	// read only the schema would do for an all-optional tool.
	args := map[string]string{
		"bwg_power":           `{"action":"restart"}`,
		"bwg_set_ptr":         `{"ip":"203.0.113.10","ptr":"box.example.com"}`,
		"bwg_set_hostname":    `{"hostname":"box.example.com"}`,
		"bwg_snapshot_create": `{"description":"before the upgrade"}`,
	}

	s, _, _, _ := newTestServer(t, false)
	for _, tl := range s.toolset() {
		t.Run(tl.Name, func(t *testing.T) {
			// A fresh server per tool: Serve consumes its input stream.
			srv, in, out, _ := newTestServer(t, false)
			a := args[tl.Name]
			if a == "" {
				a = "{}"
			}
			got := exchange(t, srv, in, out, fmt.Sprintf(
				`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":%q,"arguments":%s}}`,
				tl.Name, a))

			if len(got) != 1 {
				t.Fatalf("got %d responses, want 1", len(got))
			}
			if errObj, ok := got[0]["error"]; ok {
				t.Fatalf("%s failed at the protocol level: %v", tl.Name, errObj)
			}
			result, ok := got[0]["result"].(map[string]any)
			if !ok {
				t.Fatalf("%s returned no result: %v", tl.Name, got[0])
			}
			if result["isError"] == true {
				t.Fatalf("%s reported a tool error: %v", tl.Name, result)
			}
			// Every tool answers with text content an agent can read.
			content, ok := result["content"].([]any)
			if !ok || len(content) == 0 {
				t.Fatalf("%s returned no content: %v", tl.Name, result)
			}
			text, _ := content[0].(map[string]any)["text"].(string)
			if strings.TrimSpace(text) == "" {
				t.Fatalf("%s returned empty text", tl.Name)
			}
			var payload any
			if err := json.Unmarshal([]byte(text), &payload); err != nil {
				t.Fatalf("%s returned text that is not JSON: %v\n%s", tl.Name, err, text)
			}
		})
	}
}

// The tool an agent reaches for first is the fleet overview, so its
// shape is worth pinning rather than only its exit status.
func TestIncidentsToolExplainsItself(t *testing.T) {
	feed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<?xml version="1.0" encoding="utf-8"?>
<feed xmlns="http://www.w3.org/2005/Atom"><title>Status</title>
<entry><id>https://bwhstatus.com/issue.php?id=2</id>
<title>Osaka upstream maintenance</title>
<updated>2026-08-06T10:00:00-07:00</updated>
<published>2026-08-06T09:00:00-07:00</published>
<content type="text">Location: Osaka, Japan. Impacts VMs on nodes v31xx.</content>
</entry></feed>`)
	}))
	defer feed.Close()
	t.Setenv("BWG_STATUS_FEED", feed.URL)

	s, _, _, _ := newTestServer(t, true)
	res, err := s.incidentsTool(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	m, ok := res.(map[string]any)
	if !ok {
		t.Fatalf("incidentsTool returned %T", res)
	}
	// The matching is a heuristic; the payload has to say so, or an
	// agent will read "no match" as "all clear". See TASTE.md.
	blob, _ := json.Marshal(m)
	if !strings.Contains(strings.ToLower(string(blob)), "heuristic") {
		t.Errorf("the incidents payload does not qualify its own matching: %s", blob)
	}
}
