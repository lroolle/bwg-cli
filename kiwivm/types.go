package kiwivm

import (
	"encoding/json"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"
)

// envelope is the outcome KiwiVM puts in every response body. It is
// decoded separately from the payload so the public types below stay
// pure data — no "error": 0 riding along in every struct a caller
// prints.
type envelope struct {
	Error      int          `json:"error"`
	Message    string       `json:"message,omitempty"`
	Additional string       `json:"additionalErrorInfo,omitempty"`
	Locking    *LockingInfo `json:"additionalLockingInfo,omitempty"`
}

func (e envelope) err(op string) error {
	if e.Error == CodeOK {
		return nil
	}
	return &APIError{
		Op: op, Code: e.Error, Message: e.Message,
		Additional: e.Additional, Locking: e.Locking,
	}
}

// -- wire quirks -------------------------------------------------------
//
// KiwiVM is a PHP service and its JSON leaks PHP's type model: numbers
// arrive as strings, booleans as "0"/"1", and — the one that bites
// hardest — an empty associative array serializes as `[]` instead of
// `{}`. The helpers below absorb that so callers never see it.

// Int is a JSON number that KiwiVM may send as a string.
type Int int64

func (i *Int) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "" || s == "null" || s == `""` {
		*i = 0
		return nil
	}
	if s[0] == '"' {
		var str string
		if err := json.Unmarshal(b, &str); err != nil {
			return err
		}
		if str = strings.TrimSpace(str); str == "" {
			*i = 0
			return nil
		}
		// Counters occasionally arrive as decimal strings ("1234.0").
		f, err := strconv.ParseFloat(str, 64)
		if err != nil {
			return fmt.Errorf("kiwivm: %q is not a number", str)
		}
		*i = Int(f)
		return nil
	}
	var f float64
	if err := json.Unmarshal(b, &f); err != nil {
		return err
	}
	*i = Int(f)
	return nil
}

func (i Int) MarshalJSON() ([]byte, error) { return json.Marshal(int64(i)) }

// Int64 returns the value as a plain int64.
func (i Int) Int64() int64 { return int64(i) }

// Int returns the value as a plain int.
func (i Int) Int() int { return int(i) }

// Bool is a JSON boolean that KiwiVM may send as 0/1 or "0"/"1".
type Bool bool

func (v *Bool) UnmarshalJSON(b []byte) error {
	s := strings.ToLower(strings.TrimSpace(strings.Trim(string(b), `"`)))
	switch s {
	case "", "null", "0", "false", "no", "off":
		*v = false
	default:
		*v = true
	}
	return nil
}

func (v Bool) MarshalJSON() ([]byte, error) { return json.Marshal(bool(v)) }

// Bool returns the value as a plain bool.
func (v Bool) Bool() bool { return bool(v) }

// Map is a JSON object that KiwiVM may serialize as `[]` when empty,
// because that is what PHP's json_encode does to an empty associative
// array. Decoding into a plain map fails on those responses.
type Map[V any] map[string]V

func (m *Map[V]) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "null" || s == "[]" {
		*m = Map[V]{}
		return nil
	}
	raw := map[string]V{}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	*m = raw
	return nil
}

// Keys returns the map's keys sorted, so output and tests do not
// depend on Go's map iteration order.
func (m Map[V]) Keys() []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Strings is a JSON array of strings that KiwiVM may send as null, as
// an object keyed by index, or with non-string members.
type Strings []string

func (s *Strings) UnmarshalJSON(b []byte) error {
	t := strings.TrimSpace(string(b))
	if t == "null" || t == "{}" || t == `""` {
		*s = nil
		return nil
	}
	var direct []string
	if err := json.Unmarshal(b, &direct); err == nil {
		*s = direct
		return nil
	}
	var loose []any
	if err := json.Unmarshal(b, &loose); err == nil {
		out := make([]string, 0, len(loose))
		for _, v := range loose {
			out = append(out, fmt.Sprint(v))
		}
		*s = out
		return nil
	}
	var keyed map[string]any
	if err := json.Unmarshal(b, &keyed); err != nil {
		return err
	}
	keys := make([]string, 0, len(keyed))
	for k := range keyed {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, errA := strconv.Atoi(keys[i])
		bb, errB := strconv.Atoi(keys[j])
		if errA == nil && errB == nil {
			return a < bb
		}
		return keys[i] < keys[j]
	})
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, fmt.Sprint(keyed[k]))
	}
	*s = out
	return nil
}

// -- service info ------------------------------------------------------

// Nullroute describes an IP nullrouted during a (D)DoS attack.
type Nullroute struct {
	// IP is filled in from the map key.
	IP        string `json:"ip"`
	Timestamp Int    `json:"nullroute_timestamp"`
	DurationS Int    `json:"nullroute_duration_s"`
	// Log is the raw packet dump KiwiVM captured. It can be long.
	Log string `json:"log,omitempty"`
}

// StartedAt returns when the nullroute began.
func (n Nullroute) StartedAt() time.Time { return time.Unix(n.Timestamp.Int64(), 0) }

// ExpiresAt returns when the nullroute lifts, and whether KiwiVM gave
// enough detail to say.
func (n Nullroute) ExpiresAt() (time.Time, bool) {
	if n.Timestamp <= 0 || n.DurationS <= 0 {
		return time.Time{}, false
	}
	return n.StartedAt().Add(time.Duration(n.DurationS.Int64()) * time.Second), true
}

// Nullroutes maps an IP address to its nullroute detail. KiwiVM sends
// `[]` when nothing is nullrouted, an object when something is, and
// occasionally a bare array of IPs carrying no detail.
type Nullroutes map[string]Nullroute

func (n *Nullroutes) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "null" || s == "[]" {
		*n = Nullroutes{}
		return nil
	}
	obj := map[string]Nullroute{}
	if err := json.Unmarshal(b, &obj); err == nil {
		for ip, nr := range obj {
			nr.IP = ip
			obj[ip] = nr
		}
		*n = obj
		return nil
	}
	var ips []string
	if err := json.Unmarshal(b, &ips); err != nil {
		return err
	}
	out := make(Nullroutes, len(ips))
	for _, ip := range ips {
		out[ip] = Nullroute{IP: ip}
	}
	*n = out
	return nil
}

// ServiceInfo is the plan, location, network and quota state of a VPS,
// from getServiceInfo.
type ServiceInfo struct {
	VMType   string `json:"vm_type"`
	Hostname string `json:"hostname"`
	Plan     string `json:"plan"`
	OS       string `json:"os"`
	Email    string `json:"email"`

	NodeAlias         string `json:"node_alias"`
	NodeLocationID    string `json:"node_location_id"`
	NodeLocation      string `json:"node_location"`
	NodeDatacenter    string `json:"node_datacenter"`
	LocationIPv6Ready Bool   `json:"location_ipv6_ready"`

	PlanDisk Int `json:"plan_disk"`
	PlanRAM  Int `json:"plan_ram"`
	PlanSwap Int `json:"plan_swap"`

	// PlanMonthlyData and DataCounter are raw counters. Both are scaled
	// by MonthlyDataMultiplier for the figures KiwiVM displays; use
	// [ServiceInfo.Bandwidth] rather than reading them directly.
	PlanMonthlyData       Int `json:"plan_monthly_data"`
	DataCounter           Int `json:"data_counter"`
	MonthlyDataMultiplier Int `json:"monthly_data_multiplier"`
	DataNextReset         Int `json:"data_next_reset"`

	IPAddresses           Strings    `json:"ip_addresses"`
	PrivateIPAddresses    Strings    `json:"private_ip_addresses"`
	IPv6SitTunnelEndpoint string     `json:"ipv6_sit_tunnel_endpoint,omitempty"`
	IPNullroutes          Nullroutes `json:"ip_nullroutes"`
	PlanMaxIPv6s          Int        `json:"plan_max_ipv6s"`

	ISO1          string  `json:"iso1,omitempty"`
	ISO2          string  `json:"iso2,omitempty"`
	AvailableISOs Strings `json:"available_isos"`

	PlanPrivateNetworkAvailable     Bool        `json:"plan_private_network_available"`
	LocationPrivateNetworkAvailable Bool        `json:"location_private_network_available"`
	RDNSAPIAvailable                Bool        `json:"rdns_api_available"`
	PTR                             Map[string] `json:"ptr"`

	Suspended        Bool `json:"suspended"`
	PolicyViolation  Bool `json:"policy_violation"`
	SuspensionCount  Int  `json:"suspension_count"`
	TotalAbusePoints Int  `json:"total_abuse_points"`
	MaxAbusePoints   Int  `json:"max_abuse_points"`
}

// Bandwidth is the monthly transfer picture, multiplier applied.
type Bandwidth struct {
	// Used, Total and Free are bytes with the multiplier applied.
	Used  int64 `json:"used"`
	Total int64 `json:"total"`
	Free  int64 `json:"free"`
	// Percent is Used/Total as 0-100. The multiplier scales both sides
	// equally, so this is the one figure that is right regardless of
	// how the multiplier is interpreted.
	Percent float64 `json:"percent"`
	// Multiplier is the location's bandwidth accounting coefficient.
	Multiplier int64 `json:"multiplier"`
	// ResetsAt is when the counter rolls over; zero if unknown.
	ResetsAt time.Time `json:"resetsAt"`
}

// ResetsIn returns the time until the transfer counter resets, or zero
// if the reset time is unknown or already past.
func (b Bandwidth) ResetsIn() time.Duration {
	if b.ResetsAt.IsZero() {
		return 0
	}
	if d := time.Until(b.ResetsAt); d > 0 {
		return d
	}
	return 0
}

// Bandwidth reports monthly transfer with the location multiplier
// applied to both the allowance and the counter, matching the KiwiVM
// panel.
func (s *ServiceInfo) Bandwidth() Bandwidth {
	mult := s.MonthlyDataMultiplier.Int64()
	if mult <= 0 {
		mult = 1
	}
	used := s.DataCounter.Int64() * mult
	total := s.PlanMonthlyData.Int64() * mult

	b := Bandwidth{Used: used, Total: total, Multiplier: mult}
	if total > used {
		b.Free = total - used
	}
	if total > 0 {
		b.Percent = float64(used) / float64(total) * 100
	}
	if ts := s.DataNextReset.Int64(); ts > 0 {
		b.ResetsAt = time.Unix(ts, 0)
	}
	return b
}

// IPv4 returns the assigned IPv4 addresses.
func (s *ServiceInfo) IPv4() []string { return filterIPs(s.IPAddresses, false) }

// IPv6 returns the assigned IPv6 /64 subnets. KiwiVM mixes them into
// the same ip_addresses array as the IPv4 addresses.
func (s *ServiceInfo) IPv6() []string { return filterIPs(s.IPAddresses, true) }

// PrimaryIP returns the first IPv4 address, falling back to the first
// IPv6 subnet, or "" when the VPS has neither.
func (s *ServiceInfo) PrimaryIP() string {
	if v4 := s.IPv4(); len(v4) > 0 {
		return v4[0]
	}
	if v6 := s.IPv6(); len(v6) > 0 {
		return v6[0]
	}
	return ""
}

// AbusePercent returns accumulated abuse points as a share of the
// plan's yearly limit, 0-100. Zero when the limit is unknown.
func (s *ServiceInfo) AbusePercent() float64 {
	if s.MaxAbusePoints <= 0 {
		return 0
	}
	return float64(s.TotalAbusePoints) / float64(s.MaxAbusePoints) * 100
}

// Healthy reports whether nothing demands attention: not suspended, no
// open policy violation, no live nullroute.
func (s *ServiceInfo) Healthy() bool {
	return !s.Suspended.Bool() && !s.PolicyViolation.Bool() && len(s.IPNullroutes) == 0
}

func filterIPs(addrs []string, wantV6 bool) []string {
	var out []string
	for _, a := range addrs {
		host := strings.TrimSpace(a)
		if host == "" {
			continue
		}
		// IPv6 subnets arrive as "2001:db8::/64".
		probe := host
		if base, _, ok := strings.Cut(host, "/"); ok {
			probe = base
		}
		ip := net.ParseIP(probe)
		if ip == nil {
			continue
		}
		if (ip.To4() == nil) == wantV6 {
			out = append(out, host)
		}
	}
	return out
}

// LiveServiceInfo is [ServiceInfo] plus guest-reported state, from
// getLiveServiceInfo. Which hypervisor-specific group is populated
// depends on VMType.
type LiveServiceInfo struct {
	ServiceInfo

	IsCPUThrottled Bool `json:"is_cpu_throttled"`
	SSHPort        Int  `json:"ssh_port"`

	// OpenVZ
	VzStatus map[string]any `json:"vz_status,omitempty"`
	VzQuota  map[string]any `json:"vz_quota,omitempty"`

	// KVM
	VeStatus            string `json:"ve_status,omitempty"`
	VeMac1              string `json:"ve_mac1,omitempty"`
	VeUsedDiskSpaceB    Int    `json:"ve_used_disk_space_b,omitempty"`
	VeDiskQuotaGB       Int    `json:"ve_disk_quota_gb,omitempty"`
	IsDiskThrottled     Bool   `json:"is_disk_throttled,omitempty"`
	LiveHostname        string `json:"live_hostname,omitempty"`
	LoadAverage         string `json:"load_average,omitempty"`
	MemAvailableKB      Int    `json:"mem_available_kb,omitempty"`
	SwapTotalKB         Int    `json:"swap_total_kb,omitempty"`
	SwapAvailableKB     Int    `json:"swap_available_kb,omitempty"`
	ScreendumpPNGBase64 string `json:"screendump_png_base64,omitempty"`
}

// State returns a normalized power state: "running", "stopped",
// "starting", or "unknown". KVM reports it directly; OpenVZ has no
// state field, so a populated vz_status is the only signal.
func (l *LiveServiceInfo) State() string {
	if l.VeStatus != "" {
		return strings.ToLower(l.VeStatus)
	}
	if len(l.VzStatus) > 0 {
		return "running"
	}
	return "unknown"
}

// Running reports whether the guest is up.
func (l *LiveServiceInfo) Running() bool { return l.State() == "running" }

// DiskUsedBytes returns occupied disk space, and false when the
// hypervisor does not report it. OpenVZ exposes this through vz_quota
// in 1 KiB blocks rather than a dedicated byte field.
func (l *LiveServiceInfo) DiskUsedBytes() (int64, bool) {
	if l.VeUsedDiskSpaceB > 0 {
		return l.VeUsedDiskSpaceB.Int64(), true
	}
	if v, ok := numFromAny(l.VzQuota["disk_used"]); ok {
		return v * 1024, true
	}
	return 0, false
}

// DiskTotalBytes returns the disk quota, preferring the live figure
// and falling back to the plan.
func (l *LiveServiceInfo) DiskTotalBytes() (int64, bool) {
	if l.VeDiskQuotaGB > 0 {
		return l.VeDiskQuotaGB.Int64() * 1024 * 1024 * 1024, true
	}
	if v, ok := numFromAny(l.VzQuota["disk_hard"]); ok {
		return v * 1024, true
	}
	if l.PlanDisk > 0 {
		return l.PlanDisk.Int64(), true
	}
	return 0, false
}

// MemUsedBytes returns used RAM, and false when the hypervisor does
// not report enough to compute it.
func (l *LiveServiceInfo) MemUsedBytes() (int64, bool) {
	if l.MemAvailableKB > 0 && l.PlanRAM > 0 {
		used := l.PlanRAM.Int64() - l.MemAvailableKB.Int64()*1024
		if used < 0 {
			used = 0
		}
		return used, true
	}
	if v, ok := numFromAny(l.VzStatus["physpages"]); ok {
		return v * 4096, true // OpenVZ counts 4 KiB pages
	}
	return 0, false
}

// numFromAny reads a number out of a decoded JSON value. OpenVZ
// beancounters arrive as {held, maxheld, barrier, limit} objects, so
// an object falls through to its "held" member.
func numFromAny(v any) (int64, bool) {
	switch t := v.(type) {
	case float64:
		return int64(t), true
	case int64:
		return t, true
	case json.Number:
		n, err := t.Int64()
		return n, err == nil
	case string:
		n, err := strconv.ParseInt(strings.TrimSpace(t), 10, 64)
		return n, err == nil
	case map[string]any:
		if h, ok := t["held"]; ok {
			return numFromAny(h)
		}
	}
	return 0, false
}

// -- OS, SSH, password -------------------------------------------------

// AvailableOS is the installed OS plus the installable templates.
type AvailableOS struct {
	Installed string  `json:"installed"`
	Templates Strings `json:"templates"`
}

// ReinstallResult is what reinstallOS hands back. The root password
// appears exactly once, here; nothing retrieves it later.
type ReinstallResult struct {
	RootPassword      string  `json:"rootPassword"`
	SSHPort           Int     `json:"sshPort"`
	SSHKeys           Strings `json:"sshKeys"`
	SSHKeysBrief      Strings `json:"sshKeysBrief"`
	NotificationEmail string  `json:"notificationEmail"`
}

// SSHKeys are the keys reinstallOS will install, from both storage
// tiers. Each field is a newline-separated key list; the slice
// accessors split them.
type SSHKeys struct {
	Veid               string `json:"ssh_keys_veid"`
	User               string `json:"ssh_keys_user"`
	Preferred          string `json:"ssh_keys_preferred"`
	ShortenedVeid      string `json:"shortened_ssh_keys_veid"`
	ShortenedUser      string `json:"shortened_ssh_keys_user"`
	ShortenedPreferred string `json:"shortened_ssh_keys_preferred"`
}

// VeidSlice returns the per-VM keys held in Hypervisor Vault.
func (k *SSHKeys) VeidSlice() []string { return splitKeys(k.Veid) }

// UserSlice returns the account-level keys from the billing portal.
func (k *SSHKeys) UserSlice() []string { return splitKeys(k.User) }

// PreferredSlice returns the keys reinstallOS will actually install.
// Per-VM keys shadow account-level keys entirely.
func (k *SSHKeys) PreferredSlice() []string { return splitKeys(k.Preferred) }

func splitKeys(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// RootPassword is the result of resetRootPassword.
type RootPassword struct {
	Password string `json:"password"`
}

// -- usage and audit ---------------------------------------------------

// UsageSample is one interval of resource usage.
type UsageSample struct {
	Timestamp       Int `json:"timestamp"`
	CPUUsage        Int `json:"cpu_usage"`
	NetworkInBytes  Int `json:"network_in_bytes"`
	NetworkOutBytes Int `json:"network_out_bytes"`
	DiskReadBytes   Int `json:"disk_read_bytes"`
	DiskWriteBytes  Int `json:"disk_write_bytes"`
}

// Time returns the sample's timestamp.
func (u UsageSample) Time() time.Time { return time.Unix(u.Timestamp.Int64(), 0) }

// UsageStats is the sampled usage series from getRawUsageStats.
type UsageStats struct {
	VMType string        `json:"vm_type"`
	Data   []UsageSample `json:"data"`
}

// Totals sums network and disk bytes across every sample.
func (u *UsageStats) Totals() (netIn, netOut, diskRead, diskWrite int64) {
	for _, s := range u.Data {
		netIn += s.NetworkInBytes.Int64()
		netOut += s.NetworkOutBytes.Int64()
		diskRead += s.DiskReadBytes.Int64()
		diskWrite += s.DiskWriteBytes.Int64()
	}
	return
}

// Window returns the time span the samples cover.
func (u *UsageStats) Window() (start, end time.Time) {
	for i, s := range u.Data {
		t := s.Time()
		if i == 0 || t.Before(start) {
			start = t
		}
		if i == 0 || t.After(end) {
			end = t
		}
	}
	return
}

// AuditEntry is one KiwiVM control-panel event.
type AuditEntry struct {
	Timestamp     Int    `json:"timestamp"`
	RequestorIPv4 Int    `json:"requestor_ipv4"`
	Type          Int    `json:"type"`
	Summary       string `json:"summary"`
}

// Time returns the event time.
func (a AuditEntry) Time() time.Time { return time.Unix(a.Timestamp.Int64(), 0) }

// RequestorIP renders the requestor address, which KiwiVM encodes as a
// 32-bit integer rather than a string. Returns "" when out of range.
func (a AuditEntry) RequestorIP() string {
	v := a.RequestorIPv4.Int64()
	if v <= 0 || v > 0xFFFFFFFF {
		return ""
	}
	u := uint32(v)
	return net.IPv4(byte(u>>24), byte(u>>16), byte(u>>8), byte(u)).String()
}

// AuditLog is the response from getAuditLog.
type AuditLog struct {
	LogEntries []AuditEntry `json:"log_entries"`
}

// RateLimit is the remaining API budget from getRateLimitStatus.
type RateLimit struct {
	Remaining15Min Int `json:"remaining_points_15min"`
	Remaining24H   Int `json:"remaining_points_24h"`
}

// -- snapshots and backups ---------------------------------------------

// Snapshot is one stored snapshot.
type Snapshot struct {
	FileName        string `json:"fileName"`
	OS              string `json:"os"`
	Description     string `json:"description"`
	Size            Int    `json:"size"`
	Uncompressed    Int    `json:"uncompressed"`
	MD5             string `json:"md5"`
	Sticky          Bool   `json:"sticky"`
	PurgesIn        Int    `json:"purgesIn"`
	DownloadLink    string `json:"downloadLink"`
	DownloadLinkSSL string `json:"downloadLinkSSL"`
}

// PurgesAt returns when an unprotected snapshot will be purged. Sticky
// snapshots are never purged, so ok is false for them.
func (s Snapshot) PurgesAt() (time.Time, bool) {
	if s.Sticky.Bool() || s.PurgesIn <= 0 {
		return time.Time{}, false
	}
	return time.Now().Add(time.Duration(s.PurgesIn.Int64()) * time.Second), true
}

// SnapshotList is the response from snapshot/list.
type SnapshotList struct {
	Snapshots []Snapshot `json:"snapshots"`
}

// SnapshotCreated is the response from snapshot/create.
type SnapshotCreated struct {
	NotificationEmail string `json:"notificationEmail"`
}

// SnapshotExport carries the token snapshot/import consumes.
type SnapshotExport struct {
	Token string `json:"token"`
}

// Backup is one automatic backup.
type Backup struct {
	// Token identifies the backup for backup/copyToSnapshot. KiwiVM
	// usually supplies it as the map key rather than a field, so prefer
	// [BackupList.Sorted], which fills it in.
	Token     string `json:"backupToken"`
	Size      Int    `json:"size"`
	OS        string `json:"os"`
	MD5       string `json:"md5"`
	Timestamp Int    `json:"timestamp"`
}

// Time returns when the backup was taken.
func (b Backup) Time() time.Time { return time.Unix(b.Timestamp.Int64(), 0) }

// BackupList is the response from backup/list. KiwiVM returns backups
// as an object keyed by token.
type BackupList struct {
	Backups Map[Backup] `json:"backups"`
}

// Sorted returns the backups newest first, each with Token populated.
func (b *BackupList) Sorted() []Backup {
	out := make([]Backup, 0, len(b.Backups))
	for token, bk := range b.Backups {
		if bk.Token == "" {
			bk.Token = token
		}
		out = append(out, bk)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Timestamp != out[j].Timestamp {
			return out[i].Timestamp > out[j].Timestamp
		}
		return out[i].Token < out[j].Token
	})
	return out
}

// -- network -----------------------------------------------------------

// IPv6Added is the response from ipv6/add.
type IPv6Added struct {
	AssignedSubnet string `json:"assigned_subnet"`
}

// PrivateIPsAvailable is the response from privateIp/getAvailableIps.
type PrivateIPsAvailable struct {
	AvailableIPs Strings `json:"available_ips"`
}

// PrivateIPsAssigned is the response from privateIp/assign.
type PrivateIPsAssigned struct {
	AssignedIPs Strings `json:"assigned_ips"`
}

// -- migration ---------------------------------------------------------

// MigrateLocations is the response from migrate/getLocations.
type MigrateLocations struct {
	CurrentLocation         string      `json:"currentLocation"`
	Locations               Strings     `json:"locations"`
	Descriptions            Map[string] `json:"descriptions"`
	DataTransferMultipliers Map[Int]    `json:"dataTransferMultipliers"`
}

// MigrateStarted is the response from migrate/start.
type MigrateStarted struct {
	NotificationEmail string  `json:"notificationEmail"`
	NewIPs            Strings `json:"newIps"`
}

// -- abuse -------------------------------------------------------------

// Suspension is one outstanding suspension case.
type Suspension struct {
	RecordID         Int    `json:"record_id"`
	Flag             string `json:"flag"`
	IsSoft           Bool   `json:"is_soft"`
	EvidenceRecordID Int    `json:"evidence_record_id"`
	AbusePoints      Int    `json:"abuse_points"`
}

// APIResolvable reports whether [Client.Unsuspend] can clear this
// case, or whether it needs a support ticket.
func (s Suspension) APIResolvable() bool { return s.IsSoft.Bool() }

// SuspensionDetails is the response from getSuspensionDetails.
type SuspensionDetails struct {
	SuspensionCount  Int          `json:"suspension_count"`
	TotalAbusePoints Int          `json:"total_abuse_points"`
	MaxAbusePoints   Int          `json:"max_abuse_points"`
	Suspensions      []Suspension `json:"suspensions,omitempty"`
	// Evidence maps an evidence record ID to the complaint text.
	Evidence Map[string] `json:"evidence,omitempty"`
}

// PolicyViolation is one unresolved policy violation.
type PolicyViolation struct {
	RecordID     Int    `json:"record_id"`
	Timestamp    Int    `json:"timestamp"`
	SuspendAt    Int    `json:"suspend_at"`
	Flag         string `json:"flag"`
	IsSoft       Bool   `json:"is_soft"`
	AbusePoints  Int    `json:"abuse_points"`
	EvidenceData string `json:"evidence_data"`
}

// APIResolvable reports whether [Client.ResolvePolicyViolation] can
// clear this case, or whether it needs a support ticket.
func (p PolicyViolation) APIResolvable() bool { return p.IsSoft.Bool() }

// SuspendsAt returns the deadline after which the service is
// suspended, and whether one was given.
func (p PolicyViolation) SuspendsAt() (time.Time, bool) {
	if p.SuspendAt <= 0 {
		return time.Time{}, false
	}
	return time.Unix(p.SuspendAt.Int64(), 0), true
}

// PolicyViolations is the response from getPolicyViolations.
type PolicyViolations struct {
	TotalAbusePoints Int               `json:"total_abuse_points"`
	MaxAbusePoints   Int               `json:"max_abuse_points"`
	PolicyViolations []PolicyViolation `json:"policy_violations,omitempty"`
}

// -- notifications -----------------------------------------------------

// NotificationPreference is one email notification setting.
type NotificationPreference struct {
	FriendlyDescription string `json:"friendly_description"`
	IsEnabled           Bool   `json:"is_enabled"`
	ChangedTimestamp    Int    `json:"changed_timestamp"`
	SValue              string `json:"s_value"`
}

// NotificationPreferences is the response from
// kiwivm/getNotificationPreferences. KiwiVM groups preferences by
// category, so EmailPreferences is category -> preference ID -> value.
type NotificationPreferences struct {
	EmailPreferences  Map[Map[NotificationPreference]] `json:"email_preferences"`
	NotificationEmail string                           `json:"notificationEmail"`
}

// Flat returns every preference keyed by its ID, dropping the category
// grouping that only matters for panel layout.
func (n *NotificationPreferences) Flat() map[string]NotificationPreference {
	out := map[string]NotificationPreference{}
	for _, group := range n.EmailPreferences {
		for id, pref := range group {
			out[id] = pref
		}
	}
	return out
}

// NotificationUpdate is the response from
// kiwivm/setNotificationPreferences. Updated lists only what actually
// changed, which can be narrower than Submitted.
type NotificationUpdate struct {
	Submitted    Map[Int]    `json:"submitted_email_preferences"`
	Updated      Map[Int]    `json:"updated_email_preferences"`
	Descriptions Map[string] `json:"friendly_descriptions"`
}

// -- shell -------------------------------------------------------------

// ShellCD is the response from basicShell/cd.
type ShellCD struct {
	PWD string `json:"pwd"`
}

// ShellExec is the response from basicShell/exec. KiwiVM overloads the
// shared envelope here: "error" carries the command's exit status and
// "message" its console output, so a non-zero exit is not an API
// failure. [Client.ShellExec] accounts for that.
type ShellExec struct {
	ExitStatus int    `json:"error"`
	Output     string `json:"message"`
}

// ScriptExec is the response from shellScript/exec.
type ScriptExec struct {
	// Log is the name of the output log file inside the VPS.
	Log string `json:"log"`
}
