---
name: bwg-cli
description: >
  BandwagonHost / KiwiVM VPS fleet control. Use when working with
  BandwagonHost or 64clouds servers: checking bandwidth against the
  monthly cap, power state, snapshots, backups, OS reinstall, rDNS,
  IPv6, suspensions and abuse points. Triggers on: bandwagonhost, BWH,
  BWG, kiwivm, 64clouds, VPS bandwidth, VPS snapshot, VPS reinstall.
---

# bwg — BandwagonHost fleet CLI

```
bwg
├── ls                          # FLEET: bandwidth, quota, what needs attention
│   └── --tag --alerting --sort --live
├── info                        # one server: plan, addresses, quota (fast)
├── status                      # one server: live power/load/disk (slow, ~15s)
├── usage                       # traffic per day  --raw --days
├── audit                       # panel audit log  --limit --grep
├── ratelimit                   # remaining API budget
├── incidents [id]              # BWH status page, matched to YOUR fleet
│   └── --ongoing --all --tag --full
├── ssh [-- args]               # ssh in, port from API  --print --ipv6 -i
├── snapshot  ls|create|rm|restore|sticky|export|import
├── backup    ls|restore        # restore = copy to a snapshot first
├── os        ls|reinstall
├── keys      ls|set            # SSH keys used by reinstall
├── passwd                      # new root password
├── host <name>                 # set the KiwiVM hostname record
├── net       ls|ptr|ipv6|private
├── iso       ls|mount|unmount
├── abuse     ls|resolve|unsuspend
├── migrate   ls|start
├── notify    ls|set
├── power     start|stop|restart|kill   (start/stop/restart also top-level)
├── exec <cmd>                  # run a command in the guest, synchronous
├── run [script]                # run a script in the guest, detached
├── server    ls|add|rm|set|default|show|import|check
├── api       ops|call          # the risk catalogue, and the escape hatch
├── mcp                         # MCP stdio server
├── update                      # self-update from GitHub releases  --check
└── completion bash|zsh|fish    # shell completion scripts
```

Global: `--server/-s` `--json` `--jq` `--read-only` `--dry-run` `--yes/-y`
`--verbose/-v` `--no-color` `--config` `--timeout` `--concurrency`

## Read this first

**Default to `--read-only` unless the task is explicitly to change
something.** It is enforced in the SDK, below the CLI, so a write
cannot happen by accident:

```bash
bwg --read-only ls          # or: export BWG_READ_ONLY=1
```

**Preview with `--dry-run`.** Every write command supports it. It
validates, shows the consent card, and stops — no API call is made.
With `--json` it emits `{"dryRun":true, "endpoint":"...", ...}`.

**Writes need `--yes` when there is no terminal.** Without it, bwg
refuses rather than hanging on a prompt nobody can answer. That
refusal is exit code 3 and is not a bug — it means the command was
understood and deliberately not run.

**Check the risk before acting.** `bwg api ops` classifies every
endpoint, and it is the same table the gate reads:

```bash
bwg api ops --risk destructive --json --jq '.ops[] | {endpoint, why}'
```

- `read` — changes nothing
- `write` — another call undoes it (start/stop/restart, snapshot create, PTR)
- `destructive` — irreversibly loses data, identity or access

## Setup

Two values per VPS, both from the **KiwiVM control panel**:

- **VEID** — the VPS ID number, visible in the panel URL after `?veid=`
- **API key** — the per-VPS secret under the **API** tab (`private_xxxxxxxx`)

There is no account-level API: every call needs the pair for one
specific box, so a fleet is a list of pairs.

```bash
# One box, no config file — the agent-friendly path:
export BWG_VEID=1347645
export BWG_KIWIVM_API_KEY=private_xxxxxxxx     # or "1347645:private_xxx" combined

# A fleet:
bwg server add tokyo --veid 1347645 --key private_xxx --tag prod
bwg server import keys.csv       # the billing portal's API key export
bwg server check                 # verify every credential pair
```

Config lives at `~/.config/bwg/config.yaml` (mode 0600);
`--json` output never contains a key.

## Exit codes

Branch on these instead of parsing messages.

| Code | Meaning |
|------|---------|
| 0 | success |
| 1 | general failure |
| 2 | configuration: no server chosen, or none configured |
| 3 | refused: read-only mode, or confirmation not given |
| 4 | KiwiVM rejected the credentials |
| other | `bwg exec` passes the guest command's exit status through |

## JSON shapes

```
ls        → {"servers":[{"server","veid","hostname","plan","location","os",
              "ipv4":[],"ipv6":[],"bandwidth":{"used","total","free","percent",
              "multiplier","resetsAt"},"abuse":{"points","max","percent"},
              "alerts":[]}],"failed":[{"server","error"}],
              "totals":{"servers","reachable","bandwidthUsed","percent",
              "needsAttention"}}
info      → {"server","info":{...raw getServiceInfo...},
              "derived":{"bandwidth","ipv4","ipv6","abusePercent","healthy"}}
status    → {"server","state","running","sshPort","hostname",
              "resources":{"memUsed","memTotal","diskUsed","diskTotal",
              "loadAverage"},"throttled":{"cpu","disk"},"live":{...}}
usage     → {"server","vmType","days":[{"date","networkIn","networkOut",
              "diskRead","diskWrite","cpuAvg","samples"}],"totals","bandwidth"}
snapshot  → {"server","snapshots":[{"fileName","os","description","size",
              "sticky","purgesIn","md5","downloadLinkSSL"}],"count"}
abuse ls  → {"server","abusePoints","maxAbusePoints","percent",
              "suspensions":[...],"violations":[...],"evidence":{}}
api ops   → {"ops":[{"endpoint","risk","summary","why"}],"count","readOnly"}
server ls → {"servers":[{"name","veid","apiKey" (masked),"tags"}],"default"}
```

Most payloads carry a `hints` object naming the follow-up command.

## Recipes

**Which box is about to blow its cap**

```bash
bwg ls --json --jq '.servers[] | select(.bandwidth.percent > 80) | {server, pct: .bandwidth.percent}'
bwg ls --alerting                      # anything needing attention, human-readable
```

**Is anything wrong across the fleet**

```bash
bwg ls --json --jq '[.servers[] | select(.alerts | length > 0) | .server]'
bwg ls --json --jq '.totals.needsAttention'
```

**Before doing anything risky, snapshot**

```bash
bwg snapshot create -d "before $(date -I) change" --yes
bwg snapshot ls --json --jq '.snapshots[0].fileName'
```

**Is it the provider or is it me?**

```bash
bwg incidents --ongoing                    # open incidents, matched to your fleet
bwg incidents --json --jq '.summary.operational'
bwg incidents --json --jq '[.incidents[] | select(.affects) | {title, servers: [.affects[].server]}]'
```

Run this BEFORE concluding a slow or unreachable server is your own
fault. It reads BandwagonHost's status page and says which of your
boxes an incident names, by node group ("v31xx") and location.

The matching is a heuristic over incident prose. `affects` carries the
reasons so you can judge them. **A match is a prompt to investigate; no
match is not an all-clear** — do not report "no incidents affect this
server" as if it were verified. Servers listed under `unchecked` could
not be reached, so nothing is known about them either way.

**Bandwidth arithmetic** — already done for you. `bandwidth.percent` is
the reliable figure: the location multiplier scales the allowance and
the counter equally, so the percentage is correct however the
multiplier is read. Do not recompute it from `plan_monthly_data`.

**Reach an endpoint bwg has no command for**

```bash
bwg api call getServiceInfo --jq '.node_datacenter'
bwg api call setPTR -f ip=1.2.3.4 -f ptr=mail.example.com --yes
```

The gate still applies; `bwg api call` cannot reach an unclassified
endpoint at all.

## Things that will bite you

- **`bwg status` takes up to 15 seconds.** It queries the running
  guest. Use `bwg info` when you only need plan, addresses or quota.
- **`bwg exec` is destructive by classification**, whatever the
  command, because it is arbitrary root code. Its exit status becomes
  bwg's, so `bwg exec 'test -f /etc/nginx/nginx.conf'` composes.
- **A locked VPS fails every other call** while a snapshot, reinstall
  or migration runs. The error carries the progress percentage.
- **Backups cannot be restored directly.** `bwg backup restore <token>`
  copies one into a snapshot; then `bwg snapshot restore <fileName>`.
- **reinstallOS shows the root password once.** It is in the command's
  output and in `--json` as `rootPassword`, and KiwiVM will not show it
  again. Capture it in the same step.
- **Reinstall wipes SSH access if no keys are registered.** Check
  `bwg keys ls` first — `effective: none` means password-only after.
- **migrate/start replaces every IPv4 address.** DNS, firewall
  allowlists and license keys all break until updated.
- **Rate limits are per 15 minutes and per 24 hours.** A fleet sweep
  costs one call per server; `bwg ratelimit` costs one more.
- **KiwiVM returns the same auth error for a wrong veid and a wrong
  key.** Exit code 4 means "this pair does not work", not "bad key".
- **`bwg incidents` costs one getServiceInfo per server** to know
  where each one lives. Use `--all` to skip the correlation entirely.
- **The `env` server outranks the configured default.** If `BWG_VEID`
  is exported, single-server commands use it unless `--server` says
  otherwise.

## Destructive operations

These need `--yes` off a terminal, and four of them ask for the server
name typed out when interactive (`reinstallOS`, `snapshot restore`,
`migrate start`, `cloneFromExternalServer`) because the mistake worth
catching is the wrong box, which y/N cannot catch.

Never run these speculatively. If the task does not name the operation,
it is not the operation:

```
kill · reinstallOS · resetRootPassword · snapshot rm · snapshot restore
ipv6 rm · iso mount/unmount · migrate start · exec · run
abuse resolve · abuse unsuspend
```

## MCP

```bash
claude mcp add bwg -- bwg mcp --read-only
```

Tools come from the same catalogue, plus `bwg_incidents`, which reads
the public status feed — it carries no credentials, so it stays
available even in read-only mode. In read-only mode the mutating
tools are not advertised at all, so an agent is never offered a tool
that would be refused. MCP has no confirmation channel — a host that
grants a call gets it — so serve read-only unless the host has its own
approval layer.
