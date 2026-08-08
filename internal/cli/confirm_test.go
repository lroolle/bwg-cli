package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/lroolle/bwg-cli/internal/config"
	"github.com/lroolle/bwg-cli/kiwivm"
)

// gateApp is an App with captured streams and a scripted answer.
func gateApp(answer string) (*App, *bytes.Buffer) {
	errOut := &bytes.Buffer{}
	return &App{
		Out: &bytes.Buffer{}, ErrOut: errOut, In: strings.NewReader(answer),
	}, errOut
}

var testServer = &config.Server{Name: "tokyo", VEID: "1347645", APIKey: "private_x"}

func TestReadsAreNeverGated(t *testing.T) {
	app, errOut := gateApp("")
	for _, op := range kiwivm.ListOps() {
		if op.Risk != kiwivm.RiskRead {
			continue
		}
		if err := app.Confirm(Consent{Op: op, Server: testServer}); err != nil {
			t.Errorf("%s (read) was gated: %v", op.Endpoint, err)
		}
	}
	if errOut.Len() != 0 {
		t.Errorf("a read produced a prompt: %q", errOut)
	}
}

// Read-only must refuse before the card is drawn. Being walked through
// a decision and then told it was never possible is worse than a plain
// refusal.
func TestReadOnlyRefusesWithoutAsking(t *testing.T) {
	app, errOut := gateApp("y\n")
	app.ReadOnly = true
	app.Yes = true // must not help

	err := app.Confirm(Consent{Op: kiwivm.Ops["reinstallOS"], Server: testServer})
	if err == nil {
		t.Fatal("read-only mode allowed a destructive operation")
	}
	if CodeFor(err) != ExitRefused {
		t.Errorf("exit code = %d, want refused", CodeFor(err))
	}
	if strings.Contains(errOut.String(), "DESTRUCTIVE") {
		t.Errorf("a card was drawn for an operation that was never possible:\n%s", errOut)
	}
}

func TestYesProceedsButStillRecords(t *testing.T) {
	app, errOut := gateApp("")
	app.Yes = true

	if err := app.Confirm(Consent{
		Op: kiwivm.Ops["snapshot/delete"], Server: testServer, Target: "snap.tar.gz",
	}); err != nil {
		t.Fatalf("--yes did not proceed: %v", err)
	}
	// An unattended run must still leave a trail of what it changed.
	rec := errOut.String()
	for _, want := range []string{"snap.tar.gz", "tokyo", "--yes"} {
		if !strings.Contains(rec, want) {
			t.Errorf("the record is missing %q: %q", want, rec)
		}
	}
}

// A script must fail fast rather than block on a question nobody can
// answer. output.Interactive() is false under `go test`.
func TestNonInteractiveRefusesRatherThanBlocking(t *testing.T) {
	app, _ := gateApp("y\n")

	err := app.Confirm(Consent{Op: kiwivm.Ops["stop"], Server: testServer})
	if err == nil {
		t.Fatal("a write proceeded with no terminal and no --yes")
	}
	if CodeFor(err) != ExitRefused {
		t.Errorf("exit code = %d, want refused", CodeFor(err))
	}
	if !strings.Contains(err.Error(), "--yes") {
		t.Errorf("the refusal does not name the fix: %v", err)
	}
}

// The card is a decision surface. Everything the answer depends on has
// to be on it — most of all what a destructive operation destroys.
func TestCardCarriesTheDecidingFacts(t *testing.T) {
	app, errOut := gateApp("")
	app.renderCard(Consent{
		Op:     kiwivm.Ops["snapshot/restore"],
		Server: testServer,
		Target: "1347645_20260801.tar.gz",
		Facts: [][2]string{
			{"Hostname", "tokyo.example.com"},
			{"Address", "203.0.113.10"},
			{"Snapshot OS", "debian-11"},
		},
	})

	card := errOut.String()
	musts := map[string]string{
		"the risk tier":         "DESTRUCTIVE",
		"the server name":       "tokyo",
		"the veid":              "1347645",
		"the target":            "1347645_20260801.tar.gz",
		"the hostname":          "tokyo.example.com",
		"the address":           "203.0.113.10",
		"the caller's facts":    "debian-11",
		"the endpoint":          "snapshot/restore",
		"what is lost":          "overwritten",
		"the irreversible flag": "Irreversible",
	}
	for what, want := range musts {
		if !strings.Contains(card, want) {
			t.Errorf("the card omits %s (%q):\n%s", what, want, card)
		}
	}
}

func TestWriteCardIsNotLabelledDestructive(t *testing.T) {
	app, errOut := gateApp("")
	app.renderCard(Consent{Op: kiwivm.Ops["snapshot/create"], Server: testServer})

	card := errOut.String()
	if !strings.Contains(card, "WRITE") {
		t.Errorf("a write card is not labelled WRITE:\n%s", card)
	}
	if strings.Contains(card, "DESTRUCTIVE") || strings.Contains(card, "Irreversible") {
		t.Errorf("a write card is dressed up as destructive:\n%s", card)
	}
}

// Every destructive operation must be able to say what it destroys, or
// its card is not carrying its weight.
func TestEveryDestructiveCardStatesWhatIsLost(t *testing.T) {
	for _, op := range kiwivm.ListOps() {
		if op.Risk != kiwivm.RiskDestructive {
			continue
		}
		app, errOut := gateApp("")
		app.renderCard(Consent{Op: op, Server: testServer})
		if !strings.Contains(errOut.String(), "Irreversible:") {
			t.Errorf("%s draws a card with no statement of loss:\n%s", op.Endpoint, errOut)
		}
	}
}

func TestYesNoAnswers(t *testing.T) {
	cases := map[string]bool{
		"y\n": true, "Y\n": true, "yes\n": true, "YES\n": true, " y \n": true,
		"n\n": false, "\n": false, "no\n": false, "": false, "maybe\n": false,
	}
	for answer, wantOK := range cases {
		app, _ := gateApp(answer)
		err := app.askYesNo()
		if gotOK := err == nil; gotOK != wantOK {
			t.Errorf("answer %q: proceeded=%v, want %v (err=%v)", answer, gotOK, wantOK, err)
		}
		if err != nil && !errors.Is(err, ErrDeclined) {
			t.Errorf("answer %q: %v is not a decline", answer, err)
		}
	}
}

// The catastrophic tier asks for the server's name because the mistake
// it guards against is "wrong box", which y/N cannot catch.
func TestCatastrophicOpsRequireTheServerName(t *testing.T) {
	for endpoint := range catastrophic {
		op, ok := kiwivm.LookupOp(endpoint)
		if !ok {
			t.Errorf("%s is in the catastrophic set but not in the registry", endpoint)
			continue
		}
		if op.Risk != kiwivm.RiskDestructive {
			t.Errorf("%s is catastrophic but classified %s", endpoint, op.Risk)
		}

		// The right name proceeds.
		app, _ := gateApp("tokyo\n")
		if err := app.askForName(Consent{Op: op, Server: testServer}); err != nil {
			t.Errorf("%s: the correct name was rejected: %v", endpoint, err)
		}
		// A plain yes does not.
		app, _ = gateApp("y\n")
		if err := app.askForName(Consent{Op: op, Server: testServer}); err == nil {
			t.Errorf("%s: 'y' passed a typed-name confirmation", endpoint)
		}
		// Nor does a different server's name.
		app, _ = gateApp("osaka\n")
		if err := app.askForName(Consent{Op: op, Server: testServer}); err == nil {
			t.Errorf("%s: the wrong server name was accepted", endpoint)
		}
	}
}

// Keeping the typed-name tier small is what keeps it meaningful. If
// this trips, the question is whether the new operation really loses
// the whole box.
func TestCatastrophicSetStaysSmall(t *testing.T) {
	if len(catastrophic) > 6 {
		t.Errorf("%d operations demand a typed name; people will start pasting reflexively",
			len(catastrophic))
	}
	for _, want := range []string{"reinstallOS", "snapshot/restore", "migrate/start"} {
		if !catastrophic[want] {
			t.Errorf("%s replaces the whole box but only asks y/N", want)
		}
	}
}

func TestActionLineNamesTheTargetAndServer(t *testing.T) {
	app, _ := gateApp("")
	line := app.actionLine(Consent{
		Op: kiwivm.Ops["snapshot/delete"], Server: testServer, Target: "snap.tar.gz",
	})
	for _, want := range []string{"snapshot", "snap.tar.gz", "tokyo"} {
		if !strings.Contains(line, want) {
			t.Errorf("the action line omits %q: %q", want, line)
		}
	}
}

func TestDryRunShowsCardButDoesNotProceed(t *testing.T) {
	app, errOut := gateApp("")
	app.DryRun = true

	err := app.Confirm(Consent{
		Op: kiwivm.Ops["snapshot/delete"], Server: testServer, Target: "snap.tar.gz",
	})
	if !errors.Is(err, ErrDryRun) {
		t.Fatalf("dry-run did not return ErrDryRun: %v", err)
	}
	if CodeFor(err) != ExitOK {
		t.Errorf("dry-run exit code = %d, want 0 (success)", CodeFor(err))
	}
	card := errOut.String()
	if !strings.Contains(card, "DESTRUCTIVE") {
		t.Errorf("dry-run omits the risk tier:\n%s", card)
	}
	if !strings.Contains(card, "snap.tar.gz") {
		t.Errorf("dry-run omits the target:\n%s", card)
	}
	if !strings.Contains(card, "DRY RUN") {
		t.Errorf("dry-run omits the dry-run label:\n%s", card)
	}
}

func TestDryRunJSONIncludesDryRunFlag(t *testing.T) {
	stdout := &bytes.Buffer{}
	app := &App{Out: stdout, ErrOut: &bytes.Buffer{}, JSON: true, DryRun: true}

	err := app.Confirm(Consent{
		Op: kiwivm.Ops["stop"], Server: testServer,
	})
	if !errors.Is(err, ErrDryRun) {
		t.Fatalf("dry-run JSON did not return ErrDryRun: %v", err)
	}
	out := stdout.String()
	if !strings.Contains(out, `"dryRun":true`) && !strings.Contains(out, `"dryRun": true`) {
		t.Errorf("dry-run JSON does not include dryRun:true:\n%s", out)
	}
	if !strings.Contains(out, `"stop"`) {
		t.Errorf("dry-run JSON does not name the endpoint:\n%s", out)
	}
}

func TestDryRunReadsAreUnaffected(t *testing.T) {
	app, _ := gateApp("")
	app.DryRun = true

	err := app.Confirm(Consent{Op: kiwivm.Ops["getServiceInfo"], Server: testServer})
	if err != nil {
		t.Errorf("dry-run gated a read: %v", err)
	}
}

func TestDryRunBeatsReadOnly(t *testing.T) {
	app, _ := gateApp("")
	app.ReadOnly = true
	app.DryRun = true

	err := app.Confirm(Consent{Op: kiwivm.Ops["stop"], Server: testServer})
	if errors.Is(err, ErrDryRun) {
		t.Fatal("dry-run should not fire when read-only already refuses")
	}
	if CodeFor(err) != ExitRefused {
		t.Errorf("read-only + dry-run = exit %d, want refused", CodeFor(err))
	}
}
