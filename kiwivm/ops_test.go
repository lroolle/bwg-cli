package kiwivm

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestOpsRegistryIsWellFormed(t *testing.T) {
	for key, op := range Ops {
		if key != op.Endpoint {
			t.Errorf("Ops[%q] holds endpoint %q — the key must be the endpoint", key, op.Endpoint)
		}
		if op.Summary == "" {
			t.Errorf("%s has no summary; the CLI help and the MCP tool list both read it", key)
		}
		if strings.HasPrefix(key, "/") {
			t.Errorf("%s has a leading slash; the client joins paths itself", key)
		}
		switch op.Risk {
		case RiskRead, RiskWrite:
			if op.Why != "" {
				t.Errorf("%s is %s but carries a destructive rationale", key, op.Risk)
			}
		case RiskDestructive:
			// A destructive classification without a stated reason is
			// unauditable: nobody can tell whether it earned the tier.
			if op.Why == "" {
				t.Errorf("%s is destructive but does not say what is lost", key)
			}
		default:
			t.Errorf("%s has an unknown risk %d", key, op.Risk)
		}
	}
}

// Keeping the destructive set small is what makes a destructive
// confirmation mean anything. If this ever trips, the question is
// whether the new operation truly loses something unrecoverable — not
// whether to raise the bound.
func TestDestructiveSetStaysSmall(t *testing.T) {
	var destructive, write, read int
	for _, op := range Ops {
		switch op.Risk {
		case RiskDestructive:
			destructive++
		case RiskWrite:
			write++
		default:
			read++
		}
	}
	t.Logf("%d read, %d write, %d destructive", read, write, destructive)
	if destructive > len(Ops)/2 {
		t.Errorf("%d of %d operations are destructive; the tier has lost its meaning",
			destructive, len(Ops))
	}
	if read == 0 || write == 0 {
		t.Error("a registry with no reads or no writes means the classification is broken")
	}
}

func TestRiskRendersAsAName(t *testing.T) {
	for risk, want := range map[Risk]string{
		RiskRead: "read", RiskWrite: "write", RiskDestructive: "destructive", Risk(99): "unknown",
	} {
		if got := risk.String(); got != want {
			t.Errorf("Risk(%d).String() = %q, want %q", risk, got, want)
		}
	}

	// JSON output is a public contract for --json consumers.
	b, err := json.Marshal(struct {
		R Risk `json:"risk"`
	}{RiskDestructive})
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"risk":"destructive"}` {
		t.Errorf("Risk marshals as %s, want a lowercase name", b)
	}
}

func TestListOpsIsSortedAndComplete(t *testing.T) {
	ops := ListOps()
	if len(ops) != len(Ops) {
		t.Fatalf("ListOps() returned %d of %d operations", len(ops), len(Ops))
	}
	for i := 1; i < len(ops); i++ {
		if ops[i-1].Endpoint >= ops[i].Endpoint {
			t.Fatalf("ListOps() is not sorted: %q before %q", ops[i-1].Endpoint, ops[i].Endpoint)
		}
	}
}

func TestLookupOp(t *testing.T) {
	op, ok := LookupOp("snapshot/restore")
	if !ok || op.Risk != RiskDestructive {
		t.Errorf("LookupOp(snapshot/restore) = %+v, %v", op, ok)
	}
	if _, ok := LookupOp("snapshot/teleport"); ok {
		t.Error("LookupOp invented an endpoint")
	}
}

// The endpoints named in these tiers are the ones a reviewer should
// argue about. Pinning them makes a reclassification show up in a diff
// instead of sliding through.
func TestKnownClassifications(t *testing.T) {
	expect := map[string]Risk{
		"getServiceInfo":   RiskRead,
		"snapshot/list":    RiskRead,
		"basicShell/cd":    RiskRead,
		"start":            RiskWrite,
		"stop":             RiskWrite, // Start undoes it
		"restart":          RiskWrite, // Start undoes it
		"snapshot/create":  RiskWrite,
		"setPTR":           RiskWrite,
		"kill":             RiskDestructive, // unsaved guest data
		"reinstallOS":      RiskDestructive,
		"snapshot/delete":  RiskDestructive,
		"snapshot/restore": RiskDestructive,
		"basicShell/exec":  RiskDestructive, // arbitrary root code
		"migrate/start":    RiskDestructive, // IPv4 addresses replaced
		"ipv6/delete":      RiskDestructive, // subnet not reissued
	}
	for endpoint, want := range expect {
		op, ok := Ops[endpoint]
		if !ok {
			t.Errorf("%s is missing from the registry", endpoint)
			continue
		}
		if op.Risk != want {
			t.Errorf("%s is %s, expected %s", endpoint, op.Risk, want)
		}
	}
}
