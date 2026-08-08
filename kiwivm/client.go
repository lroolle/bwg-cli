package kiwivm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultBaseURL is the public KiwiVM API root.
const DefaultBaseURL = "https://api.64clouds.com/v1"

// DefaultTimeout covers the slowest documented call, getLiveServiceInfo,
// which the API says may take up to 15 seconds.
const DefaultTimeout = 45 * time.Second

// maxBodyBytes caps how much of a response we will read. Snapshots of
// a VGA console (screendump_png_base64) and nullroute packet dumps are
// the large ones; 32 MiB is well clear of both and still bounds a
// misbehaving endpoint.
const maxBodyBytes = 32 << 20

// Client talks to the KiwiVM API for exactly one VPS. It is safe for
// concurrent use.
type Client struct {
	veid     string
	apiKey   string
	baseURL  string
	http     *http.Client
	readOnly bool
	ua       string
	trace    func(method, endpoint string, status int, dur time.Duration)
}

// Option configures a [Client].
type Option func(*Client)

// WithBaseURL overrides the API root. Used by tests and by anyone
// proxying the API.
func WithBaseURL(u string) Option {
	return func(c *Client) {
		if u != "" {
			c.baseURL = strings.TrimRight(u, "/")
		}
	}
}

// WithHTTPClient supplies the HTTP client to use.
func WithHTTPClient(h *http.Client) Option {
	return func(c *Client) {
		if h != nil {
			c.http = h
		}
	}
}

// WithTimeout sets the per-request timeout. Ignored when the caller
// also supplies their own client via [WithHTTPClient] after this.
func WithTimeout(d time.Duration) Option {
	return func(c *Client) {
		if d > 0 {
			c.http.Timeout = d
		}
	}
}

// WithUserAgent sets the User-Agent header.
func WithUserAgent(ua string) Option {
	return func(c *Client) {
		if ua != "" {
			c.ua = ua
		}
	}
}

// WithTrace installs a callback invoked after every HTTP round trip.
// It never receives credentials — only method, endpoint, status and
// duration — so it is safe to log verbatim.
func WithTrace(fn func(method, endpoint string, status int, dur time.Duration)) Option {
	return func(c *Client) { c.trace = fn }
}

// ReadOnly builds a client that refuses every operation classified
// above [RiskRead], returning *[ReadOnlyError] before any request is
// made. This is the strongest guarantee the package offers: it cannot
// be undone on an existing client, and it applies to the SDK, the CLI
// and the MCP server alike.
func ReadOnly() Option {
	return func(c *Client) { c.readOnly = true }
}

// New builds a client for one VPS. veid and apiKey come as a pair from
// the KiwiVM panel; the same api_key with the wrong veid authenticates
// as neither.
func New(veid, apiKey string, opts ...Option) *Client {
	c := &Client{
		veid:    strings.TrimSpace(veid),
		apiKey:  strings.TrimSpace(apiKey),
		baseURL: DefaultBaseURL,
		http:    &http.Client{Timeout: DefaultTimeout},
		ua:      "bwg-cli (+https://github.com/lroolle/bwg-cli)",
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// VEID returns the VPS ID this client is bound to.
func (c *Client) VEID() string { return c.veid }

// IsReadOnly reports whether this client refuses non-read operations.
func (c *Client) IsReadOnly() bool { return c.readOnly }

// BaseURL returns the API root in use.
func (c *Client) BaseURL() string { return c.baseURL }

// Can reports whether the named endpoint would be permitted on this
// client, and why not when it would not. Callers building a tool list
// for an agent should use this rather than advertising operations that
// are certain to fail.
func (c *Client) Can(endpoint string) (bool, error) {
	op, ok := Ops[endpoint]
	if !ok {
		return false, fmt.Errorf("kiwivm: unknown endpoint %q", endpoint)
	}
	if c.readOnly && op.Risk > RiskRead {
		return false, &ReadOnlyError{Op: op}
	}
	return true, nil
}

// call performs one API request.
//
// The read-only gate lives here, in front of the HTTP client, so no
// path through this package can mutate a VPS on a read-only client.
// Reads go out as GET; anything else goes out as POST form data, which
// keeps the api_key out of URLs, proxy logs and shell history.
func (c *Client) call(ctx context.Context, endpoint string, params url.Values, out any) error {
	op, known := Ops[endpoint]
	if !known {
		return fmt.Errorf("kiwivm: unknown endpoint %q", endpoint)
	}
	if c.readOnly && op.Risk > RiskRead {
		return &ReadOnlyError{Op: op}
	}
	if c.veid == "" {
		return &APIError{Op: endpoint, Code: CodeMissingParam, Message: "veid is missing"}
	}

	if params == nil {
		params = url.Values{}
	}
	// Copy so a caller-supplied Values is never mutated with secrets.
	form := url.Values{"veid": {c.veid}, "api_key": {c.apiKey}}
	for k, vs := range params {
		for _, v := range vs {
			form.Add(k, v)
		}
	}

	method := http.MethodPost
	if op.Risk == RiskRead {
		method = http.MethodGet
	}

	body, status, err := c.roundTrip(ctx, method, endpoint, form)
	if err != nil {
		return err
	}

	var env envelope
	if err := json.Unmarshal(body, &env); err != nil {
		return notJSON(endpoint, status, body, err)
	}
	if err := env.err(endpoint); err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return &TransportError{Op: endpoint, Status: status, Err: fmt.Errorf("decoding %T: %w", out, err)}
	}
	return nil
}

// notJSON reports a body that parsed as neither an error nor a result.
// The status is carried so IsTransient can tell a 200 carrying a proxy
// interstitial (a complete, wrong answer — do not retry blindly) from a
// request that never landed.
func notJSON(endpoint string, status int, body []byte, err error) error {
	return &TransportError{
		Op: endpoint, Status: status,
		Err: fmt.Errorf("response is not JSON (%s): %w", firstLine(string(body)), err),
	}
}

// callRaw is call without envelope interpretation, for the one
// endpoint (basicShell/exec) that reuses "error" for a command's exit
// status rather than an API outcome.
func (c *Client) callRaw(ctx context.Context, endpoint string, params url.Values, out any) error {
	op, known := Ops[endpoint]
	if !known {
		return fmt.Errorf("kiwivm: unknown endpoint %q", endpoint)
	}
	if c.readOnly && op.Risk > RiskRead {
		return &ReadOnlyError{Op: op}
	}
	if c.veid == "" {
		return &APIError{Op: endpoint, Code: CodeMissingParam, Message: "veid is missing"}
	}

	form := url.Values{"veid": {c.veid}, "api_key": {c.apiKey}}
	for k, vs := range params {
		for _, v := range vs {
			form.Add(k, v)
		}
	}

	method := http.MethodPost
	if op.Risk == RiskRead {
		method = http.MethodGet
	}
	body, status, err := c.roundTrip(ctx, method, endpoint, form)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, out); err != nil {
		return notJSON(endpoint, status, body, err)
	}
	return nil
}

// roundTrip performs the HTTP exchange and returns the body and the
// status code. A non-200 is an error; a 200 is handed back for the
// caller to interpret, since KiwiVM reports failures inside the body.
func (c *Client) roundTrip(ctx context.Context, method, endpoint string, form url.Values) ([]byte, int, error) {
	target := c.baseURL + "/" + endpoint

	var req *http.Request
	var err error
	if method == http.MethodGet {
		req, err = http.NewRequestWithContext(ctx, method, target+"?"+form.Encode(), nil)
	} else {
		req, err = http.NewRequestWithContext(ctx, method, target, strings.NewReader(form.Encode()))
		if err == nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
	}
	if err != nil {
		return nil, 0, &TransportError{Op: endpoint, Err: err}
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.ua)

	start := time.Now()
	resp, err := c.http.Do(req)
	if err != nil {
		if c.trace != nil {
			c.trace(method, endpoint, 0, time.Since(start))
		}
		return nil, 0, &TransportError{Op: endpoint, Err: c.redact(err)}
	}
	defer resp.Body.Close()
	if c.trace != nil {
		c.trace(method, endpoint, resp.StatusCode, time.Since(start))
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return nil, resp.StatusCode, &TransportError{Op: endpoint, Status: resp.StatusCode, Err: err}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, resp.StatusCode, &TransportError{
			Op: endpoint, Status: resp.StatusCode,
			Err: fmt.Errorf("%s", firstLine(string(body))),
		}
	}
	return body, resp.StatusCode, nil
}

// minRedactableKey is the shortest api_key worth substituting out of
// an error message. Below it, the "key" is a test placeholder whose
// characters occur in ordinary text — blindly replacing "k" would turn
// "api_key" into "api_REDACTEDey" and corrupt the diagnostic while
// protecting nothing.
const minRedactableKey = 8

// redact hides the api_key in an error before it escapes.
//
// A read goes out as GET, so the credential is in the query string,
// and net/http wraps failures in *url.Error, which renders the whole
// URL. Without this, a plain "connection refused" prints a working API
// key into any log or CI transcript that catches it.
//
// The original error is kept as the wrapped cause, so errors.Is and
// errors.As still see through to context.DeadlineExceeded and friends.
// Only the rendered text changes.
func (c *Client) redact(err error) error {
	if err == nil || len(c.apiKey) < minRedactableKey {
		return err
	}
	msg := err.Error()
	if !strings.Contains(msg, c.apiKey) {
		return err
	}
	return &redactedError{
		msg:   strings.ReplaceAll(msg, c.apiKey, "REDACTED"),
		cause: err,
	}
}

// redactedError reports a sanitized message while remaining
// transparent to errors.Is and errors.As.
type redactedError struct {
	msg   string
	cause error
}

func (e *redactedError) Error() string { return e.msg }
func (e *redactedError) Unwrap() error { return e.cause }

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "empty response body"
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200] + "..."
	}
	return s
}

// fetch performs a call that decodes into a fresh T, returning nil on
// any failure so a caller who checks the value before the error cannot
// mistake a zero struct for a real answer.
func fetch[T any](ctx context.Context, c *Client, endpoint string, params url.Values) (*T, error) {
	var out T
	if err := c.call(ctx, endpoint, params, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
