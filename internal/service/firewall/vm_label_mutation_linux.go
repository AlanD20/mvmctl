package firewall

import (
	"context"
	"errors"

	"mvmctl/internal/lib/authorityfs"
	"mvmctl/internal/lib/model"
)

// UpdateVMLabels publishes one already-admitted VM's metadata under the firewall lock.
// Its caller retains protected backing authority through the returned journal revision.
// The metadata path does not install rules or resume an interrupted kernel transaction.
func UpdateVMLabels(
	ctx context.Context, ownerUID uint32, vmID string, expected uint64, labels model.VMLabels,
) (uint64, error) {
	if labels == nil {
		return 0, invalidGeneration("VM label update requires an explicit map")
	}
	if err := labels.Validate(); err != nil {
		return 0, err
	}
	return mutateVMLabelMetadata(ctx, ownerUID, vmID, vmLabelMetadataChange{expected: expected, labels: labels})
}

// RetireVMLabels records removal before protected backing is destroyed. Retrying removal
// preserves the same tombstone, including when no labels were ever enrolled.
func RetireVMLabels(ctx context.Context, ownerUID uint32, vmID string) error {
	_, err := mutateVMLabelMetadata(ctx, ownerUID, vmID, vmLabelMetadataChange{retire: true})
	return err
}

type vmLabelMetadataChange struct {
	expected uint64
	labels   model.VMLabels
	retire   bool
}

func mutateVMLabelMetadata(
	ctx context.Context, ownerUID uint32, vmID string, change vmLabelMetadataChange,
) (_ uint64, returnErr error) {
	if err := validateVMLabelIdentity(ownerUID, vmID); err != nil {
		return 0, err
	}
	global, err := authorityfs.OpenFirewall(ctx)
	if err != nil {
		return 0, preserveGenerationAuthorityError("open VM label mutation authority", err)
	}
	cleanup := context.WithoutCancel(ctx)
	defer func() { returnErr = errors.Join(returnErr, global.Release(cleanup)) }()
	lock, err := global.Acquire(ctx)
	if err != nil {
		return 0, preserveGenerationAuthorityError("lock VM label mutation authority", err)
	}
	defer func() { returnErr = errors.Join(returnErr, lock.Release(cleanup)) }()
	kernel, err := retainHostResetNamespace(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { returnErr = errors.Join(returnErr, kernel.Release(cleanup)) }()
	identity := kernel.state.identity
	if _, err := verifyPinnedGenerationNamespace(ctx, kernel, identity); err != nil {
		return 0, err
	}
	store, err := openGenerationRecordStore(ctx, realGenerationRecordStoreDeps(), productionGenerationRecordStorePolicy())
	if err != nil {
		return 0, err
	}
	defer func() { returnErr = errors.Join(returnErr, store.Release(cleanup)) }()
	bootID, err := readSystemBootID(ctx)
	if err != nil {
		return 0, err
	}
	binding := generationRecordBinding{bootID: bootID, namespace: identity}
	record, recordEvidence, found, err := store.read(ctx)
	if err != nil {
		return 0, err
	}
	switch {
	case !found:
		_, _, _, intentFound, err := store.readIntent(ctx)
		if err != nil {
			return 0, err
		}
		if intentFound {
			return 0, invalidGeneration("firewall intent exists without a generation record")
		}
		record, _, err = initializeEmptyGeneration(ctx, kernel, store, binding)
		if err != nil {
			return 0, err
		}
	case record.binding.bootID != binding.bootID:
		record, _, err = retirePriorBootGeneration(ctx, kernel, store, binding, record, recordEvidence)
		if err != nil {
			return 0, err
		}
	}
	return publishVMLabelMetadata(ctx, store, kernel, record, binding, ownerUID, vmID, change)
}

func publishVMLabelMetadata(
	ctx context.Context, store *generationRecordStore, kernel generationKernelApplication,
	record generationRecord, binding generationRecordBinding, ownerUID uint32, vmID string,
	change vmLabelMetadataChange,
) (uint64, error) {
	if record.binding != binding || record.lifecycle != generationRecordCommitted {
		return 0, invalidGeneration("firewall generation is unavailable for label mutation")
	}
	journal, raw, evidence, found, err := store.readIntent(ctx)
	if err != nil {
		return 0, err
	}
	namespace := NamespaceIdentity{Device: binding.namespace.device, Inode: binding.namespace.inode}
	before := Intent{}
	if found {
		if journal.BootID != binding.bootID || journal.HostNamespace != namespace {
			return 0, invalidGeneration("VM label journal differs from current firewall binding")
		}
		before, err = selectPriorBootIntent(record, journal.Before, journal.Target)
		if err != nil {
			return 0, err
		}
	}
	compiled, err := before.compile()
	if err != nil {
		return 0, err
	}
	if generationRecordIdentityFromCompiled(compiled) != record.committed {
		return 0, invalidGeneration("VM label intent differs from committed firewall generation")
	}
	target := cloneIntent(before)
	if target.VMLabels == nil {
		target.VMLabels = make(vmLabelRecords)
	}
	next, err := change.apply(target.VMLabels, ownerUID, vmID)
	if err != nil {
		return 0, err
	}
	projection, err := target.compile()
	if err != nil {
		return 0, err
	}
	if generationRecordIdentityFromCompiled(projection) != record.committed {
		return 0, invalidGeneration("VM label change requires a kernel policy transaction")
	}
	if err := verifyExactCommittedGeneration(ctx, kernel, compiled); err != nil {
		return 0, err
	}
	// Both sides of this record describe the same committed kernel generation. The
	// verified journal replacement is the commit point for the metadata revision.
	_, _, err = store.writeIntent(ctx, raw, evidence, intentJournal{
		SchemaVersion: intentJournalSchema, BootID: binding.bootID, HostNamespace: namespace,
		Before: target, Target: target,
	})
	if err != nil {
		return 0, err
	}
	return next.Revision, nil
}

func (change vmLabelMetadataChange) apply(records vmLabelRecords, ownerUID uint32, vmID string) (vmLabelRecord, error) {
	if !change.retire {
		if change.expected == 0 {
			return records.enroll(ownerUID, vmID, change.labels)
		}
		return records.replace(ownerUID, vmID, change.expected, change.labels)
	}
	record, found := records[vmID]
	if !found {
		record = vmLabelRecord{OwnerUID: ownerUID, Revision: 1, Labels: make(model.VMLabels), Retired: true}
		records[vmID] = record
		return record, nil
	}
	if record.OwnerUID != ownerUID {
		return vmLabelRecord{}, invalidGeneration("VM label retirement owner differs")
	}
	if !record.Retired {
		if err := records.retire(ownerUID, vmID, record.Revision); err != nil {
			return vmLabelRecord{}, err
		}
	}
	return records[vmID], nil
}
