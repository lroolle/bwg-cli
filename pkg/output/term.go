package output

import "os"

// IsTerminal reports whether f is attached to a character device.
//
// This is the stdlib-only check: it cannot distinguish a terminal from
// /dev/null, but the two decisions that depend on it — whether to
// colour output and whether a confirmation prompt can be answered —
// both fail safe under that confusion. It costs no dependency.
func IsTerminal(f *os.File) bool {
	if f == nil {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// Interactive reports whether a human can answer a prompt right now:
// stdin must be readable by a person and stderr must be visible to
// them. Anything else is a script, and a script must not be stopped by
// a question it cannot answer.
func Interactive() bool {
	return IsTerminal(os.Stdin) && IsTerminal(os.Stderr)
}
