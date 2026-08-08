// Package mcp serves the bwg fleet over the Model Context Protocol on
// stdio, so an agent host can call KiwiVM operations as tools.
//
// The protocol handling here is hand-rolled against the MCP spec's
// stdio transport: newline-delimited JSON-RPC 2.0. That is a few
// hundred lines and no dependency, which matters for a binary people
// install with curl.
//
// The safety model is inherited, not reimplemented. Tools are
// generated from kiwivm.Ops, and a read-only server simply does not
// advertise the operations it would refuse — an agent is never shown a
// tool that is certain to fail.
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"

	"github.com/lroolle/bwg-cli/bwhstatus"
	"github.com/lroolle/bwg-cli/internal/config"
	"github.com/lroolle/bwg-cli/internal/fleet"
	"github.com/lroolle/bwg-cli/kiwivm"
)

// protocolVersion is the MCP revision this server implements.
const protocolVersion = "2024-11-05"

// Server serves the fleet over MCP.
type Server struct {
	cfg      *config.Config
	readOnly bool
	version  string

	in  io.Reader
	out io.Writer
	// writeMu serialises responses; JSON-RPC over stdio has no framing
	// beyond newlines, so two interleaved writes would corrupt both.
	writeMu sync.Mutex
}

// New builds an MCP server over the given streams.
func New(cfg *config.Config, readOnly bool, version string, in io.Reader, out io.Writer) *Server {
	return &Server{cfg: cfg, readOnly: readOnly, version: version, in: in, out: out}
}

// -- JSON-RPC ---------------------------------------------------------------

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// JSON-RPC 2.0 error codes.
const (
	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternalError  = -32603
)

// Serve reads requests until the input closes or ctx is cancelled.
func (s *Server) Serve(ctx context.Context) error {
	scanner := bufio.NewScanner(s.in)
	// Tool results can carry a whole getServiceInfo payload; the
	// default 64 KiB line limit is not enough for the requests that
	// echo one back.
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)

	for scanner.Scan() {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var req request
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			s.reply(response{JSONRPC: "2.0", Error: &rpcError{codeParseError, err.Error()}})
			continue
		}
		// A request with no id is a notification: the spec says answer
		// nothing at all, not even an error.
		if len(req.ID) == 0 {
			continue
		}
		s.reply(s.handle(ctx, req))
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

func (s *Server) reply(resp response) {
	resp.JSONRPC = "2.0"
	data, err := json.Marshal(resp)
	if err != nil {
		data, _ = json.Marshal(response{JSONRPC: "2.0", ID: resp.ID,
			Error: &rpcError{codeInternalError, "the result could not be encoded"}})
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	fmt.Fprintf(s.out, "%s\n", data)
}

func (s *Server) handle(ctx context.Context, req request) response {
	out := response{ID: req.ID}

	switch req.Method {
	case "initialize":
		out.Result = map[string]any{
			"protocolVersion": protocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "bwg", "version": s.version},
			"instructions":    s.instructions(),
		}
	case "ping":
		out.Result = map[string]any{}
	case "tools/list":
		out.Result = map[string]any{"tools": s.tools()}
	case "tools/call":
		result, err := s.callTool(ctx, req.Params)
		if err != nil {
			out.Error = &rpcError{codeInvalidParams, err.Error()}
			break
		}
		out.Result = result
	default:
		out.Error = &rpcError{codeMethodNotFound, "unknown method " + req.Method}
	}
	return out
}

func (s *Server) instructions() string {
	mode := "read and write"
	if s.readOnly {
		mode = "READ-ONLY: only observation tools are available, and no " +
			"tool exposed here can change a server"
	}
	names := s.cfg.Names()
	if env := config.ServerFromEnv(); env != nil {
		names = append([]string{config.EnvServerName}, names...)
	}

	return fmt.Sprintf(
		"bwg manages BandwagonHost (KiwiVM) VPS instances. Mode: %s.\n\n"+
			"Known servers: %s. Every tool takes an optional \"server\" argument; "+
			"omit it to use the default (%s).\n\n"+
			"Start with bwg_fleet for an overview of every box, then bwg_info or "+
			"bwg_status for one. Bandwidth figures already have the location "+
			"multiplier applied.",
		mode, strings.Join(names, ", "), orNone(s.cfg.Default))
}

func orNone(s string) string {
	if s == "" {
		return "none set"
	}
	return s
}

// -- tools -------------------------------------------------------------------

type tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`

	// endpoint ties the tool back to its risk classification in
	// kiwivm.Ops. Empty means the tool does not call KiwiVM at all —
	// only bwg_incidents, which reads a public status feed with no
	// credentials and cannot change anything. See mutating().
	endpoint string
	// run performs the call.
	run func(ctx context.Context, c *kiwivm.Client, args map[string]any) (any, error)
}

// tools returns the advertised tool list.
//
// A read-only server omits every tool it would refuse. Advertising a
// tool and then rejecting every call is how an agent ends up in a
// retry loop; not offering it at all is honest and costs nothing.
// mutating reports whether a tool could change a server. An unknown
// endpoint counts as mutating: a tool nobody classified must not be
// assumed safe.
func mutating(t tool) bool {
	if t.endpoint == "" {
		return false // reads a public feed; no credentials, no writes
	}
	op, ok := kiwivm.LookupOp(t.endpoint)
	if !ok {
		return true
	}
	return op.Risk > kiwivm.RiskRead
}

func (s *Server) tools() []map[string]any {
	var out []map[string]any
	for _, t := range s.toolset() {
		if s.readOnly && mutating(t) {
			continue
		}
		out = append(out, map[string]any{
			"name": t.Name, "description": t.Description, "inputSchema": t.InputSchema,
		})
	}
	return out
}

func serverArg(extra map[string]any, required []string) map[string]any {
	props := map[string]any{
		"server": map[string]any{
			"type":        "string",
			"description": "Which configured server to act on. Omit for the default.",
		},
	}
	for k, v := range extra {
		props[k] = v
	}
	schema := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func str(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}

func (s *Server) toolset() []tool {
	return []tool{
		{
			Name: "bwg_fleet",
			Description: "Overview of every configured VPS: plan, location, bandwidth used " +
				"against the monthly cap (multiplier applied), abuse points, and anything " +
				"needing attention. Start here.",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
			endpoint:    "getServiceInfo",
		},
		{
			Name: "bwg_info",
			Description: "Plan, location, addresses, rDNS and bandwidth quota for one VPS. " +
				"Fast; does not touch the guest.",
			InputSchema: serverArg(nil, nil),
			endpoint:    "getServiceInfo",
			run: func(ctx context.Context, c *kiwivm.Client, _ map[string]any) (any, error) {
				info, err := c.ServiceInfo(ctx)
				if err != nil {
					return nil, err
				}
				return map[string]any{
					"info": info, "bandwidth": info.Bandwidth(),
					"ipv4": info.IPv4(), "ipv6": info.IPv6(), "healthy": info.Healthy(),
				}, nil
			},
		},
		{
			Name: "bwg_status",
			Description: "Live state of one VPS: power state, load, memory, disk and SSH port. " +
				"Queries the running guest and can take up to 15 seconds.",
			InputSchema: serverArg(nil, nil),
			endpoint:    "getLiveServiceInfo",
			run: func(ctx context.Context, c *kiwivm.Client, _ map[string]any) (any, error) {
				live, err := c.LiveServiceInfo(ctx)
				if err != nil {
					return nil, err
				}
				// The base64 console screenshot is hundreds of kilobytes
				// and would swamp an agent's context for no benefit.
				live.ScreendumpPNGBase64 = ""
				mem, _ := live.MemUsedBytes()
				disk, _ := live.DiskUsedBytes()
				return map[string]any{
					"state": live.State(), "running": live.Running(),
					"sshPort": live.SSHPort.Int(), "memUsedBytes": mem,
					"diskUsedBytes": disk, "loadAverage": live.LoadAverage,
					"cpuThrottled":  live.IsCPUThrottled.Bool(),
					"diskThrottled": live.IsDiskThrottled.Bool(),
				}, nil
			},
		},
		{
			Name:        "bwg_usage",
			Description: "Sampled CPU, network and disk usage for one VPS, with totals.",
			InputSchema: serverArg(nil, nil),
			endpoint:    "getRawUsageStats",
			run: func(ctx context.Context, c *kiwivm.Client, _ map[string]any) (any, error) {
				stats, err := c.RawUsageStats(ctx)
				if err != nil {
					return nil, err
				}
				in, out, dr, dw := stats.Totals()
				start, end := stats.Window()
				return map[string]any{
					"vmType": stats.VMType, "samples": len(stats.Data),
					"from": start, "to": end,
					"totals": map[string]int64{"networkIn": in, "networkOut": out,
						"diskRead": dr, "diskWrite": dw},
					"data": stats.Data,
				}, nil
			},
		},
		{
			Name:        "bwg_snapshots",
			Description: "List the snapshots stored for one VPS.",
			InputSchema: serverArg(nil, nil),
			endpoint:    "snapshot/list",
			run: func(ctx context.Context, c *kiwivm.Client, _ map[string]any) (any, error) {
				return c.Snapshots(ctx)
			},
		},
		{
			Name:        "bwg_backups",
			Description: "List the automatic backups KiwiVM holds for one VPS, newest first.",
			InputSchema: serverArg(nil, nil),
			endpoint:    "backup/list",
			run: func(ctx context.Context, c *kiwivm.Client, _ map[string]any) (any, error) {
				list, err := c.Backups(ctx)
				if err != nil {
					return nil, err
				}
				return map[string]any{"backups": list.Sorted()}, nil
			},
		},
		{
			Name: "bwg_abuse",
			Description: "Suspensions, unresolved policy violations and the abuse-point " +
				"balance for one VPS.",
			InputSchema: serverArg(nil, nil),
			endpoint:    "getSuspensionDetails",
			run: func(ctx context.Context, c *kiwivm.Client, _ map[string]any) (any, error) {
				susp, err := c.SuspensionDetails(ctx)
				if err != nil {
					return nil, err
				}
				viol, err := c.PolicyViolations(ctx)
				if err != nil {
					return nil, err
				}
				return map[string]any{"suspensions": susp, "violations": viol}, nil
			},
		},
		{
			Name:        "bwg_audit",
			Description: "KiwiVM control-panel audit log for one VPS, with requesting IPs.",
			InputSchema: serverArg(nil, nil),
			endpoint:    "getAuditLog",
			run: func(ctx context.Context, c *kiwivm.Client, _ map[string]any) (any, error) {
				return c.AuditLog(ctx)
			},
		},
		{
			Name:        "bwg_os_list",
			Description: "Installed OS and the templates available for reinstall on one VPS.",
			InputSchema: serverArg(nil, nil),
			endpoint:    "getAvailableOS",
			run: func(ctx context.Context, c *kiwivm.Client, _ map[string]any) (any, error) {
				return c.AvailableOS(ctx)
			},
		},
		{
			Name: "bwg_incidents",
			Description: "BandwagonHost service incidents from the official status page, " +
				"matched against the configured servers. Says WHICH of your boxes an " +
				"incident may touch and why. Matching is a heuristic over incident prose: " +
				"a match is a prompt to investigate, and no match is not an all-clear. " +
				"Use this when a server is unreachable or slow before concluding it is " +
				"your own fault.",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{
				"ongoing": map[string]any{"type": "boolean",
					"description": "Only incidents that are not yet resolved."},
			}},
			// No endpoint: this reads a public feed, not KiwiVM.
			endpoint: "",
		},
		{
			Name:        "bwg_rate_limit",
			Description: "Remaining KiwiVM API budget for the 15-minute and 24-hour windows.",
			InputSchema: serverArg(nil, nil),
			endpoint:    "getRateLimitStatus",
			run: func(ctx context.Context, c *kiwivm.Client, _ map[string]any) (any, error) {
				return c.RateLimitStatus(ctx)
			},
		},

		// -- writes ------------------------------------------------------
		{
			Name: "bwg_power",
			Description: "Start, stop or restart a VPS. Recoverable: start undoes any of " +
				"them. Does not include kill, which loses unsaved data.",
			InputSchema: serverArg(map[string]any{
				"action": map[string]any{"type": "string", "enum": []string{"start", "stop", "restart"},
					"description": "Which power action to take."},
			}, []string{"action"}),
			endpoint: "restart",
			run: func(ctx context.Context, c *kiwivm.Client, args map[string]any) (any, error) {
				action, _ := args["action"].(string)
				var err error
				switch action {
				case "start":
					err = c.Start(ctx)
				case "stop":
					err = c.Stop(ctx)
				case "restart":
					err = c.Restart(ctx)
				default:
					return nil, fmt.Errorf("action must be start, stop or restart")
				}
				if err != nil {
					return nil, err
				}
				return map[string]any{"action": action, "status": "accepted"}, nil
			},
		},
		{
			Name:        "bwg_snapshot_create",
			Description: "Create a snapshot. KiwiVM locks the VPS while it runs.",
			InputSchema: serverArg(map[string]any{
				"description": str("Label for the snapshot."),
			}, nil),
			endpoint: "snapshot/create",
			run: func(ctx context.Context, c *kiwivm.Client, args map[string]any) (any, error) {
				desc, _ := args["description"].(string)
				return c.CreateSnapshot(ctx, desc)
			},
		},
		{
			Name:        "bwg_set_ptr",
			Description: "Set the reverse DNS (PTR) record for one of the VPS's IP addresses.",
			InputSchema: serverArg(map[string]any{
				"ip":  str("The IP address to set rDNS for."),
				"ptr": str("The hostname. Pass an empty string to clear it."),
			}, []string{"ip", "ptr"}),
			endpoint: "setPTR",
			run: func(ctx context.Context, c *kiwivm.Client, args map[string]any) (any, error) {
				ip, _ := args["ip"].(string)
				ptr, _ := args["ptr"].(string)
				if err := c.SetPTR(ctx, ip, ptr); err != nil {
					return nil, err
				}
				return map[string]any{"ip": ip, "ptr": ptr, "status": "set"}, nil
			},
		},
		{
			Name:        "bwg_set_hostname",
			Description: "Set the hostname KiwiVM records for a VPS. Does not change the running guest.",
			InputSchema: serverArg(map[string]any{
				"hostname": str("The new hostname."),
			}, []string{"hostname"}),
			endpoint: "setHostname",
			run: func(ctx context.Context, c *kiwivm.Client, args map[string]any) (any, error) {
				h, _ := args["hostname"].(string)
				if err := c.SetHostname(ctx, h); err != nil {
					return nil, err
				}
				return map[string]any{"hostname": h, "status": "set"}, nil
			},
		},
	}
}

func (s *Server) callTool(ctx context.Context, raw json.RawMessage) (any, error) {
	var params struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, fmt.Errorf("invalid tool call: %w", err)
	}

	var found *tool
	for _, t := range s.toolset() {
		if t.Name == params.Name {
			t := t
			found = &t
			break
		}
	}
	if found == nil {
		return nil, fmt.Errorf("unknown tool %q", params.Name)
	}

	// Re-check the gate at call time. The tool list already hides
	// refused operations, but a client can call anything it likes, and
	// this must not depend on the client having read the list.
	if s.readOnly && mutating(*found) {
		risk := "mutating"
		if op, ok := kiwivm.LookupOp(found.endpoint); ok {
			risk = op.Risk.String()
		}
		return toolError(fmt.Sprintf(
			"%s is a %s operation and this server is running read-only",
			found.Name, risk)), nil
	}

	switch found.Name {
	case "bwg_fleet":
		return s.fleetTool(ctx)
	case "bwg_incidents":
		return s.incidentsTool(ctx)
	}

	name, _ := params.Arguments["server"].(string)
	srv, err := s.cfg.Resolve(name)
	if err != nil {
		return toolError(err.Error()), nil
	}
	c := fleet.ClientFor(srv, s.readOnly)

	result, err := found.run(ctx, c, params.Arguments)
	if err != nil {
		// A failed KiwiVM call is a tool result, not a protocol error:
		// the agent should see the message and adapt, not receive a
		// transport failure it cannot interpret.
		return toolError(err.Error()), nil
	}
	return toolResult(map[string]any{"server": srv.Name, "result": result})
}

// fleetTool sweeps every server. It is the one tool that is not scoped
// to a single box.
func (s *Server) fleetTool(ctx context.Context) (any, error) {
	servers := s.cfg.List()
	if len(servers) == 0 {
		return toolError("no servers are configured; add one with 'bwg server add'"), nil
	}

	type entry struct {
		Server    string  `json:"server"`
		Hostname  string  `json:"hostname"`
		Plan      string  `json:"plan"`
		Location  string  `json:"location"`
		OS        string  `json:"os"`
		UsedBytes int64   `json:"bandwidthUsedBytes"`
		CapBytes  int64   `json:"bandwidthCapBytes"`
		Percent   float64 `json:"bandwidthPercent"`
		Suspended bool    `json:"suspended"`
		Violation bool    `json:"policyViolation"`
		Abuse     int     `json:"abusePoints"`
		Error     string  `json:"error,omitempty"`
	}

	results := fleet.Map(ctx, servers, fleet.DefaultConcurrency,
		func(ctx context.Context, srv *config.Server) (entry, error) {
			info, err := fleet.ClientFor(srv, s.readOnly).ServiceInfo(ctx)
			if err != nil {
				return entry{Server: srv.Name}, err
			}
			b := info.Bandwidth()
			return entry{
				Server: srv.Name, Hostname: info.Hostname, Plan: info.Plan,
				Location: info.NodeLocation, OS: info.OS,
				UsedBytes: b.Used, CapBytes: b.Total, Percent: b.Percent,
				Suspended: info.Suspended.Bool(), Violation: info.PolicyViolation.Bool(),
				Abuse: info.TotalAbusePoints.Int(),
			}, nil
		})

	entries := make([]entry, 0, len(results))
	for _, r := range results {
		e := r.Value
		e.Server = r.Server.Name
		if !r.OK() {
			e.Error = r.Error
		}
		entries = append(entries, e)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Percent > entries[j].Percent })

	return toolResult(map[string]any{"servers": entries, "count": len(entries)})
}

// incidentsTool reads the status feed and correlates it with the
// fleet. Like bwg_fleet it is not scoped to one server.
func (s *Server) incidentsTool(ctx context.Context) (any, error) {
	incidents, err := bwhstatus.New().Fetch(ctx)
	if err != nil {
		return toolError(err.Error() + " (the status page is a courtesy signal; " +
			"the servers themselves may be fine)"), nil
	}

	// Correlation needs each server's location and node, one call each.
	// A server that does not answer is reported as unchecked rather
	// than quietly treated as unaffected.
	var unchecked []string
	targets := []bwhstatus.Target{}
	for _, r := range fleet.Map(ctx, s.cfg.List(), fleet.DefaultConcurrency,
		func(ctx context.Context, srv *config.Server) (bwhstatus.Target, error) {
			info, err := fleet.ClientFor(srv, s.readOnly).ServiceInfo(ctx)
			if err != nil {
				return bwhstatus.Target{Name: srv.Name}, err
			}
			return bwhstatus.Target{
				Name: srv.Name, Location: info.NodeLocation,
				NodeAlias: info.NodeAlias, Datacenter: info.NodeDatacenter,
			}, nil
		}) {
		if r.OK() {
			targets = append(targets, r.Value)
		} else {
			unchecked = append(unchecked, r.Server.Name)
		}
	}

	type affects struct {
		Server  string   `json:"server"`
		Reasons []string `json:"reasons"`
	}
	type reported struct {
		bwhstatus.Incident
		Affects []affects `json:"affects,omitempty"`
	}

	out := make([]reported, 0, len(incidents))
	for _, inc := range incidents {
		r := reported{Incident: inc}
		for _, t := range targets {
			if reasons := bwhstatus.Match(inc, t); len(reasons) > 0 {
				r.Affects = append(r.Affects, affects{Server: t.Name, Reasons: reasons})
			}
		}
		out = append(out, r)
	}

	return toolResult(map[string]any{
		"summary":   bwhstatus.Summarize(incidents),
		"incidents": out,
		"unchecked": unchecked,
		"source":    bwhstatus.DefaultFeedURL,
		"caveat": "Matching is a heuristic over incident text. A match is a prompt to " +
			"investigate; no match is not an all-clear.",
	})
}

func toolResult(v any) (any, error) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": string(data)}},
	}, nil
}

func toolError(msg string) any {
	return map[string]any{
		"isError": true,
		"content": []map[string]any{{"type": "text", "text": msg}},
	}
}
