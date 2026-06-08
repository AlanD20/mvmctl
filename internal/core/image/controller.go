package image

import (
	"context"
	"fmt"
	"path/filepath"

	"mvmctl/internal/lib/model"
)

// Controller matches Python's Controller.
// Manages image operations for a specific image — compression, decompression,
// and tmpfs caching for fast VM rootfs cloning.
type Controller struct {
	image *model.ImageItem
	repo  Repository
}

// NewController creates an Controller from an entity.
// entity can be an *model.ImageItem or a string identifier.
func NewController(ctx context.Context, entity any, repo Repository) (*Controller, error) {
	ctrl := &Controller{repo: repo}
	switch e := entity.(type) {
	case *model.ImageItem:
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

// Get returns the resolved model.ImageItem.
func (c *Controller) Get() *model.ImageItem {
	return c.image
}

// ImagePath returns the image storage path.
func (c *Controller) ImagePath() string {
	return c.image.Path
}

// CompressedPath returns the compressed path for this image.
// Derives the suffix strictly from model.ImageItem.CompressedFormat.
// Returns an error if CompressedFormat is nil or empty — that state means
// "image is not compressed" (see service.go copyToCache branch), and silently
// guessing a suffix would produce a path that does not exist on disk.
func (c *Controller) CompressedPath() (string, error) {
	if c.image.CompressedFormat == nil || *c.image.CompressedFormat == "" {
		return "", fmt.Errorf("image %q has no CompressedFormat; cannot derive CompressedPath", c.image.ID)
	}
	format := *c.image.CompressedFormat
	suffix := "." + format
	if format[0] == '.' {
		suffix = format
	}
	base := c.image.Path
	ext := filepath.Ext(base)
	return base[:len(base)-len(ext)] + suffix, nil
}
