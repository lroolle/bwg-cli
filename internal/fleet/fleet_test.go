package fleet

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lroolle/bwg-cli/internal/config"
	"github.com/lroolle/bwg-cli/kiwivm"
)

func servers(n int) []*config.Server {
	out := make([]*config.Server, n)
	for i := range out {
		out[i] = &config.Server{
			Name:   fmt.Sprintf("s%02d", i),
			VEID:   fmt.Sprint(1000 + i),
			APIKey: "private_key",
		}
	}
	return out
}

// Results must come back in input order even though the calls finish
// out of order, or a fleet table reshuffles itself every run.
func TestMapPreservesInputOrder(t *testing.T) {
	ss := servers(8)
	got := Map(context.Background(), ss, 4,
		func(ctx context.Context, s *config.Server) (string, error) {
			// Later servers finish first.
			time.Sleep(time.Duration(8-indexOf(ss, s)) * time.Millisecond)
			return s.Name, nil
		})

	if len(got) != len(ss) {
		t.Fatalf("got %d results, want %d", len(got), len(ss))
	}
	for i, r := range got {
		if r.Server != ss[i] || r.Value != ss[i].Name {
			t.Errorf("position %d holds %v, want %s", i, r.Value, ss[i].Name)
		}
	}
}

func indexOf(ss []*config.Server, s *config.Server) int {
	for i := range ss {
		if ss[i] == s {
			return i
		}
	}
	return -1
}

// One unreachable box must not hide the answer for the rest.
func TestMapIsolatesFailures(t *testing.T) {
	ss := servers(5)
	boom := errors.New("connection refused")

	got := Map(context.Background(), ss, 3,
		func(ctx context.Context, s *config.Server) (int, error) {
			if s.Name == "s02" {
				return 0, boom
			}
			return 42, nil
		})

	ok, failed := Split(got)
	if len(ok) != 4 || len(failed) != 1 {
		t.Fatalf("Split gave %d ok / %d failed, want 4/1", len(ok), len(failed))
	}
	if failed[0].Server.Name != "s02" {
		t.Errorf("wrong server failed: %s", failed[0].Server.Name)
	}
	if !errors.Is(failed[0].Err, boom) {
		t.Errorf("Err = %v, want the original error", failed[0].Err)
	}
	// JSON consumers read Error, not Err.
	if failed[0].Error != boom.Error() {
		t.Errorf("Error = %q, want it mirrored from Err", failed[0].Error)
	}
	if failed[0].OK() {
		t.Error("a failed result reported OK")
	}
	for _, r := range ok {
		if r.Value != 42 || r.Error != "" {
			t.Errorf("healthy result corrupted: %+v", r)
		}
	}
}

// The concurrency bound is what keeps a fleet sweep from spending the
// rate-limit budget it exists to report on.
func TestMapRespectsTheConcurrencyBound(t *testing.T) {
	const limit = 3
	var (
		mu       sync.Mutex
		inFlight int
		peak     int
	)

	Map(context.Background(), servers(20), limit,
		func(ctx context.Context, s *config.Server) (bool, error) {
			mu.Lock()
			inFlight++
			if inFlight > peak {
				peak = inFlight
			}
			mu.Unlock()

			time.Sleep(5 * time.Millisecond)

			mu.Lock()
			inFlight--
			mu.Unlock()
			return true, nil
		})

	if peak > limit {
		t.Errorf("peak concurrency %d exceeded the limit of %d", peak, limit)
	}
	if peak < 2 {
		t.Errorf("peak concurrency %d — the calls did not actually overlap", peak)
	}
}

func TestMapDefaultsConcurrency(t *testing.T) {
	got := Map(context.Background(), servers(3), 0,
		func(ctx context.Context, s *config.Server) (int, error) { return 1, nil })
	if len(got) != 3 {
		t.Fatalf("got %d results", len(got))
	}
	for _, r := range got {
		if !r.OK() {
			t.Errorf("%s failed: %v", r.Server.Name, r.Err)
		}
	}
}

func TestMapCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var started atomic.Int32

	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()

	got := Map(ctx, servers(30), 2,
		func(ctx context.Context, s *config.Server) (int, error) {
			started.Add(1)
			select {
			case <-time.After(30 * time.Millisecond):
				return 1, nil
			case <-ctx.Done():
				return 0, ctx.Err()
			}
		})

	// Every server still gets a result — cancelled ones carry the
	// reason rather than vanishing.
	if len(got) != 30 {
		t.Fatalf("got %d results, want one per server even after cancellation", len(got))
	}
	_, failed := Split(got)
	if len(failed) == 0 {
		t.Error("cancellation produced no failed results")
	}
	for _, r := range failed {
		if !errors.Is(r.Err, context.Canceled) {
			t.Errorf("%s failed with %v, want context.Canceled", r.Server.Name, r.Err)
		}
	}
	if int(started.Load()) >= 30 {
		t.Error("cancellation did not stop new work from starting")
	}
}

func TestMapOnAnEmptyFleet(t *testing.T) {
	got := Map(context.Background(), nil, 4,
		func(ctx context.Context, s *config.Server) (int, error) {
			t.Error("fn ran with no servers")
			return 0, nil
		})
	if len(got) != 0 {
		t.Errorf("got %d results from an empty fleet", len(got))
	}
	ok, failed := Split(got)
	if len(ok) != 0 || len(failed) != 0 {
		t.Error("Split invented results")
	}
}

func TestMapRecordsElapsed(t *testing.T) {
	got := Map(context.Background(), servers(1), 1,
		func(ctx context.Context, s *config.Server) (int, error) {
			time.Sleep(5 * time.Millisecond)
			return 1, nil
		})
	if got[0].Elapsed < 4*time.Millisecond {
		t.Errorf("Elapsed = %v, want it measured", got[0].Elapsed)
	}
}

func TestClientForAppliesServerSettings(t *testing.T) {
	s := &config.Server{VEID: "1347645", APIKey: "private_key", Endpoint: "https://proxy.test/v1"}

	c := ClientFor(s, false)
	if c.VEID() != "1347645" {
		t.Errorf("VEID = %q", c.VEID())
	}
	if c.BaseURL() != "https://proxy.test/v1" {
		t.Errorf("endpoint override ignored: %q", c.BaseURL())
	}
	if c.IsReadOnly() {
		t.Error("client is read-only without being asked")
	}

	ro := ClientFor(s, true)
	if !ro.IsReadOnly() {
		t.Error("read-only was not applied")
	}
	if ok, _ := ro.Can("reinstallOS"); ok {
		t.Error("a read-only fleet client would allow a reinstall")
	}

	plain := ClientFor(&config.Server{VEID: "1", APIKey: "k"}, false)
	if plain.BaseURL() != kiwivm.DefaultBaseURL {
		t.Errorf("no endpoint override should mean the default: %q", plain.BaseURL())
	}
}
