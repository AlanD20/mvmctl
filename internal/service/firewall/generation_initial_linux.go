package firewall

import (
	"context"

	"mvmctl/internal/lib/authorityfs"
)

type productionGenerationNetworkAuthority struct {
	mutation *authorityfs.NetworkMutation
	kernel   *kernelObservationNamespaceLease
	identity generationNamespaceIdentity
}

func (authority *productionGenerationNetworkAuthority) firewallGenerationNamespace(
	ctx context.Context,
) (generationNamespaceIdentity, error) {
	if authority == nil || authority.mutation == nil || authority.kernel == nil {
		return generationNamespaceIdentity{}, generationAuthorityError(
			"network mutation authority is unavailable",
			nil,
			nil,
		)
	}
	if err := authority.mutation.Verify(ctx); err != nil {
		return generationNamespaceIdentity{}, preserveGenerationAuthorityError("verify network mutation lock", err)
	}
	state := authority.kernel.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if !state.active || state.released {
		return generationNamespaceIdentity{}, generationAuthorityError(
			"host network namespace pin is released",
			nil,
			nil,
		)
	}
	identity, err := inspectKernelObservationNamespace(ctx, state.deps, state.fd)
	if err != nil {
		return generationNamespaceIdentity{}, err
	}
	if identity != authority.identity {
		return generationNamespaceIdentity{}, generationAuthorityError(
			"host network namespace identity changed",
			nil,
			nil,
		)
	}
	return identity, nil
}

func (authority *productionGenerationNetworkAuthority) createInitialRecord(
	ctx context.Context,
	store *generationRecordStore,
	binding generationRecordBinding,
) (generationRecord, generationRecordFileEvidence, error) {
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
	record, err := newCommittedGenerationRecord(binding, generationRecordIdentityFromCompiled(empty))
	if err != nil {
		return generationRecord{}, generationRecordFileEvidence{}, err
	}
	evidence, err := store.create(ctx, record)
	if err != nil {
		return generationRecord{}, evidence, err
	}
	return record, evidence, nil
}
