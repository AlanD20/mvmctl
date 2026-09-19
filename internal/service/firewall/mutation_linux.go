// Package firewall owns privileged host-global nftables generations.
// Layer: root service. Kernel expressions and descriptors never cross this package's boundary.
package firewall

import (
	"context"
	"slices"
	"sync"

	"mvmctl/internal/lib/authorityfs"
)

// MutationLease retains the global firewall lock after an earlier concrete network lock.
// THREAD-SAFE: methods serialize generation changes and close-once release across copied handles.
// CALLER MUST HOLD: the owning network service's lifecycle mutex until Release completes; that
// owner must not release or transfer its NetworkMutation while this lease is retained.
type MutationLease struct{ state *mutationLeaseState }

type mutationLeaseState struct {
	// mu guards graph transactions, endpoint verification and release of retained kernel/record authority.
	mu              sync.Mutex
	network         *productionGenerationNetworkAuthority
	persistent      *persistentGenerationLease
	store           *generationRecordStore
	binding         EndpointBinding
	intent          Intent
	compiled        compiledGeneration
	journalRaw      []byte
	journalEvidence generationRecordFileEvidence
	released        bool
	releaseErr      error
	uncertain       bool
}

// VerifiedVMEndpointLease is private endpoint cleanup authority minted only after exact kernel readback.
// It does not retain a global mutation lock or treat a previous generation digest as present authority.
type VerifiedVMEndpointLease struct{ state *verifiedVMEndpointState }

type verifiedVMEndpointState struct {
	// mu guards the endpoint's cleanup completion across copies and later mutation leases.
	mu      sync.Mutex
	binding EndpointBinding
	removed bool
}

// BeginVMEndpoint acquires the global firewall lock before the network service creates down topology.
// CRITICAL: binding is resolved from root-owned network/topology authority, never caller requests.
// Root/CAP_NET_ADMIN administrators are trusted; detected foreign or changed state fails closed.
func BeginVMEndpoint(
	ctx context.Context, mutation *authorityfs.NetworkMutation, binding EndpointBinding,
) (_ *MutationLease, returnErr error) {
	if ctx == nil {
		return nil, generationAuthorityError("firewall mutation context is required", nil, nil)
	}
	if err := validateEndpointBinding(binding, true); err != nil {
		return nil, err
	}
	return beginMutation(ctx, mutation, binding)
}

func beginMutation(
	ctx context.Context, mutation *authorityfs.NetworkMutation, binding EndpointBinding,
) (_ *MutationLease, returnErr error) {
	if err := verifyNetworkMutation(ctx, mutation, binding); err != nil {
		return nil, err
	}
	identity := generationNamespaceIdentity{device: binding.HostNamespace.Device, inode: binding.HostNamespace.Inode}
	deps := realKernelObservationNamespaceDeps()
	namespaceAuthority := newKernelObservationNamespaceAuthorityWithAllDeps(
		deps,
		realKernelApplicationNamespaceDeps(deps),
	)
	kernel, err := namespaceAuthority.retainExpectedNamespace(ctx, identity)
	if err != nil {
		return nil, err
	}
	network := &productionGenerationNetworkAuthority{mutation: mutation, kernel: kernel, identity: identity}
	state := &mutationLeaseState{network: network, binding: binding}
	lease := &MutationLease{state: state}
	defer func() {
		if returnErr != nil {
			returnErr = appendGenerationCleanupError(
				returnErr,
				"release rejected firewall mutation",
				lease.Release(context.WithoutCancel(ctx)),
			)
		}
	}()
	authority := newPersistentGenerationAuthorityWithDeps(realPersistentGenerationAuthorityDeps())
	admitted, err := authority.acquireAfterNetwork(ctx, network)
	if err != nil {
		return nil, err
	}
	persistent, ok := admitted.(*persistentGenerationLease)
	if !ok {
		return nil, appendGenerationCleanupError(
			generationAuthorityError("firewall persistent authority has unexpected type", nil, nil),
			"release rejected generation",
			admitted.Release(ctx),
		)
	}
	state.persistent = persistent
	store, ok := persistent.state.store.(*generationRecordStore)
	if !ok {
		return nil, generationAuthorityError("firewall production record store is unavailable", nil, nil)
	}
	state.store = store
	if err := state.loadIntent(ctx); err != nil {
		return nil, err
	}
	return lease, nil
}

func verifyNetworkMutation(ctx context.Context, mutation *authorityfs.NetworkMutation, binding EndpointBinding) error {
	if mutation == nil {
		return generationAuthorityError("network mutation is required before firewall mutation", nil, nil)
	}
	if err := mutation.Verify(ctx); err != nil {
		return preserveGenerationAuthorityError("verify firewall network lock", err)
	}
	owner, key, err := mutation.Binding()
	if err != nil {
		return preserveGenerationAuthorityError("bind firewall network lock", err)
	}
	expected, err := authorityfs.NewNetworkKey(binding.NetworkID)
	if err != nil {
		return preserveGenerationAuthorityError("resolve firewall network key", err)
	}
	if owner != binding.OwnerUID || key != expected {
		return generationAuthorityError("firewall network lock binding differs", nil, nil)
	}
	return nil
}

// CALLER MUST HOLD: state.mu or exclusive ownership during construction.
func (state *mutationLeaseState) loadIntent(ctx context.Context) error {
	record, err := state.persistent.admittedRecord()
	if err != nil {
		return err
	}
	if record.binding.bootID != state.binding.BootID || record.binding.namespace != state.network.identity {
		return generationAuthorityError(
			"firewall endpoint boot or namespace differs from generation authority",
			nil,
			nil,
		)
	}
	journal, raw, evidence, found, err := state.store.readIntent(ctx)
	if err != nil {
		return err
	}
	if !found {
		empty, err := compileGeneration(generationIntent{})
		if err != nil {
			return err
		}
		if record.lifecycle != generationRecordCommitted ||
			record.committed != generationRecordIdentityFromCompiled(empty) {
			return generationAuthorityError("nonempty firewall generation has no root intent journal", nil, nil)
		}
		if err := verifyExactCommittedGeneration(ctx, state.network.kernel, empty); err != nil {
			return err
		}
		journal = intentJournal{
			SchemaVersion: 1,
			BootID:        record.binding.bootID,
			HostNamespace: state.binding.HostNamespace,
		}
		raw, evidence, err = state.store.writeIntent(ctx, nil, generationRecordFileEvidence{}, journal)
		if err != nil {
			return err
		}
	}
	if journal.BootID != record.binding.bootID || journal.HostNamespace != state.binding.HostNamespace {
		return generationAuthorityError("firewall intent journal boot or namespace differs", nil, nil)
	}
	before, err := journal.Before.compile()
	if err != nil {
		return err
	}
	target, err := journal.Target.compile()
	if err != nil {
		return err
	}
	switch record.lifecycle {
	case generationRecordApplying:
		if err := state.persistent.applyCompiledGeneration(ctx, state.network.kernel, before, target); err != nil {
			return err
		}
		state.intent, state.compiled = journal.Target, target
	case generationRecordCommitted:
		switch record.committed {
		case generationRecordIdentityFromCompiled(target):
			state.intent, state.compiled = journal.Target, target
		case generationRecordIdentityFromCompiled(before):
			state.intent, state.compiled = journal.Before, before
		default:
			return generationAuthorityError("root intent does not match committed firewall generation", nil, nil)
		}
	default:
		return generationAuthorityError("firewall generation lifecycle is invalid", nil, nil)
	}
	if err := verifyExactCommittedGeneration(ctx, state.network.kernel, state.compiled); err != nil {
		return err
	}
	state.journalRaw, state.journalEvidence = raw, evidence
	state.uncertain = false
	return nil
}

func (state *mutationLeaseState) ready(ctx context.Context) error {
	if ctx == nil {
		return generationAuthorityError("firewall mutation context is required", nil, nil)
	}
	if state.released || state.network == nil || state.persistent == nil {
		return generationAuthorityError("firewall mutation authority is released", nil, nil)
	}
	if err := verifyNetworkMutation(ctx, state.network.mutation, state.binding); err != nil {
		return err
	}
	if _, err := state.network.firewallGenerationNamespace(ctx); err != nil {
		return err
	}
	if err := state.verifyRetainedRecord(ctx); err != nil {
		return err
	}
	if state.uncertain {
		return state.loadIntent(ctx)
	}
	return nil
}

// CALLER MUST HOLD: state.mu.
func (state *mutationLeaseState) verifyRetainedRecord(ctx context.Context) error {
	persistent := state.persistent.state
	persistent.mu.Lock()
	defer persistent.mu.Unlock()
	if !persistent.active || persistent.mutationAmbiguous {
		return generationAuthorityError("retained firewall record authority is unavailable", nil, nil)
	}
	current, evidence, found, err := state.store.read(ctx)
	if err != nil {
		return err
	}
	if !found || evidence != persistent.evidence || !generationRecordsEqual(current, persistent.record) {
		return generationAuthorityError("retained firewall generation record changed", nil, nil)
	}
	return nil
}

func (state *mutationLeaseState) applyIntent(ctx context.Context, target Intent) error {
	compiled, err := target.compile()
	if err != nil {
		return err
	}
	journal := intentJournal{
		SchemaVersion: 1, BootID: state.binding.BootID, HostNamespace: state.binding.HostNamespace,
		Before: state.intent, Target: target,
	}
	state.uncertain = true
	raw, evidence, err := state.store.writeIntent(ctx, state.journalRaw, state.journalEvidence, journal)
	if err != nil {
		return err
	}
	state.journalRaw, state.journalEvidence = raw, evidence
	if err := state.persistent.applyCompiledGeneration(
		ctx,
		state.network.kernel,
		state.compiled,
		compiled,
	); err != nil {
		return err
	}
	if err := verifyExactCommittedGeneration(ctx, state.network.kernel, compiled); err != nil {
		return err
	}
	state.intent, state.compiled = target, compiled
	state.uncertain = false
	return nil
}

// ApplyVMEndpoint adds one fully observed down endpoint while preserving the complete host graph.
// An error returns no activation authority; durable before/target state remains available for cleanup.
func (lease *MutationLease) ApplyVMEndpoint(
	ctx context.Context, binding EndpointBinding, network NetworkIntent,
) (*VerifiedVMEndpointLease, error) {
	if lease == nil || lease.state == nil {
		return nil, generationAuthorityError("firewall mutation lease is unavailable", nil, nil)
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if err := state.ready(ctx); err != nil {
		return nil, err
	}
	if err := validateEndpointBinding(binding, false); err != nil {
		return nil, err
	}
	prepared := binding
	prepared.HostVeth.IfIndex = state.binding.HostVeth.IfIndex
	if prepared != state.binding ||
		(state.binding.HostVeth.IfIndex != 0 && binding.HostVeth.IfIndex != state.binding.HostVeth.IfIndex) {
		return nil, generationAuthorityError("prepared firewall endpoint binding changed", nil, nil)
	}
	if network.OwnerUID != binding.OwnerUID || network.NetworkID != binding.NetworkID ||
		network.HostBridge != binding.ManagedBridge.Name || network.Subnet != binding.Prefix || network.HostIPv4 != binding.Gateway {
		return nil, generationAuthorityError("firewall network projection differs from endpoint authority", nil, nil)
	}
	target := cloneIntent(state.intent)
	found := false
	for _, endpoint := range target.Endpoints {
		if endpoint.VMID == binding.VMID {
			if endpoint != binding {
				return nil, generationAuthorityError("existing firewall endpoint binding differs", nil, nil)
			}
			found = true
		}
	}
	if !found {
		target.Endpoints = append(target.Endpoints, binding)
	}
	found = false
	for _, existing := range target.Networks {
		if existing.NetworkID == network.NetworkID {
			if existing != network {
				return nil, generationAuthorityError("existing firewall network intent differs", nil, nil)
			}
			found = true
		}
	}
	if !found {
		target.Networks = append(target.Networks, network)
	}
	if err := target.reserveEndpoint(endpointReservation(binding)); err != nil {
		return nil, err
	}
	state.binding = binding
	if err := state.applyIntent(ctx, target); err != nil {
		return nil, err
	}
	return &VerifiedVMEndpointLease{state: &verifiedVMEndpointState{binding: binding}}, nil
}

// VerifyVMEndpoint freshly rechecks the complete generation before each network exposure checkpoint.
func (lease *MutationLease) VerifyVMEndpoint(
	ctx context.Context, endpoint *VerifiedVMEndpointLease, binding EndpointBinding,
) error {
	if lease == nil || lease.state == nil || endpoint == nil || endpoint.state == nil {
		return generationAuthorityError("verified firewall endpoint authority is unavailable", nil, nil)
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	endpoint.state.mu.Lock()
	defer endpoint.state.mu.Unlock()
	if endpoint.state.removed || endpoint.state.binding != binding || state.binding != binding {
		return generationAuthorityError("verified firewall endpoint binding differs", nil, nil)
	}
	if err := state.ready(ctx); err != nil {
		return err
	}
	if !slices.Contains(state.intent.Endpoints, binding) {
		return generationAuthorityError("verified endpoint is absent from current firewall generation", nil, nil)
	}
	return verifyExactCommittedGeneration(ctx, state.network.kernel, state.compiled)
}

// RemoveVMEndpoint removes an exact retained endpoint after the network service lowers its host veth.
func (lease *MutationLease) RemoveVMEndpoint(
	ctx context.Context, endpoint *VerifiedVMEndpointLease, binding EndpointBinding,
) error {
	if lease == nil || lease.state == nil || endpoint == nil || endpoint.state == nil {
		return generationAuthorityError("firewall endpoint cleanup authority is unavailable", nil, nil)
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	endpoint.state.mu.Lock()
	defer endpoint.state.mu.Unlock()
	if endpoint.state.binding != binding {
		return generationAuthorityError("firewall endpoint cleanup binding differs", nil, nil)
	}
	if err := state.removeEndpoint(ctx, binding); err != nil {
		return err
	}
	endpoint.state.removed = true
	return nil
}

// RemoveEndpoint recovers cleanup from an exact root-owned topology binding without minting activation authority.
// CALLER MUST HOLD: root topology cleanup authority with the host veth already verified down.
func (lease *MutationLease) RemoveEndpoint(ctx context.Context, binding EndpointBinding) error {
	if lease == nil || lease.state == nil {
		return generationAuthorityError("firewall mutation lease is unavailable", nil, nil)
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.removeEndpoint(ctx, binding)
}

// CALLER MUST HOLD: state.mu.
func (state *mutationLeaseState) removeEndpoint(ctx context.Context, binding EndpointBinding) error {
	if err := state.ready(ctx); err != nil {
		return err
	}
	if binding != state.binding {
		return generationAuthorityError("firewall endpoint cleanup binding differs", nil, nil)
	}
	target := cloneIntent(state.intent)
	for _, endpoint := range target.Endpoints {
		if endpoint.VMID == binding.VMID && endpoint != binding {
			return generationAuthorityError("firewall endpoint was replaced", nil, nil)
		}
	}
	target.Endpoints = slices.DeleteFunc(
		target.Endpoints,
		func(endpoint EndpointBinding) bool { return endpoint.VMID == binding.VMID },
	)
	return state.applyIntent(ctx, target)
}

// Release drops only firewall mutation resources. The network owner releases its earlier lock afterward.
func (lease *MutationLease) Release(ctx context.Context) error {
	if lease == nil || lease.state == nil {
		return nil
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released {
		return state.releaseErr
	}
	if ctx == nil {
		return generationAuthorityError("firewall release context is required", nil, nil)
	}
	cleanupCtx := context.WithoutCancel(ctx)
	if state.network != nil && state.network.kernel != nil {
		state.releaseErr = appendGenerationCleanupError(
			state.releaseErr,
			"release firewall namespace pin",
			state.network.kernel.Release(cleanupCtx),
		)
	}
	if state.persistent != nil {
		state.releaseErr = appendGenerationCleanupError(
			state.releaseErr,
			"release firewall generation authority",
			state.persistent.Release(cleanupCtx),
		)
	}
	state.released = true
	return state.releaseErr
}

func cloneIntent(intent Intent) Intent {
	return Intent{
		Reservations: slices.Clone(intent.Reservations),
		Endpoints:    slices.Clone(intent.Endpoints), Networks: slices.Clone(intent.Networks),
		VMGrants: slices.Clone(intent.VMGrants), NetworkGrants: slices.Clone(intent.NetworkGrants),
		HostServices: slices.Clone(intent.HostServices),
	}
}
