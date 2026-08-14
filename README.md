<div align="center">

# bwg

**BandwagonHost / KiwiVM fleet control for humans and AI agents.**

<p><strong>One table for the whole account · CLI · Go SDK · MCP server · read-only enforced in the client, not by a prompt</strong></p>

[![ci](https://github.com/lroolle/bwg-cli/actions/workflows/ci.yml/badge.svg)](https://github.com/lroolle/bwg-cli/actions/workflows/ci.yml)
[![release](https://img.shields.io/github/v/release/lroolle/bwg-cli?label=release)](https://github.com/lroolle/bwg-cli/releases/latest)
[![go reference](https://pkg.go.dev/badge/github.com/lroolle/bwg-cli/kiwivm.svg)](https://pkg.go.dev/github.com/lroolle/bwg-cli/kiwivm)
[![license](https://img.shields.io/github/license/lroolle/bwg-cli?color=blue)](LICENSE)

[Page](https://lroolle.github.io/bwg-cli/) ·
[Install](#install) ·
[Quick start](#quick-start) ·
[Proof](#proof) ·
[Agents](#built-for-agents) ·
[llms.txt](llms.txt) ·
[Changelog](CHANGELOG.md)

<sub>AI agents / LLMs: read <a href="llms.txt">/llms.txt</a> for the short version, or
<a href="skills/bwg-cli/SKILL.md">skills/bwg-cli/SKILL.md</a> for the operational contract —
command tree, JSON shapes, exit codes.</sub>

</div>

```
$ bwg ls
SERVER  HOSTNAME           PLAN      LOCATION         BANDWIDTH   USED  RESETS  STATE
osaka   osaka.example.com  speed-2g  JP, Osaka        █████████░   94%  6d 3h   attention
tokyo   tokyo.example.com  micro128  JP, Tokyo        █████░░░░░   47%  6d 3h   ok
la      la.example.com     kvm-2g    US, Los Angeles  ██░░░░░░░░   19%  6d 3h   ok

Total: 1.8 TiB of 3.9 TiB across 3 servers (45%)

! osaka: bandwidth at 94%
```

<sub>That output is pinned by a test
([`render_test.go`](internal/cli/render_test.go)), so it cannot drift
away from what the tool actually prints.</sub>

One command answers the question a BandwagonHost account actually
raises: **which box is about to blow its monthly cap, and is anything
suspended?**

## Why this exists

KiwiVM's API authenticates per VPS — there is no account-level
endpoint. So every fleet-wide question is N calls with N credential
pairs, and the official panel makes you visit N pages. `bwg` sweeps
them concurrently and prints one table.

The other half is that a KiwiVM API key can reinstall your server.
Handing that to an agent needs a real answer, not a prompt.

## Read-only is a capability, not a flag

```go
c := kiwivm.New(veid, key, kiwivm.ReadOnly())
c.Restart(ctx)     // *ReadOnlyError — no HTTP request is made
c.ServiceInfo(ctx) // fine
```

Every endpoint is classified in one registry. The gate sits in the SDK
in front of the HTTP client, so the CLI, the MCP server and any Go
program importing the package all inherit it. A test reflects over
every client method and asserts that a read-only client refuses all 29
mutating operations *without touching the network* — so a method added
later is covered automatically.

```bash
bwg --read-only ls        # or: export BWG_READ_ONLY=1
bwg api ops               # see how every endpoint is classified
```

Three tiers, and the line between them is narrow on purpose:

| Risk | Meaning | Examples |
|------|---------|----------|
| `read` | changes nothing | `getServiceInfo`, `snapshot/list` |
| `write` | another call undoes it | `start`, `stop`, `restart`, `snapshot/create`, `setPTR` |
| `destructive` | irreversibly loses data, identity or access | `reinstallOS`, `snapshot/restore`, `kill`, `migrate/start` |

Stopping a VPS is a *write* because `start` undoes it. Deleting a
snapshot is *destructive* because nothing brings it back. Keeping the
destructive set small is what makes a destructive confirmation mean
something — every destructive entry has to state what is lost, and a
test enforces that.

## Proof

The safety model is the product, so it is tested structurally rather
than case by case. Every claim above has a command that checks it:

| Claim | How it is enforced | Reproduce |
|-------|--------------------|-----------|
| A read-only client cannot mutate | A test reflects over every client method, discovers which endpoint it calls, and asserts refusal **with no network access**. Methods added later are covered without anyone remembering. | `go test ./kiwivm -run ReadOnly -v` |
| The destructive tier stays meaningful | Every destructive entry must state what it loses, and the tier must stay a minority — 14 of 45 endpoints today. Both are assertions, not conventions. | `go test ./kiwivm -run 'Ops\|Destructive' -v` |
| No command panics on real payloads | Every CLI command and all 15 MCP tools run against a full fixture set, human and `--json`. | `go test ./internal/... ` |
| `--json` never carries an API key | Enforced by the type: `Server` marshals through a redacting shape, so a new field cannot leak by accident. | `go test ./internal/config -run JSON -v` |
| The `bwg ls` output above is real | Pinned by [`render_test.go`](internal/cli/render_test.go), so the README cannot drift from the tool. | `go test ./internal/cli -run Rendering -v` |

84.5% statement coverage (`go test ./... -cover`); CI runs gofmt, `go
vet` and `-race` tests on Linux, macOS and Windows, with the safety
tests as a separate job so a gate regression fails on its own line.

```bash
make check     # everything CI runs, locally
```

## When to use it, when to skip it

Use it if you have more than one BandwagonHost box, or one box and an
agent you would rather not hand a reinstall-capable key to.

Skip it if:

- **You have one VPS and the panel is already open.** One page, no
  install. bwg earns its place at N > 1, or when you want the fleet in
  JSON.
- **You need billing, renewals or new capacity.** KiwiVM's API has no
  billing endpoints, so neither does bwg. That stays in the portal.
- **Your VPS is not BandwagonHost/64clouds.** This speaks KiwiVM and
  nothing else.
- **You want a dashboard.** This is a terminal tool that prints tables
  and JSON; `bwg ls --json` into your own dashboard is the intended
  seam.

## Install

```bash
# One-liner
curl -fsSL https://raw.githubusercontent.com/lroolle/bwg-cli/main/install.sh | bash

# Go
go install github.com/lroolle/bwg-cli/cmd/bwg@latest

# From source
git clone https://github.com/lroolle/bwg-cli && cd bwg-cli && make install
```

## Quick start

Two values per VPS, both from the **KiwiVM control panel**:

| Value | What it is | Where to find it |
|-------|-----------|------------------|
| **VEID** | VPS ID number | In the panel URL: `kiwivm.64clouds.com/main.php?veid=1347645` |
| **API key** | Per-VPS secret | KiwiVM panel > **API** tab (looks like `private_xxxxxxxx`) |

```bash
# One box, no config file
export BWG_VEID=1347645
export BWG_API_KEY=private_xxxxxxxx

# A fleet
bwg server add tokyo --veid 1347645 --key private_xxx --tag prod
bwg server import keys.csv      # bulk, from the billing portal's CSV export
bwg server check                # verify every credential pair
```

```bash
bwg ls                          # the fleet
bwg ls --tag prod --alerting    # only production boxes needing attention
bwg info                        # plan, addresses, quota (fast)
bwg status                      # live power, load, disk (slow — queries the guest)
bwg ssh                         # ssh in, port resolved from the API
bwg usage --days 7              # traffic per day
bwg snapshot create -d "before the upgrade"
bwg abuse                       # suspensions and policy violations
bwg incidents                   # BandwagonHost outages, matched to YOUR servers
```

### Is it me, or is it them?

`bwg incidents` reads BandwagonHost's status page and correlates it
with your fleet, so "there is an incident somewhere" becomes "your box
is in it":

```
$ bwg incidents
! 1 ongoing incident(s) on the BandwagonHost status page.

● ongoing  #1785900000  Packet loss in Tokyo            1d 16h ago
  affects tokyo — mentions Tokyo, where this server is

● resolved #1785907793  Osaka upstream maintenance      2d 7h ago
  affects osaka — names node group v31xx, and this server is on v3105

Matching is a heuristic over incident text — a match is a prompt to look,
and no match is not an all-clear.
```

It reads the page's Atom feed rather than scraping HTML, and needs no
credentials. The matching is deliberately transparent: bwg prints *why*
it thinks a server is involved — the node group, the location — so you
judge the inference rather than trusting a badge.

## Built for agents

Every command speaks JSON, errors name their own fix, and exit codes
carry the reason.

```bash
bwg ls --json --jq '.servers[] | select(.bandwidth.percent > 80) | .server'
bwg status --json --jq '.state'
bwg api ops --risk destructive --json
```

| Exit | Meaning |
|------|---------|
| 0 | success |
| 1 | general failure |
| 2 | configuration — no server chosen, or none configured |
| 3 | refused — read-only mode, or confirmation not given |
| 4 | KiwiVM rejected the credentials |

- Data on stdout, diagnostics on stderr — always safe to pipe.
- `--dry-run` shows the consent card and what would happen, without
  calling the API. With `--json` it emits a structured preview:
  `{"dryRun":true, "endpoint":"...", "risk":"...", ...}`.
- Writes refuse rather than block when there is no terminal, naming
  `--yes`. A script never hangs on a question nobody can answer.
- `--json` never contains an API key. That is a property of the type,
  not a rule each command remembers.
- `bwg api call` reaches endpoints without a dedicated command, still
  through the gate.

### The consent card

Interactive mutations show what the decision depends on, not just what
is about to happen:

```
DESTRUCTIVE Restore a snapshot over the VPS
────────────────────────────────────────────────────────────
Server:       tokyo (veid 1347645)
Target:       1347645_20260801_aaaa.tar.gz
Hostname:     tokyo.example.com
Address:      203.0.113.10
Snapshot OS:  debian-11
Description:  before upgrade
Endpoint:     snapshot/restore

Irreversible: current VPS data is overwritten by the snapshot
────────────────────────────────────────────────────────────
This cannot be undone. Type tokyo to confirm:
```

Four operations replace the whole box — `reinstallOS`,
`snapshot restore`, `migrate start`, `cloneFromExternalServer` — and
ask for the server's name typed out. The mistake worth catching there
is not "did I mean to do this" but "did I mean to do this *here*", and
y/N cannot catch a wrong box.

### MCP

```bash
claude mcp add bwg -- bwg mcp --read-only
```

Tools are generated from the same registry. In read-only mode the
mutating tools are **not advertised at all** — an agent is never
offered a tool that is certain to be refused. MCP has no confirmation
channel, so serve read-only unless the host has an approval layer you
trust.

## The SDK

```go
import "github.com/lroolle/bwg-cli/kiwivm"

c := kiwivm.New("1347645", os.Getenv("BWG_API_KEY"), kiwivm.ReadOnly())

info, err := c.ServiceInfo(ctx)
if kiwivm.IsAuth(err) { /* the pair is wrong */ }
if kiwivm.IsTransient(err) { /* retry */ }

b := info.Bandwidth()
fmt.Printf("%.0f%% of %d bytes, resets in %s\n", b.Percent, b.Total, b.ResetsIn())
```

It covers every documented KiwiVM endpoint and absorbs the API's PHP
quirks so callers never see them: numbers that arrive as strings,
booleans as `"0"`/`"1"`, and empty objects serialized as `[]`.

**The bandwidth multiplier**, specifically, is the field everyone gets
wrong. KiwiVM scales both the allowance and the counter by
`monthly_data_multiplier`, so `Bandwidth().Percent` is correct however
the multiplier is interpreted — it is the one number that cannot be
read two ways. `bwg info` spells out what a multiplier above 1 means
rather than just printing it.

## Configuration

`~/.config/bwg/config.yaml` (respects `$XDG_CONFIG_HOME`, mode 0600):

```yaml
default: tokyo
servers:
  tokyo:
    veid: "1347645"
    api_key: private_xxxxxxxx
    note: main box
    tags: [prod, jp]
    ssh_user: root
    ssh_port: 0          # 0 = ask the API, which knows the real port
```

Which server a command acts on, most explicit first:

1. `--server <name>`
2. `$BWG_SERVER`
3. credentials in the environment (`BWG_VEID` + `BWG_API_KEY`,
   appearing as a server named `env`)
4. the configured `default`
5. the only configured server

| Variable | Purpose |
|----------|---------|
| `BWG_VEID` | VPS ID, no config file needed |
| `BWG_API_KEY` | API key; also accepts `veid:api_key` combined |
| `BWG_READ_ONLY` | force read-only; nothing can turn it back off |
| `BWG_SERVER` | which configured server to use |
| `BWG_CONFIG` | config file location |
| `BWG_COLOR` | `always` / `never` (also honours `NO_COLOR`) |
| `BWG_STATUS_FEED` | override the status-page feed (mirror or proxy) |

v0.1.0 spelled the key `BWG_KIWIVM_API_KEY`. That still works and warns
once per run; it will be removed at v1.0.

## Updating

```bash
bwg update            # download and install the latest release
bwg update --check    # just check, do not install
```

The binary is replaced atomically; the old version is kept as
`bwg.old` until the next update.

## Shell completion

```bash
bwg completion bash > /etc/bash_completion.d/bwg            # Linux
bwg completion bash > /opt/homebrew/etc/bash_completion.d/bwg  # macOS
bwg completion zsh  > "${fpath[1]}/_bwg"
bwg completion fish > ~/.config/fish/completions/bwg.fish
```

## Development

```bash
make check     # gofmt + vet + race tests + build
make cover
```

## Acknowledgments

- [strahe/bwh](https://github.com/strahe/bwh) — the prior Go client for
  this API, and the source of several hard-won wire-format details. If
  you want a library and none of the CLI, it is a good one.
- [dhslegen/bandwagon-dashboard](https://github.com/dhslegen/bandwagon-dashboard)
  — a copy of the KiwiVM REST API documentation.
- [gh](https://github.com/cli/cli) — the model for what a CLI should be.

## Status

Unofficial. Uses BandwagonHost's public KiwiVM REST API; not affiliated
with BandwagonHost or 64clouds.

MIT licensed. [Project page](https://lroolle.github.io/bwg-cli/) ·
[CHANGELOG](CHANGELOG.md) · [SECURITY.md](SECURITY.md) for what is
stored where.
