package output

import (
	"strings"
	"testing"
)

// env builds a lookup over a fixed map, the way os.LookupEnv behaves:
// a variable set to "" is still set.
func env(pairs map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		v, ok := pairs[k]
		return v, ok
	}
}

// The precedence here is the whole contract: a pipe is never coloured,
// NO_COLOR is honoured, and BWG_COLOR overrides both because the person
// who set it is answering this exact question.
func TestColorPrecedence(t *testing.T) {
	cases := []struct {
		name  string
		vars  map[string]string
		isTTY bool
		want  bool
	}{
		{"a terminal gets colour", nil, true, true},
		{"a pipe does not", nil, false, false},
		{"NO_COLOR wins over a terminal", map[string]string{"NO_COLOR": ""}, true, false},
		{"TERM=dumb wins over a terminal", map[string]string{"TERM": "dumb"}, true, false},
		{"BWG_COLOR=always beats a pipe", map[string]string{"BWG_COLOR": "always"}, false, true},
		{"BWG_COLOR=always beats NO_COLOR",
			map[string]string{"BWG_COLOR": "always", "NO_COLOR": "1"}, false, true},
		{"BWG_COLOR=never beats a terminal", map[string]string{"BWG_COLOR": "never"}, true, false},
		{"BWG_COLOR=1 is a yes", map[string]string{"BWG_COLOR": "1"}, false, true},
		{"BWG_COLOR=0 is a no", map[string]string{"BWG_COLOR": "0"}, true, false},
		{"case and spaces do not matter", map[string]string{"BWG_COLOR": " Never "}, true, false},
		{"an unrecognised value falls through to the terminal check",
			map[string]string{"BWG_COLOR": "purple"}, true, true},
		{"an empty BWG_COLOR is not an answer",
			map[string]string{"BWG_COLOR": "", "NO_COLOR": ""}, true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := colorFor(env(c.vars), c.isTTY); got != c.want {
				t.Errorf("colorFor(%v, tty=%v) = %v, want %v", c.vars, c.isTTY, got, c.want)
			}
		})
	}
}

// Colour is severity, so every helper has to actually emit its code —
// and emit nothing at all when colour is off, because a pipe must
// receive exactly the bytes it would have received without a terminal.
func TestColorHelpers(t *testing.T) {
	t.Cleanup(func() { SetColor(false) })

	SetColor(true)
	for name, got := range map[string]string{
		"Dim":    Dim("x"),
		"Warn":   Warn("x"),
		"Bad":    Bad("x"),
		"Good":   Good("x"),
		"Strong": Strong("x"),
	} {
		if !strings.HasPrefix(got, "\x1b[") || !strings.HasSuffix(got, "\x1b[0m") {
			t.Errorf("%s did not wrap its text: %q", name, got)
		}
		if visibleLen(got) != 1 {
			t.Errorf("%s changed the visible width: %q", name, got)
		}
	}
	if got := Colorize("x", None); got != "x" {
		t.Errorf("the empty colour still wrapped: %q", got)
	}
	if got := Colorize("", Red); got != "" {
		t.Errorf("an empty string was wrapped: %q", got)
	}

	SetColor(false)
	for _, got := range []string{Dim("x"), Warn("x"), Bad("x"), Good("x"), Strong("x")} {
		if got != "x" {
			t.Errorf("colour leaked into a pipe: %q", got)
		}
	}
}
