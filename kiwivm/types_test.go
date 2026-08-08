package kiwivm

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

// Every fixture below is shaped like something KiwiVM actually
// returns: PHP's json_encode turning empty associative arrays into
// [], numbers arriving as strings, booleans as "0"/"1".

func TestIntAcceptsWhatPHPSends(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{`42`, 42},
		{`"42"`, 42},
		{`"1234.0"`, 1234},
		{`0`, 0},
		{`""`, 0},
		{`null`, 0},
		{`-7`, -7},
		{`1099511627776`, 1099511627776}, // beyond float32, common for disk quotas
	}
	for _, c := range cases {
		var got Int
		if err := json.Unmarshal([]byte(c.in), &got); err != nil {
			t.Errorf("Int(%s): %v", c.in, err)
			continue
		}
		if got.Int64() != c.want {
			t.Errorf("Int(%s) = %d, want %d", c.in, got.Int64(), c.want)
		}
	}

	var bad Int
	if err := json.Unmarshal([]byte(`"not a number"`), &bad); err == nil {
		t.Error("Int accepted a non-numeric string")
	}
}

func TestIntRoundTripsAsANumber(t *testing.T) {
	b, err := json.Marshal(Int(1234))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "1234" {
		t.Errorf("Int marshals as %s, want a bare number", b)
	}
}

func TestBoolAcceptsWhatPHPSends(t *testing.T) {
	cases := map[string]bool{
		`1`: true, `0`: false,
		`"1"`: true, `"0"`: false,
		`true`: true, `false`: false,
		`"true"`: true, `"false"`: false,
		`null`: false, `""`: false,
	}
	for in, want := range cases {
		var got Bool
		if err := json.Unmarshal([]byte(in), &got); err != nil {
			t.Errorf("Bool(%s): %v", in, err)
			continue
		}
		if got.Bool() != want {
			t.Errorf("Bool(%s) = %v, want %v", in, got.Bool(), want)
		}
	}
}

// An empty associative array is the quirk that breaks naive clients:
// PHP renders it as [] and a plain map[string]T decode fails.
func TestMapAcceptsEmptyArray(t *testing.T) {
	var m Map[string]
	if err := json.Unmarshal([]byte(`[]`), &m); err != nil {
		t.Fatalf("Map from []: %v", err)
	}
	if m == nil || len(m) != 0 {
		t.Errorf("Map from [] = %#v, want an empty non-nil map", m)
	}

	if err := json.Unmarshal([]byte(`{"1.2.3.4":"ns1.example.com"}`), &m); err != nil {
		t.Fatalf("Map from object: %v", err)
	}
	if m["1.2.3.4"] != "ns1.example.com" {
		t.Errorf("Map lost its value: %#v", m)
	}

	var null Map[string]
	if err := json.Unmarshal([]byte(`null`), &null); err != nil || len(null) != 0 {
		t.Errorf("Map from null = %#v, %v", null, err)
	}
}

func TestMapKeysAreSorted(t *testing.T) {
	m := Map[int]{"c": 3, "a": 1, "b": 2}
	if got := m.Keys(); !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Errorf("Keys() = %v, want sorted", got)
	}
}

func TestStringsAcceptsLooseArrays(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{`["a","b"]`, []string{"a", "b"}},
		{`null`, nil},
		{`{}`, nil},
		{`{"0":"a","1":"b","10":"c"}`, []string{"a", "b", "c"}}, // numeric keys sort numerically
		{`[1,2]`, []string{"1", "2"}},
	}
	for _, c := range cases {
		var got Strings
		if err := json.Unmarshal([]byte(c.in), &got); err != nil {
			t.Errorf("Strings(%s): %v", c.in, err)
			continue
		}
		if !reflect.DeepEqual([]string(got), c.want) {
			t.Errorf("Strings(%s) = %#v, want %#v", c.in, []string(got), c.want)
		}
	}
}

func TestNullroutesAcceptsAllThreeShapes(t *testing.T) {
	var empty Nullroutes
	if err := json.Unmarshal([]byte(`[]`), &empty); err != nil || len(empty) != 0 {
		t.Errorf("Nullroutes from []: %#v, %v", empty, err)
	}

	var detailed Nullroutes
	body := `{"1.2.3.4":{"nullroute_timestamp":1556678627,"nullroute_duration_s":360,"log":"dump"}}`
	if err := json.Unmarshal([]byte(body), &detailed); err != nil {
		t.Fatal(err)
	}
	nr := detailed["1.2.3.4"]
	if nr.IP != "1.2.3.4" {
		t.Errorf("IP not filled from the map key: %q", nr.IP)
	}
	exp, ok := nr.ExpiresAt()
	if !ok {
		t.Fatal("ExpiresAt() not computable from a full record")
	}
	if want := time.Unix(1556678627+360, 0); !exp.Equal(want) {
		t.Errorf("ExpiresAt() = %v, want %v", exp, want)
	}

	var bare Nullroutes
	if err := json.Unmarshal([]byte(`["5.6.7.8"]`), &bare); err != nil {
		t.Fatal(err)
	}
	if bare["5.6.7.8"].IP != "5.6.7.8" {
		t.Errorf("bare-array form lost the IP: %#v", bare)
	}
	if _, ok := bare["5.6.7.8"].ExpiresAt(); ok {
		t.Error("ExpiresAt() claimed a value with no timestamp")
	}
}

// The bandwidth multiplier is the figure everyone gets wrong. The
// percentage must survive it because it scales both sides equally.
func TestBandwidthAppliesTheMultiplier(t *testing.T) {
	const gb = 1024 * 1024 * 1024
	reset := time.Now().Add(72 * time.Hour).Unix()

	s := &ServiceInfo{
		PlanMonthlyData:       Int(1000 * gb),
		DataCounter:           Int(250 * gb),
		MonthlyDataMultiplier: 3,
		DataNextReset:         Int(reset),
	}
	b := s.Bandwidth()

	if b.Total != 3000*gb {
		t.Errorf("Total = %d GB, want 3000 GB", b.Total/gb)
	}
	if b.Used != 750*gb {
		t.Errorf("Used = %d GB, want 750 GB", b.Used/gb)
	}
	if b.Free != 2250*gb {
		t.Errorf("Free = %d GB, want 2250 GB", b.Free/gb)
	}
	if b.Percent < 24.99 || b.Percent > 25.01 {
		t.Errorf("Percent = %.2f, want 25", b.Percent)
	}
	if b.ResetsIn() < 71*time.Hour {
		t.Errorf("ResetsIn() = %v, want ~72h", b.ResetsIn())
	}

	// Same ratio, no multiplier: the percentage must not move.
	plain := &ServiceInfo{PlanMonthlyData: Int(1000 * gb), DataCounter: Int(250 * gb)}
	if pb := plain.Bandwidth(); pb.Percent != b.Percent {
		t.Errorf("multiplier changed Percent: %.4f vs %.4f", pb.Percent, b.Percent)
	}
}

func TestBandwidthSurvivesMissingFields(t *testing.T) {
	var s ServiceInfo
	b := s.Bandwidth()
	if b.Multiplier != 1 {
		t.Errorf("absent multiplier = %d, want it defaulted to 1", b.Multiplier)
	}
	if b.Percent != 0 || b.Free != 0 {
		t.Errorf("empty info produced %+v", b)
	}
	if !b.ResetsAt.IsZero() || b.ResetsIn() != 0 {
		t.Error("no reset timestamp should mean no reset time")
	}

	// Over quota: Free floors at zero rather than going negative.
	over := &ServiceInfo{PlanMonthlyData: 100, DataCounter: 150}
	if ob := over.Bandwidth(); ob.Free != 0 || ob.Percent != 150 {
		t.Errorf("over-quota = %+v, want Free 0 and Percent 150", ob)
	}
}

func TestIPAddressesSplitByFamily(t *testing.T) {
	s := &ServiceInfo{IPAddresses: Strings{
		"1.2.3.4", "2001:db8::/64", "5.6.7.8", "not-an-ip", "", "2001:db8:1::",
	}}

	if got, want := s.IPv4(), []string{"1.2.3.4", "5.6.7.8"}; !reflect.DeepEqual(got, want) {
		t.Errorf("IPv4() = %v, want %v", got, want)
	}
	if got, want := s.IPv6(), []string{"2001:db8::/64", "2001:db8:1::"}; !reflect.DeepEqual(got, want) {
		t.Errorf("IPv6() = %v, want %v", got, want)
	}
	if s.PrimaryIP() != "1.2.3.4" {
		t.Errorf("PrimaryIP() = %q, want the first IPv4", s.PrimaryIP())
	}

	v6only := &ServiceInfo{IPAddresses: Strings{"2001:db8::/64"}}
	if v6only.PrimaryIP() != "2001:db8::/64" {
		t.Errorf("PrimaryIP() on a v6-only box = %q", v6only.PrimaryIP())
	}
	if (&ServiceInfo{}).PrimaryIP() != "" {
		t.Error("PrimaryIP() invented an address")
	}
}

func TestHealthyAndAbusePercent(t *testing.T) {
	ok := &ServiceInfo{MaxAbusePoints: 1500, TotalAbusePoints: 300}
	if !ok.Healthy() {
		t.Error("a clean VPS reported unhealthy")
	}
	if p := ok.AbusePercent(); p < 19.9 || p > 20.1 {
		t.Errorf("AbusePercent() = %.2f, want 20", p)
	}
	if (&ServiceInfo{TotalAbusePoints: 5}).AbusePercent() != 0 {
		t.Error("AbusePercent() divided by an unknown limit")
	}

	for name, s := range map[string]*ServiceInfo{
		"suspended": {Suspended: true},
		"violation": {PolicyViolation: true},
		"nullroute": {IPNullroutes: Nullroutes{"1.2.3.4": {}}},
	} {
		if s.Healthy() {
			t.Errorf("%s VPS reported healthy", name)
		}
	}
}

func TestLiveStateAcrossHypervisors(t *testing.T) {
	kvm := &LiveServiceInfo{VeStatus: "Running"}
	if kvm.State() != "running" || !kvm.Running() {
		t.Errorf("KVM Running: State()=%q", kvm.State())
	}
	stopped := &LiveServiceInfo{VeStatus: "Stopped"}
	if stopped.State() != "stopped" || stopped.Running() {
		t.Errorf("KVM Stopped: State()=%q", stopped.State())
	}
	// OpenVZ has no state field at all; beancounters are the signal.
	ovz := &LiveServiceInfo{VzStatus: map[string]any{"numproc": 12.0}}
	if ovz.State() != "running" || !ovz.Running() {
		t.Errorf("OpenVZ with beancounters: State()=%q", ovz.State())
	}
	if (&LiveServiceInfo{}).State() != "unknown" {
		t.Error("an empty payload should be unknown, not stopped")
	}
}

func TestLiveResourceHelpers(t *testing.T) {
	const gib = 1024 * 1024 * 1024

	kvm := &LiveServiceInfo{
		VeUsedDiskSpaceB: Int(5 * gib),
		VeDiskQuotaGB:    20,
		MemAvailableKB:   Int(512 * 1024),
	}
	kvm.PlanRAM = Int(1 * gib)

	if used, ok := kvm.DiskUsedBytes(); !ok || used != 5*gib {
		t.Errorf("KVM DiskUsedBytes() = %d, %v", used, ok)
	}
	if total, ok := kvm.DiskTotalBytes(); !ok || total != 20*gib {
		t.Errorf("KVM DiskTotalBytes() = %d, %v", total, ok)
	}
	if used, ok := kvm.MemUsedBytes(); !ok || used != 512*1024*1024 {
		t.Errorf("KVM MemUsedBytes() = %d, %v", used, ok)
	}

	// OpenVZ reports through quota blocks (1 KiB) and 4 KiB pages, and
	// wraps beancounters in {held,...} objects.
	ovz := &LiveServiceInfo{
		VzQuota:  map[string]any{"disk_used": 1024.0, "disk_hard": 4096.0},
		VzStatus: map[string]any{"physpages": map[string]any{"held": 256.0}},
	}
	if used, ok := ovz.DiskUsedBytes(); !ok || used != 1024*1024 {
		t.Errorf("OpenVZ DiskUsedBytes() = %d, %v", used, ok)
	}
	if total, ok := ovz.DiskTotalBytes(); !ok || total != 4096*1024 {
		t.Errorf("OpenVZ DiskTotalBytes() = %d, %v", total, ok)
	}
	if used, ok := ovz.MemUsedBytes(); !ok || used != 256*4096 {
		t.Errorf("OpenVZ MemUsedBytes() = %d, %v", used, ok)
	}

	if _, ok := (&LiveServiceInfo{}).DiskUsedBytes(); ok {
		t.Error("DiskUsedBytes() invented a figure from nothing")
	}
	if _, ok := (&LiveServiceInfo{}).MemUsedBytes(); ok {
		t.Error("MemUsedBytes() invented a figure from nothing")
	}
}

func TestBackupsSortNewestFirstWithTokens(t *testing.T) {
	var bl BackupList
	body := `{"backups":{"tok-old":{"size":1,"timestamp":100},"tok-new":{"size":2,"timestamp":300}}}`
	if err := json.Unmarshal([]byte(body), &bl); err != nil {
		t.Fatal(err)
	}
	got := bl.Sorted()
	if len(got) != 2 {
		t.Fatalf("got %d backups, want 2", len(got))
	}
	if got[0].Token != "tok-new" || got[1].Token != "tok-old" {
		t.Errorf("order = %q, %q; want newest first with tokens from the map keys",
			got[0].Token, got[1].Token)
	}
	if !got[0].Time().Equal(time.Unix(300, 0)) {
		t.Errorf("Time() = %v", got[0].Time())
	}

	var none BackupList
	if err := json.Unmarshal([]byte(`{"backups":[]}`), &none); err != nil {
		t.Fatalf("empty backups as []: %v", err)
	}
	if len(none.Sorted()) != 0 {
		t.Error("empty backup list produced entries")
	}
}

func TestSnapshotPurgesAt(t *testing.T) {
	sticky := Snapshot{Sticky: true, PurgesIn: 3600}
	if _, ok := sticky.PurgesAt(); ok {
		t.Error("a sticky snapshot claimed a purge time")
	}
	normal := Snapshot{PurgesIn: 3600}
	at, ok := normal.PurgesAt()
	if !ok || time.Until(at) < 59*time.Minute {
		t.Errorf("PurgesAt() = %v, %v", at, ok)
	}
	if _, ok := (Snapshot{}).PurgesAt(); ok {
		t.Error("a snapshot with no purge window claimed one")
	}
}

// KiwiVM encodes the audit log requestor as a 32-bit integer.
func TestAuditRequestorIP(t *testing.T) {
	e := AuditEntry{RequestorIPv4: Int(0x01020304)}
	if got := e.RequestorIP(); got != "1.2.3.4" {
		t.Errorf("RequestorIP() = %q, want 1.2.3.4", got)
	}
	if got := (AuditEntry{RequestorIPv4: Int(0xFFFFFFFF)}).RequestorIP(); got != "255.255.255.255" {
		t.Errorf("RequestorIP() at the top of the range = %q", got)
	}
	for _, bad := range []Int{0, -1, 0x1_0000_0000} {
		if got := (AuditEntry{RequestorIPv4: bad}).RequestorIP(); got != "" {
			t.Errorf("RequestorIP() on out-of-range %d = %q, want empty", bad, got)
		}
	}
}

func TestSSHKeysSplit(t *testing.T) {
	k := &SSHKeys{
		Veid:      "ssh-rsa AAA one\n\n  ssh-ed25519 BBB two  \n",
		User:      "",
		Preferred: "ssh-rsa AAA one",
	}
	if got := k.VeidSlice(); len(got) != 2 || got[1] != "ssh-ed25519 BBB two" {
		t.Errorf("VeidSlice() = %#v", got)
	}
	if got := k.UserSlice(); len(got) != 0 {
		t.Errorf("UserSlice() on an empty string = %#v, want none", got)
	}
	if got := k.PreferredSlice(); len(got) != 1 {
		t.Errorf("PreferredSlice() = %#v", got)
	}
}

func TestUsageTotalsAndWindow(t *testing.T) {
	u := &UsageStats{Data: []UsageSample{
		{Timestamp: 200, NetworkInBytes: 10, NetworkOutBytes: 1, DiskReadBytes: 5, DiskWriteBytes: 2},
		{Timestamp: 100, NetworkInBytes: 20, NetworkOutBytes: 2, DiskReadBytes: 5, DiskWriteBytes: 3},
	}}
	in, out, dr, dw := u.Totals()
	if in != 30 || out != 3 || dr != 10 || dw != 5 {
		t.Errorf("Totals() = %d,%d,%d,%d", in, out, dr, dw)
	}
	start, end := u.Window()
	if !start.Equal(time.Unix(100, 0)) || !end.Equal(time.Unix(200, 0)) {
		t.Errorf("Window() = %v..%v, want it ordered regardless of sample order", start, end)
	}

	var empty UsageStats
	if i, o, r, w := empty.Totals(); i|o|r|w != 0 {
		t.Error("Totals() on no samples should be zero")
	}
}

func TestNotificationPreferencesFlatten(t *testing.T) {
	body := `{
	  "email_preferences": {
	    "Service": {"snapshot_done": {"friendly_description":"Snapshot finished","is_enabled":"1"}},
	    "Abuse":   {"policy_violation": {"friendly_description":"Policy violation","is_enabled":0}}
	  },
	  "notificationEmail": "me@example.com"
	}`
	var n NotificationPreferences
	if err := json.Unmarshal([]byte(body), &n); err != nil {
		t.Fatal(err)
	}
	flat := n.Flat()
	if len(flat) != 2 {
		t.Fatalf("Flat() = %d entries, want 2", len(flat))
	}
	if !flat["snapshot_done"].IsEnabled.Bool() {
		t.Error(`is_enabled:"1" did not decode as true`)
	}
	if flat["policy_violation"].IsEnabled.Bool() {
		t.Error("is_enabled:0 did not decode as false")
	}

	// The whole block arrives as [] when nothing is configured.
	var none NotificationPreferences
	if err := json.Unmarshal([]byte(`{"email_preferences":[]}`), &none); err != nil {
		t.Fatalf("empty email_preferences as []: %v", err)
	}
	if len(none.Flat()) != 0 {
		t.Error("empty preferences produced entries")
	}
}

func TestAbuseCasesReportResolvability(t *testing.T) {
	if !(Suspension{IsSoft: true}).APIResolvable() {
		t.Error("is_soft=1 suspension should be API-resolvable")
	}
	if (Suspension{}).APIResolvable() {
		t.Error("is_soft=0 suspension should require support")
	}

	pv := PolicyViolation{IsSoft: true, SuspendAt: 1571599418}
	if !pv.APIResolvable() {
		t.Error("is_soft=1 violation should be API-resolvable")
	}
	at, ok := pv.SuspendsAt()
	if !ok || !at.Equal(time.Unix(1571599418, 0)) {
		t.Errorf("SuspendsAt() = %v, %v", at, ok)
	}
	if _, ok := (PolicyViolation{}).SuspendsAt(); ok {
		t.Error("SuspendsAt() invented a deadline")
	}
}
