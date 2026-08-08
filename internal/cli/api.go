package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strings"

	"github.com/lroolle/bwg-cli/kiwivm"
	"github.com/lroolle/bwg-cli/pkg/output"
	"github.com/spf13/cobra"
)

func newAPICmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "api",
		Short: "Inspect the endpoint catalogue and call KiwiVM directly",
		Long: `The escape hatch, and the map.

'bwg api ops' prints every KiwiVM endpoint bwg knows with its risk
classification. That table is the single source of truth the read-only
gate, the confirmation prompts and the MCP tool list all read from —
so it is the honest answer to "what can this thing do to my server".

'bwg api call' reaches an endpoint bwg has no dedicated command for,
with the same credentials, the same risk gate and the same
confirmation.`,
	}
	cmd.AddCommand(apiOps(app), apiCall(app))
	return cmd
}

func apiOps(app *App) *cobra.Command {
	var riskFilter string

	cmd := &cobra.Command{
		Use:   "ops",
		Short: "List every endpoint with its risk classification",
		Long: `Print the endpoint catalogue.

Risk levels:
  read         observes state and changes nothing
  write        changes state in a way another call can undo
  destructive  irreversibly loses data, identity, or access

A read-only client refuses everything above read, in the SDK, before
any request is made. Every destructive entry states what is lost.

JSON shape: {"ops":[{"endpoint","risk","summary","why"}],"count",
"readOnly"}`,
		Example: `  bwg api ops
  bwg api ops --risk destructive
  bwg api ops --json --jq '[.ops[] | select(.risk=="read") | .endpoint]'`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ops := kiwivm.ListOps()
			if riskFilter != "" {
				want := strings.ToLower(riskFilter)
				var kept []kiwivm.Op
				for _, op := range ops {
					if op.Risk.String() == want {
						kept = append(kept, op)
					}
				}
				if kept == nil {
					return fmt.Errorf("%q is not a risk level — use read, write or destructive", riskFilter)
				}
				ops = kept
			}
			return app.Emit(
				map[string]any{"ops": ops, "count": len(ops), "readOnly": app.ReadOnly},
				func(w io.Writer) {
					t := output.NewTable("ENDPOINT", "RISK", "SUMMARY")
					for _, op := range ops {
						t.Row(op.Endpoint, riskCell(op.Risk), op.Summary)
					}
					t.Render(w)

					if riskFilter == "" || strings.EqualFold(riskFilter, "destructive") {
						fmt.Fprintf(w, "\n%s\n", output.Dim("What the destructive ones lose"))
						for _, op := range ops {
							if op.Why == "" {
								continue
							}
							fmt.Fprintf(w, "  %s %s\n", output.Bad(op.Endpoint), output.Dim(op.Why))
						}
					}
					if app.ReadOnly {
						fmt.Fprintf(w, "\n%s\n",
							output.Good("Read-only mode is on: only the read operations above will run."))
					}
				})
		},
	}
	cmd.Flags().StringVar(&riskFilter, "risk", "", "Only endpoints at this risk level")
	return cmd
}

func riskCell(r kiwivm.Risk) string {
	switch r {
	case kiwivm.RiskRead:
		return output.Good("read")
	case kiwivm.RiskWrite:
		return output.Warn("write")
	}
	return output.Bad("destructive")
}

func apiCall(app *App) *cobra.Command {
	var fields []string

	cmd := &cobra.Command{
		Use:   "call <endpoint>",
		Short: "Call a KiwiVM endpoint directly and print the raw response",
		Long: `Call any endpoint from 'bwg api ops'.

veid and api_key are supplied from the resolved server; pass anything
else with -f. The response is printed as raw JSON, exactly as KiwiVM
returned it.

The risk gate still applies: a read-only client refuses non-reads, and
write and destructive endpoints still prompt. This is an escape hatch
from bwg's command surface, not from its safety model.`,
		Example: `  bwg api call getServiceInfo
  bwg api call snapshot/list --jq '.snapshots[].fileName'
  bwg api call setPTR -f ip=1.2.3.4 -f ptr=mail.example.com`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			endpoint := strings.TrimPrefix(args[0], "/")
			op, known := kiwivm.LookupOp(endpoint)
			if !known {
				return fmt.Errorf("unknown endpoint %q\n\n  List them: bwg api ops", endpoint)
			}

			params := url.Values{}
			for _, f := range fields {
				k, v, ok := strings.Cut(f, "=")
				if !ok {
					return fmt.Errorf("-f %q must be key=value", f)
				}
				params.Set(k, v)
			}

			c, s, err := app.Client()
			if err != nil {
				return err
			}
			ctx, cancel := app.Context(cmd.Context())
			defer cancel()

			var facts [][2]string
			for _, k := range sortedValueKeys(params) {
				facts = append(facts, [2]string{k, params.Get(k)})
			}
			if err := app.Confirm(Consent{
				Op: op, Server: s, Target: endpoint, Facts: facts,
			}); err != nil {
				return err
			}

			raw, err := c.Raw(ctx, endpoint, params)
			if err != nil {
				return Explain(err, s.Name)
			}

			// The response is whatever KiwiVM said. Re-indent it so a
			// human can read it, but change nothing else.
			var pretty any
			if err := json.Unmarshal(raw, &pretty); err != nil {
				fmt.Fprintln(app.Out, string(raw))
				return nil
			}
			return app.Emit(pretty, func(w io.Writer) { output.JSON(w, pretty) })
		},
	}
	cmd.Flags().StringArrayVarP(&fields, "field", "f", nil, "Parameter as key=value (repeatable)")
	return cmd
}

func sortedValueKeys(v url.Values) []string {
	out := make([]string, 0, len(v))
	for k := range v {
		out = append(out, k)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
