# bwg-cli

BandwagonHost / KiwiVM fleet CLI. Go + Cobra.

## Build

```
make            # build
make check      # gofmt + vet + race tests + build
make cover
```

## The one idea

KiwiVM authenticates per VPS — there is no account-level endpoint — and
a KiwiVM API key can reinstall a server. Everything here follows from
those two facts:

1. A fleet is unavoidably a list of `(veid, api_key)` pairs, so
   fleet-wide questions are N concurrent calls (`internal/fleet`).
2. Handing that key to an agent needs a real guarantee, so **read-only
   is a client capability, not a CLI flag**.

`kiwivm/ops.go` classifies every endpoint `read` / `write` /
`destructive`. `Client.call` checks that registry before the HTTP
client, so the CLI, the MCP server and external SDK consumers all
inherit one gate. Nothing re-implements it.

The line between write and destructive is deliberately narrow:
*destructive = irreversible loss of data, identity, or access that no
other call in the package can restore.* Stop is a write because Start
undoes it. A destructive tier that swallows everything makes its own
confirmation worthless — see TASTE.md.

## Architecture

```
kiwivm/                 The SDK. Importable, no bwg dependencies.
  ops.go                THE RISK REGISTRY. Single source of truth for
                        the gate, the CLI prompts, the MCP tool list
                        and `bwg api ops`. Adding an endpoint here is
                        what makes it reachable at all.
  client.go             HTTP, the read-only gate, GET for reads /
                        POST for writes (keeps the key out of URLs)
  methods.go            One method per endpoint, plus Raw()
  types.go              Wire types + the PHP-quirk absorbers (Int,
                        Bool, Map[V], Strings, Nullroutes) and the
                        derived helpers that earn the SDK its keep
                        (Bandwidth, IPv4/IPv6, State, DiskUsedBytes)
  errors.go             APIError / TransportError / ReadOnlyError and
                        the Is* classifiers

bwhstatus/              Client for BandwagonHost's public status page
                        (bwhstatus.com Atom feed) plus the fleet
                        correlation. Credential-free and read-only by
                        construction, so it sits outside kiwivm.Ops —
                        the one MCP tool with no endpoint. Matching is
                        a heuristic and every surface says so.

internal/
  config/               The fleet. YAML storage keeps keys; JSON
                        output masks them via Server.MarshalJSON —
                        the invariant is enforced by the type, not by
                        each command. Also the billing-portal CSV
                        importer.
  fleet/                Bounded concurrent fan-out. Failures come back
                        per-server; one dead box never hides the rest.
  cli/                  One package, one file per command group.
    app.go              App (resolved flags + streams), exit codes,
                        ErrDryRun, Explain() — the error-to-fix mapping
    confirm.go          THE CONSENT GATE. Read the comments before
                        changing the tiers. Also the --dry-run path:
                        emitDryRun renders the card (human) or a
                        structured preview (JSON) and returns ErrDryRun.
    root.go             Command tree, global flags, shell completion
    update.go           Self-updater (GitHub releases)
    incidents.go        Status-page correlation. Prints the REASON for
                        every match; a bare "affected" badge would be
                        a claim the data cannot support.
  updater/              Download and atomically replace the binary from
                        GitHub releases. No external dependencies.
  mcp/                  MCP stdio server, hand-rolled JSON-RPC 2.0.
                        Read-only mode omits mutating tools from the
                        list rather than refusing calls to them.

pkg/output/             Table, JSON, jq, colour, humane byte/duration
                        formatting. Colour is severity, never
                        decoration; it never appears in a pipe.

skills/bwg-cli/SKILL.md The operational contract for agents. When
                        command surface or JSON shapes change, this
                        changes with them or agents read two contracts.
```

## Design rules

- Every command supports `--json` and `--jq`, routed through
  `App.Emit` so no command has to remember.
- Data to stdout, diagnostics to stderr. Always.
- `--help` documents the JSON shape for commands with a non-obvious one.
- Errors name the command that fixes them (`Explain` in app.go).
- Exit codes are interface: 0 ok, 1 error, 2 config, 3 refused, 4 auth.
  `bwg exec` passes the guest's status through.
- A write with no terminal refuses rather than blocks. A script must
  never hang on a question nobody can answer.
- `--dry-run` validates and shows the consent card but does not call
  the API. It sits in `Confirm()`, so every write command gets it
  for free. With `--json` it emits a structured preview. It exits 0
  because "successfully did nothing" is not an error.
- Mutating commands resolve their client through `App.ClientForOp`, so
  a read-only refusal beats a preflight error. Otherwise read-only
  `net ipv6 add` reports "at capacity" — true, and not the reason.

## Where the load-bearing comments live

Code is the source of truth. Prefer, in order: a comment at the
behavior, a scar in TASTE.md, a where-to-look line here.

- `kiwivm/ops.go` — the risk criterion and why each destructive entry
  earns the tier
- `kiwivm/client.go:call` — why the gate is here and not in the CLI
- `kiwivm/types.go` — the PHP wire quirks, one comment per absorber
- `internal/cli/confirm.go` — the consent tiers and the `catastrophic`
  set (typed server name vs y/N) and why it must stay small
- `internal/config/config.go` — the JSON-masks / YAML-keeps split
- `internal/mcp/mcp.go:tools` — why read-only hides rather than refuses

## Testing

The safety model is the product, so it is tested structurally rather
than case by case:

- `kiwivm/readonly_test.go` reflects over every client method,
  *discovers* which endpoint each calls, then asserts a read-only
  client refuses every non-read one with no network access. A method
  added later is covered without anyone remembering.
- `kiwivm/ops_test.go` asserts every destructive entry states what it
  loses, and that the destructive tier stays a minority.
- `internal/cli/commands_test.go` drives every command against a full
  fixture set (breadth: catches panics and malformed payloads) and
  asserts read-only blocks every write command.
- `internal/cli/confirm_test.go` covers the consent tiers.

## Release

goreleaser on tag push. CI runs the safety tests as a separate job so a
gate regression fails loudly.

Design rejections and their reasons: TASTE.md.
