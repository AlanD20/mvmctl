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
	parent := retained[len(retained)-2]
	for _, component := range []string{"logical", kind, id} {
		fd, err := openProtectedDirectory(ctx, parent, component, create, true)
		if err != nil {
			return -1, nil, err
		}
		retained = append(retained, fd)
		parent = fd
	}
	lock, err := acquireProtectedLock(ctx, parent)
	if err != nil {
		return -1, nil, err
	}
	retained = append(retained, lock)
	wanted := protectedLogicalRecord{
		Version: 1, Owner: ownerUID, Kind: kind, ID: id, Cache: protectedCacheIdentity(identity),
	}
	var record protectedLogicalRecord
	err = readProtectedRecord(ctx, parent, "logical.json", &record)
	if errors.Is(err, unix.ENOENT) && create {
		if err := writeProtectedRecord(ctx, parent, "logical.json", wanted); err != nil {
			return -1, nil, err
		}
	} else if err != nil {
		return -1, nil, err
	} else if record.Version != wanted.Version || record.Owner != wanted.Owner || record.Kind != wanted.Kind ||
		record.ID != wanted.ID || record.Cache != wanted.Cache {
		return -1, nil, managedVMResourceValidationError("logical backing identity is already bound to another cache")
	}
	return anchor, retained, nil
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
