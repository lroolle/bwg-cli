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

var (
	once    sync.Once
	enabled bool
)

// colorEnabled decides once whether to emit escape codes.
//
// Piped output is never coloured — the reason --json is trustworthy is
// that nothing decorates it. NO_COLOR is honoured because it is the
// convention, and BWG_COLOR=always exists for the CI logs and pagers
// that do render colour despite not being a terminal.
func colorEnabled() bool {
	once.Do(func() {
		switch strings.ToLower(os.Getenv("BWG_COLOR")) {
		case "always", "force", "1", "yes":
			enabled = true
			return
		case "never", "none", "0", "no":
			enabled = false
			return
		}
		if _, set := os.LookupEnv("NO_COLOR"); set {
			enabled = false
			return
		}
		if os.Getenv("TERM") == "dumb" {
			enabled = false
			return
		}
		enabled = IsTerminal(os.Stdout)
	})
	return enabled
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
