package image

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/infra"
	"mvmctl/internal/lib/model"
)

// fakeRepo is a minimal in-memory implementation of Repository for tests that
// do not exercise persistence. It avoids a nil interface in NewService while
// keeping the service hermetic.
type fakeRepo struct{}

func (fakeRepo) Get(_ context.Context, _ string) (*model.ImageItem, error) { return nil, nil }
func (fakeRepo) FindByPrefix(_ context.Context, _ string) ([]*model.ImageItem, error) {
	return nil, nil
}
func (fakeRepo) GetByType(_ context.Context, _ string) (*model.ImageItem, error) { return nil, nil }
func (fakeRepo) GetByVersionAndType(_ context.Context, _, _ string) (*model.ImageItem, error) {
	return nil, nil
}
func (fakeRepo) GetByName(_ context.Context, _ string) (*model.ImageItem, error) { return nil, nil }
func (fakeRepo) Count(_ context.Context) (int, error)                            { return 0, nil }
func (fakeRepo) ListAll(_ context.Context) ([]*model.ImageItem, error)           { return nil, nil }
func (fakeRepo) Upsert(_ context.Context, _ *model.ImageItem) error              { return nil }
func (fakeRepo) SoftDelete(_ context.Context, _ string) error                    { return nil }
func (fakeRepo) Delete(_ context.Context, _ string) error                        { return nil }
func (fakeRepo) SetDefault(_ context.Context, _ string) error                    { return nil }
func (fakeRepo) GetDefault(_ context.Context) (*model.ImageItem, error)          { return nil, nil }
func (fakeRepo) UpdateManyIsPresent(_ context.Context, _ []string, _ bool) error { return nil }

func ptr(s string) *string { return &s }

// setupHermeticCache points the warm-image cache and image directory at a
// per-test temporary directory so EnsureCached never touches the host cache.
func setupHermeticCache(t *testing.T) string {
	t.Helper()
	cacheDir := t.TempDir()
	t.Setenv(infra.EnvKey("CACHE_DIR"), cacheDir)
	t.Setenv(infra.EnvKey("WARM_POOL"), "disk")
	return cacheDir
}

// rawImageData returns deterministic, non-zero bytes for the test images.
func rawImageData() []byte {
	return []byte("concurrent ensure cached test content - mvmctl image.Service\n")
}

func expectedSHA256(data []byte) string {
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

// writeZST writes rawData compressed with zstd to path.
func writeZST(t *testing.T, path string, rawData []byte) {
	t.Helper()
	f, err := os.Create(path)
	require.NoError(t, err, "create compressed image")
	defer f.Close()

	enc, err := zstd.NewWriter(f)
	require.NoError(t, err, "create zstd encoder")
	defer enc.Close()

	_, err = enc.Write(rawData)
	require.NoError(t, err, "write compressed data")
	require.NoError(t, enc.Close(), "close zstd encoder")
	require.NoError(t, f.Close(), "close compressed image file")
}

// concurrentEnsureCached races many goroutines against s.EnsureCached for the
// same image and returns every returned path.
func concurrentEnsureCached(t *testing.T, s *Service, image *model.ImageItem, workers int) []string {
	t.Helper()

	type result struct {
		paths []string
		err   error
	}

	results := make(chan result, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			paths, err := s.EnsureCached([]*model.ImageItem{image})
			results <- result{paths: paths, err: err}
		}()
	}
	wg.Wait()
	close(results)

	var allPaths []string
	for r := range results {
		require.NoError(t, r.err, "EnsureCached should not fail under concurrency")
		require.Len(t, r.paths, 1, "EnsureCached should return exactly one path")
		allPaths = append(allPaths, r.paths[0])
	}
	return allPaths
}

// assertCachedFile verifies that path exists and contains exactly rawData.
func assertCachedFile(t *testing.T, path string, rawData []byte) {
	t.Helper()

	info, err := os.Stat(path)
	require.NoError(t, err, "cached file should exist")
	require.Positive(t, info.Size(), "cached file should not be empty")

	got, err := os.ReadFile(path)
	require.NoError(t, err, "read cached file")
	require.Equal(t, rawData, got, "cached file contents should match expected bytes")
	require.Equal(t, expectedSHA256(rawData), expectedSHA256(got), "cached file SHA256 should match")
}

func TestEnsureCached_ConcurrentCompressed(t *testing.T) {
	setupHermeticCache(t)

	rawData := rawImageData()
	imageID := "test-concurrent-compressed"

	imagesDir := infra.GetImagesDir()
	compressedPath := filepath.Join(imagesDir, imageID+".zst")
	writeZST(t, compressedPath, rawData)

	// Path must have an extension so EnsureCached can replace it with .zst.
	image := &model.ImageItem{
		ID:               imageID,
		Path:             filepath.Join(imagesDir, imageID+".ext4"),
		FSType:           "ext4",
		CompressedFormat: ptr("zst"),
	}

	s := NewService(fakeRepo{})
	paths := concurrentEnsureCached(t, s, image, 50)

	first := paths[0]
	for i, p := range paths {
		require.Equal(t, first, p, "all goroutines should return the same cached path (goroutine %d)", i)
	}
	assertCachedFile(t, first, rawData)
}

func TestEnsureCached_ConcurrentUncompressed(t *testing.T) {
	setupHermeticCache(t)

	rawData := rawImageData()
	imageID := "test-concurrent-uncompressed"

	imagesDir := infra.GetImagesDir()
	srcPath := filepath.Join(imagesDir, imageID+".ext4")
	require.NoError(t, os.WriteFile(srcPath, rawData, 0644), "create uncompressed image")

	image := &model.ImageItem{
		ID:               imageID,
		Path:             srcPath,
		FSType:           "ext4",
		CompressedFormat: ptr(""),
	}

	s := NewService(fakeRepo{})
	paths := concurrentEnsureCached(t, s, image, 50)

	first := paths[0]
	for i, p := range paths {
		require.Equal(t, first, p, "all goroutines should return the same cached path (goroutine %d)", i)
	}
	assertCachedFile(t, first, rawData)
}
