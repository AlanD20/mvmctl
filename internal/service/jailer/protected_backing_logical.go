package jailer

import (
	"context"
	"errors"

	"golang.org/x/sys/unix"
)

type protectedLogicalRecord struct {
	Version     uint32                `json:"version"`
	Owner       uint32                `json:"owner"`
	Kind        string                `json:"kind"`
	ID          string                `json:"id"`
	Cache       protectedBackingCache `json:"cache"`
	Initialized bool                  `json:"initialized"`
	// Kernel is required only by fresh kernel schema 2; mutable bindings retain schema 1.
	Kernel *protectedKernelLineage `json:"kernel,omitempty"`
}

// openProtectedLogical serializes one logical identity globally for the operator, including
// absence. Selecting a different cache can never create a disconnected second backing object.
func openProtectedLogical(
	ctx context.Context, ownerUID uint32, identity managedCacheIdentity, kind, id string, create bool,
) (_ int, _ []int, returnErr error) {
	anchor, retained, err := openProtectedAnchor(ctx, ownerUID, identity, create)
	if err != nil {
		return -1, nil, err
	}
	defer func() {
		if returnErr != nil {
			returnErr = errors.Join(returnErr, closeProtectedFDs(retained))
		}
	}()
	// openProtectedAnchor retains the fixed UID directory immediately before its cache child.
	_, logical, err := lockProtectedLogicalInAnchor(
		ctx,
		ownerUID,
		identity,
		kind,
		id,
		create,
		retained[len(retained)-2],
	)
	if err != nil {
		return -1, nil, err
	}
	retained = append(retained, logical...)
	return anchor, retained, nil
}

func lockProtectedLogicalInAnchor(
	ctx context.Context, ownerUID uint32, identity managedCacheIdentity, kind, id string, create bool, parent int,
) (_ int, _ []int, returnErr error) {
	fd, retained, _, err := tryLockProtectedLogicalInAnchor(ctx, ownerUID, identity, kind, id, create, parent)
	return fd, retained, err
}

func tryLockProtectedLogicalInAnchor(
	ctx context.Context, ownerUID uint32, identity managedCacheIdentity, kind, id string, create bool, parent int,
) (_ int, _ []int, contended bool, returnErr error) {
	var retained []int
	defer func() {
		if returnErr != nil {
			closeErr := closeProtectedFDs(retained)
			contended = contended && closeErr == nil
			returnErr = errors.Join(returnErr, closeErr)
		}
	}()
	for _, component := range []string{"logical", kind, id} {
		fd, err := openProtectedDirectory(ctx, parent, component, create, true)
		if err != nil {
			return -1, nil, false, err
		}
		retained = append(retained, fd)
		parent = fd
	}
	lock, err := acquireProtectedLockMode(ctx, parent, create)
	if err != nil {
		return -1, nil, restoreProtectedLockContention(err), err
	}
	retained = append(retained, lock)
	wanted := protectedLogicalRecord{
		Version: 1, Owner: ownerUID, Kind: kind, ID: id, Cache: protectedCacheIdentity(identity),
	}
	if kind == protectedBackingKernel {
		wanted.Version = protectedKernelLogicalVersion
		wanted.Kernel = &protectedKernelLineage{}
	}
	var record protectedLogicalRecord
	err = readProtectedRecord(ctx, parent, "logical.json", &record)
	if errors.Is(err, unix.ENOENT) && create {
		if err := writeProtectedRecord(ctx, parent, "logical.json", wanted); err != nil {
			return -1, nil, false, err
		}
	} else if err != nil {
		return -1, nil, false, err
	} else if err := validateProtectedLogical(record, ownerUID, kind, id); err != nil {
		return -1, nil, false, err
	} else if kind != protectedBackingKernel && record.Cache != wanted.Cache {
		return -1, nil, false, managedVMResourceValidationError("logical backing identity is already bound to another cache")
	}
	return parent, retained, false, nil
}

func markProtectedLogicalInitialized(ctx context.Context, directory int) error {
	var record protectedLogicalRecord
	if err := readProtectedRecord(ctx, directory, "logical.json", &record); err != nil {
		return err
	}
	if record.Initialized {
		return nil
	}
	record.Initialized = true
	return writeProtectedRecord(ctx, directory, "logical.json", record)
}
