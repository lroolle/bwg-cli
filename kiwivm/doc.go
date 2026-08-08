// Package kiwivm is a Go client for the KiwiVM REST API used by
// BandwagonHost / 64clouds VPS instances.
//
// The API is per-instance: every call authenticates with a (veid,
// api_key) pair that identifies one VPS. There is no account-level
// endpoint, so managing a fleet means holding one credential pair per
// box. See package github.com/lroolle/bwg-cli/internal/config for the
// fleet model built on top of this client.
//
// # Read-only is a capability, not a flag
//
// Every endpoint is registered in [Ops] with a [Risk] classification.
// A client built with [ReadOnly] refuses every non-read operation
// before any HTTP request is made:
//
//	c := kiwivm.New(veid, key, kiwivm.ReadOnly())
//	_, err := c.Restart(ctx)          // *ReadOnlyError, no network I/O
//	info, err := c.ServiceInfo(ctx)   // fine
//
// The guard lives here rather than in the CLI so that SDK consumers,
// the CLI, and the MCP server all inherit it. [Client.Can] reports
// whether an operation would be permitted, which is how callers build
// an honest tool list for an agent instead of advertising tools that
// will fail.
//
// # Errors
//
// KiwiVM answers HTTP 200 with an "error" field on failure. Non-zero
// values surface as *[APIError]; use [IsAuth], [IsLocked] and
// [IsRateLimited] to branch. Transport failures and 5xx surface as
// *[TransportError], which [IsTransient] reports true for — those
// deserve a retry, never a credential change.
package kiwivm
