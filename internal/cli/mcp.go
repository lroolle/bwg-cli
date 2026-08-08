package cli

import (
	"os"

	"github.com/lroolle/bwg-cli/internal/mcp"
	"github.com/spf13/cobra"
)

func newMCPCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "mcp",
		Short: "Serve the fleet over the Model Context Protocol on stdio",
		Long: `Run bwg as an MCP server so an agent host can call it as tools.

Tools are generated from the same endpoint catalogue as the CLI
('bwg api ops'), so the risk classification is identical. With
--read-only, the operations bwg would refuse are not advertised at
all: an agent is never offered a tool that is certain to fail.

Note that MCP has no confirmation channel — an agent host that grants
a tool call gets it. Serving read-only is the way to make that safe,
and it is worth doing unless the host has its own approval layer you
trust.

Add to Claude Code:

  claude mcp add bwg -- bwg mcp --read-only

Or in a client's config file:

  {"mcpServers": {"bwg": {"command": "bwg", "args": ["mcp", "--read-only"]}}}`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			// stdout is the protocol channel; nothing else may touch it.
			srv := mcp.New(app.Cfg, app.ReadOnly, app.Version, os.Stdin, os.Stdout)
			return srv.Serve(cmd.Context())
		},
	}
}
