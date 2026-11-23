package jailer

import (
	"context"
	"errors"
	"strconv"

	"golang.org/x/sys/unix"
)

// API and vsock never share the operator-writable console runtime directory.
const managedVMPrivateRuntimeMountPath = "/vmm-runtime"

func (lease *launchLease) preparePrivateRuntimeDirectory(ctx context.Context) error {
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released || !state.registrationComplete || state.record.lifecycle != instanceLifecycleLaunching ||
		state.privateRuntimeDirectory != nil {
		return instanceStateError(state.record.vmID, "private runtime requires durable launching authority")
	}
	runtime, err := openPrivateVMRuntime(ctx, state.record, true)
	state.privateRuntimeDirectory = runtime
	return err
}

// openPrivateVMRuntime admits only the fixed root-controlled hierarchy. Existing-only
// recovery never creates or repairs an entry; root-controlled absence is explicit.
func openPrivateVMRuntime(
	ctx context.Context,
	record instanceRecord,
	create bool,
) (_ *managedVMRuntimeLease, returnErr error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateInstanceRecord(record); err != nil {
		return nil, err
	}
	if record.launch == nil {
		return nil, instanceStateError(record.vmID, "private runtime execution binding is absent")
	}
	deps := realManagedVMRuntimeDeps()
	policy := productionManagedVMRuntimePolicy()
	retained := []int{}
	defer func() {
		if retained != nil {
			returnErr = appendInstanceOperationError(
				returnErr,
				"close rejected private runtime",
				closeManagedVMRuntimeFDs(context.WithoutCancel(ctx), deps, retained),
			)
		}
	}()
	rootFD, err := deps.open(ctx, policy.rootPath, managedVMRuntimeDirectoryFlags, 0)
	if err != nil {
		return nil, instanceAtomicError("open private runtime filesystem root", err)
	}
	retained = append(retained, rootFD)
	if _, err := inspectManagedVMRuntimeDirectory(ctx, deps, rootFD, "filesystem root", 0, 0, 0, false); err != nil {
		return nil, err
	}
	runFD, err := openExistingManagedVMRuntimeDirectory(ctx, deps, rootFD, "run", policy)
	if err != nil {
		return nil, err
	}
	retained = append(retained, runFD)
	mvmFD, present, err := openOptionalManagedVMRuntimeParent(ctx, deps, runFD, "mvmctl", policy)
	if err != nil {
		return nil, err
	}
	if !present {
		if !create {
			return nil, nil
		}
		mvmFD, err = openOrCreateManagedVMRuntimeParent(ctx, deps, runFD, "mvmctl", policy)
		if err != nil {
			return nil, err
		}
	}
	retained = append(retained, mvmFD)
	parent := mvmFD
	for _, name := range []string{"vmm-runtime", strconv.FormatUint(uint64(record.ownerUID), 10)} {
		fd, found, err := openPrivateVMRuntimeParent(ctx, deps, parent, name, create)
		if err != nil || !found {
			return nil, err
		}
		retained = append(retained, fd)
		parent = fd
	}
	if create {
		if err := deps.mkdirAt(ctx, parent, record.vmID, 0700); err != nil {
			return nil, instanceAtomicError("create exclusive private VM runtime", err)
		}
	}
	fd, err := deps.openAt2(
		ctx,
		parent,
		record.vmID,
		&unix.OpenHow{Flags: uint64(managedVMRuntimeDirectoryFlags), Resolve: uint64(managedVMRuntimeResolveManaged)},
	)
	if err != nil {
		if !create && errors.Is(err, unix.ENOENT) {
			return nil, nil
		}
		return nil, instanceAtomicError("pin private VM runtime", err)
	}
	retained = append(retained, fd)
	identity, err := inspectManagedVMRuntimeIdentity(ctx, deps, fd, "private VM runtime")
	if err != nil {
		return nil, err
	}
	execution := record.launch.executionIdentity
	state := &managedVMRuntimeLeaseState{
		deps:           deps,
		caller:         instanceCaller{uid: execution.uid, gid: execution.gid},
		vmID:           record.vmID,
		uidDirectoryFD: parent,
		vmDirectoryFD:  fd,
		identity:       identity,
		retained:       retained,
		leafCreated:    true,
		private:        true,
	}
	lease := &managedVMRuntimeLease{state: state}
	retained = nil
	if create {
		if err := prepareManagedVMRuntimeDirectory(ctx, state); err != nil {
			return lease, err
		}
	} else {
		if _, err := inspectManagedVMRuntimeDirectory(ctx, deps, fd, "private VM runtime", execution.uid, execution.gid, 0700, true); err != nil {
			return lease, err
		}
	}
	return lease, nil
}

func openPrivateVMRuntimeParent(
	ctx context.Context,
	deps managedVMRuntimeDeps,
	parent int,
	name string,
	create bool,
) (int, bool, error) {
	if err := ctx.Err(); err != nil {
		return -1, false, err
	}
	created := false
	if create {
		err := deps.mkdirAt(ctx, parent, name, 0700)
		if err != nil && !errors.Is(err, unix.EEXIST) {
			return -1, false, instanceAtomicError("create private runtime parent", err)
		}
		created = err == nil
	}
	fd, err := deps.openAt2(
		ctx,
		parent,
		name,
		&unix.OpenHow{Flags: uint64(managedVMRuntimeDirectoryFlags), Resolve: uint64(managedVMRuntimeResolveManaged)},
	)
	if errors.Is(err, unix.ENOENT) && !create {
		return -1, false, nil
	}
	if err != nil {
		return -1, false, instanceAtomicError("open private runtime parent", err)
	}
	if _, err := inspectManagedVMRuntimeDirectory(ctx, deps, fd, name, 0, 0, 0700, true); err != nil {
		return -1, false, appendInstanceOperationError(
			err,
			"close rejected private runtime parent",
			deps.close(context.WithoutCancel(ctx), fd),
		)
	}
	if created {
		if err := deps.fsync(ctx, parent); err != nil {
			return -1, false, errors.Join(err, deps.close(context.WithoutCancel(ctx), fd))
		}
	}
	return fd, true, nil
}
