package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/lroolle/bwg-cli/pkg/output"
	"github.com/spf13/cobra"
)

func TestMain(m *testing.M) {
	output.SetColor(false)
	os.Exit(m.Run())
}

// harness is a bwg wired to a fake KiwiVM, with its streams captured.
type harness struct {
	cmd    *cobra.Command
	app    *App
	stdout *bytes.Buffer
	stderr *bytes.Buffer
	config string

	// A fleet sweep calls the handler from several goroutines at once,
	// so the request log needs a lock.
	mu       sync.Mutex
	requests []string
}

// seen returns a copy of the recorded requests.
func (h *harness) seen() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.requests...)
}

// newHarness builds a CLI whose configured servers point at a fake
// KiwiVM. bodies maps an endpoint to the JSON it answers with.
func newHarness(t *testing.T, bodies map[string]string) *harness {
	t.Helper()
	h := &harness{stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		endpoint := strings.TrimPrefix(r.URL.Path, "/")
		h.mu.Lock()
		h.requests = append(h.requests, r.Method+" "+endpoint)
		h.mu.Unlock()
		body, ok := bodies[endpoint]
		if !ok {
			body = `{"error":0}`
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)

	h.config = filepath.Join(t.TempDir(), "config.yaml")
	cfg := fmt.Sprintf(`default: tokyo
servers:
  tokyo:
    veid: "1347645"
    api_key: private_abcdefghijklmnopqrstuv
    note: main box
    tags: [prod, jp]
    endpoint: %s
  osaka:
    veid: "1347646"
    api_key: private_bbbbbbbbbbbbbbbbbbbbbb
    tags: [prod]
    endpoint: %s
`, srv.URL, srv.URL)
	if err := os.WriteFile(h.config, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	// Keep the developer's real environment out of the test.
	for _, k := range []string{"BWG_SERVER", "BWG_VEID", "BWG_API_KEY", "BWG_KIWIVM_API_KEY", "BWG_READ_ONLY"} {
		t.Setenv(k, "")
	}
	t.Setenv("BWG_CONFIG", h.config)

	h.cmd, h.app = h.build()
	return h
}

// build returns a fresh command tree. Each run gets its own, because a
// cobra command object accumulates flag state across executions — a
// repeated --tag would append rather than replace. A real process
// builds the tree once and exits, so only tests hit this.
func (h *harness) build() (*cobra.Command, *App) {
	cmd, app := NewRoot("test")
	app.Out, app.ErrOut, app.In = h.stdout, h.stderr, strings.NewReader("")
	cmd.SetOut(h.stdout)
	cmd.SetErr(h.stderr)
	return cmd, app
}

func (h *harness) run(args ...string) error {
	h.stdout.Reset()
	h.stderr.Reset()
	h.cmd, h.app = h.build()
	h.cmd.SetArgs(args)
	return h.cmd.ExecuteContext(context.Background())
}

// runJSON runs a command with --json and decodes the result.
func (h *harness) runJSON(t *testing.T, args ...string) map[string]any {
	t.Helper()
	if err := h.run(append(args, "--json")...); err != nil {
		t.Fatalf("%v: %v\nstderr: %s", args, err, h.stderr)
	}
	var out map[string]any
	if err := json.Unmarshal(h.stdout.Bytes(), &out); err != nil {
		t.Fatalf("%v produced invalid JSON: %v\n%s", args, err, h.stdout)
	}
	return out
}

const serviceInfoBody = `{
  "error":0,"vm_type":"kvm","hostname":"tokyo.example.com","plan":"micro128",
  "os":"debian-12-x86_64","node_location":"JP, Tokyo","node_alias":"Node9",
  "plan_disk":21474836480,"plan_ram":1073741824,"plan_swap":268435456,
  "plan_monthly_data":1073741824000,"data_counter":536870912000,
  "monthly_data_multiplier":2,"data_next_reset":4102444800,
  "ip_addresses":["203.0.113.10","2001:db8::/64"],"private_ip_addresses":[],
  "ip_nullroutes":[],"ptr":{"203.0.113.10":"tokyo.example.com"},
  "available_isos":["systemrescue.iso"],"plan_max_ipv6s":4,
  "rdns_api_available":1,"location_ipv6_ready":1,
  "suspended":0,"policy_violation":0,"suspension_count":0,
  "total_abuse_points":100,"max_abuse_points":1500
}`

// -- output contract ------------------------------------------------------

func TestJSONGoesToStdoutAndNotesToStderr(t *testing.T) {
	h := newHarness(t, map[string]string{"getServiceInfo": serviceInfoBody})

	if err := h.run("info", "--json"); err != nil {
		t.Fatal(err)
	}
	// Anything on stdout must parse as JSON, with no commentary mixed in.
	var payload map[string]any
	if err := json.Unmarshal(h.stdout.Bytes(), &payload); err != nil {
		t.Fatalf("stdout is not clean JSON: %v\n%s", err, h.stdout)
	}
	if payload["server"] != "tokyo" {
		t.Errorf("server = %v", payload["server"])
	}
}

// The bandwidth multiplier is the number this whole tool exists to get
// right, so it is checked end to end and not just in the SDK.
func TestBandwidthArithmeticSurvivesToTheCLI(t *testing.T) {
	h := newHarness(t, map[string]string{"getServiceInfo": serviceInfoBody})
	payload := h.runJSON(t, "info")

	derived := payload["derived"].(map[string]any)
	bw := derived["bandwidth"].(map[string]any)

	// 500 GiB of 1000 GiB, multiplier 2 -> both doubled, still 50%.
	if pct := bw["percent"].(float64); pct < 49.9 || pct > 50.1 {
		t.Errorf("percent = %v, want 50", pct)
	}
	if used := bw["used"].(float64); used != 2*536870912000 {
		t.Errorf("used = %v, want the multiplier applied", used)
	}
	if mult := bw["multiplier"].(float64); mult != 2 {
		t.Errorf("multiplier = %v", mult)
	}
}

func TestInfoHumanOutputShowsTheEssentials(t *testing.T) {
	h := newHarness(t, map[string]string{"getServiceInfo": serviceInfoBody})
	if err := h.run("info"); err != nil {
		t.Fatal(err)
	}
	out := h.stdout.String()
	for _, want := range []string{"tokyo.example.com", "micro128", "JP, Tokyo", "203.0.113.10", "50%"} {
		if !strings.Contains(out, want) {
			t.Errorf("info output is missing %q:\n%s", want, out)
		}
	}
	// A multiplier above 1 must be explained, not just printed.
	if !strings.Contains(out, "2x") || !strings.Contains(out, "expensive-bandwidth") {
		t.Errorf("the multiplier is not explained:\n%s", out)
	}
}

// -- fleet ----------------------------------------------------------------

func TestFleetSweepsEveryServer(t *testing.T) {
	h := newHarness(t, map[string]string{"getServiceInfo": serviceInfoBody})
	payload := h.runJSON(t, "ls")

	servers := payload["servers"].([]any)
	if len(servers) != 2 {
		t.Fatalf("swept %d servers, want 2", len(servers))
	}
	totals := payload["totals"].(map[string]any)
	if totals["reachable"].(float64) != 2 {
		t.Errorf("reachable = %v", totals["reachable"])
	}
}

func TestFleetTagFilter(t *testing.T) {
	h := newHarness(t, map[string]string{"getServiceInfo": serviceInfoBody})

	payload := h.runJSON(t, "ls", "--tag", "jp")
	if got := len(payload["servers"].([]any)); got != 1 {
		t.Errorf("tag jp matched %d servers, want 1", got)
	}

	payload = h.runJSON(t, "ls", "--tag", "prod")
	if got := len(payload["servers"].([]any)); got != 2 {
		t.Errorf("tag prod matched %d servers, want 2", got)
	}

	if err := h.run("ls", "--tag", "nope"); err == nil {
		t.Error("an unmatched tag should be an error, not an empty table")
	}
}

// One unreachable box must not hide the others. This is the property
// that makes 'bwg ls' trustworthy at 3am.
func TestFleetReportsFailuresAlongsideData(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.RawQuery, "1347646") {
			w.WriteHeader(500)
			fmt.Fprint(w, "node on fire")
			return
		}
		fmt.Fprint(w, serviceInfoBody)
	}))
	defer srv.Close()

	h := newHarness(t, nil)
	cfg := fmt.Sprintf(`default: tokyo
servers:
  tokyo: {veid: "1347645", api_key: private_aaaaaaaaaaaaaaaaaaaaaa, endpoint: %s}
  osaka: {veid: "1347646", api_key: private_bbbbbbbbbbbbbbbbbbbbbb, endpoint: %s}
`, srv.URL, srv.URL)
	os.WriteFile(h.config, []byte(cfg), 0o600)

	payload := h.runJSON(t, "ls")
	if got := len(payload["servers"].([]any)); got != 1 {
		t.Errorf("healthy servers reported: %d, want 1", got)
	}
	failed := payload["failed"].([]any)
	if len(failed) != 1 {
		t.Fatalf("failures reported: %d, want 1", len(failed))
	}
	if f := failed[0].(map[string]any); f["server"] != "osaka" {
		t.Errorf("wrong server failed: %v", f)
	}
}

// -- the safety model ------------------------------------------------------

func TestReadOnlyRefusesWritesWithExitRefused(t *testing.T) {
	h := newHarness(t, map[string]string{"getServiceInfo": serviceInfoBody})

	// --yes must not defeat read-only. Read-only is the stronger claim.
	err := h.run("--read-only", "--yes", "host", "newname.example")
	if err == nil {
		t.Fatal("a write succeeded on a read-only CLI")
	}
	if code := CodeFor(err); code != ExitRefused {
		t.Errorf("exit code = %d, want %d (refused)", code, ExitRefused)
	}
	for _, req := range h.seen() {
		if strings.HasPrefix(req, "POST") {
			t.Errorf("read-only mode still sent a write: %s", req)
		}
	}
}

func TestReadOnlyFromTheEnvironment(t *testing.T) {
	h := newHarness(t, map[string]string{"getServiceInfo": serviceInfoBody})
	t.Setenv("BWG_READ_ONLY", "1")

	if err := h.run("host", "newname.example", "--yes"); err == nil {
		t.Fatal("BWG_READ_ONLY=1 did not block a write")
	} else if CodeFor(err) != ExitRefused {
		t.Errorf("exit code = %d, want refused", CodeFor(err))
	}
}

// A script must never hang on a prompt nobody can answer.
func TestNonInteractiveWriteRefusesInsteadOfBlocking(t *testing.T) {
	h := newHarness(t, map[string]string{"getServiceInfo": serviceInfoBody})

	err := h.run("host", "newname.example")
	if err == nil {
		t.Fatal("a write proceeded with no confirmation and no terminal")
	}
	if CodeFor(err) != ExitRefused {
		t.Errorf("exit code = %d, want refused", CodeFor(err))
	}
	if !strings.Contains(err.Error(), "--yes") {
		t.Errorf("the refusal does not name the fix: %v", err)
	}
}

func TestYesProceedsAndRecordsOnStderr(t *testing.T) {
	h := newHarness(t, map[string]string{"getServiceInfo": serviceInfoBody})

	if err := h.run("host", "newname.example", "--yes"); err != nil {
		t.Fatalf("--yes did not proceed: %v", err)
	}
	// Skipping the prompt must not mean skipping the record.
	if !strings.Contains(h.stderr.String(), "newname.example") {
		t.Errorf("no record of the change on stderr: %q", h.stderr)
	}
	var sawWrite bool
	for _, req := range h.seen() {
		if req == "POST setHostname" {
			sawWrite = true
		}
	}
	if !sawWrite {
		t.Errorf("setHostname was never sent: %v", h.seen())
	}
}

func TestReadsNeverPrompt(t *testing.T) {
	h := newHarness(t, map[string]string{
		"getServiceInfo": serviceInfoBody,
		"snapshot/list":  `{"error":0,"snapshots":[]}`,
		"backup/list":    `{"error":0,"backups":[]}`,
	})
	for _, args := range [][]string{
		{"info"}, {"ls"}, {"snapshot", "ls"}, {"backup", "ls"}, {"api", "ops"},
	} {
		if err := h.run(args...); err != nil {
			t.Errorf("%v failed without a prompt being answerable: %v", args, err)
		}
	}
}

// -- errors ----------------------------------------------------------------

func TestAuthFailureExplainsAndExitsFour(t *testing.T) {
	h := newHarness(t, map[string]string{
		"getServiceInfo": `{"error":700005,"message":"Authentication failure"}`,
	})
	err := h.run("info")
	if err == nil {
		t.Fatal("expected an error")
	}
	if code := CodeFor(err); code != ExitAuth {
		t.Errorf("exit code = %d, want %d (auth)", code, ExitAuth)
	}
	if !strings.Contains(err.Error(), "KiwiVM > API") {
		t.Errorf("the error does not say where to find the credentials: %v", err)
	}
}

func TestLockedVPSSurfacesProgress(t *testing.T) {
	h := newHarness(t, map[string]string{
		"getServiceInfo": `{"error":788888,"message":"VE is currently locked",
		  "additionalLockingInfo":{"completed_percent":42,
		  "friendly_progress_message":"Creating snapshot"}}`,
	})
	err := h.run("info")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "Creating snapshot") || !strings.Contains(err.Error(), "42%") {
		t.Errorf("the lock progress is not in the error: %v", err)
	}
}

func TestUnknownServerNamesTheAlternatives(t *testing.T) {
	h := newHarness(t, nil)
	err := h.run("info", "--server", "nagoya")
	if err == nil {
		t.Fatal("expected an error")
	}
	if CodeFor(err) != ExitConfig {
		t.Errorf("exit code = %d, want config", CodeFor(err))
	}
	if !strings.Contains(err.Error(), "tokyo") || !strings.Contains(err.Error(), "osaka") {
		t.Errorf("the error does not list the known servers: %v", err)
	}
}

// -- server management ------------------------------------------------------

func TestServerListNeverPrintsAKey(t *testing.T) {
	h := newHarness(t, nil)

	for _, args := range [][]string{
		{"server", "ls"}, {"server", "ls", "--json"},
		{"server", "show", "tokyo"}, {"server", "show", "tokyo", "--json"},
	} {
		if err := h.run(args...); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if strings.Contains(h.stdout.String(), "private_abcdefghijklmnopqrstuv") {
			t.Errorf("%v leaked the API key:\n%s", args, h.stdout)
		}
		if !strings.Contains(h.stdout.String(), "private_abc") {
			t.Errorf("%v masked the key beyond recognition:\n%s", args, h.stdout)
		}
	}
}

func TestServerAddVerifiesBeforeSaving(t *testing.T) {
	h := newHarness(t, map[string]string{"getServiceInfo": serviceInfoBody})
	srvURL := endpointFromConfig(t, h.config)

	err := h.run("server", "add", "nagoya",
		"--veid", "1347647", "--key", "private_cccccccccccccccccccccc")
	if err == nil {
		t.Fatal("adding a server with no reachable endpoint should fail verification")
	}

	// With an endpoint it can reach, it saves.
	h2 := newHarness(t, map[string]string{"getServiceInfo": serviceInfoBody})
	_ = srvURL
	if err := h2.run("server", "add", "nagoya",
		"--veid", "1347647", "--key", "private_cccccccccccccccccccccc", "--no-verify"); err != nil {
		t.Fatalf("--no-verify add failed: %v", err)
	}
	if err := h2.run("server", "ls"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(h2.stdout.String(), "nagoya") {
		t.Errorf("the added server is missing:\n%s", h2.stdout)
	}
}

func endpointFromConfig(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if i := strings.Index(line, "endpoint: "); i >= 0 {
			return strings.TrimSpace(line[i+len("endpoint: "):])
		}
	}
	return ""
}

func TestServerImportCSV(t *testing.T) {
	h := newHarness(t, nil)
	csvPath := filepath.Join(t.TempDir(), "keys.csv")
	os.WriteFile(csvPath, []byte(
		"hostname,VEID,API_KEY\n"+
			"kyoto.example.com,1347648,private_dddddddddddddddddddddd\n"+
			"nara.example.com,1347649,private_eeeeeeeeeeeeeeeeeeeeee\n"+
			// Already configured under a different name: must be skipped,
			// not duplicated.
			"dupe.example.com,1347645,private_ffffffffffffffffffffff\n"), 0o600)

	payload := h.runJSON(t, "server", "import", csvPath)
	added := payload["added"].([]any)
	skipped := payload["skipped"].([]any)
	if len(added) != 2 {
		t.Errorf("added %d, want 2: %v", len(added), added)
	}
	if len(skipped) != 1 {
		t.Errorf("skipped %d, want 1 (the already-configured veid): %v", len(skipped), skipped)
	}

	if err := h.run("server", "ls"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"kyoto.example.com", "nara.example.com"} {
		if !strings.Contains(h.stdout.String(), want) {
			t.Errorf("%s was not imported:\n%s", want, h.stdout)
		}
	}
}

func TestServerImportDryRunChangesNothing(t *testing.T) {
	h := newHarness(t, nil)
	csvPath := filepath.Join(t.TempDir(), "keys.csv")
	os.WriteFile(csvPath, []byte("VEID,API_KEY\n1347650,private_gggggggggggggggggggggg\n"), 0o600)

	before, _ := os.ReadFile(h.config)
	payload := h.runJSON(t, "server", "import", csvPath, "--dry-run")
	if len(payload["added"].([]any)) != 1 {
		t.Errorf("dry run should still report what it would add")
	}
	after, _ := os.ReadFile(h.config)
	if string(before) != string(after) {
		t.Error("--dry-run modified the config file")
	}
}

// -- api escape hatch --------------------------------------------------------

func TestAPIOpsListsEveryEndpoint(t *testing.T) {
	h := newHarness(t, nil)
	payload := h.runJSON(t, "api", "ops")
	ops := payload["ops"].([]any)
	if len(ops) < 30 {
		t.Errorf("only %d operations listed", len(ops))
	}
	risks := map[string]int{}
	for _, o := range ops {
		risks[o.(map[string]any)["risk"].(string)]++
	}
	for _, want := range []string{"read", "write", "destructive"} {
		if risks[want] == 0 {
			t.Errorf("no operations classified %q", want)
		}
	}
}

func TestAPICallGoesThroughTheGate(t *testing.T) {
	h := newHarness(t, map[string]string{"getServiceInfo": serviceInfoBody})

	// A read passes through and returns the raw body.
	if err := h.run("api", "call", "getServiceInfo"); err != nil {
		t.Fatalf("api call on a read failed: %v", err)
	}
	if !strings.Contains(h.stdout.String(), "tokyo.example.com") {
		t.Errorf("raw response missing:\n%s", h.stdout)
	}

	// A write is still gated.
	err := h.run("--read-only", "api", "call", "setHostname", "-f", "newHostname=x")
	if err == nil || CodeFor(err) != ExitRefused {
		t.Errorf("api call bypassed read-only: %v", err)
	}

	// An unclassified endpoint cannot be reached at all.
	if err := h.run("api", "call", "some/madeUpThing"); err == nil {
		t.Error("api call accepted an unregistered endpoint")
	}
}

// -- snapshots ---------------------------------------------------------------

func TestSnapshotRefResolvesBySubstring(t *testing.T) {
	h := newHarness(t, map[string]string{
		"getServiceInfo": serviceInfoBody,
		"snapshot/list": `{"error":0,"snapshots":[
		  {"fileName":"1347645_20260801_aaaa.tar.gz","os":"debian-12","size":1073741824,
		   "description":"before upgrade","sticky":0,"purgesIn":604800},
		  {"fileName":"1347645_20260805_bbbb.tar.gz","os":"debian-12","size":2147483648,
		   "description":"nightly","sticky":1}]}`,
	})

	payload := h.runJSON(t, "snapshot", "ls")
	if got := len(payload["snapshots"].([]any)); got != 2 {
		t.Fatalf("listed %d snapshots", got)
	}

	// A unique substring of the description is enough to delete.
	if err := h.run("snapshot", "rm", "upgrade", "--yes"); err != nil {
		t.Fatalf("substring ref did not resolve: %v", err)
	}

	// An ambiguous ref must name the candidates rather than guess.
	err := h.run("snapshot", "rm", "1347645", "--yes")
	if err == nil {
		t.Fatal("an ambiguous snapshot ref was accepted")
	}
	if !strings.Contains(err.Error(), "aaaa") || !strings.Contains(err.Error(), "bbbb") {
		t.Errorf("the ambiguity error does not list the candidates: %v", err)
	}
}

func TestSnapshotListShowsPurgeWindow(t *testing.T) {
	h := newHarness(t, map[string]string{
		"snapshot/list": `{"error":0,"snapshots":[
		  {"fileName":"a.tar.gz","os":"debian-12","size":1,"sticky":0,"purgesIn":604800},
		  {"fileName":"b.tar.gz","os":"debian-12","size":1,"sticky":1}]}`,
	})
	if err := h.run("snapshot", "ls"); err != nil {
		t.Fatal(err)
	}
	out := h.stdout.String()
	if !strings.Contains(out, "7d") {
		t.Errorf("the purge window is missing:\n%s", out)
	}
	if !strings.Contains(out, "never") {
		t.Errorf("a sticky snapshot should say it never purges:\n%s", out)
	}
}

// -- exec ---------------------------------------------------------------------

// The guest command's exit status is the meaningful result and must
// become bwg's, or shell composition breaks.
func TestExecPassesThroughTheGuestExitStatus(t *testing.T) {
	h := newHarness(t, map[string]string{
		"getServiceInfo":  serviceInfoBody,
		"basicShell/exec": `{"error":2,"message":"ls: /nope: No such file or directory"}`,
	})

	err := h.run("exec", "ls /nope", "--yes")
	if err == nil {
		t.Fatal("a failing guest command reported success")
	}
	if code := CodeFor(err); code != 2 {
		t.Errorf("exit code = %d, want the guest's 2", code)
	}
	if !strings.Contains(h.stdout.String(), "No such file") {
		t.Errorf("the command output was not printed:\n%s", h.stdout)
	}
}

func TestExecSuccessIsSilentOnStderr(t *testing.T) {
	h := newHarness(t, map[string]string{
		"getServiceInfo":  serviceInfoBody,
		"basicShell/exec": `{"error":0,"message":"total 0\n"}`,
	})
	if err := h.run("exec", "ls", "--yes"); err != nil {
		t.Fatalf("a successful command returned an error: %v", err)
	}
	if got := strings.TrimSpace(h.stdout.String()); got != "total 0" {
		t.Errorf("stdout = %q, want just the command output", got)
	}
}

// -- misc ---------------------------------------------------------------------

func TestVersion(t *testing.T) {
	h := newHarness(t, nil)
	if err := h.run("version"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(h.stdout.String(), "test") {
		t.Errorf("version output = %q", h.stdout)
	}
}

func TestEveryCommandAcceptsJSON(t *testing.T) {
	h := newHarness(t, nil)
	// --json is a global flag; every leaf command must inherit it.
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		for _, sub := range c.Commands() {
			walk(sub)
		}
		if c.Runnable() && c.Name() != "help" && c.Name() != "completion" {
			if c.InheritedFlags().Lookup("json") == nil && c.Flags().Lookup("json") == nil {
				t.Errorf("%q does not accept --json", c.CommandPath())
			}
		}
	}
	walk(h.cmd)
}

func TestCommandsDocumentThemselves(t *testing.T) {
	h := newHarness(t, nil)
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		for _, sub := range c.Commands() {
			walk(sub)
		}
		if c.Name() == "help" || c.Name() == "completion" || c == h.cmd {
			return
		}
		if c.Short == "" {
			t.Errorf("%q has no summary", c.CommandPath())
		}
	}
	walk(h.cmd)
}
