package image

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"mvmctl/internal/infra"
)

// Controller matches Python's Controller.
// Manages image operations for a specific image — compression, decompression,
// and tmpfs caching for fast VM rootfs cloning.
type Controller struct {
	image *ImageItem
	repo  Repository
}

// NewController creates an Controller from an entity.
// entity can be an *ImageItem or a string identifier.
func NewController(ctx context.Context, entity interface{}, repo Repository) (*Controller, error) {
	ctrl := &Controller{repo: repo}
	switch e := entity.(type) {
	case *ImageItem:
		ctrl.image = e
	case string:
		resolver := NewResolver(repo)
		img, err := resolver.Resolve(ctx, e)
		if err != nil {
			return nil, err
		}
		ctrl.image = img
	default:
		return nil, fmt.Errorf("invalid entity type: %T", entity)
	}
	return ctrl, nil
}

// Get returns the resolved ImageItem.
func (c *Controller) Get() *ImageItem {
	return c.image
}

// ImagePath returns the image storage path.
func (c *Controller) ImagePath() string {
	return c.image.Path
}

// CompressedPath returns the compressed path for this image.
// Uses compressed_format from ImageItem if set, otherwise defaults to .zst.
func (c *Controller) CompressedPath() string {
	fmt_ := "zst"
	if c.image.CompressedFormat != nil && *c.image.CompressedFormat != "" {
		fmt_ = *c.image.CompressedFormat
	}
	suffix := "." + fmt_
	if fmt_[0] == '.' {
		suffix = fmt_
	}
	base := c.image.Path
	ext := filepath.Ext(base)
	return base[:len(base)-len(ext)] + suffix
}

// PruneCached removes all images from the tmpfs cache (warm directory).
// Matches Python's Controller.prune_cached() static method with no parameters
// — resolves cache dir internally via infra.GetWarmImageDir().
func PruneCached() int {
	warmDir := infra.GetWarmImageDir("")
	removedCount := 0
	info, err := os.Stat(warmDir)
	if err != nil || !info.IsDir() {
		return 0
	}

	entries, err := os.ReadDir(warmDir)
	if err != nil {
		return 0
	}

	for _, entry := range entries {
		path := filepath.Join(warmDir, entry.Name())
		if err := os.Remove(path); err != nil {
			slog.Warn("Failed to remove from cache", "entry", entry.Name(), "error", err)
		} else {
			removedCount++
			slog.Info("Removed from cache", "entry", entry.Name())
		}
	}

	slog.Info("Pruned cache", "removed", removedCount)
	return removedCount
}
