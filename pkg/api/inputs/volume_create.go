package inputs

import (
	"context"
	"fmt"
	"mvmctl/internal/core/volume"
	"mvmctl/internal/lib/disk"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/lib/validators"
	"mvmctl/pkg/errs"
)

// VolumeCreateInput specifies volume create input.
type VolumeCreateInput struct {
	Name      string `json:"name"`
	Size      string `json:"size"`
	ReadOnly  *bool  `json:"read_only,omitempty"`
	Shareable *bool  `json:"shareable,omitempty"`
	Writeback *bool  `json:"writeback,omitempty"`
}

// ResolvedVolumeCreateInput specifies resolved volume create input.
type ResolvedVolumeCreateInput struct {
	Name        string
	SizeBytes   int64
	IsReadOnly  bool
	IsShareable bool
	CacheType   string
}

// Validate checks that the volume create input is valid.
func (i *VolumeCreateInput) Validate() error {
	if i.Name == "" {
		return fmt.Errorf("volume name is required")
	}
	if i.Size == "" {
		return fmt.Errorf("volume size is required")
	}
	return nil
}

// Resolve resolves the create input to a ResolvedVolumeCreateInput.
// Parses size, defaults read-only, validates name, and checks
// for existing volumes with the same name.
func (i *VolumeCreateInput) Resolve(ctx context.Context, repo volume.Repository) (*ResolvedVolumeCreateInput, error) {
	if err := i.Validate(); err != nil {
		return nil, err
	}
	sizeBytes, err := disk.ParseDiskSizeToBytes(i.Size)
	if err != nil {
		return nil, errs.New(errs.CodeValidationFailed, fmt.Sprintf("Invalid volume size: %s", err.Error()))
	}
	isReadOnly := false
	if i.ReadOnly != nil {
		isReadOnly = *i.ReadOnly
	}
	isShareable := false
	if i.Shareable != nil {
		isShareable = *i.Shareable
	}
	var cacheType string
	if i.Writeback != nil {
		if *i.Writeback {
			cacheType = model.CacheTypeWriteback
		} else {
			cacheType = model.CacheTypeUnsafe
		}
	}
	result := &ResolvedVolumeCreateInput{
		Name:        i.Name,
		SizeBytes:   sizeBytes,
		IsReadOnly:  isReadOnly,
		IsShareable: isShareable,
		CacheType:   cacheType,
	}
	// Validate volume name rules
	if err := validators.VolumeName(result.Name); err != nil {
		return nil, errs.New(errs.CodeValidationFailed, err.Error())
	}
	// Check for existing volume with same name
	existing, err := repo.GetByName(ctx, result.Name)
	if err != nil {
		return nil, errs.New(errs.CodeDatabaseError, "Failed to check existing volume: "+err.Error())
	}
	if existing != nil {
		return nil, errs.AlreadyExists(
			errs.CodeVolumeAlreadyExists,
			fmt.Sprintf("Volume '%s' already exists", result.Name),
		)
	}
	return result, nil
}
