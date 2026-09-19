package firewall

import (
	"bytes"
	"context"
	"encoding/json"
)

// retirePriorBootRecord retires old kernel authority only after observing an empty current namespace.
// CALLER MUST HOLD: the concrete network mutation lock and host-global firewall lock.
// CRITICAL: This path never applies an old generation or deletes a kernel object.
func (authority *productionGenerationNetworkAuthority) retirePriorBootRecord(
	ctx context.Context,
	store *generationRecordStore,
	binding generationRecordBinding,
	previous generationRecord,
	previousEvidence generationRecordFileEvidence,
) (generationRecord, generationRecordFileEvidence, error) {
	if err := validateGenerationRecord(previous); err != nil {
		return generationRecord{}, generationRecordFileEvidence{}, err
	}
	if previous.binding.bootID == binding.bootID {
		return generationRecord{}, generationRecordFileEvidence{}, generationAuthorityError(
			"same-boot firewall authority cannot be reset",
			nil,
			nil,
		)
	}
	if _, err := authority.firewallGenerationNamespace(ctx); err != nil {
		return generationRecord{}, generationRecordFileEvidence{}, err
	}
	empty, err := compileGeneration(generationIntent{})
	if err != nil {
		return generationRecord{}, generationRecordFileEvidence{}, err
	}
	if err := verifyExactCommittedGeneration(ctx, authority.kernel, empty); err != nil {
		return generationRecord{}, generationRecordFileEvidence{}, err
	}
	journal, raw, evidence, found, err := store.readIntent(ctx)
	if err != nil {
		return generationRecord{}, generationRecordFileEvidence{}, err
	}
	if !found {
		return generationRecord{}, generationRecordFileEvidence{}, generationAuthorityError(
			"prior-boot firewall intent journal is missing",
			nil,
			nil,
		)
	}
	currentNamespace := NamespaceIdentity{Device: binding.namespace.device, Inode: binding.namespace.inode}
	var prior priorBootIntent
	switch {
	case journal.BootID == previous.binding.bootID && journal.HostNamespace == (NamespaceIdentity{
		Device: previous.binding.namespace.device, Inode: previous.binding.namespace.inode,
	}):
		prior = priorBootIntent{
			Record: generationRecordToWire(previous),
			Before: journal.Before,
			Target: journal.Target,
		}
	case journal.PriorBoot != nil:
		prior = *journal.PriorBoot
		if !generationRecordsEqual(generationRecordFromWire(prior.Record), previous) {
			return generationRecord{}, generationRecordFileEvidence{}, generationAuthorityError(
				"prior-boot firewall transition record differs",
				nil,
				nil,
			)
		}
	default:
		return generationRecord{}, generationRecordFileEvidence{}, generationAuthorityError(
			"prior-boot firewall intent binding differs",
			nil,
			nil,
		)
	}
	logical, err := selectPriorBootIntent(previous, prior.Before, prior.Target)
	if err != nil {
		return generationRecord{}, generationRecordFileEvidence{}, err
	}
	dormant := cloneIntent(logical)
	dormant.Endpoints = nil
	dormant.ActiveNetworks = nil
	projection, err := dormant.compile()
	if err != nil {
		return generationRecord{}, generationRecordFileEvidence{}, err
	}
	if generationRecordIdentityFromCompiled(projection) != generationRecordIdentityFromCompiled(empty) {
		return generationRecord{}, generationRecordFileEvidence{}, generationAuthorityError(
			"prior-boot firewall intent retains active kernel projection",
			nil,
			nil,
		)
	}
	reset := intentJournal{
		SchemaVersion: 1, BootID: binding.bootID, HostNamespace: currentNamespace,
		Before: dormant, Target: dormant, PriorBoot: &prior,
	}
	if journal.BootID != previous.binding.bootID {
		journalBinding := generationRecordBinding{bootID: journal.BootID, namespace: generationNamespaceIdentity{
			device: journal.HostNamespace.Device, inode: journal.HostNamespace.Inode,
		}}
		if err := validateGenerationRecordBinding(journalBinding); err != nil {
			return generationRecord{}, generationRecordFileEvidence{}, err
		}
		expectedReset := reset
		expectedReset.BootID = journal.BootID
		expectedReset.HostNamespace = journal.HostNamespace
		canonical, encodeErr := json.Marshal(expectedReset)
		if encodeErr != nil || !bytes.Equal(raw, canonical) {
			return generationRecord{}, generationRecordFileEvidence{}, generationAuthorityError(
				"prior-boot firewall transition intent differs",
				encodeErr,
				nil,
			)
		}
	}
	if journal.BootID != binding.bootID || journal.HostNamespace != currentNamespace {
		if _, _, err := store.writeIntent(ctx, raw, evidence, reset); err != nil {
			return generationRecord{}, generationRecordFileEvidence{}, err
		}
	}
	// CRITICAL: Recheck both tables immediately before rebinding the durable record to this boot.
	// A crash after the journal rename resumes only from its exact old-record transition evidence.
	if err := verifyExactCommittedGeneration(ctx, authority.kernel, empty); err != nil {
		return generationRecord{}, generationRecordFileEvidence{}, err
	}
	current, err := newCommittedGenerationRecord(binding, generationRecordIdentityFromCompiled(empty))
	if err != nil {
		return generationRecord{}, generationRecordFileEvidence{}, err
	}
	proof, err := store.replace(ctx, previous, previousEvidence, current)
	if err != nil {
		return generationRecord{}, proof, err
	}
	return current, proof, nil
}

func selectPriorBootIntent(record generationRecord, beforeIntent, targetIntent Intent) (Intent, error) {
	before, err := beforeIntent.compile()
	if err != nil {
		return Intent{}, err
	}
	target, err := targetIntent.compile()
	if err != nil {
		return Intent{}, err
	}
	beforeIdentity := generationRecordIdentityFromCompiled(before)
	targetIdentity := generationRecordIdentityFromCompiled(target)
	switch record.lifecycle {
	case generationRecordCommitted:
		if record.committed == targetIdentity {
			return targetIntent, nil
		}
		if record.committed == beforeIdentity {
			return beforeIntent, nil
		}
	case generationRecordApplying:
		if record.before != nil && record.target != nil && *record.before == beforeIdentity &&
			*record.target == targetIdentity {
			// The root applying record authorizes this desired transition. Reboot retires only its
			// kernel projection; the logical target remains the durable reconciliation intent.
			return targetIntent, nil
		}
	}
	return Intent{}, generationAuthorityError(
		"prior-boot firewall intent does not match generation authority",
		nil,
		nil,
	)
}
