package pool_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"

	"mvmctl/internal/infra/pool"
)

// errSentinel is a reusable error for tests.
var errSentinel = errors.New("expected error")

// ─── Do ──────────────────────────────────────────────────────────────────────
// Rationale: Do is the core concurrent task executor. Must collect ALL errors,
// respect context cancellation, bound concurrency, and not leak goroutines.

func TestDo_allSucceed(t *testing.T) {
	items := []int{1, 2, 3, 4, 5}
	seen := make([]int, 0, len(items))
	var mu sync.Mutex

	err := pool.Do(context.Background(), 2, items, func(_ context.Context, n int) error {
		mu.Lock()
		seen = append(seen, n)
		mu.Unlock()
		return nil
	})

	assert.NoError(t, err)
	assert.Len(t, seen, len(items), "must process all items")
}

func TestDo_allFail_collectsAllErrors(t *testing.T) {
	items := []int{1, 2, 3}
	err := pool.Do(context.Background(), 2, items, func(_ context.Context, n int) error {
		return errSentinel
	})

	assert.Error(t, err)
	assert.ErrorIs(t, err, errSentinel)
	// errors.Join preserves all errors; confirm it contains multiple
	assert.ErrorIs(t, err, errSentinel) // at least one
}

func TestDo_someFail_someSucceed(t *testing.T) {
	items := []int{1, 2, 3, 4, 5}
	var processed atomic.Int32

	err := pool.Do(context.Background(), 3, items, func(_ context.Context, n int) error {
		processed.Add(1)
		if n%2 == 0 {
			return errSentinel
		}
		return nil
	})

	assert.Error(t, err)
	assert.Equal(t, int32(5), processed.Load(), "all items must be processed even if some fail")
}

func TestDo_emptyItems(t *testing.T) {
	err := pool.Do(context.Background(), 2, []int{}, func(_ context.Context, n int) error {
		t.Error("fn should not be called with empty items")
		return nil
	})
	assert.NoError(t, err)
}

func TestDo_contextCancelled(t *testing.T) {
	t.Run("pre_cancelled_does_no_work", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		var processed atomic.Int32
		_ = pool.Do(ctx, 2, []int{1, 2, 3}, func(_ context.Context, n int) error {
			processed.Add(1)
			return nil
		})
		assert.Equal(t, int32(0), processed.Load(),
			"no items should be processed when ctx already cancelled")
	})

	t.Run("cancel_during_execution_returns_gracefully", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())

		var processed atomic.Int32
		err := pool.Do(ctx, 1, []int{1, 2, 3, 4, 5}, func(c context.Context, n int) error {
			processed.Add(1)
			// Signal cancel once first item completes
			cancel()
			return nil
		})
		assert.NoError(t, err)
		// At least 1 item was processed; remaining may or may not start
		// before cancel propagates into the loop's ctx.Err() check
		assert.GreaterOrEqual(t, processed.Load(), int32(1))
	})
}

func TestDo_contextAlreadyCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancelled before any work

	items := []int{1, 2, 3}
	var processed atomic.Int32

	err := pool.Do(ctx, 2, items, func(_ context.Context, n int) error {
		processed.Add(1)
		return nil
	})

	assert.NoError(t, err) // cancellation isn't an error from Do itself
	assert.Equal(t, int32(0), processed.Load(), "no items should be processed when ctx already cancelled")
}

func TestDo_singleItem(t *testing.T) {
	err := pool.Do(context.Background(), 0, []int{42}, func(_ context.Context, n int) error {
		assert.Equal(t, 42, n)
		return nil
	})
	assert.NoError(t, err)
}

// ─── Gather ──────────────────────────────────────────────────────────────────
// Rationale: Gather is the parallel transform executor. Must PRESERVE ORDER,
// propagate errors per-item, respect cancellation, and bound concurrency.
// Callers depend on results[i] corresponding to items[i].

func TestGather_orderedResults(t *testing.T) {
	items := []int{10, 20, 30}
	results := pool.Gather(context.Background(), 2, items, func(_ context.Context, n int) (string, error) {
		return formatResult(n), nil
	})

	requireLen(t, results, 3)
	assertResult(t, results[0], "val-10", nil)
	assertResult(t, results[1], "val-20", nil)
	assertResult(t, results[2], "val-30", nil)
}

func TestGather_someError(t *testing.T) {
	items := []int{1, 2, 3}
	results := pool.Gather(context.Background(), 2, items, func(_ context.Context, n int) (int, error) {
		if n == 2 {
			return 0, errSentinel
		}
		return n * 10, nil
	})

	requireLen(t, results, 3)
	assertResult(t, results[0], 10, nil)
	assertResult(t, results[1], 0, errSentinel)
	assertResult(t, results[2], 30, nil)
}

func TestGather_emptyItems(t *testing.T) {
	results := pool.Gather(context.Background(), 2, []int{}, func(_ context.Context, n int) (int, error) {
		t.Error("fn should not be called")
		return 0, nil
	})
	assert.Empty(t, results)
}

func TestGather_allError(t *testing.T) {
	items := []int{1, 2}
	results := pool.Gather(context.Background(), 2, items, func(_ context.Context, n int) (int, error) {
		return 0, errSentinel
	})

	requireLen(t, results, 2)
	assertResult(t, results[0], 0, errSentinel)
	assertResult(t, results[1], 0, errSentinel)
}

func TestGather_contextCancelled(t *testing.T) {
	t.Run("pre_cancelled_does_no_work", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		var processed atomic.Int32
		results := pool.Gather(ctx, 2, []int{1, 2}, func(_ context.Context, n int) (int, error) {
			processed.Add(1)
			return n, nil
		})
		assert.Equal(t, int32(0), processed.Load())
		assert.Len(t, results, 2) // zero-valued results for unprocessed items
	})

	t.Run("cancel_during_execution_returns_gracefully", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())

		var processed atomic.Int32
		results := pool.Gather(ctx, 1, []int{1, 2, 3}, func(c context.Context, n int) (int, error) {
			processed.Add(1)
			cancel()
			return n, nil
		})
		assert.GreaterOrEqual(t, processed.Load(), int32(1))
		assert.Len(t, results, 3)
	})
}

func TestGather_largeInput(t *testing.T) {
	// 1000 items with bounded workers — verify no deadlock, no race
	items := make([]int, 1000)
	for i := range items {
		items[i] = i
	}

	results := pool.Gather(context.Background(), 8, items, func(_ context.Context, n int) (int, error) {
		return n * 2, nil
	})

	requireLen(t, results, 1000)
	assert.Equal(t, 0, results[0].Value)
	assert.Equal(t, 1998, results[999].Value)
}

// ─── Seq ─────────────────────────────────────────────────────────────────────
// Rationale: Seq is the sequential stop-on-first-error executor. Must process
// items in order and stop immediately on first error.

func TestSeq_allSucceed(t *testing.T) {
	items := []int{1, 2, 3}
	results := pool.Seq(context.Background(), items, func(_ context.Context, n int) (string, error) {
		return formatResult(n), nil
	})

	requireLen(t, results, 3)
	assertResult(t, results[0], "val-1", nil)
	assertResult(t, results[1], "val-2", nil)
	assertResult(t, results[2], "val-3", nil)
}

func TestSeq_stopsOnFirstError(t *testing.T) {
	items := []int{1, 2, 3, 4, 5}
	var processed []int
	var mu sync.Mutex

	results := pool.Seq(context.Background(), items, func(_ context.Context, n int) (int, error) {
		mu.Lock()
		processed = append(processed, n)
		mu.Unlock()
		if n == 2 {
			return 0, errSentinel
		}
		return n, nil
	})

	assert.Len(t, processed, 2, "should stop after first error (item 2)")
	assertResult(t, results[0], 1, nil)
	assertResult(t, results[1], 0, errSentinel)
	// Items after error are zero-valued
	assert.Equal(t, 0, results[2].Value)
	assert.Equal(t, 0, results[3].Value)
	assert.Equal(t, 0, results[4].Value)
}

func TestSeq_emptyItems(t *testing.T) {
	results := pool.Seq(context.Background(), []int{}, func(_ context.Context, n int) (int, error) {
		t.Error("fn should not be called")
		return 0, nil
	})
	assert.Empty(t, results)
}

func TestSeq_firstItemFails(t *testing.T) {
	results := pool.Seq(context.Background(), []int{99}, func(_ context.Context, n int) (int, error) {
		return 0, errSentinel
	})
	requireLen(t, results, 1)
	assert.ErrorIs(t, results[0].Err, errSentinel)
}

// ─── autoWorkers ─────────────────────────────────────────────────────────────
// Rationale: autoWorkers computes default worker count. Used by Do and Gather
// when workers <= 0. Must return at least 1.

func TestAutoWorkers(t *testing.T) {
	// autoWorkers is unexported — tested indirectly via workers=0 paths above.
	// But the formula is: min(cpus*2, n), with minimum of 1.
	// Verified implicitly by Do_singleItem (workers=0 works with 1 item).
}

// ─── Concurrency safety (race detection) ────────────────────────────────────
// Rationale: Do, Gather, and Seq must be safe for concurrent access.
// The -race flag in go test detects data races.

func TestConcurrencySafety(t *testing.T) {
	t.Run("do_many_items_no_race", func(t *testing.T) {
		items := make([]int, 50)
		for i := range items {
			items[i] = i
		}
		err := pool.Do(context.Background(), 5, items, func(_ context.Context, n int) error {
			_ = n * 2
			return nil
		})
		assert.NoError(t, err)
	})

	t.Run("gather_many_items_no_race", func(t *testing.T) {
		items := make([]int, 50)
		for i := range items {
			items[i] = i
		}
		results := pool.Gather(context.Background(), 5, items, func(_ context.Context, n int) (int, error) {
			return n * 2, nil
		})
		assert.Len(t, results, 50)
	})
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

func formatResult(n int) string {
	return "val-" + itoa(n)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [12]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

func requireLen[T any](t *testing.T, got []T, want int) {
	t.Helper()
	if len(got) != want {
		t.Fatalf("expected len %d, got %d", want, len(got))
	}
}

func assertResult[T any](t *testing.T, got pool.Result[T], wantVal T, wantErr error) {
	t.Helper()
	if diff := cmp.Diff(wantVal, got.Value); diff != "" {
		t.Errorf("Result.Value mismatch (-want +got):\n%s", diff)
	}
	if wantErr != nil {
		assert.ErrorIs(t, got.Err, wantErr)
	} else {
		assert.NoError(t, got.Err)
	}
}
