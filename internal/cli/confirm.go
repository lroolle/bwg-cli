package cli

import (
	"bufio"
	"errors"
	"fmt"
	"strings"

	"github.com/lroolle/bwg-cli/internal/config"
	"github.com/lroolle/bwg-cli/kiwivm"
	"github.com/lroolle/bwg-cli/pkg/output"
)

// catastrophic lists the operations that replace or erase an entire
// VPS. They get a typed-name confirmation rather than a y/N, because
// the mistake they guard against is not "did I mean to do this" but
// "did I mean to do this HERE" — and y/N cannot catch a wrong box.
//
// This is a UX policy, not a property of the API, which is why it
// lives here and not in kiwivm.Ops. Keep it short: the moment routine
// operations need a typed name, people start pasting names reflexively
// and the ceremony stops protecting anything.
var catastrophic = map[string]bool{
	"reinstallOS":             true,
	"snapshot/restore":        true,
	"migrate/start":           true,
	"cloneFromExternalServer": true,
}

// Consent is everything the person answering needs in order to answer
// well. The rule this type exists to enforce: whatever bwg already
// knows that the decision depends on goes ON the card. A prompt that
// withholds the deciding fact is a speed bump, not a safeguard.
type Consent struct {
	// Op is the operation being requested.
	Op kiwivm.Op
	// Server is the VPS it will act on.
	Server *config.Server
	// Target names the specific thing acted on: a snapshot file, an OS
	// template, an IP. Empty when the operation takes no object.
	Target string
	// Facts are the decision-relevant details bwg has already fetched —
	// the hostname and IP of the box, the age of the snapshot about to
	// be restored. These are the difference between an informed yes and
	// a reflexive one.
	Facts [][2]string
}

// ErrDeclined is returned when consent was asked for and refused.
var ErrDeclined = errors.New("declined")

// Confirm gates a mutating operation.
//
//	read            never asks
//	read-only mode  refuses immediately, before asking anything
//	--yes           proceeds, and says on stderr what it did
//	no terminal     refuses, naming --yes — a script must never block
//	                on a question nobody can answer
//	otherwise       shows the card and waits
func (a *App) Confirm(c Consent) error {
	if c.Op.Risk == kiwivm.RiskRead {
		return nil
	}

	// Refuse before asking. Being talked through a decision and then
	// told it was never possible is worse than a plain refusal.
	if a.ReadOnly {
		return &ExitCodeError{Code: ExitRefused, Err: fmt.Errorf(
			"%s is a %s operation and read-only mode is on\n\n"+
				"  Unset BWG_READ_ONLY or drop --read-only to allow it.",
			c.Op.Endpoint, c.Op.Risk)}
	}

	if a.DryRun {
		return a.emitDryRun(c)
	}

	if a.Yes {
		// Skipping the prompt must not mean skipping the record. An
		// unattended run should still leave a trail of what it changed.
		a.Notef("%s %s%s", output.Warn("!"), a.actionLine(c), output.Dim("  (--yes)"))
		return nil
	}

	if !output.Interactive() {
		return &ExitCodeError{Code: ExitRefused, Err: fmt.Errorf(
			"refusing to %s without confirmation\n\n"+
				"  This is a %s operation and there is no terminal to confirm on.\n"+
				"  Pass --yes to proceed, or run it interactively.",
			a.actionLine(c), c.Op.Risk)}
	}

	a.renderCard(c)

	if catastrophic[c.Op.Endpoint] {
		return a.askForName(c)
	}
	return a.askYesNo()
}

// actionLine is the one-sentence description of what is about to
// happen, reused by the card, the --yes record and the refusal.
func (a *App) actionLine(c Consent) string {
	line := strings.ToLower(c.Op.Summary)
	if c.Target != "" {
		line += " " + output.Strong(c.Target)
	}
	if c.Server != nil {
		line += " on " + output.Strong(c.Server.Name)
	}
	return line
}

func (a *App) renderCard(c Consent) {
	w := a.ErrOut
	rule := strings.Repeat("─", 60)

	label, colour := "WRITE", output.Warn
	if c.Op.Risk == kiwivm.RiskDestructive {
		label, colour = "DESTRUCTIVE", output.Bad
	}

	fmt.Fprintln(w)
	fmt.Fprintf(w, "%s %s\n", colour(label), output.Strong(c.Op.Summary))
	fmt.Fprintln(w, output.Dim(rule))

	facts := make([][2]string, 0, len(c.Facts)+4)
	if c.Server != nil {
		facts = append(facts, [2]string{"Server", c.Server.Name + output.Dim(" (veid "+c.Server.VEID+")")})
	}
	if c.Target != "" {
		facts = append(facts, [2]string{"Target", c.Target})
	}
	facts = append(facts, c.Facts...)
	facts = append(facts, [2]string{"Endpoint", output.Dim(c.Op.Endpoint)})
	output.Tabbed(w, facts)

	// The why is the whole point of the destructive tier. If it is not
	// on the card, the card is not carrying its weight.
	if c.Op.Why != "" {
		fmt.Fprintf(w, "\n%s %s\n", output.Bad("Irreversible:"), c.Op.Why)
	}
	fmt.Fprintln(w, output.Dim(rule))
}

func (a *App) askYesNo() error {
	fmt.Fprintf(a.ErrOut, "Proceed? %s ", output.Dim("[y/N]"))

	line, err := a.readLine()
	if err != nil {
		return fmt.Errorf("%w: %v", ErrDeclined, err)
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return nil
	}
	return &ExitCodeError{Code: ExitRefused, Err: ErrDeclined}
}

// askForName makes the person name the box they are about to lose.
// Typing it is the only check that catches the wrong-server mistake,
// which is the one that actually happens.
func (a *App) askForName(c Consent) error {
	want := c.Server.Name
	fmt.Fprintf(a.ErrOut, "This cannot be undone. Type %s to confirm: ", output.Strong(want))

	line, err := a.readLine()
	if err != nil {
		return fmt.Errorf("%w: %v", ErrDeclined, err)
	}
	if strings.TrimSpace(line) != want {
		return &ExitCodeError{Code: ExitRefused, Err: fmt.Errorf(
			"%w: name did not match", ErrDeclined)}
	}
	return nil
}

func (a *App) readLine() (string, error) {
	r := bufio.NewReader(a.In)
	line, err := r.ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return line, nil
}

// emitDryRun shows what a write would do without doing it. In JSON
// mode the preview goes to stdout as structured data; in human mode
// the consent card goes to stderr and nothing hits stdout.
func (a *App) emitDryRun(c Consent) error {
	if a.JSON || a.JQ != "" {
		payload := map[string]any{
			"dryRun":   true,
			"endpoint": c.Op.Endpoint,
			"risk":     c.Op.Risk.String(),
			"summary":  c.Op.Summary,
		}
		if c.Server != nil {
			payload["server"] = c.Server.Name
		}
		if c.Target != "" {
			payload["target"] = c.Target
		}
		if c.Op.Why != "" {
			payload["why"] = c.Op.Why
		}
		if len(c.Facts) > 0 {
			facts := make([][2]string, len(c.Facts))
			copy(facts, c.Facts)
			payload["facts"] = facts
		}
		if err := a.Emit(payload, nil); err != nil {
			return err
		}
		return ErrDryRun
	}

	a.renderCard(c)
	fmt.Fprintf(a.ErrOut, "\n%s\n", output.Dim("DRY RUN — no changes made"))
	return ErrDryRun
}
