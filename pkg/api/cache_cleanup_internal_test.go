package api

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/model"
	"mvmctl/internal/service/jailer"
	"mvmctl/pkg/errs"
)

type refusingProtectedCacheCleaner struct {
	ack    jailer.ProtectedCacheCleanResult
	err    error
	called bool
}

func (cleaner *refusingProtectedCacheCleaner) Clean(
	_ context.Context, _ jailer.ProtectedCacheCleanRequest,
) (jailer.ProtectedCacheCleanResult, error) {
	cleaner.called = true
	return cleaner.ack, cleaner.err
}

func TestCacheCleanupRefusalPreservesDirectoryAndFailureProjection(t *testing.T) {
	// Rationale: Public cleanup must not delete caller files or report success after root refusal.
	for name, cleaner := range map[string]*refusingProtectedCacheCleaner{
		"root refusal":              {err: errs.New(errs.CodeCacheCleanFailed, "permanent branch locks remain")},
		"uncertain acknowledgement": {},
	} {
		t.Run(name, func(t *testing.T) {
			cache := t.TempDir()
			path := filepath.Join(cache, "caller-owned")
			content := []byte("preserve caller bytes on incomplete root cleanup")
			require.NoError(t, os.WriteFile(path, content, 0600))
			pruned := &model.PruneAllResult{PrunedIDs: []string{"owned-kernel"}, FailedIDs: []string{}}
			op := &Operation{CacheDir: cache}
			got, err := op.completeCacheClean(t.Context(), false, pruned, cleaner)
			require.Error(t, err)
			var domain *errs.DomainError
			require.ErrorAs(t, err, &domain)
			assert.Equal(t, errs.CodeCacheCleanFailed, domain.Code)
			want := &model.CleanResult{CacheDir: cache, PruneResult: *pruned, CacheDirRemoved: false}
			if diff := cmp.Diff(want, got); diff != "" {
				t.Errorf("cleanup refusal projection (-want +got):\n%s", diff)
			}
			bytes, err := os.ReadFile(path)
			require.NoError(t, err)
			if diff := cmp.Diff(content, bytes); diff != "" {
				t.Errorf("retained caller bytes (-want +got):\n%s", diff)
			}
		})
	}
}

func TestCacheCleanupDryRunAndAbsentCacheDoNotRequestRootMutation(t *testing.T) {
	for _, dryRun := range []bool{false, true} {
		name := "absent cache"
		if dryRun {
			name = "dry run"
		}
		t.Run(name, func(t *testing.T) {
			cache := filepath.Join(t.TempDir(), "cache")
			if dryRun {
				require.NoError(t, os.Mkdir(cache, 0700))
			}
			cleaner := &refusingProtectedCacheCleaner{err: errs.New(errs.CodeInternal, "root mutation must not run")}
			op := &Operation{CacheDir: cache}
			got, err := op.completeCacheClean(t.Context(), dryRun, nil, cleaner)
			require.NoError(t, err)
			assert.False(t, cleaner.called, "no mutation is authorized")
			assert.Equal(
				t,
				dryRun,
				got.CacheDirRemoved,
				"preserve existing dry-run projection, absent cache is unchanged",
			)
			if dryRun {
				assert.DirExists(t, cache)
			} else {
				assert.NoDirExists(t, cache)
			}
		})
	}
}

func TestCacheCleanupConfirmedRetirementAllowsOrdinaryRemoval(t *testing.T) {
	cache := filepath.Join(t.TempDir(), "cache")
	require.NoError(t, os.Mkdir(cache, 0700))
	require.NoError(t, os.Mkdir(filepath.Join(cache, jailer.ProtectedCacheQuarantineDirectory), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(cache, "caller-owned"), []byte("ordinary cache data"), 0600))
	op := &Operation{CacheDir: cache}
	cleaner := &refusingProtectedCacheCleaner{ack: jailer.ProtectedCacheCleanResult{Cleaned: true}}
	got, err := op.completeCacheClean(t.Context(), false, nil, cleaner)
	require.NoError(t, err)
	assert.True(t, got.CacheDirRemoved)
	assert.NoDirExists(t, cache)
}
