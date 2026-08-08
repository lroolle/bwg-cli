package bwhstatus

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

// realFeed is the shape bwhstatus.com actually served on 2026-08-08,
// trimmed. Tests parse the real thing rather than an idealized one.
const realFeed = `<?xml version="1.0" encoding="utf-8"?>
<feed xmlns="http://www.w3.org/2005/Atom">
    <id>https://bwhstatus.com/</id>
    <title>BandwagonHost Status</title>
    <updated>2026-08-05T18:26:03-07:00</updated>
    <link rel="self" href="https://bwhstatus.com/feed.atom" />
    <link rel="alternate" href="https://bwhstatus.com/" />
    <author><name>BandwagonHost</name></author>
    <entry>
        <id>https://bwhstatus.com/issue.php?id=1785907793</id>
        <title>[Resolved] Osaka upstream maintenance</title>
        <updated>2026-08-05T18:26:03-07:00</updated>
        <published>2026-08-04T22:29:53-07:00</published>
        <link rel="alternate" href="https://bwhstatus.com/issue.php?id=1785907793" />
        <content type="text">We have received this from one of our upstream providers in Osaka, JP:

Location: Osaka, Japan

This is going to impact network connectivity for VMs hosted on nodes v31xx, v32xx, v33xx.

UPDATE: This has been completed.</content>
    </entry>
    <entry>
        <id>https://bwhstatus.com/issue.php?id=1785900000</id>
        <title>Packet loss in Los Angeles</title>
        <updated>2026-08-06T10:00:00-07:00</updated>
        <published>2026-08-06T09:00:00-07:00</published>
        <link rel="alternate" href="https://bwhstatus.com/issue.php?id=1785900000" />
        <content type="text">We are investigating elevated packet loss affecting node v4207 in Los Angeles.</content>
    </entry>
</feed>`

func parseOrFail(t *testing.T, body string) []Incident {
	t.Helper()
	got, err := Parse([]byte(body))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return got
}

func TestParseRealFeed(t *testing.T) {
	got := parseOrFail(t, realFeed)
	if len(got) != 2 {
		t.Fatalf("parsed %d incidents, want 2", len(got))
	}

	// Most recently updated first: the LA entry was updated later.
	if got[0].Number != "1785900000" {
		t.Errorf("incidents are not newest-first: %+v", got[0].Title)
	}

	la := got[0]
	if la.Title != "Packet loss in Los Angeles" {
		t.Errorf("title = %q", la.Title)
	}
	if la.Resolved {
		t.Error("an entry with no [Resolved] prefix was marked resolved")
	}
	if la.State() != "ongoing" {
		t.Errorf("State() = %q", la.State())
	}
	if la.Link != "https://bwhstatus.com/issue.php?id=1785900000" {
		t.Errorf("Link = %q", la.Link)
	}

	osaka := got[1]
	if !osaka.Resolved {
		t.Error("the [Resolved] entry was not marked resolved")
	}
	// The marker is a status signal, not part of the headline.
	if strings.Contains(osaka.Title, "Resolved") {
		t.Errorf("the [Resolved] prefix stayed in the title: %q", osaka.Title)
	}
	if osaka.Title != "Osaka upstream maintenance" {
		t.Errorf("title = %q", osaka.Title)
	}
	want := time.Date(2026, 8, 4, 22, 29, 53, 0, time.FixedZone("", -7*3600))
	if !osaka.Published.Equal(want) {
		t.Errorf("Published = %v, want %v", osaka.Published, want)
	}
}

func TestParseExtractsLocationsAndNodes(t *testing.T) {
	got := parseOrFail(t, realFeed)

	byNumber := map[string]Incident{}
	for _, inc := range got {
		byNumber[inc.Number] = inc
	}

	osaka := byNumber["1785907793"]
	if !reflect.DeepEqual(osaka.Locations, []string{"japan", "osaka"}) {
		t.Errorf("Osaka locations = %v", osaka.Locations)
	}
	if !reflect.DeepEqual(osaka.NodePrefixes, []string{"v31", "v32", "v33"}) {
		t.Errorf("Osaka node prefixes = %v, want the vNNxx groups", osaka.NodePrefixes)
	}

	la := byNumber["1785900000"]
	if !reflect.DeepEqual(la.Locations, []string{"los angeles"}) {
		t.Errorf("LA locations = %v", la.Locations)
	}
	// A specific node, "v4207", normalizes to a testable prefix.
	if !reflect.DeepEqual(la.NodePrefixes, []string{"v4207"}) {
		t.Errorf("LA node prefixes = %v", la.NodePrefixes)
	}
}

// The correlation is the whole point of the feature: an incident about
// "nodes v31xx" has to reach the box that lives on v3105.
func TestMatchByNodeAndLocation(t *testing.T) {
	incidents := parseOrFail(t, realFeed)
	osaka := incidents[1]

	onAffectedNode := Target{Name: "osaka-1", Location: "JP, Osaka", NodeAlias: "v3105"}
	reasons := Match(osaka, onAffectedNode)
	if len(reasons) < 2 {
		t.Fatalf("expected both a location and a node reason, got %v", reasons)
	}
	joined := strings.Join(reasons, " | ")
	if !strings.Contains(joined, "Osaka") {
		t.Errorf("no location reason: %v", reasons)
	}
	if !strings.Contains(joined, "v31") || !strings.Contains(joined, "v3105") {
		t.Errorf("the node reason does not name both the group and the server's node: %v", reasons)
	}

	// Right city, unaffected node group: still worth flagging, on the
	// location alone, and the reason says so.
	otherNode := Target{Name: "osaka-2", Location: "JP, Osaka", NodeAlias: "v9901"}
	if r := Match(osaka, otherNode); len(r) != 1 || !strings.Contains(r[0], "Osaka") {
		t.Errorf("location-only match = %v", r)
	}

	// A different continent must not match.
	unrelated := Target{Name: "la", Location: "US, Los Angeles", NodeAlias: "v4207"}
	if r := Match(osaka, unrelated); len(r) != 0 {
		t.Errorf("an unrelated server matched: %v", r)
	}
}

func TestMatchIsCaseInsensitiveOnNodeAlias(t *testing.T) {
	incidents := parseOrFail(t, realFeed)
	osaka := incidents[1]

	if r := Match(osaka, Target{Name: "x", NodeAlias: "V3105"}); len(r) == 0 {
		t.Error("an uppercase node alias did not match")
	}
}

func TestMatchNeedsSomethingToMatchOn(t *testing.T) {
	incidents := parseOrFail(t, realFeed)
	// A server bwg knows nothing about must produce no reasons rather
	// than a vacuous match.
	if r := Match(incidents[0], Target{Name: "mystery"}); len(r) != 0 {
		t.Errorf("an empty target matched: %v", r)
	}
}

func TestMatchAllIsOrderedAndComplete(t *testing.T) {
	incidents := parseOrFail(t, realFeed)
	targets := []Target{
		{Name: "zulu-osaka", Location: "JP, Osaka", NodeAlias: "v3105"},
		{Name: "alpha-osaka", Location: "JP, Osaka", NodeAlias: "v3201"},
		{Name: "la", Location: "US, Los Angeles", NodeAlias: "v4207"},
	}

	got := MatchAll(incidents, targets)
	if len(got) != 3 {
		t.Fatalf("got %d matches, want 3 (2 Osaka + 1 LA): %+v", len(got), got)
	}
	// Newest incident first, then target name — stable table output.
	if got[0].Target.Name != "la" {
		t.Errorf("the newest incident's match should lead: %v", got[0].Target.Name)
	}
	if got[1].Target.Name != "alpha-osaka" || got[2].Target.Name != "zulu-osaka" {
		t.Errorf("ties are not broken by target name: %v, %v",
			got[1].Target.Name, got[2].Target.Name)
	}
}

func TestOngoingAndSummarize(t *testing.T) {
	incidents := parseOrFail(t, realFeed)

	ongoing := Ongoing(incidents)
	if len(ongoing) != 1 || ongoing[0].Number != "1785900000" {
		t.Errorf("Ongoing() = %+v", ongoing)
	}

	s := Summarize(incidents)
	if s.Operational {
		t.Error("Operational is true with an unresolved incident open")
	}
	if s.Ongoing != 1 || s.Total != 2 {
		t.Errorf("Summarize() = %+v", s)
	}
	if s.LastUpdate.IsZero() {
		t.Error("LastUpdate was not set")
	}

	// All resolved, or nothing at all, is operational.
	if got := Summarize(nil); !got.Operational || got.Total != 0 {
		t.Errorf("Summarize(nil) = %+v", got)
	}
	if got := Summarize([]Incident{{Resolved: true}}); !got.Operational {
		t.Error("a feed of resolved incidents should read operational")
	}
}

func TestFetch(t *testing.T) {
	var gotUA, gotAccept string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA, gotAccept = r.Header.Get("User-Agent"), r.Header.Get("Accept")
		w.Header().Set("Content-Type", "application/atom+xml")
		w.Write([]byte(realFeed))
	}))
	defer srv.Close()

	c := New(WithFeedURL(srv.URL), WithUserAgent("bwg/test"))
	got, err := c.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("fetched %d incidents", len(got))
	}
	if gotUA != "bwg/test" {
		t.Errorf("User-Agent = %q", gotUA)
	}
	if !strings.Contains(gotAccept, "atom") {
		t.Errorf("Accept = %q", gotAccept)
	}
}

func TestFetchFailures(t *testing.T) {
	t.Run("non-200", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(503)
		}))
		defer srv.Close()
		if _, err := New(WithFeedURL(srv.URL)).Fetch(context.Background()); err == nil {
			t.Error("a 503 was accepted")
		} else if !strings.Contains(err.Error(), "503") {
			t.Errorf("the error hides the status: %v", err)
		}
	})

	t.Run("not XML", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte("<html>we moved</html>"))
		}))
		defer srv.Close()
		if _, err := New(WithFeedURL(srv.URL)).Fetch(context.Background()); err == nil {
			t.Error("an HTML body parsed as a feed")
		}
	})

	t.Run("unreachable", func(t *testing.T) {
		c := New(WithFeedURL("http://127.0.0.1:1/feed.atom"))
		if _, err := c.Fetch(context.Background()); err == nil {
			t.Error("a refused connection was accepted")
		}
	})
}

// A status page is a courtesy signal. A malformed timestamp or a
// missing field must degrade, never drop an incident on the floor.
func TestParseToleratesRoughEntries(t *testing.T) {
	feed := `<?xml version="1.0" encoding="utf-8"?>
<feed xmlns="http://www.w3.org/2005/Atom">
  <entry>
    <id>bare-id-no-number</id>
    <title>Something happened</title>
    <updated>not a date</updated>
  </entry>
</feed>`
	got := parseOrFail(t, feed)
	if len(got) != 1 {
		t.Fatalf("a rough entry was dropped: %+v", got)
	}
	if got[0].Title != "Something happened" {
		t.Errorf("title = %q", got[0].Title)
	}
	if got[0].Number != "" {
		t.Errorf("Number = %q, want empty when the id has none", got[0].Number)
	}
	if !got[0].Updated.IsZero() {
		t.Errorf("an unparseable date should be the zero time, got %v", got[0].Updated)
	}
	// With no id link, the entry id is the best pointer available.
	if got[0].Link != "bare-id-no-number" {
		t.Errorf("Link = %q", got[0].Link)
	}
}

func TestEmptyFeed(t *testing.T) {
	got := parseOrFail(t, `<?xml version="1.0"?><feed xmlns="http://www.w3.org/2005/Atom"></feed>`)
	if len(got) != 0 {
		t.Errorf("an empty feed produced %d incidents", len(got))
	}
	if s := Summarize(got); !s.Operational {
		t.Error("an empty feed should read operational")
	}
}

// "v31xx" and "v3105" both have to normalize to something a node alias
// can be prefix-tested against, and ordinary prose must not produce
// phantom node references.
func TestNodePrefixExtraction(t *testing.T) {
	cases := map[string][]string{
		"nodes v31xx, v32xx":           {"v31", "v32"},
		"node v4207 is down":           {"v4207"},
		"NODE V99XX":                   {"v99"},
		"no nodes here":                nil,
		"version 3 of the v2 protocol": nil, // v2 is too short to be a node
		"affects v31xx and also v31xx": {"v31"},
	}
	for text, want := range cases {
		if got := findNodePrefixes(text); !reflect.DeepEqual(got, want) {
			t.Errorf("findNodePrefixes(%q) = %v, want %v", text, got, want)
		}
	}
}
