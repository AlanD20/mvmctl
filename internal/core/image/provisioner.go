package image

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"mvmctl/internal/infra"
	"mvmctl/internal/lib/provisioner"
	"mvmctl/pkg/errs"
)

// Provisioner optimizes a root filesystem image — shrink, deblob, fix fstab.
// deblob() and shrink() are declarative — they only set flags.
// run() creates a fresh backend for each phase.
type Provisioner struct {
	imagePath       string
	provisionerType provisioner.ProvisionerType
	fsType          string
	deblob          bool
	shrink          bool
	convertTo       string
}

// NewProvisioner creates a new Provisioner.
func NewProvisioner(
	imagePath string,
	provisionerType provisioner.ProvisionerType,
	fsType string,
) *Provisioner {
	return &Provisioner{
		imagePath:       imagePath,
		provisionerType: provisionerType,
		fsType:          fsType,
	}
}

// createBackend creates a fresh backend for the current image.
func (p *Provisioner) createBackend(ctx context.Context) (provisioner.Backend, error) {
	cacheDir, err := infra.GetCacheDir()
	if err != nil {
		return nil, fmt.Errorf("resolve cache dir: %w", err)
	}
	return provisioner.NewBackend(ctx, provisioner.BackendOpts{
		RootfsPath:      p.imagePath,
		FsType:          p.fsType,
		CacheDir:        cacheDir,
		ProvisionerType: p.provisionerType,
		UserUID:         1000,
		UserGID:         1000,
	})
}

// --- builder methods (declarative) ---

// DetectOS detects the OS type from the image using a fresh backend session.
// Returns OS identifier (e.g. "ubuntu", "debian", "alpine").
// Errors propagate to the caller.
func (p *Provisioner) DetectOS(ctx context.Context) (string, error) {
	backend, err := p.createBackend(ctx)
	if err != nil {
		return "", err
	}
	return backend.DetectOS(ctx)
}

// Deblob marks that deblob + fstab fix should run.
func (p *Provisioner) Deblob() {
	p.deblob = true
}

// Shrink marks that filesystem shrink should run.
func (p *Provisioner) Shrink() {
	p.shrink = true
}

// ConvertTo marks that filesystem conversion should run as Phase 0.
func (p *Provisioner) ConvertTo(targetFS string) {
	p.convertTo = targetFS
}

// --- execution ---

// Run executes queued operations with the selected backend.
// Phases run in order — conversion (Phase 0), deblob (Phase 1), shrink (Phase 2).
// A single backend session is shared across all phases — unnecessary
// mount/umount cycles are avoided.  ConvertTo updates the backend's internal
// fsType so subsequent phases see the converted filesystem.
// Returns true if at least one phase ran successfully.
func (p *Provisioner) Run(ctx context.Context) (bool, error) {
	deblobOK := false
	shrinkOK := false
	convertOK := false

	backend, err := p.createBackend(ctx)
	if err != nil {
		return false, err
	}

	// Phase 0: filesystem conversion (e.g. btrfs → ext4)
	if p.convertTo != "" {
		if err := backend.ConvertTo(ctx, p.convertTo); err != nil {
			return false, err
		}
		p.fsType = p.convertTo
		p.convertTo = ""
		convertOK = true
		slog.Info("Filesystem converted")
	}

	// Phase 1: deblob + fstab fix
	if p.deblob {
		if err := backend.Deblob(ctx, nil); err != nil {
			return false, err
		}
		if err := backend.Run(ctx); err != nil {
			return false, err
		}
		p.deblob = false
		deblobOK = true
	}

	// Phase 2: shrink
	if p.shrink {
		if err := backend.Shrink(ctx); err != nil {
			return false, err
		}
		if err := backend.Run(ctx); err != nil {
			return false, err
		}
		p.shrink = false
		shrinkOK = true
	}

	return convertOK || deblobOK || shrinkOK, nil
}

// ExtractViaBackend extracts a root partition from a raw disk image.
// Uses the selected backend's ExtractPartition method.
func ExtractViaBackend(
	ctx context.Context,
	rawPath, outputPath string,
	partition int,
	disabledDetectors []string,
	provisionerType provisioner.ProvisionerType,
) (result string, err error) {
	// Wrap non-DomainError as ImageError
	defer func() {
		if err != nil {
			var de *errs.DomainError
			if !errors.As(err, &de) {
				err = errs.New(errs.CodeImageError, err.Error())
			}
		}
	}()

	cacheDir, err := infra.GetCacheDir()
	if err != nil {
		return "", fmt.Errorf("resolve cache dir: %w", err)
	}

	backend, err := provisioner.NewBackend(ctx, provisioner.BackendOpts{
		RootfsPath:      rawPath,
		FsType:          "ext4", // placeholder — backends detect fs from the image
		CacheDir:        cacheDir,
		ProvisionerType: provisionerType,
		RootUID:         0,
		RootGID:         0,
		UserUID:         1000,
		UserGID:         1000,
	})
	if err != nil {
		return "", err
	}

	return backend.ExtractPartition(ctx, rawPath, outputPath, partition, disabledDetectors)
}
