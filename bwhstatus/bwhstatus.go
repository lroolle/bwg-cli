// Package bwhstatus reads BandwagonHost's status page at
// https://bwhstatus.com and matches incidents against a fleet.
//
// The status page publishes an Atom feed, advertised from the page's
// own <link rel="alternate">. This package reads that feed rather than
// scraping the HTML: it is a stable, intentional interface, it needs
// no credentials, and it cannot change a thing.
//
// # Matching is a heuristic, and says so
//
// Incidents are prose written for humans — "This is going to impact
// network connectivity for VMs hosted on nodes v31xx, v32xx, v33xx".
// [Match] extracts the locations and node prefixes it can recognise
// and reports why it thinks an incident touches a server, so a caller
// can show the reasoning instead of asserting a conclusion. It will
// miss incidents phrased in ways nobody anticipated. Treat a match as
// a prompt to look, never as proof, and treat no match as no
// information rather than as an all-clear.
package bwhstatus

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
)

// DefaultFeedURL is the status page's Atom feed.
const DefaultFeedURL = "https://bwhstatus.com/feed.atom"

// EnvFeedURL overrides the feed location — for a mirror, a caching
// proxy, or a test.
const EnvFeedURL = "BWG_STATUS_FEED"

// DefaultTimeout bounds a status fetch. The status page is a courtesy
// signal, not the product: it must never be what makes bwg feel slow.
const DefaultTimeout = 15 * time.Second

// Client fetches the status feed.
type Client struct {
	feedURL string
	http    *http.Client
	ua      string
}

// Option configures a [Client].
type Option func(*Client)

// WithFeedURL overrides the feed location. Used by tests.
func WithFeedURL(u string) Option {
	return func(c *Client) {
		if u != "" {
			c.feedURL = u
		}
	}
}

// WithHTTPClient supplies the HTTP client to use.
func WithHTTPClient(h *http.Client) Option {
	return func(c *Client) {
		if h != nil {
			c.http = h
		}
	}
}

// WithUserAgent sets the User-Agent header.
func WithUserAgent(ua string) Option {
	return func(c *Client) {
		if ua != "" {
			c.ua = ua
		}
	}
}

// New builds a status client. It carries no credentials and can only
// read, so there is no read-only variant: every client is one.
func New(opts ...Option) *Client {
	c := &Client{
		feedURL: feedURLFromEnv(),
		http:    &http.Client{Timeout: DefaultTimeout},
		ua:      "bwg-cli (+https://github.com/lroolle/bwg-cli)",
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// feedURLFromEnv resolves the feed location, honouring EnvFeedURL.
func feedURLFromEnv() string {
	if u := strings.TrimSpace(os.Getenv(EnvFeedURL)); u != "" {
		return u
	}
	return DefaultFeedURL
}

// Incident is one entry from the status feed.
type Incident struct {
	// ID is the feed entry's identifier, usually the issue URL.
	ID string `json:"id"`
	// Number is the numeric issue id pulled out of the URL, when there
	// is one. It is what a person would quote.
	Number string `json:"number,omitempty"`
	// Title is the headline, with any "[Resolved]" prefix stripped.
	Title string `json:"title"`
	// Link points at the human-readable issue page.
	Link string `json:"link"`
	// Published is when the incident was first posted.
	Published time.Time `json:"published"`
	// Updated is the last change. For an ongoing incident this is the
	// most recent update, which is the timestamp that matters.
	Updated time.Time `json:"updated"`
	// Content is the full incident text, updates included.
	Content string `json:"content"`
	// Resolved reports whether BandwagonHost marked it resolved. The
	// feed signals this by prefixing the title with "[Resolved]".
	Resolved bool `json:"resolved"`
	// Locations are place names recognised in the incident.
	Locations []string `json:"locations,omitempty"`
	// NodePrefixes are node identifiers recognised in the incident,
	// normalized — "v31xx" becomes "v31".
	NodePrefixes []string `json:"nodePrefixes,omitempty"`
}

// State renders the incident's status as a word.
func (i Incident) State() string {
	if i.Resolved {
		return "resolved"
	}
	return "ongoing"
}

// Age returns how long ago the incident was last updated.
func (i Incident) Age() time.Duration { return time.Since(i.Updated) }

// -- feed parsing --------------------------------------------------------

type atomFeed struct {
	XMLName xml.Name    `xml:"feed"`
	Title   string      `xml:"title"`
	Updated string      `xml:"updated"`
	Entries []atomEntry `xml:"entry"`
}

type atomEntry struct {
	ID        string     `xml:"id"`
	Title     string     `xml:"title"`
	Updated   string     `xml:"updated"`
	Published string     `xml:"published"`
	Content   string     `xml:"content"`
	Links     []atomLink `xml:"link"`
}

type atomLink struct {
	Rel  string `xml:"rel,attr"`
	Href string `xml:"href,attr"`
}

// Fetch returns the incidents in the feed, most recently updated
// first.
func (c *Client) Fetch(ctx context.Context) ([]Incident, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.feedURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/atom+xml, application/xml;q=0.9")
	req.Header.Set("User-Agent", c.ua)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching the BandwagonHost status feed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the BandwagonHost status feed returned HTTP %d", resp.StatusCode)
	}
	// 4 MiB is far more than a status feed of recent incidents, and
	// bounds a page that has gone wrong.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	return Parse(body)
}

// Parse turns an Atom document into incidents. It is exported so a
// cached or offline copy can be used with the same semantics.
func Parse(body []byte) ([]Incident, error) {
	var feed atomFeed
	if err := xml.Unmarshal(body, &feed); err != nil {
		return nil, fmt.Errorf("parsing the status feed: %w", err)
	}

	out := make([]Incident, 0, len(feed.Entries))
	for _, e := range feed.Entries {
		title, resolved := splitResolved(e.Title)
		inc := Incident{
			ID:        strings.TrimSpace(e.ID),
			Number:    issueNumber(e.ID),
			Title:     title,
			Resolved:  resolved,
			Content:   strings.TrimSpace(e.Content),
			Published: parseTime(e.Published),
			Updated:   parseTime(e.Updated),
			Link:      alternateLink(e),
		}
		haystack := inc.Title + "\n" + inc.Content
		inc.Locations = findLocations(haystack)
		inc.NodePrefixes = findNodePrefixes(haystack)
		out = append(out, inc)
	}

	sort.SliceStable(out, func(i, j int) bool { return out[i].Updated.After(out[j].Updated) })
	return out, nil
}

// splitResolved strips the "[Resolved]" marker the feed puts in front
// of a finished incident's title, and reports whether it was there.
func splitResolved(title string) (string, bool) {
	t := strings.TrimSpace(title)
	for _, marker := range []string{"[Resolved]", "[RESOLVED]", "[resolved]"} {
		if strings.HasPrefix(t, marker) {
			return strings.TrimSpace(strings.TrimPrefix(t, marker)), true
		}
	}
	return t, false
}

func alternateLink(e atomEntry) string {
	for _, l := range e.Links {
		if l.Rel == "alternate" || l.Rel == "" {
			return l.Href
		}
	}
	return strings.TrimSpace(e.ID)
}

var issueIDRE = regexp.MustCompile(`id=(\d+)`)

func issueNumber(id string) string {
	if m := issueIDRE.FindStringSubmatch(id); len(m) == 2 {
		return m[1]
	}
	return ""
}

// parseTime accepts the timestamp formats an Atom feed may carry.
// A timestamp that will not parse yields the zero time rather than an
// error: a malformed date should not hide an incident.
func parseTime(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339, time.RFC3339Nano, "2006-01-02T15:04:05Z0700"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

// -- recognising what an incident is about --------------------------------

// knownLocations are the place names BandwagonHost uses for its
// locations, lowercased. Incidents are prose, so this is matched
// against the text; getServiceInfo reports locations in the same
// vocabulary ("JP, Osaka", "US, Los Angeles").
var knownLocations = []string{
	"osaka", "tokyo", "japan", "hong kong", "singapore", "taiwan",
	"los angeles", "san jose", "fremont", "seattle", "phoenix",
	"new york", "new jersey", "miami", "florida", "chicago", "dallas",
	"kansas city", "washington", "amsterdam", "netherlands", "london",
	"frankfurt", "germany", "canada", "vancouver", "toronto",
	"south korea", "korea", "india", "australia", "sydney",
}

func findLocations(text string) []string {
	lower := strings.ToLower(text)
	var found []string
	seen := map[string]bool{}
	for _, loc := range knownLocations {
		if strings.Contains(lower, loc) && !seen[loc] {
			seen[loc] = true
			found = append(found, loc)
		}
	}
	sort.Strings(found)
	return found
}

// nodeRE matches the node identifiers incidents use: "v31xx" for a
// family of nodes, "v3105" for one. Both normalize to a prefix that
// can be tested against a server's node_alias.
var nodeRE = regexp.MustCompile(`(?i)\bv(\d{2,4})(xx|\d*)\b`)

func findNodePrefixes(text string) []string {
	var found []string
	seen := map[string]bool{}
	for _, m := range nodeRE.FindAllStringSubmatch(text, -1) {
		prefix := "v" + m[1]
		if !seen[prefix] {
			seen[prefix] = true
			found = append(found, prefix)
		}
	}
	sort.Strings(found)
	return found
}

// -- matching against a fleet ---------------------------------------------

// Target is the little bwg knows about a VPS that an incident could
// refer to. It is a plain struct rather than a config or API type so
// this package stays independent of both.
type Target struct {
	// Name is the server's name in the fleet, for reporting.
	Name string
	// Location is getServiceInfo's node_location, e.g. "JP, Osaka".
	Location string
	// NodeAlias is getServiceInfo's node_alias.
	NodeAlias string
	// Datacenter is getServiceInfo's node_datacenter, when set.
	Datacenter string
}

// Match reports why an incident may affect a target, or nil when
// nothing links them.
//
// The reasons are the output that matters: showing "mentions Osaka,
// where this server is" lets a person judge the inference themselves,
// which a bare "affected" badge does not.
func Match(inc Incident, t Target) []string {
	var reasons []string

	place := strings.ToLower(t.Location + " " + t.Datacenter)
	for _, loc := range inc.Locations {
		if strings.Contains(place, loc) {
			reasons = append(reasons,
				fmt.Sprintf("mentions %s, where this server is", titleCase(loc)))
		}
	}

	if alias := strings.ToLower(strings.TrimSpace(t.NodeAlias)); alias != "" {
		for _, prefix := range inc.NodePrefixes {
			if strings.HasPrefix(alias, prefix) {
				reasons = append(reasons,
					fmt.Sprintf("names node group %sxx, and this server is on %s", prefix, t.NodeAlias))
			}
		}
	}
	return reasons
}

// Affected pairs each target with the incidents that may touch it.
type Affected struct {
	Target   Target
	Incident Incident
	Reasons  []string
}

// MatchAll returns every (incident, target) pair that Match links,
// ordered by incident recency then target name — stable output for a
// table.
func MatchAll(incidents []Incident, targets []Target) []Affected {
	var out []Affected
	for _, inc := range incidents {
		for _, t := range targets {
			if reasons := Match(inc, t); len(reasons) > 0 {
				out = append(out, Affected{Target: t, Incident: inc, Reasons: reasons})
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].Incident.Updated.Equal(out[j].Incident.Updated) {
			return out[i].Incident.Updated.After(out[j].Incident.Updated)
		}
		return out[i].Target.Name < out[j].Target.Name
	})
	return out
}

// Ongoing filters to incidents BandwagonHost has not marked resolved.
func Ongoing(incidents []Incident) []Incident {
	var out []Incident
	for _, inc := range incidents {
		if !inc.Resolved {
			out = append(out, inc)
		}
	}
	return out
}

// Summary is the overall picture derived from the feed.
//
// It is derived, not read from the page's own banner: the banner is
// HTML that would have to be scraped, and an unresolved entry in the
// feed is the same fact stated in a stable format.
type Summary struct {
	// Operational is true when no incident is open.
	Operational bool `json:"operational"`
	// Ongoing is the number of unresolved incidents.
	Ongoing int `json:"ongoing"`
	// Total is how many incidents the feed carried.
	Total int `json:"total"`
	// LastUpdate is the most recent change across all incidents.
	LastUpdate time.Time `json:"lastUpdate,omitempty"`
}

// Summarize derives the overall status from a set of incidents.
func Summarize(incidents []Incident) Summary {
	s := Summary{Operational: true, Total: len(incidents)}
	for _, inc := range incidents {
		if !inc.Resolved {
			s.Operational = false
			s.Ongoing++
		}
		if inc.Updated.After(s.LastUpdate) {
			s.LastUpdate = inc.Updated
		}
	}
	return s
}

func titleCase(s string) string {
	parts := strings.Fields(s)
	for i, p := range parts {
		if p == "" {
			continue
		}
		parts[i] = strings.ToUpper(p[:1]) + p[1:]
	}
	return strings.Join(parts, " ")
}
