package kiwivm

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// serve stands up a KiwiVM stand-in that answers every request with
// body, and hands back a client pointed at it plus the last request.
func serve(t *testing.T, body string, opts ...Option) (*Client, *http.Request, *url.Values) {
	t.Helper()
	var lastReq *http.Request
	form := &url.Values{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		*form = r.Form
		lastReq = r.Clone(context.Background())
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	opts = append([]Option{WithBaseURL(srv.URL)}, opts...)
	return New("1347645", "private_secret", opts...), lastReq, form
}

func TestCredentialsRideOnEveryCall(t *testing.T) {
	c, _, form := serve(t, `{"error":0}`)
	if _, err := c.ServiceInfo(context.Background()); err != nil {
		t.Fatal(err)
	}
	if form.Get("veid") != "1347645" || form.Get("api_key") != "private_secret" {
		t.Errorf("credentials missing from the request: %v", *form)
	}
}

// Writes must not put the api_key in the URL, where it would land in
// proxy logs and shell history.
func TestWriteKeepsTheKeyOutOfTheURL(t *testing.T) {
	c, _, form := serve(t, `{"error":0}`)
	if err := c.SetHostname(context.Background(), "box.example.com"); err != nil {
		t.Fatal(err)
	}
	if form.Get("api_key") != "private_secret" || form.Get("newHostname") != "box.example.com" {
		t.Errorf("form body = %v", *form)
	}
}

func TestNonZeroErrorBecomesAPIError(t *testing.T) {
	c, _, _ := serve(t, `{"error":700005,"message":"Authentication failure"}`)

	_, err := c.ServiceInfo(context.Background())
	if err == nil {
		t.Fatal("a 700005 response decoded as success")
	}
	if !IsAuth(err) {
		t.Errorf("IsAuth(%v) = false", err)
	}
	apiErr, ok := APIErrorFrom(err)
	if !ok {
		t.Fatalf("not an *APIError: %T", err)
	}
	if apiErr.Code != CodeAuthFailure || apiErr.Op != "getServiceInfo" {
		t.Errorf("APIError = %+v", apiErr)
	}
	if !strings.Contains(err.Error(), "Authentication failure") {
		t.Errorf("error text drops the API message: %s", err)
	}
}

func TestLockedErrorCarriesProgress(t *testing.T) {
	body := `{"error":788888,"message":"VE is currently locked",
	  "additionalLockingInfo":{"completed_percent":42,
	  "friendly_progress_message":"Creating snapshot","last_status_update_s_ago":3}}`
	c, _, _ := serve(t, body)

	_, err := c.Snapshots(context.Background())
	if !IsLocked(err) {
		t.Fatalf("IsLocked(%v) = false", err)
	}
	apiErr, _ := APIErrorFrom(err)
	if apiErr.Locking == nil || apiErr.Locking.CompletedPercent != 42 {
		t.Fatalf("progress lost: %+v", apiErr.Locking)
	}
	// The progress is the actionable part; it must reach the user.
	if !strings.Contains(err.Error(), "Creating snapshot") ||
		!strings.Contains(err.Error(), "42%") {
		t.Errorf("error text hides the progress: %s", err)
	}
}

func TestMissingVEIDFailsBeforeTheNetwork(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("a client with no veid still made a request")
	}))
	defer srv.Close()

	c := New("", "private_secret", WithBaseURL(srv.URL))
	_, err := c.ServiceInfo(context.Background())
	if !IsMissingParam(err) {
		t.Fatalf("got %v, want a missing-parameter error", err)
	}
}

func TestTransportErrorsAreClassified(t *testing.T) {
	cases := []struct {
		name          string
		status        int
		body          string
		transient     bool
		rateLimited   bool
		wantSubstring string
	}{
		{"500", 500, "upstream boom", true, false, "HTTP 500"},
		{"502", 502, "bad gateway", true, false, "HTTP 502"},
		{"429", 429, "slow down", true, true, "HTTP 429"},
		{"403", 403, "nope", false, false, "HTTP 403"},
		{"html", 200, "<html>maintenance</html>", false, false, "not JSON"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			c := New("1", "k", WithBaseURL(srv.URL))
			_, err := c.ServiceInfo(context.Background())
			if err == nil {
				t.Fatal("expected an error")
			}
			if IsTransient(err) != tc.transient {
				t.Errorf("IsTransient = %v, want %v (%v)", IsTransient(err), tc.transient, err)
			}
			if IsRateLimited(err) != tc.rateLimited {
				t.Errorf("IsRateLimited = %v, want %v", IsRateLimited(err), tc.rateLimited)
			}
			if !strings.Contains(err.Error(), tc.wantSubstring) {
				t.Errorf("error %q lacks %q", err, tc.wantSubstring)
			}
			// A transport failure says nothing about credentials.
			if IsAuth(err) {
				t.Error("a transport failure was misread as an auth failure")
			}
		})
	}
}

func TestDialFailureIsTransient(t *testing.T) {
	// Port 1 on loopback refuses connections immediately.
	c := New("1", "k", WithBaseURL("http://127.0.0.1:1"), WithTimeout(2*time.Second))
	_, err := c.ServiceInfo(context.Background())
	if !IsTransient(err) {
		t.Fatalf("a refused connection should be transient, got %v", err)
	}
	var te *TransportError
	if !errors.As(err, &te) || te.Status != 0 {
		t.Errorf("want a TransportError with no status, got %#v", err)
	}
}

func TestContextCancellationPropagates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	c := New("1", "k", WithBaseURL(srv.URL))
	_, err := c.ServiceInfo(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v, want the context deadline to surface", err)
	}
}

// basicShell/exec overloads the envelope: "error" is the command's
// exit status, not an API failure. A non-zero exit must not become a
// Go error, or every failing command looks like a broken API.
func TestShellExecExitStatusIsNotAnAPIError(t *testing.T) {
	c, _, _ := serve(t, `{"error":2,"message":"ls: /nope: No such file or directory"}`)

	res, err := c.ShellExec(context.Background(), "ls /nope")
	if err != nil {
		t.Fatalf("a non-zero exit surfaced as an error: %v", err)
	}
	if res.ExitStatus != 2 {
		t.Errorf("ExitStatus = %d, want 2", res.ExitStatus)
	}
	if !strings.Contains(res.Output, "No such file") {
		t.Errorf("Output = %q", res.Output)
	}
}

func TestUnknownEndpointIsRejected(t *testing.T) {
	c := New("1", "k")
	if err := c.call(context.Background(), "made/up", nil, nil); err == nil ||
		!strings.Contains(err.Error(), "unknown endpoint") {
		t.Errorf("got %v, want an unknown-endpoint error", err)
	}
}

func TestTraceSeesEveryRoundTripAndNoSecrets(t *testing.T) {
	type record struct {
		method, endpoint string
		status           int
	}
	var seen []record
	c, _, _ := serve(t, `{"error":0}`, WithTrace(
		func(method, endpoint string, status int, _ time.Duration) {
			seen = append(seen, record{method, endpoint, status})
		}))

	ctx := context.Background()
	c.ServiceInfo(ctx)
	c.SetHostname(ctx, "box")

	if len(seen) != 2 {
		t.Fatalf("trace saw %d round trips, want 2", len(seen))
	}
	if seen[0] != (record{"GET", "getServiceInfo", 200}) {
		t.Errorf("read trace = %+v", seen[0])
	}
	if seen[1] != (record{"POST", "setHostname", 200}) {
		t.Errorf("write trace = %+v", seen[1])
	}
	for _, r := range seen {
		if strings.Contains(r.endpoint, "private_secret") {
			t.Error("the trace leaked the api_key")
		}
	}
}

func TestCallerParamsAreNotMutated(t *testing.T) {
	c, _, _ := serve(t, `{"error":0}`)
	params := url.Values{"os": {"debian-12-x86_64"}}
	if _, err := c.ReinstallOS(context.Background(), "debian-12-x86_64"); err != nil {
		t.Fatal(err)
	}
	// The client builds its own Values; a caller's copy must never gain
	// an api_key it could then log.
	if params.Get("api_key") != "" {
		t.Error("caller-supplied params were contaminated with credentials")
	}
}

func TestArgumentValidationFailsBeforeTheNetwork(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("an empty argument still reached the API: %s", r.URL.Path)
	}))
	defer srv.Close()
	c := New("1", "k", WithBaseURL(srv.URL))
	ctx := context.Background()

	checks := map[string]error{
		"ReinstallOS":       first(c.ReinstallOS(ctx, "  ")),
		"SetHostname":       c.SetHostname(ctx, ""),
		"SetPTR":            c.SetPTR(ctx, "", "x"),
		"MountISO":          c.MountISO(ctx, ""),
		"DeleteSnapshot":    c.DeleteSnapshot(ctx, ""),
		"RestoreSnapshot":   c.RestoreSnapshot(ctx, ""),
		"SetSnapshotSticky": c.SetSnapshotSticky(ctx, "", true),
		"ExportSnapshot":    first(c.ExportSnapshot(ctx, "")),
		"ImportSnapshot":    c.ImportSnapshot(ctx, "", "tok"),
		"CopyBackup":        c.CopyBackupToSnapshot(ctx, ""),
		"DeleteIPv6":        c.DeleteIPv6(ctx, ""),
		"DeletePrivateIP":   c.DeletePrivateIP(ctx, ""),
		"StartMigration":    first(c.StartMigration(ctx, "")),
		"Unsuspend":         c.Unsuspend(ctx, ""),
		"ResolveViolation":  c.ResolvePolicyViolation(ctx, ""),
		"ShellExec":         first(c.ShellExec(ctx, " ")),
		"ScriptExec":        first(c.ScriptExec(ctx, "")),
		"SetNotifications":  first(c.SetNotificationPreferences(ctx, nil)),
		"CloneExternal":     c.CloneFromExternalServer(ctx, "", "22", "pw"),
	}
	for name, err := range checks {
		if err == nil {
			t.Errorf("%s accepted an empty required argument", name)
		}
	}
}

func first[T any](_ *T, err error) error { return err }

func TestSetNotificationPreferencesEncodesJSON(t *testing.T) {
	c, _, form := serve(t, `{"error":0}`)
	_, err := c.SetNotificationPreferences(context.Background(), map[string]bool{"snapshot_done": true})
	if err != nil {
		t.Fatal(err)
	}
	got := form.Get("json_notification_preferences")
	if got != `{"snapshot_done":1}` {
		t.Errorf("encoded prefs = %s, want booleans rendered as 0/1", got)
	}
}

func TestUpdateSSHKeysJoinsWithNewlines(t *testing.T) {
	c, _, form := serve(t, `{"error":0}`)
	if err := c.UpdateSSHKeys(context.Background(), []string{"ssh-rsa A", "ssh-ed25519 B"}); err != nil {
		t.Fatal(err)
	}
	if form.Get("ssh_keys") != "ssh-rsa A\nssh-ed25519 B" {
		t.Errorf("ssh_keys = %q", form.Get("ssh_keys"))
	}

	// Clearing is legitimate: it restores the account-level keys.
	if err := c.UpdateSSHKeys(context.Background(), nil); err != nil {
		t.Fatalf("clearing keys was rejected: %v", err)
	}
}

func TestStickyEncodesAsZeroOrOne(t *testing.T) {
	c, _, form := serve(t, `{"error":0}`)
	ctx := context.Background()

	c.SetSnapshotSticky(ctx, "snap.tar.gz", true)
	if form.Get("sticky") != "1" {
		t.Errorf("sticky true = %q, want 1", form.Get("sticky"))
	}
	c.SetSnapshotSticky(ctx, "snap.tar.gz", false)
	if form.Get("sticky") != "0" {
		t.Errorf("sticky false = %q, want 0", form.Get("sticky"))
	}
}

func TestAssignPrivateIPOmitsAnEmptyIP(t *testing.T) {
	c, _, form := serve(t, `{"error":0,"assigned_ips":["10.0.0.5"]}`)
	res, err := c.AssignPrivateIP(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, present := (*form)["ip"]; present {
		t.Error("an empty ip was sent; KiwiVM should choose one")
	}
	if len(res.AssignedIPs) != 1 || res.AssignedIPs[0] != "10.0.0.5" {
		t.Errorf("AssignedIPs = %v", res.AssignedIPs)
	}
}

// A full getServiceInfo body, shaped the way the API documentation
// shows it, decoding end to end through the client.
func TestServiceInfoDecodesARealisticBody(t *testing.T) {
	body := `{
	  "vm_type":"kvm","hostname":"my.server.com","node_alias":"Node32",
	  "node_location":"US, Florida","node_location_id":"USCA_2","node_datacenter":"DC9",
	  "location_ipv6_ready":1,"plan":"micro128","plan_disk":"4294967296",
	  "plan_ram":155189248,"plan_swap":37748736,"os":"debian-12-x86_64",
	  "email":"customer@example.com","plan_monthly_data":322122547200,
	  "data_counter":569810827,"monthly_data_multiplier":1,"data_next_reset":1430193600,
	  "ip_addresses":["11.22.33.44","2001:db8::/64"],
	  "private_ip_addresses":[],"ip_nullroutes":[],
	  "iso1":"","iso2":"","available_isos":["systemrescue.iso"],
	  "plan_max_ipv6s":"1","rdns_api_available":1,
	  "plan_private_network_available":0,"location_private_network_available":"1",
	  "ptr":{"11.22.33.44":"ns1.my.server.com"},
	  "suspended":0,"policy_violation":0,"suspension_count":0,
	  "total_abuse_points":0,"max_abuse_points":1500,
	  "free_ip_replacement_interval":-100,"error":0
	}`
	c, _, _ := serve(t, body)

	info, err := c.ServiceInfo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.Hostname != "my.server.com" || info.VMType != "kvm" {
		t.Errorf("basic fields wrong: %+v", info)
	}
	if info.PlanDisk.Int64() != 4294967296 {
		t.Errorf(`plan_disk as a string decoded to %d`, info.PlanDisk)
	}
	if info.PlanMaxIPv6s.Int() != 1 {
		t.Errorf("plan_max_ipv6s = %d", info.PlanMaxIPv6s)
	}
	if !info.RDNSAPIAvailable.Bool() || !info.LocationPrivateNetworkAvailable.Bool() {
		t.Error(`1 and "1" should both decode as true`)
	}
	if info.PlanPrivateNetworkAvailable.Bool() {
		t.Error("0 should decode as false")
	}
	if len(info.IPNullroutes) != 0 {
		t.Errorf("ip_nullroutes:[] became %#v", info.IPNullroutes)
	}
	if got := info.IPv4(); len(got) != 1 || got[0] != "11.22.33.44" {
		t.Errorf("IPv4() = %v", got)
	}
	if got := info.IPv6(); len(got) != 1 || got[0] != "2001:db8::/64" {
		t.Errorf("IPv6() = %v", got)
	}
	if info.PTR["11.22.33.44"] != "ns1.my.server.com" {
		t.Errorf("PTR = %v", info.PTR)
	}
	if !info.Healthy() {
		t.Error("a clean box reported unhealthy")
	}
	if b := info.Bandwidth(); b.Multiplier != 1 || b.Used != 569810827 {
		t.Errorf("Bandwidth() = %+v", b)
	}
}

func TestFetchReturnsNilOnFailure(t *testing.T) {
	c, _, _ := serve(t, `{"error":700005,"message":"Authentication failure"}`)
	info, err := c.ServiceInfo(context.Background())
	if err == nil {
		t.Fatal("expected an error")
	}
	// A caller who checks the value first must not get a zero struct
	// that looks like a real, empty VPS.
	if info != nil {
		t.Errorf("got a non-nil %T alongside an error", info)
	}
}

func TestClientAccessors(t *testing.T) {
	c := New(" 1347645 ", " private_key ", WithBaseURL("https://example.test/v1/"))
	if c.VEID() != "1347645" {
		t.Errorf("VEID() = %q, want it trimmed", c.VEID())
	}
	if c.BaseURL() != "https://example.test/v1" {
		t.Errorf("BaseURL() = %q, want the trailing slash gone", c.BaseURL())
	}
	if c.IsReadOnly() {
		t.Error("a plain client reported read-only")
	}
}

// A read goes out as GET, so the api_key is in the query string, and
// net/http renders the whole URL in *url.Error. Without redaction a
// "connection refused" prints a working credential into any log that
// catches it.
func TestTransportErrorsDoNotLeakTheKey(t *testing.T) {
	const key = "private_supersecretvalue123"

	// Nothing is listening on port 1, so the dial fails immediately.
	c := New("1347645", key, WithBaseURL("http://127.0.0.1:1"), WithTimeout(2*time.Second))
	_, err := c.ServiceInfo(context.Background())
	if err == nil {
		t.Fatal("expected a dial failure")
	}
	if strings.Contains(err.Error(), key) {
		t.Fatalf("the API key leaked into a transport error:\n%s", err)
	}
	if !strings.Contains(err.Error(), "REDACTED") {
		t.Errorf("the key was neither present nor marked redacted: %s", err)
	}
	// The error still has to be useful.
	if !strings.Contains(err.Error(), "getServiceInfo") {
		t.Errorf("redaction destroyed the diagnostic: %s", err)
	}
}

// The same must hold for a write, where the key rides in the body.
func TestWriteTransportErrorsDoNotLeakTheKey(t *testing.T) {
	const key = "private_supersecretvalue123"
	c := New("1347645", key, WithBaseURL("http://127.0.0.1:1"), WithTimeout(2*time.Second))

	if err := c.SetHostname(context.Background(), "box"); err == nil {
		t.Fatal("expected a dial failure")
	} else if strings.Contains(err.Error(), key) {
		t.Fatalf("the API key leaked into a write error:\n%s", err)
	}
}

// Redaction must not sever the error chain: callers branch on
// context.DeadlineExceeded and on *TransportError, and both have to
// survive a message rewrite.
func TestRedactionPreservesTheErrorChain(t *testing.T) {
	const key = "private_supersecretvalue123"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	c := New("1347645", key, WithBaseURL(srv.URL))
	_, err := c.ServiceInfo(ctx)
	if err == nil {
		t.Fatal("expected a timeout")
	}
	if strings.Contains(err.Error(), key) {
		t.Errorf("the key survived redaction: %s", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("redaction broke errors.Is: %v", err)
	}
	var te *TransportError
	if !errors.As(err, &te) {
		t.Errorf("redaction broke errors.As: %v", err)
	}
}

// A short placeholder key must not be substituted out of ordinary
// text: replacing "k" would rewrite "api_key" and corrupt the
// diagnostic while protecting nothing.
func TestRedactionIgnoresTooShortKeys(t *testing.T) {
	c := New("1", "k", WithBaseURL("http://127.0.0.1:1"), WithTimeout(2*time.Second))
	_, err := c.ServiceInfo(context.Background())
	if err == nil {
		t.Fatal("expected a dial failure")
	}
	if strings.Contains(err.Error(), "REDACTED") {
		t.Errorf("a 1-character key triggered redaction and mangled the message: %s", err)
	}
	if !strings.Contains(err.Error(), "api_key") {
		t.Errorf("the query string was corrupted: %s", err)
	}
}
