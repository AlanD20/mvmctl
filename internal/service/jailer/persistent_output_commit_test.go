package jailer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/pkg/errs"
)

// Rationale: each failure before publication must leave no named file; errors after the link must preserve it and
// report the exact publication or uncertain-outcome boundary instead of permitting a destructive retry.
func TestPersistentOutputPublicationFailureBoundaries(t *testing.T) {
	t.Parallel()
	tests := []struct {
		stage     string
		visible   bool
		published bool
		uncertain bool
		unknown   bool
	}{
		{stage: "anonymous open"},
		{stage: "chown"},
		{stage: "chmod"},
		{stage: "truncate"},
		{stage: "metadata"},
		{stage: "file sync"},
		{stage: "publish", unknown: true},
		{stage: "effectful publish error", visible: true, unknown: true},
		{stage: "parent sync", visible: true, published: true, uncertain: true},
	}
	for _, tc := range tests {
		t.Run(tc.stage, func(t *testing.T) {
			t.Parallel()
			fixture := newPersistentOutputFixture(t)
			fault := errs.New(errs.CodeVMAtomicFailed, "injected output failure", errs.WithClass(errs.ClassRetryable),
				errs.WithEntity("original entity"), errs.WithDetails(map[string]any{"original_detail": "retained"}))
			switch tc.stage {
			case "anonymous open":
				realOpen := fixture.deps.openAt2
				fixture.deps.openAt2 = func(ctx context.Context, fd int, name string, how *unix.OpenHow) (int, error) {
					if name == "." {
						return -1, fault
					}
					return realOpen(ctx, fd, name, how)
				}
			case "chown":
				fixture.deps.fchown = func(context.Context, int, int, int) error { return fault }
			case "chmod":
				fixture.deps.fchmod = func(context.Context, int, uint32) error { return fault }
			case "truncate":
				fixture.deps.ftruncate = func(context.Context, int, int64) error { return fault }
			case "metadata":
				realStat := fixture.deps.statx
				fixture.deps.statx = func(ctx context.Context, fd, flags, mask int, stat *unix.Statx_t) error {
					err := realStat(ctx, fd, flags, mask, stat)
					if err == nil && stat.Mode&unix.S_IFMT == unix.S_IFREG {
						return fault
					}
					return err
				}
			case "file sync", "parent sync":
				realSync := fixture.deps.fsync
				fixture.deps.fsync = func(ctx context.Context, fd int) error {
					var stat unix.Stat_t
					if err := unix.Fstat(fd, &stat); err != nil {
						return err
					}
					if (stat.Mode&unix.S_IFMT == unix.S_IFDIR) == (tc.stage == "parent sync") {
						return fault
					}
					return realSync(ctx, fd)
				}
			case "publish":
				fixture.deps.publish = func(context.Context, int, int, managedPersistentOutputName) error { return fault }
			case "effectful publish error":
				realPublish := fixture.deps.publish
				fixture.deps.publish = func(ctx context.Context, fd, parentFD int, name managedPersistentOutputName) error {
					if err := realPublish(ctx, fd, parentFD, name); err != nil {
						return err
					}
					return fault
				}
			}
			err := fixture.prepare(t.Context(), managedPersistentMetricsEnabled)
			require.Error(t, err)
			domain := errs.AsDomainError(err)
			require.NotNil(t, domain)
			assert.Equal(t, fault.Code, domain.Code)
			assert.Equal(t, fault.Class, domain.Class)
			assert.Equal(t, fault.Entity, domain.Entity)
			assert.Equal(t, "retained", domain.Details["original_detail"])
			assert.Equal(t, tc.published, domain.Details["output_published"])
			assert.Equal(t, tc.uncertain, domain.Details["durability_uncertain"])
			assert.Equal(t, tc.unknown, domain.Details["outcome_unknown"])
			assert.Equal(t, true, domain.Details["output_cleanup_required"])
			entries, readErr := os.ReadDir(fixture.directory)
			require.NoError(t, readErr)
			if tc.visible {
				require.Len(t, entries, 1)
				assert.Equal(t, "firecracker.log", entries[0].Name())
				assert.Empty(t, readPersistentOutput(t, fixture.directory, "firecracker.log"))
				fixture.write(t, "firecracker.log", "evidence after failure")
			} else {
				assert.Empty(t, entries)
			}
			require.Error(t, fixture.prepare(t.Context(), managedPersistentMetricsEnabled))
			if tc.visible {
				assert.Equal(t, "evidence after failure", readPersistentOutput(t, fixture.directory, "firecracker.log"))
			}
		})
	}
}

func TestPersistentOutputPublicationNeverReplacesConcurrentLeaf(t *testing.T) {
	t.Parallel()
	fixture := newPersistentOutputFixture(t)
	realPublish := fixture.deps.publish
	fixture.deps.publish = func(ctx context.Context, fd, parentFD int, name managedPersistentOutputName) error {
		fixture.write(t, name.basename(), "concurrent caller file")
		return realPublish(ctx, fd, parentFD, name)
	}
	err := fixture.prepare(t.Context(), managedPersistentMetricsDisabled)
	require.ErrorIs(t, err, unix.EEXIST)
	assert.Equal(t, errs.CodeValidationFailed, errs.AsDomainError(err).Code)
	assert.Equal(t, false, errs.AsDomainError(err).Details["outcome_unknown"])
	assert.Equal(t, "concurrent caller file", readPersistentOutput(t, fixture.directory, "firecracker.log"))
}

func TestPersistentOutputPartialTruncationCannotBeRetried(t *testing.T) {
	t.Parallel()
	fixture := newPersistentOutputFixture(t)
	fixture.write(t, "firecracker.log", "old log")
	fixture.write(t, "firecracker.console.log", "old console")
	realTruncate := fixture.deps.ftruncate
	fault := errors.New("truncation failed after effect")
	fixture.deps.ftruncate = func(ctx context.Context, fd int, size int64) error {
		if err := realTruncate(ctx, fd, size); err != nil {
			return err
		}
		return fault
	}
	err := fixture.prepare(t.Context(), managedPersistentMetricsDisabled)
	require.ErrorIs(t, err, fault)
	assert.Empty(t, readPersistentOutput(t, fixture.directory, "firecracker.log"))
	assert.Equal(t, "old console", readPersistentOutput(t, fixture.directory, "firecracker.console.log"))
	assert.Equal(t, true, errs.AsDomainError(err).Details["outcome_unknown"])
	fixture.write(t, "firecracker.log", "new evidence")
	require.Error(t, fixture.prepare(t.Context(), managedPersistentMetricsDisabled))
	assert.Equal(t, "new evidence", readPersistentOutput(t, fixture.directory, "firecracker.log"))
}

func TestPersistentOutputsCopiedLaunchPreparesOnlyOnce(t *testing.T) {
	t.Parallel()
	fixture := newPersistentOutputFixture(t)
	fixture.write(t, "firecracker.log", "previous log")
	fixture.write(t, "firecracker.console.log", "previous console")
	copyOf := *fixture.launch
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, lease := range []*launchLease{fixture.launch, &copyOf} {
		go func() {
			<-start
			results <- preparePersistentOutputsWithDeps(t.Context(), lease, fixture.cache, managedPersistentMetricsDisabled, fixture.deps)
		}()
	}
	close(start)
	first, second := <-results, <-results
	assert.True(t, (first == nil) != (second == nil), "exactly one preparation succeeds")
	assertPersistentOutputFiles(t, fixture, []string{"firecracker.console.log", "firecracker.log"})
	fixture.write(t, "firecracker.log", "new launch log")
	require.Error(t, fixture.prepare(t.Context(), managedPersistentMetricsDisabled))
	assert.Equal(t, "new launch log", readPersistentOutput(t, fixture.directory, "firecracker.log"))
}

// Rationale: cancellation must stop before another output is mutated, but retain any already-published evidence.
func TestPersistentOutputCancellationPreservesPublishedFile(t *testing.T) {
	t.Parallel()
	fixture := newPersistentOutputFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	realPublish := fixture.deps.publish
	fixture.deps.publish = func(ctx context.Context, fd, parentFD int, name managedPersistentOutputName) error {
		err := realPublish(ctx, fd, parentFD, name)
		cancel()
		return err
	}
	err := fixture.prepare(ctx, managedPersistentMetricsEnabled)
	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, true, errs.AsDomainError(err).Details["output_published"])
	assert.Empty(t, readPersistentOutput(t, fixture.directory, "firecracker.log"))
	assert.NoFileExists(t, filepath.Join(fixture.directory, "firecracker.console.log"))
	assert.NoFileExists(t, filepath.Join(fixture.directory, "firecracker.metrics"))
}

func TestPersistentOutputsReleaseCopiesOnceInReverseOrder(t *testing.T) {
	t.Parallel()
	fixture := newPersistentOutputFixture(t)
	require.NoError(t, fixture.prepare(t.Context(), managedPersistentMetricsEnabled))
	outputs := fixture.launch.state.persistentOutputs
	copyOf := *outputs
	state := outputs.state
	wantClosed := append([]int(nil), state.retained...)
	for left, right := 0, len(wantClosed)-1; left < right; left, right = left+1, right-1 {
		wantClosed[left], wantClosed[right] = wantClosed[right], wantClosed[left]
	}
	closed := make([]int, 0, len(wantClosed))
	fault := errors.New("terminal descriptor close error")
	realClose := state.deps.close
	var closeCount atomic.Int32
	state.deps.close = func(ctx context.Context, fd int) error {
		assert.NoError(t, ctx.Err())
		closed = append(closed, fd)
		closeCount.Add(1)
		return errors.Join(realClose(ctx, fd), fault)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	results := make(chan error, 2)
	go func() { results <- outputs.Release(ctx) }()
	go func() { results <- copyOf.Release(ctx) }()
	first, second := <-results, <-results
	require.ErrorIs(t, first, fault)
	assert.Same(t, first, second)
	if diff := cmp.Diff(wantClosed, closed); diff != "" {
		t.Errorf("descriptor release order (-want +got):\n%s", diff)
	}
	assert.Equal(t, int32(len(wantClosed)), closeCount.Load())
	assertPersistentOutputFiles(
		t,
		fixture,
		[]string{"firecracker.console.log", "firecracker.log", "firecracker.metrics"},
	)
	// The lifecycle must report the same terminal close error once, then forget the consumed capability and converge.
	cleanup, err := fixture.launch.Abort(t.Context())
	require.NoError(t, err)
	require.ErrorIs(t, cleanup.Complete(t.Context()), fault)
	assert.Equal(t, instanceLifecycleCleaning, cleanup.snapshot().lifecycle)
	require.NoError(t, cleanup.Complete(t.Context()))
	require.NoError(t, cleanup.Release(t.Context()))
	assert.Equal(t, int32(len(wantClosed)), closeCount.Load())
}
