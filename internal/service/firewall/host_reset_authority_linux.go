package firewall

import (
	"context"
	"errors"

	"golang.org/x/sys/unix"

	"mvmctl/internal/infra"
)

type hostResetStoreAbsence struct {
	deps     generationRecordStoreDeps
	retained []int
	name     string
	device   uint64
	inode    uint64
}

// openHostResetRecordStore opens existing authority only: reset never manufactures missing metadata.
func openHostResetRecordStore(
	ctx context.Context,
) (_ *generationRecordStore, _ *hostResetStoreAbsence, returnErr error) {
	return openHostResetRecordStoreWithDeps(
		ctx, realGenerationRecordStoreDeps(), productionGenerationRecordStorePolicy(),
	)
}

func openHostResetRecordStoreWithDeps(
	ctx context.Context,
	deps generationRecordStoreDeps,
	policy generationRecordStorePolicy,
) (_ *generationRecordStore, _ *hostResetStoreAbsence, returnErr error) {
	names, err := generationRecordPathComponents(infra.FirewallGenerationRoot)
	if err != nil {
		return nil, nil, err
	}
	retained := make([]int, 0, len(names)+1)
	defer func() {
		if returnErr != nil {
			returnErr = appendGenerationCleanupError(
				returnErr,
				"close rejected firewall reset store",
				closeGenerationRecordFDs(context.WithoutCancel(ctx), deps, retained),
			)
		}
	}()
	rootFD, err := deps.open(ctx, policy.rootPath, generationRecordDirectoryFlags, 0)
	if err != nil {
		return nil, nil, generationAuthorityError("open firewall reset filesystem root", err, nil)
	}
	retained = append(retained, rootFD)
	if err := verifyGenerationRecordDirectory(ctx, deps, rootFD, "filesystem root", policy, false); err != nil {
		return nil, nil, err
	}
	parent := rootFD
	for index, name := range names {
		if err := ctx.Err(); err != nil {
			return nil, nil, generationAuthorityError("open firewall reset record store", err, nil)
		}
		resolve := uint64(generationRecordTraversalResolve)
		if index >= 3 {
			resolve = generationRecordManagedResolve
		}
		fd, err := deps.openAt2(
			ctx,
			parent,
			name,
			&unix.OpenHow{Flags: generationRecordDirectoryFlags, Resolve: resolve},
		)
		if errors.Is(err, unix.ENOENT) && index >= 2 {
			var stat unix.Stat_t
			if err := deps.fstat(ctx, parent, &stat); err != nil {
				return nil, nil, generationAuthorityError("inspect firewall reset absent-store ancestor", err, nil)
			}
			return nil, &hostResetStoreAbsence{
				deps:     deps,
				retained: retained,
				name:     name,
				device:   uint64(stat.Dev),
				inode:    stat.Ino,
			}, nil
		}
		if err != nil {
			return nil, nil, generationAuthorityError("open existing firewall reset directory "+name, err, nil)
		}
		retained = append(retained, fd)
		if err := verifyGenerationRecordDirectory(
			ctx, deps, fd, name, policy, generationRecordPathDirectoryClass(index) == generationRecordPrivateDirectory,
		); err != nil {
			return nil, nil, err
		}
		parent = fd
	}
	return &generationRecordStore{state: &generationRecordStoreState{
		deps: deps, policy: policy, directory: parent, retained: retained, active: true,
	}}, nil, nil
}

func (absence *hostResetStoreAbsence) recheck(ctx context.Context) (returnErr error) {
	store, current, err := openHostResetRecordStore(ctx)
	if err != nil {
		return err
	}
	defer func() {
		if store != nil {
			returnErr = appendGenerationCleanupError(
				returnErr,
				"close appeared firewall reset store",
				store.Release(context.WithoutCancel(ctx)),
			)
		}
		if current != nil {
			returnErr = appendGenerationCleanupError(
				returnErr,
				"close firewall reset absence readback",
				closeGenerationRecordFDs(context.WithoutCancel(ctx), current.deps, current.retained),
			)
		}
	}()
	if store != nil || current == nil || current.name != absence.name || current.device != absence.device ||
		current.inode != absence.inode {
		return generationAuthorityError("firewall reset absent-store authority changed", nil, nil)
	}
	return nil
}

func recheckHostResetStore(ctx context.Context, expected *generationRecordStore) (returnErr error) {
	store, absence, err := openHostResetRecordStore(ctx)
	if err != nil {
		return err
	}
	defer func() {
		if store != nil {
			returnErr = appendGenerationCleanupError(
				returnErr,
				"close firewall reset store readback",
				store.Release(context.WithoutCancel(ctx)),
			)
		}
		if absence != nil {
			returnErr = appendGenerationCleanupError(
				returnErr,
				"close missing firewall reset store readback",
				closeGenerationRecordFDs(context.WithoutCancel(ctx), absence.deps, absence.retained),
			)
		}
	}()
	if store == nil || absence != nil {
		return generationAuthorityError("firewall reset store disappeared during inspection", nil, nil)
	}
	var before, after unix.Stat_t
	if err := expected.state.deps.fstat(ctx, expected.state.directory, &before); err != nil {
		return generationAuthorityError("inspect retained firewall reset store", err, nil)
	}
	if err := store.state.deps.fstat(ctx, store.state.directory, &after); err != nil {
		return generationAuthorityError("inspect current firewall reset store", err, nil)
	}
	if before.Dev != after.Dev || before.Ino != after.Ino {
		return generationAuthorityError("firewall reset store was replaced during inspection", nil, nil)
	}
	return nil
}

func retainHostResetNamespace(ctx context.Context) (*kernelObservationNamespaceLease, error) {
	deps := realKernelObservationNamespaceDeps()
	fd, err := deps.open(ctx, kernelObservationCurrentNetworkNamespacePath, kernelObservationNamespaceOpenFlags, 0)
	if err != nil {
		return nil, preserveGenerationAuthorityError("open firewall reset host namespace", err)
	}
	identity, inspectErr := inspectKernelObservationNamespace(ctx, deps, fd)
	closeErr := deps.close(context.WithoutCancel(ctx), fd)
	if inspectErr != nil || closeErr != nil {
		return nil, appendGenerationCleanupError(inspectErr, "close firewall reset namespace observation", closeErr)
	}
	authority := newKernelObservationNamespaceAuthorityWithAllDeps(deps, realKernelApplicationNamespaceDeps(deps))
	return authority.retainExpectedNamespace(ctx, identity)
}
