package output

import (
	"os"
	"strings"
	"sync"
)

// Color is an ANSI SGR code.
type Color string

// The palette. Deliberately small: colour here means severity, not
// decoration, so every one of these has to earn its meaning.
const (
	None   Color = ""
	Red    Color = "31"
	Green  Color = "32"
	Yellow Color = "33"
	Blue   Color = "34"
	Gray   Color = "90"
	Bold   Color = "1"
)

// EnvColor forces colour on or off: "always" / "never".
const EnvColor = "BWG_COLOR"

var (
	once    sync.Once
	enabled bool
)

// colorEnabled decides once whether to emit escape codes.
func colorEnabled() bool {
	once.Do(func() { enabled = colorFor(os.LookupEnv, IsTerminal(os.Stdout)) })
	return enabled
}

// colorFor is the decision itself, kept pure so the precedence between
// three environment variables and the terminal check is testable
// without a pty.
//
// Piped output is never coloured — the reason --json is trustworthy is
// that nothing decorates it. NO_COLOR is honoured because it is the
// convention, and BWG_COLOR=always exists for the CI logs and pagers
// that do render colour despite not being a terminal. BWG_COLOR wins
// over NO_COLOR: it is the more specific of the two, and someone who
// sets it is answering this exact question.
func colorFor(lookup func(string) (string, bool), isTTY bool) bool {
	if v, _ := lookup(EnvColor); v != "" {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "always", "force", "1", "yes":
			return true
		case "never", "none", "0", "no":
			return false
		}
	}
	if _, set := lookup("NO_COLOR"); set {
		return false
	}
	if v, _ := lookup("TERM"); v == "dumb" {
		return false
	}
	return isTTY
}

// SetColor overrides colour detection. Tests use it; so does the root
// command when --no-color is passed.
func SetColor(on bool) {
	once.Do(func() {})
	enabled = on
}

// Colorize wraps s in a colour when colour is enabled.
func Colorize(s string, c Color) string {
	if c == None || s == "" || !colorEnabled() {
		return s
	}
	return "\x1b[" + string(c) + "m" + s + "\x1b[0m"
}

// Dim renders secondary text — headers, labels, units.
func Dim(s string) string { return Colorize(s, Gray) }

// Warn renders something that needs attention soon.
func Warn(s string) string { return Colorize(s, Yellow) }

// Bad renders something wrong now.
func Bad(s string) string { return Colorize(s, Red) }

// Good renders something healthy.
func Good(s string) string { return Colorize(s, Green) }

// Strong renders emphasis.
func Strong(s string) string { return Colorize(s, Bold) }
