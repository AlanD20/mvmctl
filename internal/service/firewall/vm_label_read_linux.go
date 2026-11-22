package firewall

import (
	"context"
	"errors"

	"mvmctl/internal/lib/authorityfs"
	"mvmctl/internal/lib/model"
)

// ReadVMLabels reads logical state for an already admitted VM. A zero revision
// denotes an unlabelled identity that has never been enrolled.
func ReadVMLabels(
	ctx context.Context, ownerUID uint32, vmID string,
) (_ model.VMLabels, _ uint64, returnErr error) {
	if err := validateVMLabelIdentity(ownerUID, vmID); err != nil {
		return nil, 0, err
	}
	global, err := authorityfs.OpenFirewall(ctx)
	if err != nil {
		return nil, 0, preserveGenerationAuthorityError("open VM label read authority", err)
	}
	cleanup := context.WithoutCancel(ctx)
	defer func() { returnErr = errors.Join(returnErr, global.Release(cleanup)) }()
	lock, err := global.Acquire(ctx)
	if err != nil {
		return nil, 0, preserveGenerationAuthorityError("lock VM label read authority", err)
	}
	defer func() { returnErr = errors.Join(returnErr, lock.Release(cleanup)) }()
	store, absence, err := openHostResetRecordStore(ctx)
	if err != nil {
		return nil, 0, err
	}
	if absence != nil {
		defer func() {
			returnErr = errors.Join(returnErr, closeGenerationRecordFDs(cleanup, absence.deps, absence.retained))
		}()
		return make(model.VMLabels), 0, nil
	}
	defer func() { returnErr = errors.Join(returnErr, store.Release(cleanup)) }()
	return readCommittedVMLabels(ctx, store, ownerUID, vmID)
}

func readCommittedVMLabels(
	ctx context.Context, store *generationRecordStore, ownerUID uint32, vmID string,
) (model.VMLabels, uint64, error) {
	record, _, recordFound, err := store.read(ctx)
	if err != nil {
		return nil, 0, err
	}
	journal, _, _, intentFound, err := store.readIntent(ctx)
	if err != nil {
		return nil, 0, err
	}
	// With just the generation record, only first initialization can represent
	// a legitimately empty intent. A nonempty generation requires both records.
	if !intentFound {
		if recordFound {
			empty, err := compileGeneration(generationIntent{})
			if err != nil {
				return nil, 0, err
			}
			if record.lifecycle != generationRecordCommitted || record.committed != generationRecordIdentityFromCompiled(empty) {
				return nil, 0, invalidGeneration("VM label read requires committed firewall intent")
			}
		}
		return make(model.VMLabels), 0, nil
	}
	namespace := NamespaceIdentity{Device: record.binding.namespace.device, Inode: record.binding.namespace.inode}
	if !recordFound || record.lifecycle != generationRecordCommitted ||
		journal.BootID != record.binding.bootID || journal.HostNamespace != namespace {
		return nil, 0, invalidGeneration("VM label read requires a committed firewall journal binding")
	}
	intent, err := selectPriorBootIntent(record, journal.Before, journal.Target)
	if err != nil {
		return nil, 0, err
	}
	labels, found := intent.VMLabels[vmID]
	if !found {
		return make(model.VMLabels), 0, nil
	}
	if labels.OwnerUID != ownerUID || labels.Retired {
		return nil, 0, invalidGeneration("VM label identity is foreign or retired")
	}
	return cloneVMLabels(labels.Labels), labels.Revision, nil
}
