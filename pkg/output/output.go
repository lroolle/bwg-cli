// Package output renders bwg results for the two audiences that read
// them: a person scanning a terminal, and a program parsing stdout.
//
// The split is absolute. Data goes to stdout, diagnostics to stderr,
// so a pipeline never has to strip warnings out of its input. Colour
// appears only on a terminal, never in a pipe.
package output

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"sort"
	"strings"
	"text/tabwriter"
	"time"
)

// -- JSON ---------------------------------------------------------------

// JSON writes v as indented JSON. HTML escaping is off: these payloads
// carry hostnames and shell commands, and < helps nobody.
func JSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

// JQ filters v through the jq binary. jq is not vendored: someone who
// asks for --jq has it, and shelling out keeps the full language
// rather than a subset that surprises people.
func JQ(w io.Writer, v any, expr string) error {
	var buf strings.Builder
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return err
	}

	cmd := exec.Command("jq", expr)
	cmd.Stdin = strings.NewReader(buf.String())
	cmd.Stdout = w
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		if _, lookErr := exec.LookPath("jq"); lookErr != nil {
			return fmt.Errorf("--jq needs the jq binary on PATH: %w", lookErr)
		}
		return fmt.Errorf("jq %q: %w", expr, err)
	}
	return nil
}

// -- tables --------------------------------------------------------------

// Table accumulates rows and renders them as aligned columns.
type Table struct {
	headers []string
	rows    [][]string
	// right marks columns to right-align, for numbers.
	right map[int]bool
}

// NewTable starts a table with the given column headers.
func NewTable(headers ...string) *Table {
	return &Table{headers: headers, right: map[int]bool{}}
}

// RightAlign marks columns (0-based) as right-aligned.
func (t *Table) RightAlign(cols ...int) *Table {
	for _, c := range cols {
		t.right[c] = true
	}
	return t
}

// Row appends a row. Short rows are padded, long rows are kept whole
// rather than silently truncated.
func (t *Table) Row(cells ...string) *Table {
	t.rows = append(t.rows, cells)
	return t
}

// Len returns the number of rows.
func (t *Table) Len() int { return len(t.rows) }

// SortBy sorts rows by a column, stably.
func (t *Table) SortBy(col int, less func(a, b string) bool) *Table {
	sort.SliceStable(t.rows, func(i, j int) bool {
		return less(cell(t.rows[i], col), cell(t.rows[j], col))
	})
	return t
}

// Render writes the table. With no rows it writes nothing at all —
// a lone header row reads like data that failed to load.
func (t *Table) Render(w io.Writer) {
	if len(t.rows) == 0 {
		return
	}
	widths := make([]int, len(t.headers))
	for i, h := range t.headers {
		widths[i] = visibleLen(h)
	}
	for _, r := range t.rows {
		for i := range r {
			if i < len(widths) && visibleLen(r[i]) > widths[i] {
				widths[i] = visibleLen(r[i])
			}
		}
	}

	writeRow := func(cells []string, dim bool) {
		var b strings.Builder
		for i := range t.headers {
			c := cell(cells, i)
			pad := widths[i] - visibleLen(c)
			if pad < 0 {
				pad = 0
			}
			if t.right[i] {
				b.WriteString(strings.Repeat(" ", pad))
				b.WriteString(c)
			} else {
				b.WriteString(c)
				if i < len(t.headers)-1 {
					b.WriteString(strings.Repeat(" ", pad))
				}
			}
			if i < len(t.headers)-1 {
				b.WriteString("  ")
			}
		}
		line := strings.TrimRight(b.String(), " ")
		if dim {
			line = Dim(line)
		}
		fmt.Fprintln(w, line)
	}

	upper := make([]string, len(t.headers))
	for i, h := range t.headers {
		upper[i] = strings.ToUpper(h)
	}
	writeRow(upper, true)
	for _, r := range t.rows {
		writeRow(r, false)
	}
}

func cell(row []string, i int) string {
	if i < len(row) {
		return row[i]
	}
	return ""
}

// Tabbed renders key/value detail lines aligned on the values.
func Tabbed(w io.Writer, pairs [][2]string) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, p := range pairs {
		if p[1] == "" {
			continue
		}
		fmt.Fprintf(tw, "%s\t%s\n", Dim(p[0]+":"), p[1])
	}
	tw.Flush()
}

// CSV writes headers and rows as comma-separated values.
func CSV(w io.Writer, headers []string, rows [][]string) error {
	cw := csv.NewWriter(w)
	defer cw.Flush()
	if err := cw.Write(headers); err != nil {
		return err
	}
	for _, r := range rows {
		if err := cw.Write(r); err != nil {
			return err
		}
	}
	return cw.Error()
}

// -- humane values ----------------------------------------------------------

// Bytes renders a byte count the way a person reads a quota: three
// significant figures and a binary unit.
func Bytes(n int64) string {
	if n == 0 {
		return "0 B"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%s%d B", sign(neg), n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit && exp < 5; v /= unit {
		div *= unit
		exp++
	}
	val := float64(n) / float64(div)
	format := "%s%.1f %ciB"
	if val >= 100 {
		format = "%s%.0f %ciB"
	}
	return fmt.Sprintf(format, sign(neg), val, "KMGTPE"[exp])
}

func sign(neg bool) string {
	if neg {
		return "-"
	}
	return ""
}

// Duration renders a span at the granularity a person cares about:
// days and hours for a billing reset, minutes for a recent event.
func Duration(d time.Duration) string {
	if d <= 0 {
		return "0s"
	}
	switch {
	case d >= 24*time.Hour:
		days := int(d.Hours()) / 24
		hours := int(d.Hours()) % 24
		if hours == 0 {
			return fmt.Sprintf("%dd", days)
		}
		return fmt.Sprintf("%dd %dh", days, hours)
	case d >= time.Hour:
		h := int(d.Hours())
		m := int(d.Minutes()) % 60
		if m == 0 {
			return fmt.Sprintf("%dh", h)
		}
		return fmt.Sprintf("%dh %dm", h, m)
	case d >= time.Minute:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
}

// Ago renders how long ago t was. The zero time renders empty rather
// than as 56 years, which is what a missing timestamp would look like.
func Ago(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := time.Since(t)
	if d < 0 {
		return "in " + Duration(-d)
	}
	return Duration(d) + " ago"
}

// Time renders a timestamp compactly, dropping the year when it is the
// current one. The zero time renders empty.
func Time(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	if t.Year() == time.Now().Year() {
		return t.Format("Jan 02 15:04")
	}
	return t.Format("2006-01-02 15:04")
}

// Percent renders a percentage with one decimal below 10 and none
// above, so a column of them lines up.
func Percent(p float64) string {
	if math.IsNaN(p) || math.IsInf(p, 0) {
		return "-"
	}
	if p < 10 {
		return fmt.Sprintf("%.1f%%", p)
	}
	return fmt.Sprintf("%.0f%%", p)
}

// Bar renders a fixed-width usage bar. It is the fastest way to see
// which box in a fleet is about to blow its quota.
func Bar(percent float64, width int) string {
	if width <= 0 {
		width = 10
	}
	p := percent
	if p < 0 {
		p = 0
	}
	over := p > 100
	if over {
		p = 100
	}
	filled := int(math.Round(p / 100 * float64(width)))
	bar := strings.Repeat("█", filled) + strings.Repeat("░", width-filled)
	return Colorize(bar, severity(percent))
}

// severity maps a usage percentage to the colour it deserves. The
// thresholds are the ones that matter for a monthly quota: 75% is
// worth noticing, 90% is worth acting on.
func severity(percent float64) Color {
	switch {
	case percent >= 90:
		return Red
	case percent >= 75:
		return Yellow
	default:
		return Green
	}
}

// Usage renders a percentage in the colour its severity earns.
func Usage(percent float64) string {
	return Colorize(Percent(percent), severity(percent))
}

// Truncate shortens s to at most n characters, marking the cut.
func Truncate(s string, n int) string {
	if n <= 1 || visibleLen(s) <= n {
		return s
	}
	runes := []rune(s)
	return string(runes[:n-1]) + "…"
}

func visibleLen(s string) int {
	n, inEscape := 0, false
	for _, r := range s {
		switch {
		case r == '\x1b':
			inEscape = true
		case inEscape && r == 'm':
			inEscape = false
		case !inEscape:
			n++
		}
	}
	return n
}
