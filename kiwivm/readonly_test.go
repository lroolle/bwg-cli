package kiwivm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// callableMethods returns every exported *Client method whose first
// argument is a context, i.e. every method that reaches the API.
func callableMethods(t *testing.T) []reflect.Method {
	t.Helper()
	ctxType := reflect.TypeOf((*context.Context)(nil)).Elem()
	typ := reflect.TypeOf(&Client{})

	var out []reflect.Method
	for i := range typ.NumMethod() {
		m := typ.Method(i)
		if m.Type.NumIn() < 2 || m.Type.In(1) != ctxType {
			continue // VEID, IsReadOnly, BaseURL, Can
		}
		// Raw takes its endpoint as data rather than binding one, so
		// there is no static mapping to discover. TestRawIsGated covers
		// it directly.
		if m.Name == "Raw" {
			continue
		}
		out = append(out, m)
	}
	if len(out) == 0 {
		t.Fatal("no API methods discovered — reflection is broken, not the client")
	}
	return out
}

// plausibleArgs builds arguments a method will accept past its own
// input validation, so the test exercises the gate rather than the
// argument checks.
func plausibleArgs(m reflect.Method, c *Client) []reflect.Value {
	args := []reflect.Value{reflect.ValueOf(c), reflect.ValueOf(context.Background())}
	for i := 2; i < m.Type.NumIn(); i++ {
		in := m.Type.In(i)
		switch in.Kind() {
		case reflect.String:
			args = append(args, reflect.ValueOf("probe"))
		case reflect.Slice:
			s := reflect.MakeSlice(in, 1, 1)
			s.Index(0).Set(reflect.ValueOf("probe"))
			args = append(args, s)
		case reflect.Map:
			mp := reflect.MakeMap(in)
			mp.SetMapIndex(reflect.ValueOf("probe"), reflect.ValueOf(true))
			args = append(args, mp)
		case reflect.Bool:
			args = append(args, reflect.ValueOf(true))
		default:
			args = append(args, reflect.Zero(in))
		}
	}
	return args
}

func errFrom(results []reflect.Value) error {
	last := results[len(results)-1]
	if last.IsNil() {
		return nil
	}
	return last.Interface().(error)
}

// hit is what one method's request looked like on the wire.
type hit struct {
	Endpoint   string
	HTTPMethod string
}

// discoverEndpoints maps each client method to the endpoint it calls
// and the HTTP method it uses, by actually invoking it against a
// recording server. Deriving the map instead of hardcoding it means a
// newly added method is covered by the gate tests automatically.
func discoverEndpoints(t *testing.T) map[string]hit {
	t.Helper()
	out := map[string]hit{}

	var last hit
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		last = hit{
			Endpoint:   strings.TrimPrefix(r.URL.Path, "/"),
			HTTPMethod: r.Method,
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"error":0}`))
	}))
	defer srv.Close()

	c := New("1347645", "private_test", WithBaseURL(srv.URL))
	for _, m := range callableMethods(t) {
		last = hit{}
		results := m.Func.Call(plausibleArgs(m, c))
		if err := errFrom(results); err != nil {
			t.Fatalf("%s against a healthy server: %v", m.Name, err)
		}
		if last.Endpoint == "" {
			t.Fatalf("%s made no request", m.Name)
		}
		out[m.Name] = last
	}
	return out
}

// TestReadOnlyRefusesEveryMutation is the load-bearing safety test: a
// read-only client must refuse every write and destructive operation
// WITHOUT touching the network. The server fails the test if reached,
// so a gate that merely inspects the response would not pass.
func TestReadOnlyRefusesEveryMutation(t *testing.T) {
	endpoints := discoverEndpoints(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("read-only client reached the network: %s %s", r.Method, r.URL.Path)
		w.Write([]byte(`{"error":0}`))
	}))
	defer srv.Close()

	c := New("1347645", "private_test", WithBaseURL(srv.URL), ReadOnly())
	if !c.IsReadOnly() {
		t.Fatal("IsReadOnly() = false after ReadOnly()")
	}

	var checked int
	for _, m := range callableMethods(t) {
		ep := endpoints[m.Name].Endpoint
		op, ok := Ops[ep]
		if !ok {
			t.Fatalf("%s calls unregistered endpoint %q", m.Name, ep)
		}
		if op.Risk == RiskRead {
			continue
		}
		checked++

		err := errFrom(m.Func.Call(plausibleArgs(m, c)))
		if err == nil {
			t.Errorf("%s (%s %s) succeeded on a read-only client", m.Name, op.Risk, ep)
			continue
		}
		if !IsReadOnly(err) {
			t.Errorf("%s (%s): got %v, want a read-only refusal", m.Name, ep, err)
			continue
		}
		var roErr *ReadOnlyError
		if !asReadOnly(err, &roErr) || roErr.Op.Endpoint != ep {
			t.Errorf("%s: refusal names the wrong endpoint", m.Name)
		}
	}

	if checked == 0 {
		t.Fatal("no mutating methods were checked")
	}
	t.Logf("refused %d mutating operations without network access", checked)
}

// TestReadOnlyAllowsEveryRead is the other half: the gate must not
// block the operations it is supposed to allow.
func TestReadOnlyAllowsEveryRead(t *testing.T) {
	endpoints := discoverEndpoints(t)

	var served int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		served++
		w.Write([]byte(`{"error":0}`))
	}))
	defer srv.Close()

	c := New("1347645", "private_test", WithBaseURL(srv.URL), ReadOnly())
	for _, m := range callableMethods(t) {
		ep := endpoints[m.Name].Endpoint
		if Ops[ep].Risk != RiskRead {
			continue
		}
		if err := errFrom(m.Func.Call(plausibleArgs(m, c))); err != nil {
			t.Errorf("%s (read) blocked on a read-only client: %v", m.Name, err)
		}
	}
	if served == 0 {
		t.Fatal("no read reached the server")
	}
}

// TestReadsUseGETWritesUsePOST checks the credential-handling rule:
// anything that changes state goes out as POST form data, so the
// api_key never lands in a URL, a proxy log, or shell history.
func TestReadsUseGETWritesUsePOST(t *testing.T) {
	for name, hit := range discoverEndpoints(t) {
		op, ok := Ops[hit.Endpoint]
		if !ok {
			t.Fatalf("%s calls unregistered endpoint %q", name, hit.Endpoint)
		}
		want := http.MethodPost
		if op.Risk == RiskRead {
			want = http.MethodGet
		}
		if hit.HTTPMethod != want {
			t.Errorf("%s (%s %s): used %s, want %s",
				name, op.Risk, hit.Endpoint, hit.HTTPMethod, want)
		}
	}
}

// TestCanMatchesGate keeps Can honest: what it advertises must be what
// the gate actually permits. An agent builds its tool list from Can.
func TestCanMatchesGate(t *testing.T) {
	ro := New("1", "k", ReadOnly())
	rw := New("1", "k")

	for _, op := range ListOps() {
		gotRO, err := ro.Can(op.Endpoint)
		if want := op.Risk == RiskRead; gotRO != want {
			t.Errorf("read-only Can(%q) = %v, want %v", op.Endpoint, gotRO, want)
		}
		if !gotRO && !IsReadOnly(err) {
			t.Errorf("read-only Can(%q) refused with %v, want a read-only error", op.Endpoint, err)
		}
		if ok, err := rw.Can(op.Endpoint); !ok || err != nil {
			t.Errorf("read-write Can(%q) = %v, %v; want true, nil", op.Endpoint, ok, err)
		}
	}

	if ok, err := rw.Can("nope"); ok || err == nil {
		t.Error("Can on an unknown endpoint should fail")
	}
}

func asReadOnly(err error, target **ReadOnlyError) bool {
	e, ok := err.(*ReadOnlyError)
	if ok {
		*target = e
	}
	return ok
}

// TestRawIsGated covers the escape hatch that callableMethods skips.
// Raw is the one way to reach an endpoint without a typed method, so
// if the gate leaked anywhere, it would leak here.
func TestRawIsGated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"error":0,"hostname":"box"}`))
	}))
	defer srv.Close()

	ro := New("1347645", "private_test", WithBaseURL(srv.URL), ReadOnly())

	// Reads pass and return the body verbatim.
	body, err := ro.Raw(context.Background(), "getServiceInfo", nil)
	if err != nil {
		t.Fatalf("Raw on a read: %v", err)
	}
	if !strings.Contains(string(body), "hostname") {
		t.Errorf("Raw did not return the body: %s", body)
	}

	// Every non-read is refused.
	for _, op := range ListOps() {
		if op.Risk == RiskRead {
			continue
		}
		if _, err := ro.Raw(context.Background(), op.Endpoint, nil); !IsReadOnly(err) {
			t.Errorf("Raw(%s) on a read-only client: got %v, want a refusal", op.Endpoint, err)
		}
	}

	// An unregistered endpoint has no classification, so it cannot be
	// reached at all — even read-write.
	rw := New("1347645", "private_test", WithBaseURL(srv.URL))
	if _, err := rw.Raw(context.Background(), "made/up", nil); err == nil ||
		!strings.Contains(err.Error(), "unknown endpoint") {
		t.Errorf("Raw reached an unclassified endpoint: %v", err)
	}
}
