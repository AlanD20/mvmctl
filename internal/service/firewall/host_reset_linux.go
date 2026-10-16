package firewall

import (
	"bytes"
	"context"
	"sync"

	"mvmctl/internal/lib/authorityfs"
)

// HostResetGuard retains firewall serialization after proving root intent and both kernel tables empty.
// THREAD-SAFE: copied guards share their retained resources and cached close-once result.
type HostResetGuard struct{ state *hostResetGuardState }

type hostResetGuardState struct {
	// mu guards release of the exact descriptors and mutation lock through host reset.
	mu         sync.Mutex
	global     *authorityfs.Firewall
	lock       *authorityfs.Lock
	store      *generationRecordStore
	absence    *hostResetStoreAbsence
	kernel     *kernelObservationNamespaceLease
	released   bool
	releaseErr error
}

// AcquireHostResetGuard proves firewall reset readiness without applying, adopting, or deleting state.
// CALLER MUST HOLD: exclusive host admission, Jailer index, then network index, through Release.
// CRITICAL: Only committed empty current-boot state or fully absent metadata, plus observed table
// absence, qualifies. Partial metadata never supplies reset authority.
func AcquireHostResetGuard(ctx context.Context) (_ *HostResetGuard, returnErr error) {
	if ctx == nil {
		return nil, generationAuthorityError("firewall host reset context is required", nil, nil)
	}
	if err := ctx.Err(); err != nil {
		return nil, generationAuthorityError("acquire firewall host reset guard", err, nil)
	}
	state := &hostResetGuardState{}
	guard := &HostResetGuard{state: state}
	defer func() {
		if returnErr != nil {
			returnErr = appendGenerationCleanupError(
				returnErr,
				"release rejected firewall reset guard",
				guard.Release(context.WithoutCancel(ctx)),
			)
		}
	}()
	var err error
	state.global, err = authorityfs.OpenFirewall(ctx)
	if err != nil {
		return nil, preserveGenerationAuthorityError("open firewall host reset authority", err)
	}
	state.lock, err = state.global.Acquire(ctx)
	if err != nil {
		return nil, preserveGenerationAuthorityError("lock firewall host reset authority", err)
	}
	state.store, state.absence, err = openHostResetRecordStore(ctx)
	if err != nil {
		return nil, err
	}
	state.kernel, err = retainHostResetNamespace(ctx)
	if err != nil {
		return nil, err
	}
	bootID, err := readSystemBootID(ctx)
	if err != nil {
		return nil, preserveGenerationAuthorityError("read firewall reset host boot", err)
	}
	identity := state.kernel.state.identity
	empty, err := compileGeneration(generationIntent{})
	if err != nil {
		return nil, err
	}
	if state.absence != nil {
		if err := verifyExactCommittedGeneration(ctx, state.kernel, empty); err != nil {
			return nil, err
		}
		if err := state.absence.recheck(ctx); err != nil {
			return nil, err
		}
		return guard, nil
	}
	record, recordEvidence, recordFound, err := state.store.read(ctx)
	if err != nil {
		return nil, err
	}
	journal, journalRaw, journalEvidence, intentFound, err := state.store.readIntent(ctx)
	if err != nil {
		return nil, err
	}
	if !recordFound && !intentFound {
		if err := verifyExactCommittedGeneration(ctx, state.kernel, empty); err != nil {
			return nil, err
		}
		_, _, recordFound, err = state.store.read(ctx)
		if err != nil {
			return nil, err
		}
		_, _, _, intentFound, err = state.store.readIntent(ctx)
		if err != nil {
			return nil, err
		}
		if recordFound || intentFound {
			return nil, generationAuthorityError("firewall reset absent metadata changed during inspection", nil, nil)
		}
		if err := recheckHostResetStore(ctx, state.store); err != nil {
			return nil, err
		}
		return guard, nil
	}
	if !recordFound || !intentFound {
		return nil, generationAuthorityError("firewall reset authority metadata is partially missing", nil, nil)
	}
	if err := admitGenerationRecord(record, generationRecordBinding{bootID: bootID, namespace: identity}); err != nil {
		return nil, err
	}
	if record.lifecycle != generationRecordCommitted ||
		record.committed != generationRecordIdentityFromCompiled(empty) {
		return nil, generationAuthorityError(
			"firewall reset requires a committed empty generation; finish cleanup first",
			nil,
			nil,
		)
	}
	if journal.BootID != bootID ||
		journal.HostNamespace != (NamespaceIdentity{Device: identity.device, Inode: identity.inode}) {
		return nil, generationAuthorityError(
			"firewall reset intent binding differs from current host authority",
			nil,
			nil,
		)
	}
	// The previous graph is historical only after the exact target became committed. A newer pending
	// target cannot be hidden by the preceding empty generation, including policies for stopped VMs.
	if _, err := journal.Before.compile(); err != nil {
		return nil, err
	}
	target, err := journal.Target.compile()
	if err != nil {
		return nil, err
	}
	if generationRecordIdentityFromCompiled(target) != record.committed || !hostResetIntentEmpty(journal.Target) {
		return nil, generationAuthorityError(
			"firewall reset requires an empty desired inventory without pending intent",
			nil,
			nil,
		)
	}
	if err := verifyExactCommittedGeneration(ctx, state.kernel, empty); err != nil {
		return nil, err
	}
	current, currentEvidence, found, err := state.store.read(ctx)
	if err != nil {
		return nil, err
	}
	if !found || currentEvidence != recordEvidence || !generationRecordsEqual(current, record) {
		return nil, generationAuthorityError("firewall reset generation record changed during inspection", nil, nil)
	}
	_, currentRaw, currentIntentEvidence, found, err := state.store.readIntent(ctx)
	if err != nil {
		return nil, err
	}
	if !found || currentIntentEvidence != journalEvidence || !bytes.Equal(currentRaw, journalRaw) {
		return nil, generationAuthorityError("firewall reset intent changed during inspection", nil, nil)
	}
	if err := recheckHostResetStore(ctx, state.store); err != nil {
		return nil, err
	}
	return guard, nil
}

func hostResetIntentEmpty(intent Intent) bool {
	return len(intent.ActiveNetworks) == 0 && len(intent.Reservations) == 0 && len(intent.Endpoints) == 0 &&
		len(
			intent.Networks,
		) == 0 && len(intent.VMGrants) == 0 && len(intent.NetworkGrants) == 0 && len(intent.HostServices) == 0
}

// Release drops the namespace pin, existing store, firewall lock, and authority in reverse order.
func (guard *HostResetGuard) Release(ctx context.Context) error {
	if guard == nil || guard.state == nil {
		return nil
	}
	state := guard.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released {
		return state.releaseErr
	}
	if ctx == nil {
		return generationAuthorityError("firewall reset release context is required", nil, nil)
	}
	cleanupCtx := context.WithoutCancel(ctx)
	if state.kernel != nil {
		state.releaseErr = appendGenerationCleanupError(
			state.releaseErr,
			"release firewall reset namespace",
			state.kernel.Release(cleanupCtx),
		)
	}
	if state.store != nil {
		state.releaseErr = appendGenerationCleanupError(
			state.releaseErr,
			"release firewall reset store",
			state.store.Release(cleanupCtx),
		)
	}
	if state.absence != nil {
		state.releaseErr = appendGenerationCleanupError(
			state.releaseErr,
			"release firewall reset absent-store authority",
			closeGenerationRecordFDs(cleanupCtx, state.absence.deps, state.absence.retained),
		)
	}
	if state.lock != nil {
		state.releaseErr = appendGenerationCleanupError(
			state.releaseErr,
			"release firewall reset lock",
			state.lock.Release(cleanupCtx),
		)
	}
	if state.global != nil {
		state.releaseErr = appendGenerationCleanupError(
			state.releaseErr,
			"release firewall reset authority",
			state.global.Release(cleanupCtx),
		)
	}
	state.released = true
	return state.releaseErr
}
