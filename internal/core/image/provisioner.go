package image

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"syscall"

	"mvmctl/internal/infra"
	"mvmctl/internal/infra/errs"
	"mvmctl/internal/infra/provisioner"
	"mvmctl/internal/infra/provisioner/guestfs"
	"mvmctl/internal/infra/provisioner/loopmount"
)

// Provisioner matches Python's Provisioner in _provisioner.py.
// Optimizes a root filesystem image — shrink, deblob, fix fstab.
// deblob() and shrink() are declarative — they only set flags.
// run() creates a fresh backend for each phase.
type Provisioner struct {
	ctx             context.Context
	imagePath       string
	provisionerType provisioner.ProvisionerType
	fsType          string
	cacheDir        string
	deblob          bool
	shrink          bool
	convertTo       string
}

// NewProvisioner creates a new Provisioner.
// Matches Python's Provisioner.__init__() which takes:
//
//	image_path: Path, *, provisioner_type: provisioner.ProvisionerType, fs_type: str
//
// cacheDir is resolved by the caller and passed through.
func NewProvisioner(
	ctx context.Context,
	imagePath string,
	provisionerType provisioner.ProvisionerType,
	fsType string,
	cacheDir string,
) *Provisioner {
	return &Provisioner{
		ctx:             ctx,
		imagePath:       imagePath,
		provisionerType: provisionerType,
		fsType:          fsType,
		cacheDir:        cacheDir,
	}
}

// createBackend creates a fresh backend for the current image.
// Matches Python's ProvisionerBackend.get_image().
// cacheDir is taken from the Provisioner struct (resolved by the caller at
// construction time), not from the environment at call time.
func (p *Provisioner) createBackend() (provisioner.Backend, error) {
	return provisioner.NewBackend(p.ctx, provisioner.BackendOpts{
		RootfsPath:      p.imagePath,
		FsType:          p.fsType,
		CacheDir:        p.cacheDir,
		ProvisionerType: p.provisionerType,
		UserUID:         1000,
		UserGID:         1000,
	})
}

// -- builder methods (declarative) -------------------------------------------

// DetectOS detects the OS type from the image using a fresh backend session.
// Returns OS identifier (e.g. "ubuntu", "debian", "alpine").
// Matches Python's Provisioner.detect_os() which lets errors propagate
// to the caller (Service wraps it in try/except).
func (p *Provisioner) DetectOS() (string, error) {
	backend, err := p.createBackend()
	if err != nil {
		return "", err
	}
	return backend.DetectOS(p.ctx)
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

// isExpectedProvisionerError checks if an error matches Python's
// (LoopMountError, OSError, RuntimeError) catch pattern.
//   - LoopMountError = DomainError with "loopmount." code prefix
//   - OSError = os.PathError, os.LinkError, os.SyscallError, syscall.Errno
//   - RuntimeError = any other non-DomainError
//
// Non-loopmount DomainErrors (e.g. image.*, vm.*) are NOT caught — they propagate.
func isExpectedProvisionerError(err error) bool {
	var de *errs.DomainError
	if errors.As(err, &de) {
		// LoopMountError: DomainError with "loopmount." prefix
		return strings.HasPrefix(string(de.Code), "loopmount.")
	}
	// OSError equivalent: os.PathError, os.LinkError, os.SyscallError
	if isOSError(err) {
		return true
	}
	// RuntimeError equivalent: any non-DomainError error
	return true
}

// isOSError checks if an error is Go's equivalent of Python's OSError.
func isOSError(err error) bool {
	var pe *os.PathError
	if errors.As(err, &pe) {
		return true
	}
	var le *os.LinkError
	if errors.As(err, &le) {
		return true
	}
	var se *os.SyscallError
	if errors.As(err, &se) {
		return true
	}
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return true
	}
	return false
}

// -- execution ---------------------------------------------------------------

// Run executes queued operations with the selected backend.
// Phases run in order — conversion (Phase 0), deblob (Phase 1), shrink (Phase 2).
// Each phase uses a fresh backend session so a failure in one never leaks into the next.
// Returns true if at least one phase ran successfully.
// IMPORTANT: Matching Python error propagation EXACTLY:
//   - backend creation + declarative calls (deblob, shrink) = OUTSIDE try/except → errors propagate as (false, err)
//   - convert_to + run = INSIDE try/except → errors caught, logged as warnings, phase skipped
//   - Only (LoopMountError, OSError, RuntimeError) are caught — matching Python EXACTLY
func (p *Provisioner) Run() (bool, error) {
	deblobOK := false
	shrinkOK := false
	convertOK := false

	// Phase 0: filesystem conversion (e.g. btrfs → ext4)
	if p.convertTo != "" {
		backend, err := p.createBackend()
		if err != nil {
			return false, err
		}
		if err := backend.ConvertTo(p.ctx, p.convertTo); err != nil {
			if isExpectedProvisionerError(err) {
				slog.Warn(
					"Filesystem conversion skipped",
					"error",
					err,
					"hint",
					"Build the provisioner binary with 'python scripts/build_services.py' or enable libguestfs to enable fs conversion.",
				)
			} else {
				return false, err
			}
		} else {
			p.fsType = p.convertTo
			convertOK = true
			slog.Info("Filesystem converted", "from", p.fsType, "to", p.convertTo)
		}
	}

	// Phase 1: deblob + fstab fix (fresh backend — no state leakage)
	if p.deblob {
		backend, err := p.createBackend()
		if err != nil {
			return false, err
		}
		if err := backend.Deblob(p.ctx, nil); err != nil {
			return false, err
		}
		if err := backend.Run(p.ctx); err != nil {
			if isExpectedProvisionerError(err) {
				slog.Warn(
					"Debloating skipped",
					"error",
					err,
					"hint",
					"Build the provisioner binary with 'python scripts/build_services.py' or enable libguestfs to enable boot optimization.",
				)
			} else {
				return false, err
			}
		} else {
			deblobOK = true
		}
	}

	// Phase 2: shrink (fresh backend — deblob state completely isolated)
	if p.shrink {
		backend, err := p.createBackend()
		if err != nil {
			return false, err
		}
		if err := backend.Shrink(p.ctx); err != nil {
			return false, err
		}
		if err := backend.Run(p.ctx); err != nil {
			if isExpectedProvisionerError(err) {
				slog.Warn("Shrink skipped (image may already be minimal)", "error", err)
			} else {
				return false, err
			}
		} else {
			shrinkOK = true
		}
	}

	return convertOK || deblobOK || shrinkOK, nil
}

// ExtractViaBackend extracts a root partition from a raw disk image.
// Uses the selected backend's ExtractPartition method.
// Matches Python's _extract_via_backend() which catches RuntimeError and
// re-raises as ImageError — keeping Go behavior identical.
func ExtractViaBackend(
	ctx context.Context,
	rawPath, outputPath string,
	partition int,
	disabledDetectors []string,
	provisionerType provisioner.ProvisionerType,
) (result string, err error) {
	// Wrap non-DomainError as ImageError — matching Python's:
	// except RuntimeError as e:
	//     raise ImageError(str(e)) from e
	defer func() {
		if err != nil {
			var de *errs.DomainError
			if !errors.As(err, &de) {
				err = NewImageError(err.Error())
			}
		}
	}()

	// For extraction, fsType is a placeholder — the backend detects it from the image.
	fsType := "ext4"

	// Convert partition int to *int (nil = auto-detect, matching Python int | None = None).
	var partitionPtr *int
	if partition > 0 {
		partitionPtr = &partition
	}

	switch provisionerType {
	case provisioner.ProvisionerLoopMount:
		cacheDir, err := infra.GetCacheDir()
		if err != nil {
			return "", fmt.Errorf("extract partition: cannot resolve cache directory: %w", err)
		}
		backend := loopmount.NewLoopMountBackend(ctx, rawPath, fsType, cacheDir)
		return backend.ExtractPartition(ctx, rawPath, outputPath, partitionPtr, disabledDetectors)
	case provisioner.ProvisionerGuestFS:
		backend := guestfs.NewGuestfsBackend(rawPath, 0, 0, 1000, 1000)
		return backend.ExtractPartition(ctx, rawPath, outputPath, partitionPtr, disabledDetectors)
	default:
		return "", fmt.Errorf("image provisioner: unknown provisioner type: %s", provisionerType)
	}
}
