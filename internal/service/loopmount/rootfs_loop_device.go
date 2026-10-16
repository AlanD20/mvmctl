package loopmount

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"golang.org/x/sys/unix"

	"mvmctl/pkg/errs"
)

const (
	maxRootfsLoopConfigureAttempts = 16
	rootfsLoopOpenFlags            = unix.O_RDWR | unix.O_CLOEXEC | unix.O_NOFOLLOW
	maxRootfsLoopCleanupDetail     = 512
)

type rootfsLoopDeps struct {
	open      func(context.Context, string, int, uint32) (int, error)
	fstat     func(context.Context, int, *unix.Stat_t) error
	getFree   func(context.Context, int) (int, error)
	configure func(context.Context, int, *unix.LoopConfig) error
	clear     func(context.Context, int) error
	close     func(context.Context, int) error
}

type rootfsLoopDevice struct {
	state *rootfsLoopDeviceState
}

type rootfsLoopDeviceState struct {
	mu sync.Mutex

	deps       rootfsLoopDeps
	fd         int
	index      int
	identity   rootfsBlockIdentity
	access     rootfsAccess
	released   bool
	releaseErr error
}

// attachRootfsLoopDevice atomically configures one kernel-derived loop device from the admitted rootfs descriptor.
// A configure failure is never cleared: another process may have won the free-device race and own that loop device.
func attachRootfsLoopDevice(
	ctx context.Context,
	deps rootfsLoopDeps,
	lease *rootfsLease,
) (_ *rootfsLoopDevice, returnErr error) {
	if ctx == nil {
		return nil, rootfsAtomicError("loop device context is required", nil)
	}
	if err := ctx.Err(); err != nil {
		return nil, rootfsAtomicError("loop device context ended", err)
	}
	if err := validateRootfsLoopDeps(deps); err != nil {
		return nil, err
	}
	if lease == nil {
		return nil, rootfsAtomicError("managed rootfs authority is missing", nil)
	}

	lease.mu.Lock()
	defer lease.mu.Unlock()
	if lease.released {
		return nil, rootfsAtomicError("managed rootfs authority is inactive", nil)
	}
	config, err := newRootfsLoopConfig(lease.rootfsFD, lease.access)
	if err != nil {
		return nil, err
	}

	controlFD, err := deps.open(ctx, rootfsLoopControlPath, rootfsLoopOpenFlags, 0)
	if err != nil {
		return nil, rootfsAtomicError("open loop-control device", err)
	}
	defer func() {
		returnErr = appendRootfsLoopCleanupError(
			returnErr,
			"close loop-control device",
			deps.close(context.WithoutCancel(ctx), controlFD),
		)
	}()
	var controlStat unix.Stat_t
	if err := deps.fstat(ctx, controlFD, &controlStat); err != nil {
		return nil, rootfsAtomicError("inspect loop-control device", err)
	}
	if err := validateRootfsLoopControl(controlStat); err != nil {
		return nil, err
	}

	for attempt := 0; attempt < maxRootfsLoopConfigureAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, rootfsAtomicError("loop device context ended", err)
		}
		index, getFreeErr := deps.getFree(ctx, controlFD)
		if getFreeErr != nil {
			return nil, rootfsAtomicError("allocate free loop device", getFreeErr)
		}
		path, pathErr := rootfsLoopDevicePath(index)
		if pathErr != nil {
			return nil, pathErr
		}
		loopFD, openErr := deps.open(ctx, path, rootfsLoopOpenFlags, 0)
		if openErr != nil {
			return nil, rootfsAtomicError("open allocated loop device", openErr)
		}

		identity, inspectErr := inspectRootfsLoopDevice(ctx, deps, index, loopFD)
		if inspectErr != nil {
			return nil, appendRootfsLoopCleanupError(
				inspectErr,
				"close rejected loop device",
				deps.close(context.WithoutCancel(ctx), loopFD),
			)
		}
		attemptConfig := config
		configureErr := deps.configure(ctx, loopFD, &attemptConfig)
		if configureErr == nil {
			return &rootfsLoopDevice{state: &rootfsLoopDeviceState{
				deps: deps, fd: loopFD, index: index, identity: identity, access: lease.access,
			}}, nil
		}

		var primary error = rootfsAtomicError("configure admitted rootfs loop device", configureErr)
		closeErr := deps.close(context.WithoutCancel(ctx), loopFD)
		primary = appendRootfsLoopCleanupError(primary, "close unconfigured loop device", closeErr)
		if !errors.Is(configureErr, unix.EBUSY) || closeErr != nil {
			return nil, primary
		}
	}
	return nil, rootfsAtomicError("loop device allocation remained busy", unix.EBUSY)
}

// Release explicitly detaches the configured loop device before closing its descriptor. AUTOCLEAR remains crash safety.
func (device *rootfsLoopDevice) Release(ctx context.Context) error {
	if device == nil {
		return nil
	}
	if device.state == nil {
		return rootfsAtomicError("loop device lease is not active", nil)
	}
	state := device.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released {
		return state.releaseErr
	}

	cleanupCtx := context.Background()
	var result error
	if ctx == nil {
		result = rootfsAtomicError("loop device cleanup context is required", nil)
	} else {
		cleanupCtx = context.WithoutCancel(ctx)
	}
	if state.fd < 0 {
		result = appendRootfsLoopCleanupError(
			result,
			"release loop device lease",
			errors.New("loop device descriptor is invalid"),
		)
	} else {
		if state.deps.clear == nil {
			result = appendRootfsLoopCleanupError(
				result,
				"detach loop device",
				errors.New("loop device clear dependency is missing"),
			)
		} else {
			clearErr := state.deps.clear(cleanupCtx, state.fd)
			if errors.Is(clearErr, unix.ENXIO) {
				clearErr = nil
			}
			result = appendRootfsLoopCleanupError(result, "detach loop device", clearErr)
		}
		if state.deps.close == nil {
			result = appendRootfsLoopCleanupError(
				result,
				"close loop device",
				errors.New("loop device close dependency is missing"),
			)
		} else {
			result = appendRootfsLoopCleanupError(result, "close loop device", state.deps.close(cleanupCtx, state.fd))
		}
	}

	state.fd = -1
	state.index = -1
	state.identity = rootfsBlockIdentity{}
	state.access = 0
	state.released = true
	state.releaseErr = result
	return state.releaseErr
}

func inspectRootfsLoopDevice(
	ctx context.Context,
	deps rootfsLoopDeps,
	index int,
	loopFD int,
) (rootfsBlockIdentity, error) {
	var stat unix.Stat_t
	if err := deps.fstat(ctx, loopFD, &stat); err != nil {
		return rootfsBlockIdentity{}, rootfsAtomicError("inspect allocated loop device", err)
	}
	return validateRootfsLoopDevice(index, stat)
}

func validateRootfsLoopDeps(deps rootfsLoopDeps) error {
	if deps.open == nil || deps.fstat == nil || deps.getFree == nil || deps.configure == nil ||
		deps.clear == nil || deps.close == nil {
		return rootfsAtomicError("loop device dependencies are incomplete", nil)
	}
	return nil
}

func appendRootfsLoopCleanupError(primary error, message string, cause error) error {
	if cause == nil {
		return primary
	}
	detail := boundedRootfsLoopCleanupDetail(message, cause)
	if primary == nil {
		return newRootfsLoopCleanupError(detail, cause)
	}
	if domainErr := errs.AsDomainError(primary); domainErr != nil {
		domainErr.Message += "; " + detail
		domainErr.Err = errors.Join(domainErr.Err, cause)
		if domainErr.Details == nil {
			domainErr.Details = make(map[string]any)
		}
		domainErr.Details["loopmount_loop_cleanup_failed"] = true
		domainErr.Details["loopmount_loop_cleanup_detail"] = detail
		return domainErr
	}
	return newRootfsLoopCleanupError(detail, errors.Join(primary, cause))
}

func newRootfsLoopCleanupError(detail string, cause error) *errs.DomainError {
	domainErr := rootfsAtomicError(detail, cause)
	domainErr.Details = map[string]any{
		"loopmount_loop_cleanup_failed": true,
		"loopmount_loop_cleanup_detail": detail,
	}
	return domainErr
}

func boundedRootfsLoopCleanupDetail(message string, cause error) string {
	detail := fmt.Sprintf("%s: %v", message, cause)
	if len(detail) <= maxRootfsLoopCleanupDetail {
		return detail
	}
	return detail[:maxRootfsLoopCleanupDetail-3] + "..."
}

func systemRootfsLoopDeps() rootfsLoopDeps {
	return rootfsLoopDeps{
		open: func(_ context.Context, path string, flags int, mode uint32) (int, error) {
			return unix.Open(path, flags, mode)
		},
		fstat: func(_ context.Context, fd int, stat *unix.Stat_t) error {
			return unix.Fstat(fd, stat)
		},
		getFree: func(_ context.Context, controlFD int) (int, error) {
			return unix.IoctlRetInt(controlFD, unix.LOOP_CTL_GET_FREE)
		},
		configure: func(_ context.Context, loopFD int, config *unix.LoopConfig) error {
			return unix.IoctlLoopConfigure(loopFD, config)
		},
		clear: func(_ context.Context, loopFD int) error {
			return unix.IoctlSetInt(loopFD, unix.LOOP_CLR_FD, 0)
		},
		close: func(_ context.Context, fd int) error {
			return unix.Close(fd)
		},
	}
}
