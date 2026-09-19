package jailer

import (
	"context"
	"errors"
	"os"
	"sort"
	"strings"
	"sync"

	"golang.org/x/sys/unix"

	"mvmctl/internal/infra"
)

// HostResetGuard retains the global instance index and admitted filesystem roots.
// Copies share one close-once state; no path, record, or descriptor escapes the guard.
type HostResetGuard struct{ state *hostResetGuardState }

type hostResetGuardState struct {
	// mu guards release and all retained descriptor/lock ownership.
	mu                  sync.Mutex
	roots               *instanceAuthorityRoots
	index               *authorityLock
	executionIdentities *executionIdentityResetGuard
	retained            []int
	released            bool
	releaseErr          error
}

// AcquireHostResetGuard admits only globally cleaned schema-3 instance authority
// and absent fixed VM jail, runtime and cgroup resources. It never cleans or adopts.
// CRITICAL: The administrator must already hold exclusive host admission, including
// its durable disabled gate, and retain it until reset or rollback has finished.
// Network/firewall and provisioner quiescence require their own owning guards.
func AcquireHostResetGuard(ctx context.Context) (_ *HostResetGuard, returnErr error) {
	if ctx == nil || os.Geteuid() != 0 || os.Getegid() != 0 {
		return nil, instanceStateError("", "host reset instance guard requires root and a context")
	}
	if err := ctx.Err(); err != nil {
		return nil, instanceAtomicError("acquire host reset instance guard", err)
	}
	deps := realInstanceAuthorityDeps()
	policy := productionInstanceAuthorityPolicy()
	rootFD, err := deps.open(ctx, policy.rootPath, instanceDirectoryFlags, 0)
	if err != nil {
		return nil, instanceAtomicError("open host reset filesystem root", err)
	}
	roots := &instanceAuthorityRoots{deps: deps, policy: policy, stateFD: -1, retained: []int{rootFD}}
	guard := &HostResetGuard{state: &hostResetGuardState{roots: roots}}
	defer func() {
		if returnErr != nil {
			returnErr = appendInstanceOperationError(
				returnErr,
				"release rejected host reset guard",
				guard.Release(context.WithoutCancel(ctx)),
			)
		}
	}()
	if err := verifyInstanceDirectory(
		ctx,
		deps,
		rootFD,
		"host reset filesystem root",
		policy,
		instanceAncestorDirectory,
	); err != nil {
		return nil, err
	}
	authority, err := (systemInstanceAuthorityTree{}).openJailer(ctx)
	if err != nil {
		return nil, err
	}
	roots.authority = authority
	guard.state.index, err = roots.acquireIndexLock(ctx)
	if err != nil {
		return nil, err
	}
	if err := guard.pinExistingInstanceState(ctx); err != nil {
		return nil, err
	}
	if err := guard.requireCleanedInstanceInventory(ctx); err != nil {
		return nil, err
	}
	if err := guard.requireNoJailResources(ctx); err != nil {
		return nil, err
	}
	if err := guard.requireNoRuntimeResources(ctx); err != nil {
		return nil, err
	}
	if err := guard.requireNoCgroupResources(ctx); err != nil {
		return nil, err
	}
	guard.state.executionIdentities, err = acquireExecutionIdentityResetGuard(ctx)
	if err != nil {
		return nil, err
	}
	return guard, nil
}

func (guard *HostResetGuard) pinExistingInstanceState(ctx context.Context) error {
	roots := guard.state.roots
	names, err := instanceAuthorityPathComponents(infra.JailerInstanceRoot, 4)
	if err != nil {
		return err
	}
	parent := roots.retained[0]
	for index, name := range names {
		class := instanceAncestorDirectory
		if index == 2 {
			class = instanceStateBaseDirectory
		}
		if index == 3 {
			class = instancePrivateDirectory
		}
		fd, err := openInstanceDirectory(ctx, roots.deps, parent, name, false, class, roots.policy)
		if errors.Is(err, unix.ENOENT) && index >= 2 {
			return nil
		}
		if err != nil {
			return err
		}
		roots.retained = append(roots.retained, fd)
		parent = fd
	}
	roots.stateFD = parent
	return nil
}

func (guard *HostResetGuard) requireCleanedInstanceInventory(ctx context.Context) error {
	roots := guard.state.roots
	if roots.stateFD < 0 {
		return nil
	}
	names, err := roots.deps.readDirNames(ctx, roots.stateFD)
	if err != nil {
		return instanceAtomicError("enumerate host reset instance owners", err)
	}
	sort.Strings(names)
	seen := make(map[string]struct{})
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return instanceAtomicError("scan host reset instance owners", err)
		}
		uid, canonical := validateCanonicalUIDName(name)
		if !canonical {
			return instanceAtomicError("host reset found a noncanonical instance owner", nil)
		}
		fd, err := openInstanceDirectory(
			ctx,
			roots.deps,
			roots.stateFD,
			name,
			false,
			instancePrivateDirectory,
			roots.policy,
		)
		if err != nil {
			return err
		}
		guard.state.retained = append(guard.state.retained, fd)
		directories := &instanceUIDDirectories{deps: roots.deps, policy: roots.policy, uid: uid, stateFD: fd}
		records, err := roots.deps.readDirNames(ctx, fd)
		if err != nil {
			return instanceAtomicError("enumerate host reset instance records", err)
		}
		sort.Strings(records)
		for _, filename := range records {
			vmID, suffix := strings.CutSuffix(filename, ".json")
			if !suffix || !vmIDPattern.MatchString(vmID) {
				return instanceAtomicError("host reset found an unexpected instance entry", nil)
			}
			if _, duplicate := seen[vmID]; duplicate {
				return instanceAtomicError("host reset found duplicate global VM ownership", nil)
			}
			seen[vmID] = struct{}{}
			record, err := readHostResetRecord(ctx, directories, vmID)
			if err != nil {
				return err
			}
			if record.lifecycle != instanceLifecycleCleaned {
				return instanceStateError(vmID, "host reset requires every instance to be cleaned")
			}
		}
	}
	return nil
}

func readHostResetRecord(
	ctx context.Context,
	directories *instanceUIDDirectories,
	vmID string,
) (_ instanceRecord, returnErr error) {
	fd, err := unix.Openat2(directories.stateFD, vmID+".json", &unix.OpenHow{
		Flags:   instanceReadFlags | unix.O_NONBLOCK,
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS | unix.RESOLVE_NO_XDEV,
	})
	if err != nil {
		return instanceRecord{}, instanceAtomicError("open reset instance record", err)
	}
	defer func() {
		returnErr = appendInstanceOperationError(
			returnErr,
			"close reset instance record",
			directories.deps.close(context.WithoutCancel(ctx), fd),
		)
	}()
	if err := verifyInstanceRegularFile(
		ctx,
		directories.deps,
		fd,
		"reset instance record",
		directories.policy,
	); err != nil {
		return instanceRecord{}, err
	}
	var before, after, binding unix.Stat_t
	if err := unix.Fstat(fd, &before); err != nil {
		return instanceRecord{}, instanceAtomicError("inspect reset instance record", err)
	}
	raw, err := readAllInstanceRecord(ctx, directories.deps, fd)
	if err != nil {
		return instanceRecord{}, instanceAtomicError("read reset instance record", err)
	}
	record, err := decodeInstanceRecord(raw)
	if err != nil {
		return instanceRecord{}, err
	}
	if record.ownerUID != directories.uid || record.vmID != vmID {
		return instanceRecord{}, instanceAtomicError("reset instance record owner binding differs", nil)
	}
	if err := unix.Fstat(fd, &after); err != nil {
		return instanceRecord{}, instanceAtomicError("reinspect reset instance record", err)
	}
	if err := unix.Fstatat(directories.stateFD, vmID+".json", &binding, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return instanceRecord{}, instanceAtomicError("recheck reset instance record name", err)
	}
	if before.Dev != after.Dev || before.Ino != after.Ino || before.Mode != after.Mode || before.Uid != after.Uid ||
		before.Gid != after.Gid ||
		before.Nlink != after.Nlink ||
		before.Size != after.Size ||
		before.Mtim != after.Mtim ||
		before.Ctim != after.Ctim ||
		binding.Dev != after.Dev ||
		binding.Ino != after.Ino {
		return instanceRecord{}, instanceAtomicError("reset instance record changed during admission", nil)
	}
	return record, nil
}

// Release closes descriptor pins and drops the index lock exactly once.
// The owning administrator still holds its exclusive admission gate during release.
func (guard *HostResetGuard) Release(ctx context.Context) error {
	if guard == nil || guard.state == nil {
		return nil
	}
	state := guard.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released {
		return state.releaseErr
	}
	cleanupCtx := context.Background()
	if ctx != nil {
		cleanupCtx = context.WithoutCancel(ctx)
	}
	if state.roots != nil {
		state.releaseErr = appendInstanceOperationError(
			state.releaseErr,
			"close host reset resource pins",
			closeInstanceFDs(cleanupCtx, state.roots.deps, state.retained),
		)
		state.retained = nil
	}
	if state.index != nil {
		if state.executionIdentities != nil {
			state.releaseErr = appendInstanceOperationError(state.releaseErr, "release host reset execution identities",
				state.executionIdentities.Release(cleanupCtx))
			state.executionIdentities = nil
		}
		state.releaseErr = appendInstanceOperationError(
			state.releaseErr,
			"release host reset instance index",
			state.index.Release(cleanupCtx),
		)
		state.index = nil
	}
	if state.roots != nil {
		// A rejected acquire may precede opening the authority lock tree.
		if state.roots.authority == nil {
			state.releaseErr = appendInstanceOperationError(
				state.releaseErr,
				"close host reset roots",
				closeInstanceFDs(cleanupCtx, state.roots.deps, state.roots.retained),
			)
		} else {
			state.releaseErr = appendInstanceOperationError(
				state.releaseErr,
				"release host reset roots",
				state.roots.Release(cleanupCtx),
			)
		}
		state.roots = nil
	}
	state.released = true
	return state.releaseErr
}
