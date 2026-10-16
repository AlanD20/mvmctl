package jailer

import (
	"context"
	"errors"
	"sync"

	"golang.org/x/sys/unix"

	"mvmctl/internal/infra"
)

type managedPersistentMetricsPresence uint8

const (
	managedPersistentMetricsDisabled managedPersistentMetricsPresence = iota + 1
	managedPersistentMetricsEnabled
)

type managedPersistentOutputName uint8

const (
	managedPersistentLog managedPersistentOutputName = iota + 1
	managedPersistentConsole
	managedPersistentMetrics
)

const managedPersistentOutputRequiredStat = unix.STATX_TYPE | unix.STATX_MODE | unix.STATX_NLINK |
	unix.STATX_UID | unix.STATX_GID | unix.STATX_INO | unix.STATX_SIZE | unix.STATX_MTIME | unix.STATX_CTIME

type managedPersistentOutputDeps struct {
	openAt2      func(context.Context, int, string, *unix.OpenHow) (int, error)
	statx        func(context.Context, int, int, int, *unix.Statx_t) error
	fchown       func(context.Context, int, int, int) error
	fchmod       func(context.Context, int, uint32) error
	ftruncate    func(context.Context, int, int64) error
	fsync        func(context.Context, int) error
	publish      func(context.Context, int, int, managedPersistentOutputName) error
	close        func(context.Context, int) error
	getAccessACL func(context.Context, int, []byte) (int, error)
	setAccessACL func(context.Context, int, []byte, int) error
}

type managedPersistentOutput struct {
	name          managedPersistentOutputName
	fd            int
	isolatedGrant bool
}

// managedVMPersistentOutputLease owns only individually admitted persistent files and their directory chain.
// THREAD-SAFE: value copies share the same preparation and close-once state; no descriptor or path is exposed.
type managedVMPersistentOutputLease struct {
	state *managedVMPersistentOutputState
}

type managedVMPersistentOutputState struct {
	// mu serializes preparation, readiness, descriptor ownership, and release across copied capabilities.
	mu    sync.Mutex
	deps  managedPersistentOutputDeps
	cache *managedCacheLease
	owner instanceCaller
	// executionUID derives only from this VM's durable launch record, never a requested ACL.
	executionUID        uint32
	vmDirectoryFD       int
	retained            []int
	outputs             []managedPersistentOutput
	attempted           bool
	ready               bool
	mutationAttempted   bool
	modified            bool
	published           bool
	durabilityUncertain bool
	outcomeUnknown      bool
	released            bool
	releaseErr          error
}

func validateManagedPersistentOutputDeps(deps managedPersistentOutputDeps) error {
	if deps.openAt2 == nil || deps.statx == nil || deps.fchown == nil || deps.fchmod == nil ||
		deps.ftruncate == nil || deps.fsync == nil || deps.publish == nil || deps.close == nil ||
		deps.getAccessACL == nil || deps.setAccessACL == nil {
		return managedVMResourceAtomicError("persistent output effects are incomplete", nil)
	}
	return nil
}

func managedPersistentOutputNames(metrics managedPersistentMetricsPresence) ([]managedPersistentOutput, error) {
	outputs := []managedPersistentOutput{{name: managedPersistentLog, fd: -1}, {name: managedPersistentConsole, fd: -1}}
	switch metrics {
	case managedPersistentMetricsDisabled:
	case managedPersistentMetricsEnabled:
		outputs = append(outputs, managedPersistentOutput{name: managedPersistentMetrics, fd: -1})
	default:
		return nil, managedVMResourceValidationError("persistent output metrics selection is invalid")
	}
	return outputs, nil
}

func (name managedPersistentOutputName) basename() string {
	switch name {
	case managedPersistentLog:
		return infra.VMFirecrackerLogFilename
	case managedPersistentConsole:
		return infra.VMFirecrackerConsoleLogFilename
	case managedPersistentMetrics:
		return infra.VMFirecrackerMetricsFilename
	default:
		return ""
	}
}

// CALLER MUST HOLD: state.mu. Directory and file opens never create, repair, or truncate caller state.
func admitManagedPersistentOutputs(ctx context.Context, state *managedVMPersistentOutputState, vmID string) error {
	parentFD := state.cache.cacheFD
	for _, name := range [...]string{"vms", vmID} {
		how := unix.OpenHow{
			Flags:   uint64(unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW),
			Resolve: uint64(managedVMResourceResolveFlags),
		}
		fd, err := state.deps.openAt2(ctx, parentFD, name, &how)
		if err != nil {
			return classifyManagedVMResourceOpenError("persistent output directory", err)
		}
		state.retained = append(state.retained, fd)
		if _, err := inspectManagedPersistentOutputObject(ctx, state, fd, true, 0); err != nil {
			return err
		}
		parentFD = fd
	}
	state.vmDirectoryFD = parentFD
	for index := range state.outputs {
		if err := ctx.Err(); err != nil {
			return persistentOutputError("admit persistent output", err)
		}
		output := &state.outputs[index]
		how := unix.OpenHow{
			Flags:   uint64(unix.O_RDWR | unix.O_NONBLOCK | unix.O_CLOEXEC | unix.O_NOFOLLOW),
			Resolve: uint64(managedVMResourceResolveFlags),
		}
		fd, err := state.deps.openAt2(ctx, parentFD, output.name.basename(), &how)
		if errors.Is(err, unix.ENOENT) {
			continue
		}
		if err != nil {
			return classifyManagedVMResourceOpenError("persistent output", err)
		}
		state.retained = append(state.retained, fd)
		output.fd = fd
		stat, err := inspectManagedPersistentOutputObject(ctx, state, fd, false, 1)
		if err != nil {
			return err
		}
		output.isolatedGrant = stat.Mode == unix.S_IFREG|0660
	}
	return nil
}

// inspectManagedPersistentOutputObject requires two stable synchronized observations of the pinned object.
func inspectManagedPersistentOutputObject(
	ctx context.Context,
	state *managedVMPersistentOutputState,
	fd int,
	directory bool,
	links uint32,
) (unix.Statx_t, error) {
	var before unix.Statx_t
	for pass := range 2 {
		if err := ctx.Err(); err != nil {
			return unix.Statx_t{}, persistentOutputError("inspect persistent output", err)
		}
		var current unix.Statx_t
		if err := state.deps.statx(ctx, fd, managedVMResourceStatxFlags, unix.STATX_BASIC_STATS, &current); err != nil {
			return unix.Statx_t{}, persistentOutputError("inspect persistent output", err)
		}
		if current.Mask&managedPersistentOutputRequiredStat != managedPersistentOutputRequiredStat ||
			current.Ino == 0 || (current.Dev_major == 0 && current.Dev_minor == 0) ||
			current.Attributes&current.Attributes_mask&unix.STATX_ATTR_AUTOMOUNT != 0 {
			return unix.Statx_t{}, managedVMResourceValidationError(
				"persistent output metadata is incomplete or unsafe",
			)
		}
		if current.Uid != state.owner.uid {
			return unix.Statx_t{}, managedVMResourceValidationError(
				"persistent output owner does not match launch authority",
			)
		}
		if directory {
			if current.Mode&unix.S_IFMT != unix.S_IFDIR || current.Mode&0700 != 0700 || current.Mode&07022 != 0 {
				return unix.Statx_t{}, managedVMResourceValidationError(
					"persistent output directory type or mode is unsafe",
				)
			}
		} else {
			if current.Mode&unix.S_IFMT != unix.S_IFREG || current.Gid != state.owner.gid || current.Nlink != links {
				return unix.Statx_t{}, managedVMResourceValidationError(
					"persistent output must be an individually pinned caller-owned regular file",
				)
			}
			if _, err := inspectIsolatedPersistentOutputACL(ctx, state, fd, current.Mode); err != nil {
				return unix.Statx_t{}, err
			}
		}
		if pass == 1 && !sameManagedPersistentOutputStat(before, current) {
			return unix.Statx_t{}, managedVMResourceValidationError(
				"persistent output metadata changed during admission",
			)
		}
		before = current
	}
	return before, nil
}

func sameManagedPersistentOutputStat(left, right unix.Statx_t) bool {
	return left.Dev_major == right.Dev_major && left.Dev_minor == right.Dev_minor && left.Ino == right.Ino &&
		left.Mode == right.Mode && left.Uid == right.Uid && left.Gid == right.Gid && left.Nlink == right.Nlink &&
		left.Size == right.Size && left.Mtime == right.Mtime && left.Ctime == right.Ctime
}

func (lease *managedVMPersistentOutputLease) requireReady() error {
	if lease == nil {
		return nil
	}
	if lease.state == nil {
		return managedVMResourceAtomicError("persistent output authority is incomplete", nil)
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released || !state.ready {
		return annotatePersistentOutputError(
			managedVMResourceAtomicError("persistent output preparation did not complete", nil),
			state,
		)
	}
	return nil
}

// Release closes every retained descriptor once. Published persistent files are never removed.
func (lease *managedVMPersistentOutputLease) Release(ctx context.Context) error {
	if lease == nil || lease.state == nil {
		return nil
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released {
		return state.releaseErr
	}
	cleanupCtx := processCleanupContext(ctx)
	var result error
	for index := len(state.retained) - 1; index >= 0; index-- {
		result = appendInstanceOperationError(
			result,
			"close persistent output descriptor",
			state.deps.close(cleanupCtx, state.retained[index]),
		)
	}
	if state.cache != nil {
		result = appendInstanceOperationError(
			result,
			"close persistent output cache authority",
			state.cache.Release(cleanupCtx),
		)
	}
	state.retained = nil
	state.cache = nil
	state.vmDirectoryFD = -1
	state.outputs = nil
	state.released = true
	state.ready = false
	state.releaseErr = annotatePersistentOutputError(result, state)
	return state.releaseErr
}

func realManagedPersistentOutputDeps() managedPersistentOutputDeps {
	return managedPersistentOutputDeps{
		openAt2: func(_ context.Context, parentFD int, name string, how *unix.OpenHow) (int, error) {
			return unix.Openat2(parentFD, name, how)
		},
		statx: func(_ context.Context, fd, flags, mask int, stat *unix.Statx_t) error {
			return unix.Statx(fd, "", flags, mask, stat)
		},
		fchown:    func(_ context.Context, fd, uid, gid int) error { return unix.Fchown(fd, uid, gid) },
		fchmod:    func(_ context.Context, fd int, mode uint32) error { return unix.Fchmod(fd, mode) },
		ftruncate: func(_ context.Context, fd int, size int64) error { return unix.Ftruncate(fd, size) },
		fsync:     func(_ context.Context, fd int) error { return unix.Fsync(fd) },
		publish: func(_ context.Context, fd, parentFD int, name managedPersistentOutputName) error {
			basename := name.basename()
			if basename == "" {
				return managedVMResourceValidationError("persistent output name is invalid")
			}
			return unix.Linkat(fd, "", parentFD, basename, unix.AT_EMPTY_PATH)
		},
		close: func(_ context.Context, fd int) error { return unix.Close(fd) },
		getAccessACL: func(_ context.Context, fd int, target []byte) (int, error) {
			return unix.Fgetxattr(fd, isolatedOutputAccessACLName, target)
		},
		setAccessACL: func(_ context.Context, fd int, value []byte, flags int) error {
			return unix.Fsetxattr(fd, isolatedOutputAccessACLName, value, flags)
		},
	}
}
