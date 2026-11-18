package firewall

import (
	"context"
)

// validateLegacyTerminalIntent admits only the observed pre-lifetime terminal grammar. Retained network
// metadata may precede an empty target; no old floating ownership or active graph is interpreted as current intent.
func validateLegacyTerminalIntent(journal intentJournal) error {
	historyInventory := journal.Before
	historyInventory.Networks = nil
	if journal.SchemaVersion != 1 || journal.PriorBoot != nil || !hostResetIntentEmpty(journal.Target) ||
		!hostResetIntentEmpty(historyInventory) {
		return generationAuthorityError("legacy firewall intent is not exact terminal empty history", nil, nil)
	}
	history, err := journal.Before.compile()
	if err != nil {
		return err
	}
	empty, err := compileGeneration(generationIntent{})
	if err != nil {
		return err
	}
	if generationRecordIdentityFromCompiled(history) != generationRecordIdentityFromCompiled(empty) {
		return generationAuthorityError("legacy firewall history retains an active projection", nil, nil)
	}
	return nil
}

// admitLegacyTerminalIntent treats old metadata solely as terminal history bound to committed empty authority.
// Current boot/namespace and fresh kernel absence remain the retaining consumer's responsibility.
func admitLegacyTerminalIntent(record generationRecord, journal intentJournal) error {
	if err := validateLegacyTerminalIntent(journal); err != nil {
		return err
	}
	if err := validateGenerationRecord(record); err != nil {
		return err
	}
	empty, err := compileGeneration(generationIntent{})
	if err != nil {
		return err
	}
	if record.lifecycle != generationRecordCommitted ||
		record.committed != generationRecordIdentityFromCompiled(empty) ||
		journal.BootID != record.binding.bootID ||
		journal.HostNamespace != (NamespaceIdentity{
			Device: record.binding.namespace.device, Inode: record.binding.namespace.inode,
		}) {
		return generationAuthorityError(
			"legacy firewall terminal history differs from committed empty authority",
			nil,
			nil,
		)
	}
	return nil
}

// publishLegacyTerminalIntent republishes only an already empty terminal journal in the explicit lifetime schema.
// CALLER MUST HOLD: retained network and global firewall mutation authority. No kernel mutation is performed.
func publishLegacyTerminalIntent(
	ctx context.Context,
	store *generationRecordStore,
	kernel generationKernelApplication,
	record generationRecord,
	journal intentJournal,
	raw []byte,
	evidence generationRecordFileEvidence,
) (intentJournal, []byte, generationRecordFileEvidence, error) {
	if err := admitLegacyTerminalIntent(record, journal); err != nil {
		return journal, raw, evidence, err
	}
	current, recordEvidence, found, err := store.read(ctx)
	if err != nil || !found || !generationRecordsEqual(current, record) {
		return journal, raw, evidence, generationAuthorityError(
			"legacy terminal generation changed before observation",
			err,
			nil,
		)
	}
	empty, err := compileGeneration(generationIntent{})
	if err != nil {
		return journal, raw, evidence, err
	}
	if err := verifyExactCommittedGeneration(ctx, kernel, empty); err != nil {
		return journal, raw, evidence, err
	}
	current, currentEvidence, found, err := store.read(ctx)
	if err != nil || !found || currentEvidence != recordEvidence || !generationRecordsEqual(current, record) {
		return journal, raw, evidence, generationAuthorityError(
			"legacy terminal generation changed during observation",
			err,
			nil,
		)
	}
	next := journal
	next.SchemaVersion = intentJournalSchema
	// writeIntent reopens and compares the exact old bytes and file evidence before replacing them.
	updated, proof, err := store.writeIntent(ctx, raw, evidence, next)
	if err != nil {
		return journal, raw, evidence, err
	}
	return next, updated, proof, nil
}
