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
) (model.VMLabels, uint64, error) {
	if err := validateVMLabelIdentity(ownerUID, vmID); err != nil {
		return nil, 0, err
	}
	var labels model.VMLabels
	var revision uint64
	err := withCommittedLabelIntent(ctx, func(intent Intent) error {
		var err error
		labels, revision, err = readVMLabels(intent, ownerUID, vmID)
		return err
	})
	return labels, revision, err
}

func withCommittedLabelIntent(ctx context.Context, read func(Intent) error) (returnErr error) {
	global, err := authorityfs.OpenFirewall(ctx)
	if err != nil {
		return preserveGenerationAuthorityError("open VM label read authority", err)
	}
	cleanup := context.WithoutCancel(ctx)
	defer func() { returnErr = errors.Join(returnErr, global.Release(cleanup)) }()
	lock, err := global.Acquire(ctx)
	if err != nil {
		return preserveGenerationAuthorityError("lock VM label read authority", err)
	}
	defer func() { returnErr = errors.Join(returnErr, lock.Release(cleanup)) }()
	store, absence, err := openHostResetRecordStore(ctx)
	if err != nil {
		return err
	}
	if absence != nil {
		defer func() {
			returnErr = errors.Join(returnErr, closeGenerationRecordFDs(cleanup, absence.deps, absence.retained))
		}()
		return read(Intent{})
	}
	defer func() { returnErr = errors.Join(returnErr, store.Release(cleanup)) }()
	intent, err := readCommittedLabelIntent(ctx, store)
	if err != nil {
		return err
	}
	return read(intent)
}

func readCommittedLabelIntent(ctx context.Context, store *generationRecordStore) (Intent, error) {
	record, _, recordFound, err := store.read(ctx)
	if err != nil {
		return Intent{}, err
	}
	journal, _, _, intentFound, err := store.readIntent(ctx)
	if err != nil {
		return Intent{}, err
	}
	// With just the generation record, only first initialization can represent
	// a legitimately empty intent. A nonempty generation requires both records.
	if !intentFound {
		if recordFound {
			empty, err := compileGeneration(generationIntent{})
			if err != nil {
				return Intent{}, err
			}
			if record.lifecycle != generationRecordCommitted || record.committed != generationRecordIdentityFromCompiled(empty) {
				return Intent{}, invalidGeneration("VM label read requires committed firewall intent")
			}
		}
		return Intent{}, nil
	}
	namespace := NamespaceIdentity{Device: record.binding.namespace.device, Inode: record.binding.namespace.inode}
	if !recordFound || record.lifecycle != generationRecordCommitted ||
		journal.BootID != record.binding.bootID || journal.HostNamespace != namespace {
		return Intent{}, invalidGeneration("VM label read requires a committed firewall journal binding")
	}
	return selectPriorBootIntent(record, journal.Before, journal.Target)
}

func readVMLabels(intent Intent, ownerUID uint32, vmID string) (model.VMLabels, uint64, error) {
	labels, found := intent.VMLabels[vmID]
	if !found {
		return make(model.VMLabels), 0, nil
	}
	if labels.OwnerUID != ownerUID || labels.Retired {
		return nil, 0, invalidGeneration("VM label identity is foreign or retired")
	}
	return cloneVMLabels(labels.Labels), labels.Revision, nil
}
