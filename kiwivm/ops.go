package kiwivm

import "sort"

// Risk classifies what an endpoint can do to a VPS. The dividing line
// between Write and Destructive is deliberately narrow:
//
//	Destructive = irreversible loss of data, identity, or access
//	              that no other call in this package can restore.
//
// Stopping a VPS is a Write because Start undoes it. Deleting a
// snapshot is Destructive because nothing brings it back. Keeping the
// Destructive set small is what makes a Destructive confirmation mean
// something; a prompt that guards everything guards nothing.
type Risk int

const (
	// RiskRead observes state and changes nothing.
	RiskRead Risk = iota
	// RiskWrite changes state in a way another call can undo.
	RiskWrite
	// RiskDestructive irreversibly loses data, identity, or access.
	RiskDestructive
)

func (r Risk) String() string {
	switch r {
	case RiskRead:
		return "read"
	case RiskWrite:
		return "write"
	case RiskDestructive:
		return "destructive"
	}
	return "unknown"
}

// MarshalText renders the risk as its lowercase name in JSON.
func (r Risk) MarshalText() ([]byte, error) { return []byte(r.String()), nil }

// Op describes one KiwiVM endpoint.
type Op struct {
	// Endpoint is the path under the API base URL, e.g. "snapshot/list".
	Endpoint string `json:"endpoint"`
	// Risk is what this endpoint can do. See [Risk].
	Risk Risk `json:"risk"`
	// Summary is a one-line description, reused by the CLI and the MCP
	// tool list so all three surfaces describe an endpoint identically.
	Summary string `json:"summary"`
	// Why explains a Destructive classification. Empty for other risks.
	Why string `json:"why,omitempty"`
}

// Ops is the registry of every endpoint this package can call, keyed
// by endpoint path. It is the single source of truth for risk: the
// client gate, the CLI confirmation prompts, the MCP tool list, and
// the generated docs all read from here.
var Ops = map[string]Op{
	// -- Power ---------------------------------------------------------
	"start":   {"start", RiskWrite, "Start the VPS", ""},
	"stop":    {"stop", RiskWrite, "Stop the VPS", ""},
	"restart": {"restart", RiskWrite, "Reboot the VPS", ""},
	"kill": {"kill", RiskDestructive, "Force-stop a VPS that will not stop normally",
		"unsaved data in the guest is lost"},

	// -- Service information -------------------------------------------
	"getServiceInfo":     {"getServiceInfo", RiskRead, "Plan, location, network and quota for the VPS", ""},
	"getLiveServiceInfo": {"getLiveServiceInfo", RiskRead, "Service info plus live guest status (slow: up to 15s)", ""},
	"getRateLimitStatus": {"getRateLimitStatus", RiskRead, "Remaining API rate-limit points", ""},

	// -- OS and SSH ----------------------------------------------------
	"getAvailableOS": {"getAvailableOS", RiskRead, "Installed OS and installable templates", ""},
	"reinstallOS": {"reinstallOS", RiskDestructive, "Reinstall the operating system",
		"every byte on the VPS disk is erased"},
	"getSshKeys":    {"getSshKeys", RiskRead, "SSH keys in Hypervisor Vault and the billing portal", ""},
	"updateSshKeys": {"updateSshKeys", RiskWrite, "Replace the per-VM SSH keys used by reinstallOS", ""},
	"resetRootPassword": {"resetRootPassword", RiskDestructive, "Generate and set a new root password",
		"the current root password becomes unrecoverable and anything using it is locked out"},

	// -- Usage and audit -----------------------------------------------
	"getUsageGraphs":   {"getUsageGraphs", RiskRead, "Legacy usage graphs (obsolete; use getRawUsageStats)", ""},
	"getRawUsageStats": {"getRawUsageStats", RiskRead, "Per-interval CPU, network and disk usage samples", ""},
	"getAuditLog":      {"getAuditLog", RiskRead, "KiwiVM control-panel audit log", ""},

	// -- Hostname, DNS, ISO --------------------------------------------
	"setHostname": {"setHostname", RiskWrite, "Set the VPS hostname", ""},
	"setPTR":      {"setPTR", RiskWrite, "Set the PTR (rDNS) record for an IP", ""},
	"iso/mount": {"iso/mount", RiskDestructive, "Boot from an ISO image instead of primary storage",
		"changes boot media; a VPS left on a wrong ISO is unreachable until corrected"},
	"iso/unmount": {"iso/unmount", RiskDestructive, "Remove the ISO and boot from primary storage",
		"changes boot media; requires a full shutdown and restart"},

	// -- Shell ---------------------------------------------------------
	"basicShell/cd": {"basicShell/cd", RiskRead, "Resolve a directory change inside the VPS", ""},
	"basicShell/exec": {"basicShell/exec", RiskDestructive, "Run a shell command inside the VPS as root (synchronous)",
		"arbitrary root code execution; effects are entirely up to the command"},
	"shellScript/exec": {"shellScript/exec", RiskDestructive, "Run a shell script inside the VPS as root (asynchronous)",
		"arbitrary root code execution; runs detached with no way to recall it"},

	// -- Snapshots and backups -----------------------------------------
	"snapshot/list":         {"snapshot/list", RiskRead, "List snapshots", ""},
	"snapshot/create":       {"snapshot/create", RiskWrite, "Create a snapshot", ""},
	"snapshot/toggleSticky": {"snapshot/toggleSticky", RiskWrite, "Protect a snapshot from automatic purge, or stop protecting it", ""},
	"snapshot/export":       {"snapshot/export", RiskWrite, "Mint a transfer token for a snapshot", ""},
	"snapshot/import":       {"snapshot/import", RiskWrite, "Import a snapshot from another instance", ""},
	"snapshot/delete": {"snapshot/delete", RiskDestructive, "Delete a snapshot",
		"the snapshot cannot be recovered"},
	"snapshot/restore": {"snapshot/restore", RiskDestructive, "Restore a snapshot over the VPS",
		"current VPS data is overwritten by the snapshot"},
	"backup/list":           {"backup/list", RiskRead, "List automatic backups", ""},
	"backup/copyToSnapshot": {"backup/copyToSnapshot", RiskWrite, "Copy an automatic backup into a restorable snapshot", ""},

	// -- Network -------------------------------------------------------
	"ipv6/add": {"ipv6/add", RiskWrite, "Allocate an IPv6 /64 subnet", ""},
	"ipv6/delete": {"ipv6/delete", RiskDestructive, "Release an IPv6 /64 subnet",
		"the subnet returns to the pool and will not be reissued to you"},
	"privateIp/getAvailableIps": {"privateIp/getAvailableIps", RiskRead, "List assignable private IPv4 addresses", ""},
	"privateIp/assign":          {"privateIp/assign", RiskWrite, "Assign a private IPv4 address", ""},
	"privateIp/delete":          {"privateIp/delete", RiskWrite, "Remove a private IPv4 address", ""},

	// -- Migration -----------------------------------------------------
	"migrate/getLocations": {"migrate/getLocations", RiskRead, "List migration target locations", ""},
	"migrate/start": {"migrate/start", RiskDestructive, "Migrate the VPS to another location",
		"every IPv4 address is replaced; the old addresses are not recoverable"},
	"cloneFromExternalServer": {"cloneFromExternalServer", RiskDestructive, "Clone a remote server into this VPS (OpenVZ only)",
		"the current VPS contents are replaced by the remote server's"},

	// -- Suspension and policy -----------------------------------------
	"getSuspensionDetails": {"getSuspensionDetails", RiskRead, "Suspensions, abuse points and evidence", ""},
	"getPolicyViolations":  {"getPolicyViolations", RiskRead, "Active policy violations awaiting resolution", ""},
	"unsuspend": {"unsuspend", RiskDestructive, "Clear an abuse case and unsuspend the VPS",
		"consumes a one-time case resolution that cannot be re-opened through the API"},
	"resolvePolicyViolation": {"resolvePolicyViolation", RiskDestructive, "Mark a policy violation resolved",
		"consumes a one-time case resolution that cannot be re-opened through the API"},

	// -- KiwiVM settings -----------------------------------------------
	"kiwivm/getNotificationPreferences": {"kiwivm/getNotificationPreferences", RiskRead, "Email notification preferences", ""},
	"kiwivm/setNotificationPreferences": {"kiwivm/setNotificationPreferences", RiskWrite, "Change email notification preferences", ""},
}

// LookupOp returns the registered operation for an endpoint.
func LookupOp(endpoint string) (Op, bool) {
	op, ok := Ops[endpoint]
	return op, ok
}

// ListOps returns every registered operation sorted by endpoint, for
// stable output in docs, `bwg api ops` and the MCP tool list.
func ListOps() []Op {
	out := make([]Op, 0, len(Ops))
	for _, op := range Ops {
		out = append(out, op)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Endpoint < out[j].Endpoint })
	return out
}
