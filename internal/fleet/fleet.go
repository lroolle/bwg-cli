// Package fleet runs an operation across many VPS instances at once.
//
// KiwiVM has no account-level endpoint, so every fleet-wide question —
// "which box is near its bandwidth cap", "is anything suspended" — is
// N independent API calls. This package makes that one concurrent
// pass with a bounded worker count, and it never lets one unreachable
// box hide the answer for the others: failures come back as per-server
// results, not as an aborted run.
package fleet

import (
	"context"
	"sync"
	"time"

	"github.com/lroolle/bwg-cli/internal/config"
	"github.com/lroolle/bwg-cli/kiwivm"
)

// DefaultConcurrency bounds parallel API calls. KiwiVM meters requests
// per 15 minutes and per 24 hours, so a fleet sweep that fans out
// without limit spends the budget it was meant to report on.
const DefaultConcurrency = 6

// Result pairs one server with the outcome of running against it.
type Result[T any] struct {
	// Server is the instance this result came from.
	Server *config.Server `json:"server"`
	// Value is the operation's result. Meaningless when Err is set.
	Value T `json:"value,omitempty"`
	// Err is why this server produced nothing. Nil on success.
	Err error `json:"-"`
	// Error is Err as a string, for JSON consumers.
	Error string `json:"error,omitempty"`
	// Elapsed is how long this server's call took.
	Elapsed time.Duration `json:"-"`
}

// OK reports whether this server answered.
func (r Result[T]) OK() bool { return r.Err == nil }

// Map runs fn against every server concurrently and returns the
// results in the order the servers were given, so output is stable
// across runs regardless of which box answers first.
//
// fn's error is recorded against its server; it never stops the sweep.
func Map[T any](ctx context.Context, servers []*config.Server, concurrency int,
	fn func(context.Context, *config.Server) (T, error),
) []Result[T] {
	if concurrency <= 0 {
		concurrency = DefaultConcurrency
	}
	results := make([]Result[T], len(servers))
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup

	for i, s := range servers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				results[i] = Result[T]{Server: s, Err: ctx.Err(), Error: ctx.Err().Error()}
				return
			}

			start := time.Now()
			v, err := fn(ctx, s)
			r := Result[T]{Server: s, Value: v, Err: err, Elapsed: time.Since(start)}
			if err != nil {
				r.Error = err.Error()
			}
			results[i] = r
		}()
	}
	wg.Wait()
	return results
}

// Split separates results into those that answered and those that did
// not, so a caller can render the data and report the gaps separately
// instead of interleaving them.
func Split[T any](results []Result[T]) (ok []Result[T], failed []Result[T]) {
	for _, r := range results {
		if r.OK() {
			ok = append(ok, r)
		} else {
			failed = append(failed, r)
		}
	}
	return ok, failed
}

// ClientFor builds a KiwiVM client for one server, applying the
// server's endpoint override and the caller's read-only choice.
func ClientFor(s *config.Server, readOnly bool, opts ...kiwivm.Option) *kiwivm.Client {
	base := []kiwivm.Option{}
	if s.Endpoint != "" {
		base = append(base, kiwivm.WithBaseURL(s.Endpoint))
	}
	if readOnly {
		base = append(base, kiwivm.ReadOnly())
	}
	return kiwivm.New(s.VEID, s.APIKey, append(base, opts...)...)
}
