package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lroolle/bwg-cli/kiwivm"
)

// apiBodies is a full set of plausible KiwiVM responses, so any
// command can be driven end to end.
var apiBodies = map[string]string{
	"getServiceInfo": serviceInfoBody,
	"getLiveServiceInfo": `{
	  "error":0,"vm_type":"kvm","hostname":"tokyo.example.com","plan":"micro128",
	  "os":"debian-12-x86_64","node_location":"JP, Tokyo","plan_ram":1073741824,
	  "plan_disk":21474836480,"plan_monthly_data":1073741824000,
	  "data_counter":536870912000,"monthly_data_multiplier":2,
	  "data_next_reset":4102444800,"ip_addresses":["203.0.113.10"],
	  "ip_nullroutes":[],"suspended":0,"policy_violation":0,
	  "total_abuse_points":0,"max_abuse_points":1500,
	  "ve_status":"Running","ve_mac1":"00:16:3e:aa:bb:cc","ssh_port":29876,
	  "ve_used_disk_space_b":5368709120,"ve_disk_quota_gb":20,
	  "is_cpu_throttled":0,"is_disk_throttled":0,"live_hostname":"tokyo",
	  "load_average":"0.15 0.10 0.05","mem_available_kb":524288,
	  "swap_total_kb":262144,"swap_available_kb":262144
	}`,
	"getRawUsageStats": `{"error":0,"vm_type":"kvm","data":[
	  {"timestamp":1754524800,"cpu_usage":12,"network_in_bytes":1073741824,
	   "network_out_bytes":2147483648,"disk_read_bytes":104857600,"disk_write_bytes":52428800},
	  {"timestamp":1754611200,"cpu_usage":30,"network_in_bytes":2147483648,
	   "network_out_bytes":4294967296,"disk_read_bytes":209715200,"disk_write_bytes":104857600}]}`,
	"getAuditLog": `{"error":0,"log_entries":[
	  {"timestamp":1754524800,"requestor_ipv4":16909060,"type":1,"summary":"VPS restarted via API"},
	  {"timestamp":1754611200,"requestor_ipv4":16909061,"type":2,"summary":"Snapshot created"}]}`,
	"getAvailableOS": `{"error":0,"installed":"debian-12-x86_64",
	  "templates":["debian-12-x86_64","ubuntu-24.04-x86_64","centos-9-x86_64"]}`,
	"getSshKeys": `{"error":0,"ssh_keys_veid":"ssh-ed25519 AAAAC3Nz key1",
	  "ssh_keys_user":"","ssh_keys_preferred":"ssh-ed25519 AAAAC3Nz key1",
	  "shortened_ssh_keys_preferred":"ssh-ed25519 AAAA...key1"}`,
	"snapshot/list": `{"error":0,"snapshots":[
	  {"fileName":"1347645_20260801_aaaa.tar.gz","os":"debian-12","description":"before upgrade",
	   "size":1073741824,"uncompressed":2147483648,"md5":"abc","sticky":0,"purgesIn":604800,
	   "downloadLink":"http://example/a","downloadLinkSSL":"https://example/a"}]}`,
	"backup/list": `{"error":0,"backups":{"tok-aaaa":{"size":1073741824,"os":"debian-12",
	  "md5":"abc","timestamp":1754524800}}}`,
	"privateIp/getAvailableIps": `{"error":0,"available_ips":["10.0.0.5","10.0.0.6"]}`,
	"migrate/getLocations": `{"error":0,"currentLocation":"JPTYO",
	  "locations":["JPTYO","USLAX"],
	  "descriptions":{"JPTYO":"Japan, Tokyo","USLAX":"USA, Los Angeles"},
	  "dataTransferMultipliers":{"JPTYO":3,"USLAX":1}}`,
	"getSuspensionDetails": `{"error":0,"suspension_count":1,"total_abuse_points":100,
	  "max_abuse_points":1500,
	  "suspensions":[{"record_id":11851,"flag":"copyright","is_soft":1,
	    "evidence_record_id":2207,"abuse_points":100}],
	  "evidence":{"2207":"Full text of the abuse complaint"}}`,
	"getPolicyViolations": `{"error":0,"total_abuse_points":100,"max_abuse_points":1500,
	  "policy_violations":[{"record_id":14,"timestamp":1754524800,"suspend_at":4102444800,
	    "flag":"copyright","is_soft":1,"abuse_points":100,"evidence_data":"details here"}]}`,
	"getRateLimitStatus": `{"error":0,"remaining_points_15min":900,"remaining_points_24h":9000}`,
	"kiwivm/getNotificationPreferences": `{"error":0,"notificationEmail":"me@example.com",
	  "email_preferences":{"Service":{"snapshot_done":{"friendly_description":"Snapshot finished",
	    "is_enabled":1,"changed_timestamp":1754524800,"s_value":"1"}}}}`,
	"kiwivm/setNotificationPreferences": `{"error":0,
	  "submitted_email_preferences":{"snapshot_done":1},
	  "updated_email_preferences":{"snapshot_done":1},
	  "friendly_descriptions":{"snapshot_done":"Snapshot finished"}}`,
	"snapshot/create":   `{"error":0,"notificationEmail":"me@example.com"}`,
	"snapshot/export":   `{"error":0,"token":"xfer-token-abc"}`,
	"resetRootPassword": `{"error":0,"password":"s3cr3t-generated"}`,
	"reinstallOS":       `{"error":0,"rootPassword":"newpass123","sshPort":29876,"sshKeys":["ssh-ed25519 AAAA"],"sshKeysBrief":["ssh-ed25519 AAAA..."],"notificationEmail":"me@example.com"}`,
	"ipv6/add":          `{"error":0,"assigned_subnet":"2001:db8:1::/64"}`,
	"privateIp/assign":  `{"error":0,"assigned_ips":["10.0.0.5"]}`,
	"migrate/start":     `{"error":0,"notificationEmail":"me@example.com","newIps":["198.51.100.7"]}`,
	"basicShell/exec":   `{"error":0,"message":"total 0\n"}`,
	"shellScript/exec":  `{"error":0,"log":"/tmp/script-1234.log"}`,
}

// TestEveryCommandRunsAndEmitsValidJSON drives every command against a
// full set of responses. It is a breadth test, not a depth test: its
// job is to catch a panic, a nil dereference, or a malformed payload in
// a command nobody exercised by hand.
func TestEveryCommandRunsAndEmitsValidJSON(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"info", []string{"info"}},
		{"status", []string{"status"}},
		{"ls", []string{"ls"}},
		{"ls live", []string{"ls", "--live"}},
		{"ls alerting", []string{"ls", "--alerting"}},
		{"ls sorted by name", []string{"ls", "--sort", "name"}},
		{"ls sorted by abuse", []string{"ls", "--sort", "abuse"}},
		{"usage", []string{"usage"}},
		{"usage raw", []string{"usage", "--raw"}},
		{"usage windowed", []string{"usage", "--days", "1"}},
		{"audit", []string{"audit"}},
		{"audit limited", []string{"audit", "--limit", "1"}},
		{"audit filtered", []string{"audit", "--grep", "snapshot"}},
		{"snapshot ls", []string{"snapshot", "ls"}},
		{"backup ls", []string{"backup", "ls"}},
		{"os ls", []string{"os", "ls"}},
		{"keys ls", []string{"keys", "ls"}},
		{"net ls", []string{"net", "ls"}},
		{"net private ls", []string{"net", "private", "ls"}},
		{"iso ls", []string{"iso", "ls"}},
		{"abuse ls", []string{"abuse", "ls"}},
		{"migrate ls", []string{"migrate", "ls"}},
		{"notify ls", []string{"notify", "ls"}},
		{"ratelimit", []string{"ratelimit"}},
		{"ssh print", []string{"ssh", "--print"}},
		{"api ops", []string{"api", "ops"}},
		{"api ops filtered", []string{"api", "ops", "--risk", "write"}},
		{"api call", []string{"api", "call", "getServiceInfo"}},
		{"server ls", []string{"server", "ls"}},
		{"server show", []string{"server", "show", "tokyo"}},
		{"server default", []string{"server", "default"}},
		{"server check", []string{"server", "check"}},
		{"version", []string{"version"}},

		// Writes, with the gate satisfied.
		{"snapshot create", []string{"snapshot", "create", "-d", "test", "--yes"}},
		{"snapshot sticky", []string{"snapshot", "sticky", "aaaa", "--yes"}},
		{"snapshot export", []string{"snapshot", "export", "aaaa", "--yes"}},
		{"snapshot import", []string{"snapshot", "import", "--from-veid", "1", "--token", "t", "--yes"}},
		{"snapshot rm", []string{"snapshot", "rm", "aaaa", "--yes"}},
		{"snapshot restore", []string{"snapshot", "restore", "aaaa", "--yes"}},
		{"backup restore", []string{"backup", "restore", "tok-aaaa", "--yes"}},
		{"os reinstall", []string{"os", "reinstall", "ubuntu-24.04", "--yes"}},
		{"passwd", []string{"passwd", "--yes"}},
		{"host", []string{"host", "new.example.com", "--yes"}},
		{"power start", []string{"power", "start", "--yes"}},
		{"power stop", []string{"power", "stop", "--yes"}},
		{"restart alias", []string{"restart", "--yes"}},
		{"power kill", []string{"power", "kill", "--yes"}},
		{"net ptr", []string{"net", "ptr", "203.0.113.10", "mail.example.com", "--yes"}},
		{"net ipv6 add", []string{"net", "ipv6", "add", "--yes"}},
		{"net ipv6 rm", []string{"net", "ipv6", "rm", "2001:db8:1::/64", "--yes"}},
		{"net private add", []string{"net", "private", "add", "--yes"}},
		{"net private rm", []string{"net", "private", "rm", "10.0.0.5", "--yes"}},
		{"iso mount", []string{"iso", "mount", "systemrescue.iso", "--yes"}},
		{"iso unmount", []string{"iso", "unmount", "--yes"}},
		{"abuse resolve", []string{"abuse", "resolve", "14", "--yes"}},
		{"abuse unsuspend", []string{"abuse", "unsuspend", "11851", "--yes"}},
		{"migrate start", []string{"migrate", "start", "USLAX", "--yes"}},
		{"notify set", []string{"notify", "set", "snapshot_done=on", "--yes"}},
		{"run", []string{"run", "echo hi", "--yes"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, apiBodies)

			// Human rendering must not panic and must produce something.
			if err := h.run(tc.args...); err != nil {
				t.Fatalf("%v: %v\nstderr: %s", tc.args, err, h.stderr)
			}
			if strings.TrimSpace(h.stdout.String()) == "" {
				t.Errorf("%v produced no output", tc.args)
			}

			// JSON must be well formed. Some commands emit an object,
			// some (api call) emit whatever KiwiVM sent.
			if err := h.run(append(tc.args, "--json")...); err != nil {
				t.Fatalf("%v --json: %v", tc.args, err)
			}
			var any_ any
			if err := json.Unmarshal(h.stdout.Bytes(), &any_); err != nil {
				t.Errorf("%v --json produced invalid JSON: %v\n%s", tc.args, err, h.stdout)
			}
		})
	}
}

// Whatever a command does, the API key must never reach stdout.
func TestNoCommandLeaksTheAPIKey(t *testing.T) {
	const key = "private_abcdefghijklmnopqrstuv"
	for _, args := range [][]string{
		{"info"}, {"ls"}, {"status"}, {"server", "ls"}, {"server", "show", "tokyo"},
		{"api", "call", "getServiceInfo"}, {"api", "ops"}, {"net", "ls"},
		{"ssh", "--print"}, {"server", "check"},
	} {
		h := newHarness(t, apiBodies)
		for _, variant := range [][]string{args, append(args, "--json")} {
			if err := h.run(variant...); err != nil {
				t.Fatalf("%v: %v", variant, err)
			}
			if strings.Contains(h.stdout.String(), key) {
				t.Errorf("%v leaked the API key to stdout", variant)
			}
			if strings.Contains(h.stderr.String(), key) {
				t.Errorf("%v leaked the API key to stderr", variant)
			}
		}
	}
}

// Read-only must hold across the whole command surface, not just the
// commands somebody remembered to check.
func TestReadOnlyBlocksEveryWriteCommand(t *testing.T) {
	writes := [][]string{
		{"host", "new.example.com"},
		{"power", "start"}, {"power", "stop"}, {"power", "restart"}, {"power", "kill"},
		{"restart"}, {"start"}, {"stop"},
		{"snapshot", "create"}, {"snapshot", "rm", "aaaa"}, {"snapshot", "restore", "aaaa"},
		{"snapshot", "sticky", "aaaa"}, {"snapshot", "export", "aaaa"},
		{"snapshot", "import", "--from-veid", "1", "--token", "t"},
		{"backup", "restore", "tok-aaaa"},
		{"os", "reinstall", "ubuntu-24.04"},
		{"passwd"},
		{"net", "ptr", "203.0.113.10", "mail.example.com"},
		{"net", "ipv6", "add"}, {"net", "ipv6", "rm", "2001:db8:1::/64"},
		{"net", "private", "add"}, {"net", "private", "rm", "10.0.0.5"},
		{"iso", "mount", "systemrescue.iso"}, {"iso", "unmount"},
		{"abuse", "resolve", "14"}, {"abuse", "unsuspend", "11851"},
		{"migrate", "start", "USLAX"},
		{"notify", "set", "snapshot_done=on"},
		{"exec", "ls"}, {"run", "echo hi"},
		{"keys", "set", "--clear"},
	}

	for _, args := range writes {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			h := newHarness(t, apiBodies)
			// --yes on purpose: read-only is the stronger claim and must
			// win over an explicit go-ahead.
			err := h.run(append([]string{"--read-only", "--yes"}, args...)...)
			if err == nil {
				t.Fatalf("%v succeeded on a read-only CLI", args)
			}
			if code := CodeFor(err); code != ExitRefused {
				t.Errorf("%v: exit code %d, want %d (refused)", args, code, ExitRefused)
			}
			for _, req := range h.seen() {
				if strings.HasPrefix(req, "POST") {
					t.Errorf("%v sent a write anyway: %s", args, req)
				}
			}
		})
	}
}

func TestStatusReportsLiveState(t *testing.T) {
	h := newHarness(t, apiBodies)
	payload := h.runJSON(t, "status")

	if payload["state"] != "running" || payload["running"] != true {
		t.Errorf("state = %v, running = %v", payload["state"], payload["running"])
	}
	if payload["sshPort"].(float64) != 29876 {
		t.Errorf("sshPort = %v", payload["sshPort"])
	}
	res := payload["resources"].(map[string]any)
	if res["diskUsed"].(float64) != 5368709120 {
		t.Errorf("diskUsed = %v", res["diskUsed"])
	}
	// The console screenshot must not ride along in every payload.
	live := payload["live"].(map[string]any)
	if s, ok := live["screendump_png_base64"]; ok && s != "" {
		t.Error("the base64 screenshot was included in --json")
	}
}

func TestSSHPrintUsesThePortFromTheAPI(t *testing.T) {
	h := newHarness(t, apiBodies)
	payload := h.runJSON(t, "ssh", "--print")

	if payload["port"].(float64) != 29876 {
		t.Errorf("port = %v, want the API's 29876 rather than 22", payload["port"])
	}
	if payload["user"] != "root" {
		t.Errorf("user = %v", payload["user"])
	}
	if !strings.Contains(payload["command"].(string), "-p 29876") {
		t.Errorf("command = %v", payload["command"])
	}
}

func TestUsageAggregatesByDay(t *testing.T) {
	h := newHarness(t, apiBodies)
	payload := h.runJSON(t, "usage")

	days := payload["days"].([]any)
	if len(days) != 2 {
		t.Fatalf("aggregated into %d days, want 2", len(days))
	}
	totals := payload["totals"].(map[string]any)
	if totals["networkIn"].(float64) != 1073741824+2147483648 {
		t.Errorf("networkIn = %v", totals["networkIn"])
	}
	// The quota picture belongs next to the traffic that consumed it.
	if _, ok := payload["bandwidth"]; !ok {
		t.Error("usage does not carry the bandwidth quota")
	}
}

func TestAuditDecodesTheRequestorIP(t *testing.T) {
	h := newHarness(t, apiBodies)
	payload := h.runJSON(t, "audit")

	entries := payload["entries"].([]any)
	if len(entries) != 2 {
		t.Fatalf("got %d entries", len(entries))
	}
	// Newest first.
	if entries[0].(map[string]any)["summary"] != "Snapshot created" {
		t.Errorf("entries are not newest-first: %v", entries[0])
	}
	if ip := entries[0].(map[string]any)["requestorIp"]; ip != "1.2.3.5" {
		t.Errorf("requestorIp = %v, want the integer decoded", ip)
	}
}

func TestAbuseFlagsWhatTheAPICanResolve(t *testing.T) {
	h := newHarness(t, apiBodies)
	if err := h.run("abuse", "ls"); err != nil {
		t.Fatal(err)
	}
	out := h.stdout.String()
	// A soft case must name the command that clears it.
	if !strings.Contains(out, "bwg abuse resolve 14") {
		t.Errorf("the resolvable violation does not name its fix:\n%s", out)
	}
	if !strings.Contains(out, "bwg abuse unsuspend 11851") {
		t.Errorf("the resolvable suspension does not name its fix:\n%s", out)
	}
}

func TestAbuseRefusesCasesTheAPICannotClear(t *testing.T) {
	bodies := map[string]string{}
	for k, v := range apiBodies {
		bodies[k] = v
	}
	// is_soft 0: support ticket only.
	bodies["getPolicyViolations"] = `{"error":0,"total_abuse_points":100,"max_abuse_points":1500,
	  "policy_violations":[{"record_id":14,"flag":"copyright","is_soft":0,"abuse_points":100}]}`

	h := newHarness(t, bodies)
	err := h.run("abuse", "resolve", "14", "--yes")
	if err == nil {
		t.Fatal("a hard case was submitted to the API anyway")
	}
	if !strings.Contains(err.Error(), "support ticket") {
		t.Errorf("the error does not say what to do instead: %v", err)
	}
}

func TestMigrateShowsTheMultiplierChange(t *testing.T) {
	h := newHarness(t, apiBodies)
	if err := h.run("migrate", "ls"); err != nil {
		t.Fatal(err)
	}
	out := h.stdout.String()
	// A move that triples the effective allowance is the whole reason
	// to read this table.
	if !strings.Contains(out, "3x") || !strings.Contains(out, "1x") {
		t.Errorf("the bandwidth multipliers are missing:\n%s", out)
	}
	if !strings.Contains(out, "current") {
		t.Errorf("the current location is not marked:\n%s", out)
	}
}

func TestOSReinstallSurfacesTheRootPasswordImmediately(t *testing.T) {
	h := newHarness(t, apiBodies)
	if err := h.run("os", "reinstall", "ubuntu-24.04", "--yes"); err != nil {
		t.Fatal(err)
	}
	out := h.stdout.String()
	// It is shown once and never retrievable, so it must be loud.
	if !strings.Contains(out, "newpass123") {
		t.Errorf("the root password was not printed:\n%s", out)
	}
	if !strings.Contains(out, "not show it again") {
		t.Errorf("nothing warns that the password is unrecoverable:\n%s", out)
	}
}

func TestOSReinstallResolvesAPartialTemplate(t *testing.T) {
	h := newHarness(t, apiBodies)
	payload := h.runJSON(t, "os", "reinstall", "ubuntu", "--yes")
	if payload["os"] != "ubuntu-24.04-x86_64" {
		t.Errorf("os = %v, want the substring resolved", payload["os"])
	}

	// An unknown template must list the real ones.
	err := h.run("os", "reinstall", "plan9", "--yes")
	if err == nil || !strings.Contains(err.Error(), "debian-12-x86_64") {
		t.Errorf("the error does not list the available templates: %v", err)
	}
}

func TestPTRRejectsAnAddressTheServerDoesNotHave(t *testing.T) {
	h := newHarness(t, apiBodies)
	err := h.run("net", "ptr", "198.51.100.99", "evil.example.com", "--yes")
	if err == nil {
		t.Fatal("rDNS was set for an address the VPS does not own")
	}
	if !strings.Contains(err.Error(), "203.0.113.10") {
		t.Errorf("the error does not list the real addresses: %v", err)
	}
}

func TestNotifySetReportsIgnoredIDs(t *testing.T) {
	bodies := map[string]string{}
	for k, v := range apiBodies {
		bodies[k] = v
	}
	// KiwiVM silently drops unknown IDs; bwg has to notice.
	bodies["kiwivm/setNotificationPreferences"] = `{"error":0,
	  "submitted_email_preferences":[],"updated_email_preferences":[],
	  "friendly_descriptions":[]}`

	h := newHarness(t, bodies)
	if err := h.run("notify", "set", "not_a_real_pref=on", "--yes"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(h.stdout.String(), "ignored") {
		t.Errorf("a silently-dropped preference was reported as success:\n%s", h.stdout)
	}
}

func TestServerCheckFailsLoudlyOnBadCredentials(t *testing.T) {
	h := newHarness(t, map[string]string{
		"getServiceInfo": `{"error":700005,"message":"Authentication failure"}`,
	})
	err := h.run("server", "check")
	if err == nil {
		t.Fatal("server check passed with credentials the API rejected")
	}
	if CodeFor(err) != ExitAuth {
		t.Errorf("exit code = %d, want auth", CodeFor(err))
	}
}

func TestServerSetAndDefault(t *testing.T) {
	h := newHarness(t, apiBodies)

	if err := h.run("server", "set", "tokyo", "--ssh-port", "2222", "--note", "primary"); err != nil {
		t.Fatal(err)
	}
	payload := h.runJSON(t, "server", "show", "tokyo")
	srv := payload["server"].(map[string]any)
	if srv["sshPort"].(float64) != 2222 || srv["note"] != "primary" {
		t.Errorf("server set did not persist: %v", srv)
	}

	if err := h.run("server", "default", "osaka"); err != nil {
		t.Fatal(err)
	}
	payload = h.runJSON(t, "server", "default")
	if payload["default"] != "osaka" {
		t.Errorf("default = %v", payload["default"])
	}
}

func TestServerRemoveNeedsConfirmationOffTerminal(t *testing.T) {
	h := newHarness(t, apiBodies)
	// Not interactive under go test, so it proceeds without prompting —
	// this is local config, not a server, and re-adding is trivial.
	if err := h.run("server", "rm", "osaka"); err != nil {
		t.Fatalf("removing a server failed: %v", err)
	}
	if err := h.run("server", "ls"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(h.stdout.String(), "osaka") {
		t.Errorf("osaka is still listed:\n%s", h.stdout)
	}

	if err := h.run("server", "rm", "nagoya"); err == nil {
		t.Error("removing an unknown server succeeded")
	}
}

// The deadline on a policy violation is the number that decides
// whether to act now or tonight, so it has to be a real countdown.
func TestPolicyViolationShowsTimeRemaining(t *testing.T) {
	bodies := map[string]string{}
	for k, v := range apiBodies {
		bodies[k] = v
	}
	deadline := time.Now().Add(36 * time.Hour).Unix()
	bodies["getPolicyViolations"] = fmt.Sprintf(
		`{"error":0,"total_abuse_points":100,"max_abuse_points":1500,
		  "policy_violations":[{"record_id":14,"timestamp":%d,"suspend_at":%d,
		    "flag":"copyright","is_soft":1,"abuse_points":100}]}`,
		time.Now().Add(-time.Hour).Unix(), deadline)

	h := newHarness(t, bodies)
	if err := h.run("abuse", "ls"); err != nil {
		t.Fatal(err)
	}
	out := h.stdout.String()
	if !strings.Contains(out, "Suspends at") {
		t.Fatalf("the deadline is missing:\n%s", out)
	}
	if strings.Contains(out, "(in 0s)") {
		t.Errorf("the countdown is stuck at zero — the arithmetic is wrong:\n%s", out)
	}
	// Duration truncates rather than rounds, so 36h reads as "1d 11h".
	if !strings.Contains(out, "in 1d 1") {
		t.Errorf("expected roughly a day and a half remaining:\n%s", out)
	}
}

// -- input handling ---------------------------------------------------

// The one mistake worth catching here is pasting a private key into a
// command that ships it to a third party, so the check runs before any
// network call and says what it expected.
func TestKeysSetRejectsAnythingThatIsNotAPublicKey(t *testing.T) {
	h := newHarness(t, apiBodies)
	dir := t.TempDir()

	private := filepath.Join(dir, "id_ed25519")
	if err := os.WriteFile(private, []byte(
		"-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXktdjEA\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := h.run("keys", "set", private, "--yes")
	if err == nil {
		t.Fatal("a private key was accepted")
	}
	if !strings.Contains(err.Error(), "private key") {
		t.Errorf("the error does not name the likely mistake: %v", err)
	}
	for _, req := range h.seen() {
		if strings.Contains(req, "updateSshKeys") {
			t.Errorf("a rejected key still reached the API: %v", h.seen())
		}
	}

	// A real key file, comments and blank lines included, goes through.
	pub := filepath.Join(dir, "id_ed25519.pub")
	if err := os.WriteFile(pub, []byte(
		"# my laptop\n\nssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAA eric@laptop\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := h.run("keys", "set", pub, "--yes"); err != nil {
		t.Fatalf("a valid public key was rejected: %v", err)
	}
	if !strings.Contains(h.stdout.String(), "1 key stored") {
		t.Errorf("the confirmation does not count the keys: %s", h.stdout)
	}

	// --clear and key files are contradictory instructions.
	if err := h.run("keys", "set", "--clear", pub, "--yes"); err == nil {
		t.Error("--clear with key files was accepted")
	}
	if err := h.run("keys", "set", "--yes"); err == nil {
		t.Error("keys set with nothing to set was accepted")
	}
}

func TestLooksLikePublicKey(t *testing.T) {
	for _, ok := range []string{
		"ssh-rsa AAAAB3Nza user@host",
		"ssh-ed25519 AAAAC3Nza",
		"ecdsa-sha2-nistp256 AAAAE2Vj",
		"sk-ssh-ed25519@openssh.com AAAAG",
	} {
		if !looksLikePublicKey(ok) {
			t.Errorf("%q was rejected", ok)
		}
	}
	for _, bad := range []string{
		"", "-----BEGIN OPENSSH PRIVATE KEY-----", "not a key at all",
		" ssh-rsa AAAA", // leading space: a trimmed line never has one
	} {
		if looksLikePublicKey(bad) {
			t.Errorf("%q was accepted", bad)
		}
	}
}

// `bwg run` takes its script from an argument, a file or stdin, and
// has to refuse rather than send an empty script to a root shell.
func TestRunScriptSources(t *testing.T) {
	h := newHarness(t, apiBodies)
	dir := t.TempDir()
	script := filepath.Join(dir, "bootstrap.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\napt-get update\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	if got, err := readScript(h.app, []string{"echo hi"}, ""); err != nil || got != "echo hi" {
		t.Errorf("argument form: got %q, %v", got, err)
	}
	if got, err := readScript(h.app, nil, script); err != nil || !strings.Contains(got, "apt-get") {
		t.Errorf("--file form: got %q, %v", got, err)
	}
	if _, err := readScript(h.app, []string{"echo hi"}, script); err == nil {
		t.Error("an argument and --file together were accepted")
	}
	if _, err := readScript(h.app, nil, filepath.Join(dir, "missing.sh")); err == nil {
		t.Error("a missing --file was accepted")
	}

	h.app.In = strings.NewReader("uptime\n")
	if got, err := readScript(h.app, nil, ""); err != nil || strings.TrimSpace(got) != "uptime" {
		t.Errorf("stdin form: got %q, %v", got, err)
	}
	h.app.In = strings.NewReader("   \n\n")
	if _, err := readScript(h.app, nil, ""); err == nil {
		t.Error("an empty script on stdin was accepted")
	}
}

// The consent card shows what the script does, not its shebang.
func TestFirstScriptLineSkipsCommentsAndShebangs(t *testing.T) {
	cases := map[string]string{
		"#!/bin/sh\n# set up\napt-get update\n": "apt-get update",
		"apt-get update":                        "apt-get update",
		"\n\n  uptime  \n":                      "uptime",
		// Nothing but comments: show them rather than nothing at all.
		"# only comments\n": "# only comments",
	}
	for in, want := range cases {
		if got := firstScriptLine(in); got != want {
			t.Errorf("firstScriptLine(%q) = %q, want %q", in, got, want)
		}
	}
}

// Backup tokens are 64 opaque characters, so a prefix has to work —
// and an ambiguous prefix has to say so rather than pick one.
func TestResolveBackupByPrefix(t *testing.T) {
	list := &kiwivm.BackupList{Backups: map[string]kiwivm.Backup{
		"aaaa1111": {Token: "aaaa1111", OS: "debian-12", Timestamp: kiwivm.Int(1754524800)},
		"aaaa2222": {Token: "aaaa2222", OS: "debian-12", Timestamp: kiwivm.Int(1754611200)},
		"bbbb3333": {Token: "bbbb3333", OS: "debian-12", Timestamp: kiwivm.Int(1754697600)},
	}}

	got, err := resolveBackup(list, "bbbb3333")
	if err != nil || got.Token != "bbbb3333" {
		t.Errorf("exact token: got %v, %v", got.Token, err)
	}
	got, err = resolveBackup(list, "bbbb")
	if err != nil || got.Token != "bbbb3333" {
		t.Errorf("unique prefix: got %v, %v", got.Token, err)
	}
	if _, err := resolveBackup(list, "aaaa"); err == nil {
		t.Error("an ambiguous prefix resolved to one backup")
	} else if !strings.Contains(err.Error(), "longer prefix") {
		t.Errorf("the ambiguity error does not say what to do: %v", err)
	}
	if _, err := resolveBackup(list, "zzzz"); err == nil {
		t.Error("an unknown prefix resolved")
	} else if !strings.Contains(err.Error(), "aaaa1111") {
		t.Errorf("the error does not list what is available: %v", err)
	}
	if _, err := resolveBackup(&kiwivm.BackupList{}, "aaaa"); err == nil {
		t.Error("a prefix resolved against an empty backup list")
	}
}

// The console screenshot is the only way to see a box that will not
// boot, so both the success and the "this hypervisor has no console"
// paths have to be right.
func TestStatusScreenshot(t *testing.T) {
	// A one-pixel PNG, base64 as KiwiVM returns it.
	const png = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAAAAAA6fptVAAAACklEQVR4nGMAAQAABQAB" +
		"h6FO1AAAAABJRU5ErkJggg=="
	live := strings.Replace(apiBodies["getLiveServiceInfo"], `"ve_status":"Running"`,
		`"ve_status":"Running","screendump_png_base64":"`+png+`"`, 1)

	h := newHarness(t, map[string]string{"getLiveServiceInfo": live})
	shot := filepath.Join(t.TempDir(), "console.png")
	if err := h.run("status", "--screenshot", shot); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(shot)
	if err != nil {
		t.Fatalf("no screenshot written: %v", err)
	}
	if !bytes.HasPrefix(data, []byte("\x89PNG")) {
		t.Errorf("the file is not a PNG: %q", data[:min(8, len(data))])
	}
	// The path goes to stderr: it is commentary, not data.
	if !strings.Contains(h.stderr.String(), shot) {
		t.Errorf("the screenshot path was not reported: %q", h.stderr)
	}

	// OpenVZ has no VGA console. Saying so beats writing an empty file.
	h = newHarness(t, apiBodies)
	err = h.run("status", "--screenshot", filepath.Join(t.TempDir(), "none.png"))
	if err == nil {
		t.Fatal("a VPS with no screenshot wrote one anyway")
	}
	if !strings.Contains(err.Error(), "OpenVZ") {
		t.Errorf("the error does not explain when this happens: %v", err)
	}
}

// Completions are a shipped surface: `make completions` runs these and
// a panic here would break the release.
func TestCompletionScriptsGenerate(t *testing.T) {
	for _, shell := range []string{"bash", "zsh", "fish", "powershell"} {
		h := newHarness(t, nil)
		if err := h.run("completion", shell); err != nil {
			t.Fatalf("completion %s: %v", shell, err)
		}
		if !strings.Contains(h.stdout.String(), "bwg") {
			t.Errorf("the %s completion does not mention bwg", shell)
		}
	}
	h := newHarness(t, nil)
	if err := h.run("completion", "tcsh"); err == nil {
		t.Error("an unsupported shell was accepted")
	}
}
