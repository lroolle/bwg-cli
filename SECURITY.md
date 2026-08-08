# Security

## What bwg stores, and where

| What | Where | Mode |
|------|-------|------|
| KiwiVM API keys and VEIDs | `~/.config/bwg/config.yaml` (or `$BWG_CONFIG`) | 0600, in a 0700 directory (Unix) |
| Nothing else | — | — |

There is no cache, no session file and no telemetry. bwg talks to
`https://api.64clouds.com/v1` and to nothing else.

If the config file is group- or world-readable, bwg warns on stderr and
keeps going — locking someone out of their own fleet over a permission
bit helps nobody. Saves are atomic (write to a temp file, then rename),
so an interrupted save cannot leave a truncated fleet behind.

**On Windows the mode is advisory.** NTFS uses ACLs, and Go's `Chmod`
cannot express "owner only", so the file inherits its directory's ACL —
which for a user profile directory is already owner-scoped. bwg does
not warn there, because Go reports a synthesized `0666` for every file
regardless of the real ACL, and a warning that fires on every run
recommending `chmod` would be noise.

## Credential handling

- **`--json` never contains an API key.** `config.Server` implements
  `MarshalJSON` to mask it, so this is a property of the type rather
  than a rule every command has to remember. `bwg server show` masks it
  too; there is deliberately no way to print a key back out.
- **Write requests use POST form bodies**, so the `api_key` never
  appears in a URL, a proxy log, or shell history. Reads use GET. A
  test asserts this for every client method.
- **`--verbose` logs method, endpoint, status and duration only** —
  never parameters, never credentials.
- **Transport errors are redacted.** A read is a GET, so the key is in
  the query string, and Go renders the whole URL in `*url.Error`. The
  client substitutes the key out of the message before the error
  escapes, while keeping the error chain intact so `errors.Is` still
  works. Regression tests: `TestTransportErrorsDoNotLeakTheKey`,
  `TestRedactionPreservesTheErrorChain`.
- KiwiVM returns the *same* error for a wrong VEID and a wrong key, so
  bwg reports "this pair does not work" rather than guessing which half
  is at fault.

## The read-only guarantee

`--read-only` (or `BWG_READ_ONLY=1`) is enforced in the SDK, in front
of the HTTP client — not in the CLI. A refused operation makes no
network request at all.

The environment can force read-only on; nothing can force it off. A
`--read-only` that a stray variable could clear would be worse than no
flag at all.

Verify it yourself:

```bash
go test ./kiwivm/ -run TestReadOnlyRefusesEveryMutation -v
```

That test reflects over every exported client method, discovers which
endpoint each one calls, and asserts a read-only client refuses every
non-read one against a server that fails the test if reached. A method
added later is covered without anyone remembering to add it.

## Reporting an issue

Open a GitHub security advisory on
`https://github.com/lroolle/bwg-cli`, or a normal issue if it is not
sensitive. Please do not include real API keys in either.
