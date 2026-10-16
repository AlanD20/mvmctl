package network

import (
	"context"

	"mvmctl/internal/lib/authorityfs"
	"mvmctl/pkg/errs"
)

type networkAuthority struct {
	storeDeps   networkStoreDeps
	storePolicy networkStorePolicy
	lockTree    networkAuthorityTree
}

func newNetworkAuthority() *networkAuthority {
	return newNetworkAuthorityWithPolicy(
		realNetworkStoreDeps(),
		productionNetworkStorePolicy(),
		systemNetworkAuthorityTree{},
	)
}

func newNetworkAuthorityWithPolicy(
	storeDeps networkStoreDeps,
	storePolicy networkStorePolicy,
	lockTree networkAuthorityTree,
) *networkAuthority {
	if lockTree == nil {
		panic("network lock authority is required")
	}
	return &networkAuthority{storeDeps: storeDeps, storePolicy: storePolicy, lockTree: lockTree}
}

// registerManagedNetwork persists one exact owner- and boot-bound bridge identity. It remains private until the real
// Jailer VM-lock capability can be transferred into the later topology transaction.
func (authority *networkAuthority) registerManagedNetwork(
	ctx context.Context,
	caller networkCaller,
	networkID NetworkID,
	bridge linkIdentity,
) (registered networkAuthorityRecord, returnErr error) {
	if authority == nil {
		return networkAuthorityRecord{}, networkAuthorityCorruptError("network authority is unavailable", nil)
	}
	if err := validateNetworkRegistrationInput(caller, networkID, bridge); err != nil {
		return networkAuthorityRecord{}, err
	}
	bootID, err := authority.storeDeps.currentBootID(ctx)
	if err != nil {
		return networkAuthorityRecord{}, networkAuthorityEffectError("observe current host boot identity", err)
	}
	record, err := newNetworkAuthorityRecord(caller, networkID, bootID, bridge)
	if err != nil {
		return networkAuthorityRecord{}, err
	}

	var lockRoot networkLockRoot
	var indexLock networkAuthorityLock
	var storeRoot *networkStoreRoot
	var storeUID *networkStoreUID
	var lockUID networkLockUID
	var networkLock networkAuthorityLock
	defer func() {
		cleanupCtx := context.WithoutCancel(ctx)
		// CRITICAL: Registration cleanup releases the per-network lock and both UID capabilities before the global index.
		// This is the reverse of acquisition and cannot be replaced by a generic resource stack with caller-shaped order.
		if networkLock != nil {
			returnErr = appendNetworkAuthorityError(
				returnErr,
				"release managed-network registration lock",
				networkLock.Release(cleanupCtx),
			)
		}
		if lockUID != nil {
			returnErr = appendNetworkAuthorityError(
				returnErr,
				"release network lock owner",
				lockUID.Release(cleanupCtx),
			)
		}
		if storeUID != nil {
			returnErr = appendNetworkAuthorityError(
				returnErr,
				"release network record owner",
				storeUID.Release(cleanupCtx),
			)
		}
		if storeRoot != nil {
			returnErr = appendNetworkAuthorityError(
				returnErr,
				"release network record root",
				storeRoot.Release(cleanupCtx),
			)
		}
		if indexLock != nil {
			returnErr = appendNetworkAuthorityError(
				returnErr,
				"release network ownership-index lock",
				indexLock.Release(cleanupCtx),
			)
		}
		if lockRoot != nil {
			returnErr = appendNetworkAuthorityError(
				returnErr,
				"release network lock root",
				lockRoot.Release(cleanupCtx),
			)
		}
	}()

	lockRoot, err = authority.lockTree.openNetwork(ctx)
	if err != nil {
		return networkAuthorityRecord{}, err
	}
	indexLock, err = lockRoot.acquireIndex(ctx)
	if err != nil {
		return networkAuthorityRecord{}, err
	}
	storeRoot, err = openNetworkStoreRoot(ctx, authority.storeDeps, authority.storePolicy)
	if err != nil {
		return networkAuthorityRecord{}, err
	}
	claim, err := storeRoot.findClaim(ctx, networkID)
	if err != nil {
		return networkAuthorityRecord{}, err
	}
	if claim != nil && claim.ownerUID != caller.uid {
		return networkAuthorityRecord{}, unavailableNetworkAuthorityError(networkID)
	}
	storeUID, err = storeRoot.openUID(ctx, caller.uid, true)
	if err != nil {
		return networkAuthorityRecord{}, err
	}
	lockUID, err = lockRoot.openUID(ctx, caller.uid)
	if err != nil {
		return networkAuthorityRecord{}, err
	}
	key, err := authorityfs.NewNetworkKey(string(networkID))
	if err != nil {
		return networkAuthorityRecord{}, networkAuthorityValidationError(err.Error())
	}
	networkLock, err = lockUID.acquireNetwork(ctx, key)
	if err != nil {
		return networkAuthorityRecord{}, err
	}
	existing, found, err := storeUID.readRecord(ctx, networkID)
	if err != nil {
		return networkAuthorityRecord{}, err
	}
	if found {
		if err := authorizeNetworkOwner(caller, existing); err != nil {
			return networkAuthorityRecord{}, err
		}
		if existing.bootID != bootID {
			return networkAuthorityRecord{}, staleNetworkAuthorityError(networkID)
		}
		if existing.managedBridge != bridge {
			return networkAuthorityRecord{}, errs.New(
				errs.CodeNetworkAlreadyExists,
				"network authority already exists with a different managed-bridge identity",
				errs.WithClass(errs.ClassConflict),
				errs.WithEntity(string(networkID)),
			)
		}
		return existing, nil
	}
	if claim != nil {
		return networkAuthorityRecord{}, networkAuthorityCorruptError(
			"network authority identity disappeared while registration locks were held",
			nil,
		)
	}
	if err := storeUID.writeRecord(ctx, record); err != nil {
		domainErr := errs.AsDomainError(err)
		if domainErr != nil && domainErr.Details["record_created"] == true {
			return record, err
		}
		return networkAuthorityRecord{}, err
	}
	return record, nil
}
