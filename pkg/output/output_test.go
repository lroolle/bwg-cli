package output

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	// Deterministic output regardless of where the tests run.
	SetColor(false)
	m.Run()
}

func TestJSONDoesNotEscapeHTML(t *testing.T) {
	var buf bytes.Buffer
	if err := JSON(&buf, map[string]string{"cmd": "df -h && echo <done>"}); err != nil {
		t.Fatal(err)
	}
	// Go's default encoder turns < & > into \u003c and friends, which
	// makes a shell command in a JSON field unreadable.
	if strings.Contains(buf.String(), `\u003c`) || strings.Contains(buf.String(), `\u0026`) {
		t.Errorf("HTML escaping mangled a shell command: %s", buf.String())
	}
	if !strings.Contains(buf.String(), "df -h && echo <done>") {
		t.Errorf("the command did not survive verbatim: %s", buf.String())
	}
	var back map[string]string
	if err := json.Unmarshal(buf.Bytes(), &back); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
}

func TestBytes(t *testing.T) {
	cases := map[int64]string{
		0:                   "0 B",
		512:                 "512 B",
		1024:                "1.0 KiB",
		1536:                "1.5 KiB",
		1024 * 1024:         "1.0 MiB",
		1024 * 1024 * 1024:  "1.0 GiB",
		322122547200:        "300 GiB",
		-1024:               "-1.0 KiB",
		1024 * 1024 * 102.5: "102 MiB", // three significant figures, no decimal
	}
	for in, want := range cases {
		if got := Bytes(in); got != want {
			t.Errorf("Bytes(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestDuration(t *testing.T) {
	cases := map[time.Duration]string{
		0:                        "0s",
		-time.Hour:               "0s",
		30 * time.Second:         "30s",
		90 * time.Second:         "1m",
		2 * time.Hour:            "2h",
		150 * time.Minute:        "2h 30m",
		48 * time.Hour:           "2d",
		50 * time.Hour:           "2d 2h",
		7*24*time.Hour + 30*24*0: "7d",
	}
	for in, want := range cases {
		if got := Duration(in); got != want {
			t.Errorf("Duration(%v) = %q, want %q", in, got, want)
		}
	}
}

// A missing timestamp must render as nothing, not as "56 years ago".
func TestAgoAndTimeHandleTheZeroValue(t *testing.T) {
	if got := Ago(time.Time{}); got != "" {
		t.Errorf("Ago(zero) = %q, want empty", got)
	}
	if got := Time(time.Time{}); got != "" {
		t.Errorf("Time(zero) = %q, want empty", got)
	}
	if got := Ago(time.Now().Add(-2 * time.Hour)); !strings.Contains(got, "ago") {
		t.Errorf("Ago(past) = %q", got)
	}
	if got := Ago(time.Now().Add(2 * time.Hour)); !strings.HasPrefix(got, "in ") {
		t.Errorf("Ago(future) = %q, want an 'in' prefix", got)
	}
}

func TestPercent(t *testing.T) {
	cases := map[float64]string{
		0: "0.0%", 5.25: "5.2%", 9.99: "10.0%", 25: "25%", 99.6: "100%", 150: "150%",
	}
	for in, want := range cases {
		if got := Percent(in); got != want {
			t.Errorf("Percent(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestBarWidthIsStableAcrossValues(t *testing.T) {
	// Columns only line up if every bar is the same visible width.
	for _, p := range []float64{-10, 0, 1, 50, 99.9, 100, 250} {
		if got := visibleLen(Bar(p, 10)); got != 10 {
			t.Errorf("Bar(%v, 10) is %d cells wide, want 10", p, got)
		}
	}
	if !strings.Contains(Bar(100, 10), "██████████") {
		t.Errorf("Bar(100) = %q, want it full", Bar(100, 10))
	}
	if strings.Contains(Bar(0, 10), "█") {
		t.Errorf("Bar(0) = %q, want it empty", Bar(0, 10))
	}
}

func TestSeverityThresholds(t *testing.T) {
	cases := map[float64]Color{
		0: Green, 74.9: Green, 75: Yellow, 89.9: Yellow, 90: Red, 100: Red, 150: Red,
	}
	for p, want := range cases {
		if got := severity(p); got != want {
			t.Errorf("severity(%v) = %v, want %v", p, got, want)
		}
	}
}

func TestTableRendersAlignedColumns(t *testing.T) {
	var buf bytes.Buffer
	NewTable("NAME", "SIZE").
		RightAlign(1).
		Row("short", "1 GiB").
		Row("a-much-longer-name", "100 GiB").
		Render(&buf)

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want header + 2 rows:\n%s", len(lines), buf.String())
	}
	if !strings.HasPrefix(lines[0], "NAME") {
		t.Errorf("header = %q", lines[0])
	}
	// The right-aligned column should end at the same offset on both rows.
	if len(lines[1]) != len(lines[2]) {
		t.Errorf("rows are not aligned:\n%q\n%q", lines[1], lines[2])
	}
}

// A header row with nothing under it reads like data that failed to
// load. Empty means empty.
func TestEmptyTableRendersNothing(t *testing.T) {
	var buf bytes.Buffer
	NewTable("NAME", "SIZE").Render(&buf)
	if buf.Len() != 0 {
		t.Errorf("an empty table wrote %q, want nothing", buf.String())
	}
}

func TestTableHandlesShortRows(t *testing.T) {
	var buf bytes.Buffer
	NewTable("A", "B", "C").Row("only-one").Render(&buf)
	if !strings.Contains(buf.String(), "only-one") {
		t.Errorf("a short row was dropped: %q", buf.String())
	}
}

func TestTableSortBy(t *testing.T) {
	var buf bytes.Buffer
	NewTable("NAME").
		Row("charlie").Row("alpha").Row("bravo").
		SortBy(0, func(a, b string) bool { return a < b }).
		Render(&buf)

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if lines[1] != "alpha" || lines[3] != "charlie" {
		t.Errorf("SortBy did not order the rows:\n%s", buf.String())
	}
}

func TestTruncate(t *testing.T) {
	cases := []struct {
		in, want string
		n        int
	}{
		{"short", "short", 10},
		{"exactly-10", "exactly-10", 10},
		{"this-is-far-too-long", "this-is-…", 9},
		{"abc", "abc", 0},
	}
	for _, c := range cases {
		if got := Truncate(c.in, c.n); got != c.want {
			t.Errorf("Truncate(%q, %d) = %q, want %q", c.in, c.n, got, c.want)
		}
	}
}

// Table widths are computed from visible characters, so a coloured
// cell must not be measured with its escape codes.
func TestVisibleLenIgnoresEscapeCodes(t *testing.T) {
	SetColor(true)
	defer SetColor(false)

	colored := Bad("99%")
	if !strings.Contains(colored, "\x1b[") {
		t.Fatal("colour was not applied; the rest of this test is meaningless")
	}
	if got := visibleLen(colored); got != 3 {
		t.Errorf("visibleLen(%q) = %d, want 3", colored, got)
	}
}

func TestColorRespectsTheSwitch(t *testing.T) {
	SetColor(false)
	if got := Bad("x"); got != "x" {
		t.Errorf("colour leaked while disabled: %q", got)
	}
	SetColor(true)
	defer SetColor(false)
	if got := Bad("x"); !strings.HasPrefix(got, "\x1b[31m") {
		t.Errorf("colour not applied while enabled: %q", got)
	}
	if got := Colorize("x", None); got != "x" {
		t.Errorf("Colorize with None = %q", got)
	}
	if got := Colorize("", Red); got != "" {
		t.Errorf("Colorize of an empty string = %q", got)
	}
}

func TestCSV(t *testing.T) {
	var buf bytes.Buffer
	err := CSV(&buf, []string{"name", "veid"}, [][]string{
		{"tokyo", "1347645"},
		{"has,comma", "2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, `"has,comma"`) {
		t.Errorf("a comma in a value was not quoted: %s", out)
	}
	if len(strings.Split(strings.TrimSpace(out), "\n")) != 3 {
		t.Errorf("want header + 2 rows, got:\n%s", out)
	}
}

func TestTabbedSkipsEmptyValues(t *testing.T) {
	var buf bytes.Buffer
	Tabbed(&buf, [][2]string{
		{"Hostname", "box.example.com"},
		{"Note", ""},
		{"Plan", "micro128"},
	})
	out := buf.String()
	if strings.Contains(out, "Note") {
		t.Errorf("an empty value produced a line: %s", out)
	}
	if !strings.Contains(out, "box.example.com") || !strings.Contains(out, "micro128") {
		t.Errorf("populated values missing: %s", out)
	}
}

// Section exists because Tabbed drops empty values: a heading printed
// above nothing reads as data that failed to load, which is what
// `bwg info` did for rDNS when no PTR was set.
func TestSectionOmitsAHeadingWithNothingUnderIt(t *testing.T) {
	var buf bytes.Buffer
	Section(&buf, "rDNS", [][2]string{
		{"203.0.113.10", ""},
		{"2001:db8::", ""},
	})
	if buf.String() != "" {
		t.Errorf("an all-empty section printed something: %q", buf.String())
	}

	buf.Reset()
	Section(&buf, "rDNS", nil)
	if buf.String() != "" {
		t.Errorf("an empty section printed something: %q", buf.String())
	}

	buf.Reset()
	Section(&buf, "rDNS", [][2]string{
		{"203.0.113.10", "box.example.com"},
		{"2001:db8::", ""},
	})
	out := buf.String()
	if !strings.Contains(out, "rDNS") || !strings.Contains(out, "box.example.com") {
		t.Errorf("a populated section lost its heading or its row: %q", out)
	}
	if strings.Contains(out, "2001:db8::") {
		t.Errorf("the empty row survived: %q", out)
	}
	if !strings.HasPrefix(out, "\n") {
		t.Errorf("a section must open with a blank line: %q", out)
	}
}

// "1 keys" is the kind of detail that makes a tool feel unfinished.
func TestCount(t *testing.T) {
	cases := []struct {
		n    int
		noun string
		want string
	}{
		{0, "key", "0 keys"},
		{1, "key", "1 key"},
		{2, "key", "2 keys"},
		{1, "server", "1 server"},
		{12, "point", "12 points"},
	}
	for _, c := range cases {
		if got := Count(c.n, c.noun); got != c.want {
			t.Errorf("Count(%d, %q) = %q, want %q", c.n, c.noun, got, c.want)
		}
	}
}
