package testutil

import (
	"context"
	"sort"
	"sync"
	"time"

	"mvmctl/internal/core/image"
	"mvmctl/internal/lib/model"
)

// ImageRepo is an in-memory image repository for testing.
// Includes soft-delete filtering where applicable.
type ImageRepo struct {
	mu     sync.RWMutex
	images map[string]*model.ImageItem
}

func NewImageRepo() *ImageRepo {
	return &ImageRepo{images: make(map[string]*model.ImageItem)}
}

// isNotDeleted returns true if the image is NOT soft-deleted.
func (r *ImageRepo) isNotDeleted(img *model.ImageItem) bool {
	return img.DeletedAt == nil && img.IsPresent
}

// Get returns an image by ID. Does NOT filter on soft-delete.
func (r *ImageRepo) Get(_ context.Context, id string) (*model.ImageItem, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	img, ok := r.images[id]
	if !ok {
		return nil, nil
	}
	return img, nil
}

// FindByPrefix returns images whose ID starts with prefix.
// When includeDeleted is true, soft-deleted images are also returned.
func (r *ImageRepo) FindByPrefix(_ context.Context, prefix string, includeDeleted ...bool) ([]*model.ImageItem, error) {
	checkDeleted := len(includeDeleted) == 0 || !includeDeleted[0]
	r.mu.RLock()
	defer r.mu.RUnlock()
	var result []*model.ImageItem
	for _, img := range r.images {
		if (!checkDeleted || r.isNotDeleted(img)) && len(img.ID) >= len(prefix) && img.ID[:len(prefix)] == prefix {
			result = append(result, img)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].CreatedAt < result[j].CreatedAt
	})
	return result, nil
}

// GetByType returns an image by type, preferring default and newest.
func (r *ImageRepo) GetByType(_ context.Context, imgType string) (*model.ImageItem, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var candidates []*model.ImageItem
	for _, img := range r.images {
		if img.Type == imgType && r.isNotDeleted(img) {
			candidates = append(candidates, img)
		}
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	// Sort: default first, then newest
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].IsDefault != candidates[j].IsDefault {
			return candidates[i].IsDefault // true first (DESC)
		}
		return candidates[i].CreatedAt > candidates[j].CreatedAt // newer first (DESC)
	})
	return candidates[0], nil
}

// GetByVersionAndType returns an image by version and type.
func (r *ImageRepo) GetByVersionAndType(_ context.Context, version, imgType string) (*model.ImageItem, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, img := range r.images {
		if img.Type == imgType && img.Version == version && r.isNotDeleted(img) {
			return img, nil
		}
	}
	return nil, nil
}

// GetByName returns an image by name. When includeDeleted is true, soft-deleted
// images are also returned.
func (r *ImageRepo) GetByName(_ context.Context, name string, includeDeleted ...bool) (*model.ImageItem, error) {
	checkDeleted := len(includeDeleted) == 0 || !includeDeleted[0]
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, img := range r.images {
		if img.Name == name && (!checkDeleted || r.isNotDeleted(img)) {
			return img, nil
		}
	}
	return nil, nil
}

// ListAll returns all images ordered by created_at.
func (r *ImageRepo) ListAll(_ context.Context) ([]*model.ImageItem, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var result []*model.ImageItem
	for _, img := range r.images {
		result = append(result, img)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].CreatedAt < result[j].CreatedAt
	})
	return result, nil
}

func (r *ImageRepo) Upsert(_ context.Context, img *model.ImageItem) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.images[img.ID] = img
	return nil
}

// SoftDelete marks an image as deleted.
func (r *ImageRepo) SoftDelete(_ context.Context, imageID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if img, ok := r.images[imageID]; ok {
		now := time.Now().UTC().Format(time.RFC3339)
		img.IsPresent = false
		img.DeletedAt = &now
	}
	return nil
}

func (r *ImageRepo) Delete(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.images, id)
	return nil
}

// GetDefault returns the default image.
func (r *ImageRepo) GetDefault(_ context.Context) (*model.ImageItem, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, img := range r.images {
		if img.IsDefault && img.IsPresent {
			return img, nil
		}
	}
	return nil, nil
}

// SetDefault sets one image as default, clearing all others atomically.
func (r *ImageRepo) SetDefault(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Clear existing defaults
	for _, img := range r.images {
		if img.DeletedAt == nil {
			img.IsDefault = false
		}
	}
	// Set new default
	if img, ok := r.images[id]; ok && img.DeletedAt == nil {
		img.IsDefault = true
	}
	return nil
}

// Count returns total count of all non-deleted images.
func (r *ImageRepo) Count(_ context.Context) (int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	count := 0
	for _, img := range r.images {
		if img.DeletedAt == nil {
			count++
		}
	}
	return count, nil
}

func (r *ImageRepo) UpdateManyIsPresent(_ context.Context, imageIDs []string, isPresent bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, id := range imageIDs {
		if img, ok := r.images[id]; ok {
			img.IsPresent = isPresent
		}
	}
	return nil
}

// Ensure ImageRepo implements image.Repository.
var _ image.Repository = (*ImageRepo)(nil)
