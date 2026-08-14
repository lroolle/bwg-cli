# Changelog

All notable changes to bwg are recorded here. Entries say why a change
matters, not just what moved; the commit log already says what moved.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Changed

- **The API key environment variable is `BWG_API_KEY`.** v0.1.0 accepted
  two spellings and documented the other one, so the pair people copied
  was `BWG_VEID` + `BWG_KIWIVM_API_KEY` — two halves of one credential
  in two different styles. `BWG_KIWIVM_API_KEY` still authenticates and
  now warns once per run on stderr naming the replacement; it will be
  removed at v1.0. `bwg server show env` reports whichever variable is
  actually supplying the key.
- **`bwg usage` shows the last 30 days by default** instead of every
  sample KiwiVM kept (about two years, oldest first, which pushed the
  recent end of the series and the quota summary off-screen). `--days 0`
  restores the full history. The window now applies to the whole output:
  before this, `--days 7` printed seven rows above a total covering two
  years, and `--raw` ignored `--days` entirely. JSON gains
  `"window":{"days","available"}`, and `"totals"` describes the same
  span as `"days"`.

### Fixed

- **`bwg update` failed wherever `$TMPDIR` is on a different filesystem
  from the binary** — a tmpfs `/tmp` is the common case on Linux. The
  install rename returned `EXDEV` and a healthy update was reported as a
  broken one. It now falls back to a copy, and still restores the
  previous binary if the install cannot complete.
- **`bwg info` printed an empty `rDNS` heading.** KiwiVM returns one PTR
  entry per address whether or not a record is set, and a heading over
  nothing reads as data that failed to load.
- **A VPS suspended for bandwidth was told to run `bwg abuse`**, which
  correctly answered "nothing outstanding" — a dead end at the moment
  the answer mattered. `bwg info` now names the exhausted transfer quota
  as the likely cause and says when it resets, while still pointing at
  `bwg abuse` when there is an abuse case on the record.
- Counts read as English: `1 key`, not `1 keys` or `1 key(s)`.

### Added

- Coverage went 79.0% → 84.5% (`go test ./... -cover`), concentrated
  where it was thinnest: the MCP tool implementations (56.6% → 86.4%)
  and the self-updater's download, archive extraction and install paths
  (50.0% → 79.2%), including the zip branch, which had never run.
- A [project page](https://lroolle.github.io/bwg-cli/) and a live
  [llms.txt](https://lroolle.github.io/bwg-cli/llms.txt) for agents.

## [0.1.0] - 2026-08-08

First release.

- `bwg ls` sweeps a whole BandwagonHost account concurrently and prints
  one table: bandwidth against the monthly cap with the location
  multiplier applied, plus whatever needs attention.
- Read-only is a client capability rather than a CLI flag: 45 KiwiVM
  endpoints classified `read`/`write`/`destructive` in one registry,
  gated in the SDK in front of the HTTP client, so the CLI, the MCP
  server and any Go consumer inherit the same guarantee.
- Consent cards that carry the facts the decision depends on, and a
  typed server name for the four operations that replace the whole box.
- `kiwivm` Go SDK covering every documented endpoint, absorbing the
  API's PHP-shaped JSON.
- MCP stdio server; in read-only mode the mutating tools are not
  advertised at all.
- `bwg incidents` correlates BandwagonHost's status page against your
  fleet and prints why each match was made.

[Unreleased]: https://github.com/lroolle/bwg-cli/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/lroolle/bwg-cli/releases/tag/v0.1.0
