package kiwivm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// -- power -------------------------------------------------------------

// Start boots the VPS.
func (c *Client) Start(ctx context.Context) error {
	return c.call(ctx, "start", nil, nil)
}

// Stop shuts the VPS down. [Client.Start] undoes it.
func (c *Client) Stop(ctx context.Context) error {
	return c.call(ctx, "stop", nil, nil)
}

// Restart reboots the VPS.
func (c *Client) Restart(ctx context.Context) error {
	return c.call(ctx, "restart", nil, nil)
}

// Kill force-stops a VPS that will not stop normally. Unsaved data in
// the guest is lost.
func (c *Client) Kill(ctx context.Context) error {
	return c.call(ctx, "kill", nil, nil)
}

// -- service information -----------------------------------------------

// ServiceInfo returns plan, location, network and quota state.
func (c *Client) ServiceInfo(ctx context.Context) (*ServiceInfo, error) {
	return fetch[ServiceInfo](ctx, c, "getServiceInfo", nil)
}

// LiveServiceInfo returns [Client.ServiceInfo] plus guest-reported
// state. KiwiVM documents this call as taking up to 15 seconds.
func (c *Client) LiveServiceInfo(ctx context.Context) (*LiveServiceInfo, error) {
	return fetch[LiveServiceInfo](ctx, c, "getLiveServiceInfo", nil)
}

// RateLimitStatus returns the remaining API budget. It costs a point
// itself, so polling it in a loop is self-defeating.
func (c *Client) RateLimitStatus(ctx context.Context) (*RateLimit, error) {
	return fetch[RateLimit](ctx, c, "getRateLimitStatus", nil)
}

// -- OS and SSH ---------------------------------------------------------

// AvailableOS returns the installed OS and the installable templates.
func (c *Client) AvailableOS(ctx context.Context) (*AvailableOS, error) {
	return fetch[AvailableOS](ctx, c, "getAvailableOS", nil)
}

// ReinstallOS reinstalls the operating system, erasing the disk. os
// must be one of the templates from [Client.AvailableOS].
//
// The returned root password is shown once and is not retrievable
// afterwards — callers must surface it immediately.
func (c *Client) ReinstallOS(ctx context.Context, os string) (*ReinstallResult, error) {
	if strings.TrimSpace(os) == "" {
		return nil, fmt.Errorf("kiwivm: reinstallOS needs an OS template (see AvailableOS)")
	}
	return fetch[ReinstallResult](ctx, c, "reinstallOS", url.Values{"os": {os}})
}

// SSHKeys returns the keys reinstallOS would install.
func (c *Client) SSHKeys(ctx context.Context) (*SSHKeys, error) {
	return fetch[SSHKeys](ctx, c, "getSshKeys", nil)
}

// UpdateSSHKeys replaces the per-VM keys held in Hypervisor Vault.
// These shadow the account-level keys entirely during a reinstall.
// Passing no keys clears them, which restores the account-level keys.
func (c *Client) UpdateSSHKeys(ctx context.Context, keys []string) error {
	return c.call(ctx, "updateSshKeys", url.Values{"ssh_keys": {strings.Join(keys, "\n")}}, nil)
}

// ResetRootPassword generates a new root password and returns it. The
// previous password is unrecoverable.
func (c *Client) ResetRootPassword(ctx context.Context) (*RootPassword, error) {
	return fetch[RootPassword](ctx, c, "resetRootPassword", nil)
}

// -- usage and audit -----------------------------------------------------

// RawUsageStats returns the sampled CPU, network and disk series.
func (c *Client) RawUsageStats(ctx context.Context) (*UsageStats, error) {
	return fetch[UsageStats](ctx, c, "getRawUsageStats", nil)
}

// UsageGraphs returns the legacy graph payload. KiwiVM marks this
// obsolete; use [Client.RawUsageStats]. The shape is undocumented, so
// it comes back as decoded JSON rather than a typed struct.
func (c *Client) UsageGraphs(ctx context.Context) (map[string]any, error) {
	out := map[string]any{}
	return out, c.call(ctx, "getUsageGraphs", nil, &out)
}

// AuditLog returns the KiwiVM control-panel audit log.
func (c *Client) AuditLog(ctx context.Context) (*AuditLog, error) {
	return fetch[AuditLog](ctx, c, "getAuditLog", nil)
}

// -- hostname, DNS, ISO --------------------------------------------------

// SetHostname sets the VPS hostname recorded by KiwiVM. It does not
// change the hostname inside a running guest.
func (c *Client) SetHostname(ctx context.Context, hostname string) error {
	if strings.TrimSpace(hostname) == "" {
		return fmt.Errorf("kiwivm: setHostname needs a hostname")
	}
	return c.call(ctx, "setHostname", url.Values{"newHostname": {hostname}}, nil)
}

// SetPTR sets the PTR (rDNS) record for an IP. Check
// ServiceInfo.RDNSAPIAvailable first: not every plan allows it.
func (c *Client) SetPTR(ctx context.Context, ip, ptr string) error {
	if strings.TrimSpace(ip) == "" {
		return fmt.Errorf("kiwivm: setPTR needs an IP")
	}
	return c.call(ctx, "setPTR", url.Values{"ip": {ip}, "ptr": {ptr}}, nil)
}

// MountISO sets the VPS to boot from an ISO image. The VPS must be
// fully shut down first and restarted afterwards.
func (c *Client) MountISO(ctx context.Context, iso string) error {
	if strings.TrimSpace(iso) == "" {
		return fmt.Errorf("kiwivm: iso/mount needs an image name (see ServiceInfo.AvailableISOs)")
	}
	return c.call(ctx, "iso/mount", url.Values{"iso": {iso}}, nil)
}

// UnmountISO restores booting from primary storage. The VPS must be
// fully shut down first and restarted afterwards.
func (c *Client) UnmountISO(ctx context.Context) error {
	return c.call(ctx, "iso/unmount", nil, nil)
}

// -- shell ---------------------------------------------------------------

// ShellCD resolves a directory change inside the VPS, for building an
// interactive shell on top of [Client.ShellExec]. It changes nothing.
func (c *Client) ShellCD(ctx context.Context, currentDir, newDir string) (*ShellCD, error) {
	return fetch[ShellCD](ctx, c, "basicShell/cd",
		url.Values{"currentDir": {currentDir}, "newDir": {newDir}})
}

// ShellExec runs a command inside the VPS as root and waits for it.
//
// KiwiVM reuses the response envelope for the command's result, so a
// non-zero exit status arrives as ShellExec.ExitStatus rather than as
// a Go error. The error return covers transport and permission
// failures only — always check ExitStatus too.
func (c *Client) ShellExec(ctx context.Context, command string) (*ShellExec, error) {
	if strings.TrimSpace(command) == "" {
		return nil, fmt.Errorf("kiwivm: basicShell/exec needs a command")
	}
	var out ShellExec
	if err := c.callRaw(ctx, "basicShell/exec", url.Values{"command": {command}}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ScriptExec runs a shell script inside the VPS as root, detached, and
// returns the name of the log file it writes to.
func (c *Client) ScriptExec(ctx context.Context, script string) (*ScriptExec, error) {
	if strings.TrimSpace(script) == "" {
		return nil, fmt.Errorf("kiwivm: shellScript/exec needs a script")
	}
	return fetch[ScriptExec](ctx, c, "shellScript/exec", url.Values{"script": {script}})
}

// -- snapshots ------------------------------------------------------------

// Snapshots lists stored snapshots.
func (c *Client) Snapshots(ctx context.Context) (*SnapshotList, error) {
	return fetch[SnapshotList](ctx, c, "snapshot/list", nil)
}

// CreateSnapshot starts a snapshot task. description is optional.
// KiwiVM locks the VPS for the duration and emails on completion.
func (c *Client) CreateSnapshot(ctx context.Context, description string) (*SnapshotCreated, error) {
	params := url.Values{}
	if description != "" {
		params.Set("description", description)
	}
	return fetch[SnapshotCreated](ctx, c, "snapshot/create", params)
}

// DeleteSnapshot removes a snapshot by its fileName. It cannot be
// recovered.
func (c *Client) DeleteSnapshot(ctx context.Context, fileName string) error {
	if strings.TrimSpace(fileName) == "" {
		return fmt.Errorf("kiwivm: snapshot/delete needs a fileName (see Snapshots)")
	}
	return c.call(ctx, "snapshot/delete", url.Values{"snapshot": {fileName}}, nil)
}

// RestoreSnapshot overwrites the VPS with a snapshot.
func (c *Client) RestoreSnapshot(ctx context.Context, fileName string) error {
	if strings.TrimSpace(fileName) == "" {
		return fmt.Errorf("kiwivm: snapshot/restore needs a fileName (see Snapshots)")
	}
	return c.call(ctx, "snapshot/restore", url.Values{"snapshot": {fileName}}, nil)
}

// SetSnapshotSticky protects a snapshot from automatic purge, or stops
// protecting it.
func (c *Client) SetSnapshotSticky(ctx context.Context, fileName string, sticky bool) error {
	if strings.TrimSpace(fileName) == "" {
		return fmt.Errorf("kiwivm: snapshot/toggleSticky needs a fileName (see Snapshots)")
	}
	v := "0"
	if sticky {
		v = "1"
	}
	return c.call(ctx, "snapshot/toggleSticky",
		url.Values{"snapshot": {fileName}, "sticky": {v}}, nil)
}

// ExportSnapshot mints a token another instance can import with
// [Client.ImportSnapshot].
func (c *Client) ExportSnapshot(ctx context.Context, fileName string) (*SnapshotExport, error) {
	if strings.TrimSpace(fileName) == "" {
		return nil, fmt.Errorf("kiwivm: snapshot/export needs a fileName (see Snapshots)")
	}
	return fetch[SnapshotExport](ctx, c, "snapshot/export", url.Values{"snapshot": {fileName}})
}

// ImportSnapshot pulls a snapshot from another instance. Both the
// source VEID and the token come from [Client.ExportSnapshot] run
// against that instance.
func (c *Client) ImportSnapshot(ctx context.Context, sourceVeid, sourceToken string) error {
	if strings.TrimSpace(sourceVeid) == "" || strings.TrimSpace(sourceToken) == "" {
		return fmt.Errorf("kiwivm: snapshot/import needs both sourceVeid and sourceToken")
	}
	return c.call(ctx, "snapshot/import",
		url.Values{"sourceVeid": {sourceVeid}, "sourceToken": {sourceToken}}, nil)
}

// -- backups --------------------------------------------------------------

// Backups lists the automatic backups KiwiVM holds.
func (c *Client) Backups(ctx context.Context) (*BackupList, error) {
	return fetch[BackupList](ctx, c, "backup/list", nil)
}

// CopyBackupToSnapshot turns an automatic backup into a restorable
// snapshot. It does not restore anything by itself.
func (c *Client) CopyBackupToSnapshot(ctx context.Context, backupToken string) error {
	if strings.TrimSpace(backupToken) == "" {
		return fmt.Errorf("kiwivm: backup/copyToSnapshot needs a backupToken (see Backups)")
	}
	return c.call(ctx, "backup/copyToSnapshot", url.Values{"backupToken": {backupToken}}, nil)
}

// -- network ---------------------------------------------------------------

// AddIPv6 allocates a new IPv6 /64 subnet, up to the plan's limit.
func (c *Client) AddIPv6(ctx context.Context) (*IPv6Added, error) {
	return fetch[IPv6Added](ctx, c, "ipv6/add", nil)
}

// DeleteIPv6 releases an IPv6 /64 subnet back to the pool.
func (c *Client) DeleteIPv6(ctx context.Context, subnet string) error {
	if strings.TrimSpace(subnet) == "" {
		return fmt.Errorf("kiwivm: ipv6/delete needs a /64 subnet")
	}
	return c.call(ctx, "ipv6/delete", url.Values{"ip": {subnet}}, nil)
}

// AvailablePrivateIPs lists private IPv4 addresses free to assign.
func (c *Client) AvailablePrivateIPs(ctx context.Context) (*PrivateIPsAvailable, error) {
	return fetch[PrivateIPsAvailable](ctx, c, "privateIp/getAvailableIps", nil)
}

// AssignPrivateIP assigns a private IPv4 address. An empty ip lets
// KiwiVM pick one.
func (c *Client) AssignPrivateIP(ctx context.Context, ip string) (*PrivateIPsAssigned, error) {
	params := url.Values{}
	if ip != "" {
		params.Set("ip", ip)
	}
	return fetch[PrivateIPsAssigned](ctx, c, "privateIp/assign", params)
}

// DeletePrivateIP removes a private IPv4 address from the VPS.
func (c *Client) DeletePrivateIP(ctx context.Context, ip string) error {
	if strings.TrimSpace(ip) == "" {
		return fmt.Errorf("kiwivm: privateIp/delete needs an IP")
	}
	return c.call(ctx, "privateIp/delete", url.Values{"ip": {ip}}, nil)
}

// -- migration --------------------------------------------------------------

// MigrateLocations lists the locations this VPS can move to.
func (c *Client) MigrateLocations(ctx context.Context) (*MigrateLocations, error) {
	return fetch[MigrateLocations](ctx, c, "migrate/getLocations", nil)
}

// StartMigration moves the VPS to another location. Every IPv4 address
// is replaced; the old ones do not come back.
func (c *Client) StartMigration(ctx context.Context, location string) (*MigrateStarted, error) {
	if strings.TrimSpace(location) == "" {
		return nil, fmt.Errorf("kiwivm: migrate/start needs a location ID (see MigrateLocations)")
	}
	return fetch[MigrateStarted](ctx, c, "migrate/start", url.Values{"location": {location}})
}

// CloneFromExternalServer replaces this VPS with a copy of a remote
// server reachable over SSH. OpenVZ only.
func (c *Client) CloneFromExternalServer(ctx context.Context, ip, sshPort, rootPassword string) error {
	if strings.TrimSpace(ip) == "" || strings.TrimSpace(sshPort) == "" {
		return fmt.Errorf("kiwivm: cloneFromExternalServer needs the source IP and SSH port")
	}
	return c.call(ctx, "cloneFromExternalServer", url.Values{
		"externalServerIP":           {ip},
		"externalServerSSHport":      {sshPort},
		"externalServerRootPassword": {rootPassword},
	}, nil)
}

// -- suspension and policy ----------------------------------------------------

// SuspensionDetails returns suspensions, abuse points and evidence.
func (c *Client) SuspensionDetails(ctx context.Context) (*SuspensionDetails, error) {
	return fetch[SuspensionDetails](ctx, c, "getSuspensionDetails", nil)
}

// PolicyViolations returns violations awaiting resolution.
func (c *Client) PolicyViolations(ctx context.Context) (*PolicyViolations, error) {
	return fetch[PolicyViolations](ctx, c, "getPolicyViolations", nil)
}

// Unsuspend clears an abuse case and lifts the suspension. Only cases
// where Suspension.APIResolvable reports true can be cleared this way.
func (c *Client) Unsuspend(ctx context.Context, recordID string) error {
	if strings.TrimSpace(recordID) == "" {
		return fmt.Errorf("kiwivm: unsuspend needs a record_id (see SuspensionDetails)")
	}
	return c.call(ctx, "unsuspend", url.Values{"record_id": {recordID}}, nil)
}

// ResolvePolicyViolation marks a violation resolved, which is what
// stops the pending suspension. Only cases where
// PolicyViolation.APIResolvable reports true can be resolved this way.
func (c *Client) ResolvePolicyViolation(ctx context.Context, recordID string) error {
	if strings.TrimSpace(recordID) == "" {
		return fmt.Errorf("kiwivm: resolvePolicyViolation needs a record_id (see PolicyViolations)")
	}
	return c.call(ctx, "resolvePolicyViolation", url.Values{"record_id": {recordID}}, nil)
}

// -- notifications --------------------------------------------------------------

// NotificationPreferences returns the email notification settings.
func (c *Client) NotificationPreferences(ctx context.Context) (*NotificationPreferences, error) {
	return fetch[NotificationPreferences](ctx, c, "kiwivm/getNotificationPreferences", nil)
}

// SetNotificationPreferences enables or disables notifications by
// preference ID. IDs come from [Client.NotificationPreferences].
//
// KiwiVM silently ignores unknown IDs, so compare the returned Updated
// map against what was submitted rather than assuming success.
func (c *Client) SetNotificationPreferences(ctx context.Context, prefs map[string]bool) (*NotificationUpdate, error) {
	if len(prefs) == 0 {
		return nil, fmt.Errorf("kiwivm: setNotificationPreferences needs at least one preference")
	}
	payload := make(map[string]int, len(prefs))
	for id, on := range prefs {
		if on {
			payload[id] = 1
		} else {
			payload[id] = 0
		}
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return fetch[NotificationUpdate](ctx, c, "kiwivm/setNotificationPreferences",
		url.Values{"json_notification_preferences": {string(encoded)}})
}

// -- escape hatch ----------------------------------------------------------------

// Raw calls any registered endpoint and returns KiwiVM's response body
// verbatim, for callers that need a field this package does not model
// yet. veid and api_key are added automatically.
//
// The risk gate still applies: a read-only client refuses a non-read
// endpoint here exactly as it does through the typed methods. An
// unregistered endpoint is rejected, so Raw cannot be used to reach
// something whose risk nobody has classified.
//
// Non-zero "error" values are returned as *APIError, the same as
// elsewhere; the body is returned only on success.
func (c *Client) Raw(ctx context.Context, endpoint string, params url.Values) (json.RawMessage, error) {
	var body json.RawMessage
	if err := c.call(ctx, endpoint, params, &body); err != nil {
		return nil, err
	}
	return body, nil
}
