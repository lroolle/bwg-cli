package cli

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/lroolle/bwg-cli/bwhstatus"
	"github.com/lroolle/bwg-cli/internal/config"
	"github.com/lroolle/bwg-cli/internal/fleet"
	"github.com/lroolle/bwg-cli/pkg/output"
	"github.com/spf13/cobra"
)

// IncidentReport is the `bwg incidents --json` payload.
type IncidentReport struct {
	Summary   bwhstatus.Summary  `json:"summary"`
	Incidents []ReportedIncident `json:"incidents"`
	Source    string             `json:"source"`
	Hints     map[string]string  `json:"hints,omitempty"`
	Unchecked []string           `json:"unchecked,omitempty"`
}

// ReportedIncident is one incident plus which of your servers it may
// touch. The match is a heuristic, so the reasons travel with it.
type ReportedIncident struct {
	bwhstatus.Incident
	Affects []IncidentMatch `json:"affects,omitempty"`
}

// IncidentMatch names a server an incident may touch, and why.
type IncidentMatch struct {
	Server  string   `json:"server"`
	Reasons []string `json:"reasons"`
}

func newIncidentsCmd(app *App) *cobra.Command {
	var (
		all     bool
		ongoing bool
		tags    []string
		full    bool
	)

	cmd := &cobra.Command{
		Use:     "incidents [id]",
		Aliases: []string{"incident", "bwhstatus"},
		Short:   "BandwagonHost service incidents, matched against your fleet",
		Long: `Read BandwagonHost's status page and say which of your servers it
concerns.

The value over opening bwhstatus.com is the correlation: an incident
saying "impacts nodes v31xx, v32xx" or "Osaka upstream maintenance"
gets matched against your servers' node_alias and node_location, so
"there is an incident somewhere" becomes "your osaka box is in it".

Matching is a heuristic over prose written for humans. bwg prints WHY
it thinks a server is involved so you can judge the inference. It will
miss incidents phrased in ways nobody anticipated: a match is a prompt
to look, and no match is no information — not an all-clear.

Reads the status page's Atom feed. No credentials, nothing to change.
Correlating against your fleet costs one getServiceInfo per server;
--all skips that and just lists the incidents.

JSON shape:
  {"summary":{"operational","ongoing","total","lastUpdate"},
   "incidents":[{"id","number","title","link","published","updated",
     "content","resolved","locations":[],"nodePrefixes":[],
     "affects":[{"server","reasons":[]}]}],
   "source","unchecked":[]}`,
		Example: `  bwg incidents                  # recent incidents, matched to your fleet
  bwg incidents --ongoing        # only what is unresolved
  bwg incidents --all            # every incident, no fleet lookup
  bwg incidents 1785907793       # the full text of one
  bwg incidents --json --jq '.summary.operational'`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := app.Context(cmd.Context())
			defer cancel()

			sc := bwhstatus.New(bwhstatus.WithUserAgent(
				"bwg/" + app.Version + " (+https://github.com/lroolle/bwg-cli)"))

			incidents, err := sc.Fetch(ctx)
			if err != nil {
				return fmt.Errorf("%w\n\n  The status page is a third-party courtesy signal;\n"+
					"  your servers may be fine. Check them with: bwg ls", err)
			}

			if len(args) == 1 {
				return app.showIncident(incidents, args[0])
			}
			if ongoing {
				incidents = bwhstatus.Ongoing(incidents)
			}

			report := IncidentReport{
				Summary: bwhstatus.Summarize(incidents),
				Source:  bwhstatus.DefaultFeedURL,
				Hints: map[string]string{
					"detail": "bwg incidents <id>",
					"fleet":  "bwg ls",
				},
			}

			// Correlation needs to know where each server lives, which
			// costs one call per server. --all is the way to skip it.
			var targets []bwhstatus.Target
			if !all {
				var unchecked []string
				targets, unchecked = app.fleetTargets(ctx, tags)
				report.Unchecked = unchecked
			}

			for _, inc := range incidents {
				ri := ReportedIncident{Incident: inc}
				for _, t := range targets {
					if reasons := bwhstatus.Match(inc, t); len(reasons) > 0 {
						ri.Affects = append(ri.Affects, IncidentMatch{Server: t.Name, Reasons: reasons})
					}
				}
				report.Incidents = append(report.Incidents, ri)
			}

			return app.Emit(report, func(w io.Writer) {
				renderIncidents(w, report, all, full)
			})
		},
	}

	f := cmd.Flags()
	f.BoolVar(&all, "all", false, "List every incident without matching against your fleet")
	f.BoolVar(&ongoing, "ongoing", false, "Only incidents that are not resolved")
	f.StringSliceVar(&tags, "tag", nil, "Only match against servers carrying every given tag")
	f.BoolVar(&full, "full", false, "Print each incident's full text")
	return cmd
}

// fleetTargets gathers what an incident could refer to for each
// server. Servers that do not answer are named rather than silently
// dropped: an unchecked box is not a clear box.
func (a *App) fleetTargets(ctx context.Context, tags []string) ([]bwhstatus.Target, []string) {
	servers, err := a.Servers(tags)
	if err != nil {
		return nil, nil
	}

	results := fleet.Map(ctx, servers, a.Concurrency,
		func(ctx context.Context, s *config.Server) (bwhstatus.Target, error) {
			info, err := a.ClientFor(s).ServiceInfo(ctx)
			if err != nil {
				return bwhstatus.Target{Name: s.Name}, err
			}
			return bwhstatus.Target{
				Name:       s.Name,
				Location:   info.NodeLocation,
				NodeAlias:  info.NodeAlias,
				Datacenter: info.NodeDatacenter,
			}, nil
		})

	var targets []bwhstatus.Target
	var unchecked []string
	for _, r := range results {
		if r.OK() {
			targets = append(targets, r.Value)
		} else {
			unchecked = append(unchecked, r.Server.Name)
		}
	}
	return targets, unchecked
}

func (a *App) showIncident(incidents []bwhstatus.Incident, ref string) error {
	for _, inc := range incidents {
		if inc.Number != ref && !strings.Contains(inc.ID, ref) &&
			!strings.Contains(strings.ToLower(inc.Title), strings.ToLower(ref)) {
			continue
		}
		return a.Emit(inc, func(w io.Writer) {
			fmt.Fprintf(w, "%s %s\n\n", stateBadgeFor(inc), output.Strong(inc.Title))
			output.Tabbed(w, [][2]string{
				{"Posted", output.Time(inc.Published)},
				{"Updated", fmt.Sprintf("%s (%s)", output.Time(inc.Updated), output.Ago(inc.Updated))},
				{"Link", inc.Link},
			})
			fmt.Fprintf(w, "\n%s\n", inc.Content)
		})
	}

	var known []string
	for _, inc := range incidents {
		if inc.Number != "" {
			known = append(known, inc.Number)
		}
	}
	return fmt.Errorf("no incident matches %q\n\n  Recent incident ids: %s\n  List them: bwg incidents",
		ref, strings.Join(known, ", "))
}

func renderIncidents(w io.Writer, rep IncidentReport, all, full bool) {
	if rep.Summary.Operational {
		fmt.Fprintf(w, "%s BandwagonHost reports all systems operational.\n",
			output.Good("✓"))
	} else {
		fmt.Fprintf(w, "%s %d ongoing incident(s) on the BandwagonHost status page.\n",
			output.Bad("!"), rep.Summary.Ongoing)
	}
	if len(rep.Incidents) == 0 {
		fmt.Fprintf(w, "%s\n", output.Dim("No incidents in the feed's window."))
		return
	}

	for _, inc := range rep.Incidents {
		fmt.Fprintln(w)
		id := ""
		if inc.Number != "" {
			id = output.Dim("#" + inc.Number + "  ")
		}
		fmt.Fprintf(w, "%s %s%s %s\n", stateBadgeFor(inc.Incident), id,
			output.Strong(inc.Title), output.Dim(output.Ago(inc.Updated)))

		for _, m := range inc.Affects {
			fmt.Fprintf(w, "  %s %s — %s\n",
				output.Bad("affects"), output.Strong(m.Server),
				output.Dim(strings.Join(m.Reasons, "; ")))
		}
		if full {
			for _, line := range strings.Split(inc.Content, "\n") {
				fmt.Fprintf(w, "  %s\n", output.Dim(line))
			}
		}
	}

	// An honest footer: say what the matching did and did not cover.
	fmt.Fprintln(w)
	switch {
	case all:
		fmt.Fprintf(w, "%s\n", output.Dim(
			"Not matched against your fleet (--all). Drop --all to see which servers are involved."))
	default:
		fmt.Fprintf(w, "%s\n", output.Dim(
			"Matching is a heuristic over incident text — a match is a prompt to look, "+
				"and no match is not an all-clear."))
	}
	if len(rep.Unchecked) > 0 {
		fmt.Fprintf(w, "%s could not be checked: %s\n",
			output.Warn("!"), strings.Join(rep.Unchecked, ", "))
	}
	if !full {
		fmt.Fprintf(w, "%s\n", output.Dim("Full text: bwg incidents <id>"))
	}
}

func stateBadgeFor(inc bwhstatus.Incident) string {
	if inc.Resolved {
		return output.Good("● resolved")
	}
	return output.Bad("● ongoing")
}
