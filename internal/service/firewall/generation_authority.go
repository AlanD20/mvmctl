package firewall

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"golang.org/x/sys/unix"

	"mvmctl/internal/lib/authorityfs"
	"mvmctl/pkg/errs"
)

const systemBootIDPath = "/proc/sys/kernel/random/boot_id"

// generationNetworkAuthority is the earlier lock-order capability. It deliberately exposes only the already-pinned
// host namespace identity needed to bind persistent authority, never a namespace descriptor or a namespace effect.
type generationNetworkAuthority interface {
	firewallGenerationNamespace(context.Context) (generationNamespaceIdentity, error)
}

type generationPersistentAuthority interface {
	acquireAfterNetwork(context.Context, generationNetworkAuthority) (generationPersistentLease, error)
}

type generationPersistentLease interface {
	admittedRecord() (generationRecord, error)
	replaceAdmittedRecord(context.Context, generationRecord) error
	Release(context.Context) error
}

type generationAuthorityStore interface {
	read(context.Context) (generationRecord, generationRecordFileEvidence, bool, error)
	replace(
		context.Context,
		generationRecord,
		generationRecordFileEvidence,
		generationRecord,
	) (generationRecordFileEvidence, error)
	Release(context.Context) error
}

type firewallGlobalAuthority interface {
	acquire(context.Context) (firewallGlobalLock, error)
	Release(context.Context) error
}

type firewallGlobalLock interface {
	Release(context.Context) error
}

type persistentGenerationAuthorityDeps struct {
	openGlobalAuthority func(context.Context) (firewallGlobalAuthority, error)
	openRecordStore     func(context.Context) (generationAuthorityStore, error)
	readBootID          func(context.Context) (string, error)
}

type persistentGenerationAuthority struct {
	deps persistentGenerationAuthorityDeps
}

type persistentGenerationLease struct {
	state *persistentGenerationLeaseState
}

type persistentGenerationLeaseState struct {
	// applicationMu serializes the multi-step record/kernel/record transaction across copied lease values.
	applicationMu sync.Mutex
	// mu guards the admitted record, retained capabilities, and every lifecycle field below.
	mu                sync.Mutex
	global            firewallGlobalAuthority
	lock              firewallGlobalLock
	store             generationAuthorityStore
	record            generationRecord
	evidence          generationRecordFileEvidence
	mutationAmbiguous bool
	active            bool
	released          bool
	releaseError      error
}

func newPersistentGenerationAuthority() generationPersistentAuthority {
	return newPersistentGenerationAuthorityWithDeps(realPersistentGenerationAuthorityDeps())
}

func newPersistentGenerationAuthorityWithDeps(
	deps persistentGenerationAuthorityDeps,
) *persistentGenerationAuthority {
	return &persistentGenerationAuthority{deps: deps}
}

func (authority *persistentGenerationAuthority) acquireAfterNetwork(
	ctx context.Context,
	network generationNetworkAuthority,
) (_ generationPersistentLease, returnErr error) {
	if authority == nil || network == nil {
		return nil, generationAuthorityError(
			"verified network authority is required before firewall authority",
			nil,
			nil,
		)
	}
	if ctx == nil {
		return nil, generationAuthorityError("persistent firewall generation context is required", nil, nil)
	}
	if err := ctx.Err(); err != nil {
		return nil, generationAuthorityError("acquire persistent firewall generation authority", err, nil)
	}

	// CRITICAL: This call is the earlier network-lock proof. No host-global firewall object is opened before it
	// succeeds and returns a complete identity.
	namespace, err := network.firewallGenerationNamespace(ctx)
	if err != nil {
		return nil, preserveGenerationAuthorityError("verify network authority before firewall authority", err)
	}
	if namespace.device == 0 || namespace.inode == 0 {
		return nil, generationAuthorityError("verified host network namespace identity is invalid", nil, nil)
	}
	if authority.deps.openGlobalAuthority == nil || authority.deps.openRecordStore == nil ||
		authority.deps.readBootID == nil {
		return nil, generationAuthorityError("persistent firewall generation authority is not configured", nil, nil)
	}

	var global firewallGlobalAuthority
	var lock firewallGlobalLock
	var store generationAuthorityStore
	defer func() {
		if returnErr == nil {
			return
		}
		returnErr = releasePersistentGenerationResources(ctx, returnErr, store, lock, global)
	}()

	global, err = authority.deps.openGlobalAuthority(ctx)
	if err != nil {
		return nil, preserveGenerationAuthorityError("open global firewall authority", err)
	}
	if global == nil {
		return nil, generationAuthorityError("global firewall authority returned no capability", nil, nil)
	}
	lock, err = global.acquire(ctx)
	if err != nil {
		return nil, preserveGenerationAuthorityError("acquire global firewall lock", err)
	}
	if lock == nil {
		return nil, generationAuthorityError("global firewall lock acquisition returned no capability", nil, nil)
	}
	store, err = authority.deps.openRecordStore(ctx)
	if err != nil {
		return nil, preserveGenerationAuthorityError("open firewall generation record store", err)
	}
	if store == nil {
		return nil, generationAuthorityError("firewall generation record store returned no capability", nil, nil)
	}
	bootID, err := authority.deps.readBootID(ctx)
	if err != nil {
		return nil, preserveGenerationAuthorityError("read host boot ID for firewall authority", err)
	}
	binding := generationRecordBinding{bootID: bootID, namespace: namespace}
	if err := validateGenerationRecordBinding(binding); err != nil {
		return nil, err
	}
	record, evidence, found, err := store.read(ctx)
	if err != nil {
		return nil, preserveGenerationAuthorityError("read firewall generation authority record", err)
	}
	if !found {
		return nil, generationAuthorityError(
			"firewall generation authority record is missing; qualified kernel inspection is required",
			nil,
			nil,
		)
	}
	if err := admitGenerationRecord(record, binding); err != nil {
		return nil, err
	}

	lease := &persistentGenerationLease{state: &persistentGenerationLeaseState{
		global: global, lock: lock, store: store,
		record: cloneGenerationRecord(record), evidence: evidence, active: true,
	}}
	global = nil
	lock = nil
	store = nil
	return lease, nil
}

func (lease *persistentGenerationLease) admittedRecord() (generationRecord, error) {
	if lease == nil || lease.state == nil {
		return generationRecord{}, generationAuthorityError(
			"persistent firewall generation lease is unavailable",
			nil,
			nil,
		)
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if !state.active {
		return generationRecord{}, generationAuthorityError(
			"persistent firewall generation lease is released",
			nil,
			nil,
		)
	}
	return cloneGenerationRecord(state.record), nil
}

func (lease *persistentGenerationLease) replaceAdmittedRecord(
	ctx context.Context,
	replacement generationRecord,
) error {
	if lease == nil || lease.state == nil {
		return generationAuthorityError("persistent firewall generation lease is unavailable", nil, nil)
	}
	state := lease.state
	state.applicationMu.Lock()
	defer state.applicationMu.Unlock()
	return lease.replaceAdmittedRecordDuringApplication(ctx, replacement)
}

func (lease *persistentGenerationLease) replaceAdmittedRecordDuringApplication(
	ctx context.Context,
	replacement generationRecord,
) error {
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if !state.active {
		return generationAuthorityError("persistent firewall generation lease is released", nil, nil)
	}
	if state.mutationAmbiguous {
		return generationAuthorityError(
			"firewall generation authority replacement outcome is ambiguous",
			nil,
			nil,
		)
	}
	if ctx == nil {
		return generationAuthorityError("firewall generation replacement context is required", nil, nil)
	}
	proof, err := state.store.replace(
		ctx,
		cloneGenerationRecord(state.record),
		state.evidence,
		cloneGenerationRecord(replacement),
	)
	if err != nil {
		domainErr := errs.AsDomainError(err)
		recordReplaced := false
		if domainErr != nil {
			recordReplaced, _ = domainErr.Details["record_replaced"].(bool)
		}
		if recordReplaced {
			if proof == (generationRecordFileEvidence{}) {
				state.mutationAmbiguous = true
			} else {
				state.record = cloneGenerationRecord(replacement)
				state.evidence = proof
			}
		}
		return preserveGenerationAuthorityError("replace admitted firewall generation record", err)
	}
	if proof == (generationRecordFileEvidence{}) {
		state.mutationAmbiguous = true
		return generationAuthorityError(
			"firewall generation replacement returned no file evidence",
			nil,
			nil,
		)
	}
	state.record = cloneGenerationRecord(replacement)
	state.evidence = proof
	return nil
}

// Release closes record traversal, then the firewall lock, then its authority exactly once. Cleanup preserves context
// values, ignores cancellation, and caches its first result across copied lease values.
func (lease *persistentGenerationLease) Release(ctx context.Context) error {
	if lease == nil || lease.state == nil {
		return nil
	}
	state := lease.state
	state.applicationMu.Lock()
	defer state.applicationMu.Unlock()
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released {
		return state.releaseError
	}
	if !state.active {
		state.released = true
		return nil
	}
	cleanupCtx := context.Background()
	var primary error
	if ctx == nil {
		primary = generationAuthorityError("persistent firewall generation release context is required", nil, nil)
	} else {
		cleanupCtx = context.WithoutCancel(ctx)
	}
	state.releaseError = releasePersistentGenerationResources(
		cleanupCtx,
		primary,
		state.store,
		state.lock,
		state.global,
	)
	state.store = nil
	state.lock = nil
	state.global = nil
	state.record = generationRecord{}
	state.evidence = generationRecordFileEvidence{}
	state.mutationAmbiguous = false
	state.active = false
	state.released = true
	return state.releaseError
}

func releasePersistentGenerationResources(
	ctx context.Context,
	primary error,
	store generationAuthorityStore,
	lock firewallGlobalLock,
	global firewallGlobalAuthority,
) error {
	cleanupCtx := context.WithoutCancel(ctx)
	result := primary
	if store != nil {
		result = appendGenerationCleanupError(
			result,
			"release firewall generation record store",
			store.Release(cleanupCtx),
		)
	}
	if lock != nil {
		result = appendGenerationCleanupError(
			result,
			"release global firewall lock",
			lock.Release(cleanupCtx),
		)
	}
	if global != nil {
		result = appendGenerationCleanupError(
			result,
			"release global firewall authority",
			global.Release(cleanupCtx),
		)
	}
	return result
}

// preserveGenerationAuthorityError keeps a dependency's classified metadata and complete wrapper/join chain. Only
// unclassified failures acquire the firewall authority's stable error identity.
func preserveGenerationAuthorityError(message string, cause error) error {
	if errs.AsDomainError(cause) != nil {
		return cause
	}
	return generationAuthorityError(message, cause, nil)
}

func cloneGenerationRecord(record generationRecord) generationRecord {
	result := record
	if record.before != nil {
		before := *record.before
		result.before = &before
	}
	if record.target != nil {
		target := *record.target
		result.target = &target
	}
	return result
}

type systemFirewallGlobalAuthority struct {
	authority *authorityfs.Firewall
}

func (authority *systemFirewallGlobalAuthority) acquire(ctx context.Context) (firewallGlobalLock, error) {
	if authority == nil || authority.authority == nil {
		return nil, generationAuthorityError("global firewall authority is unavailable", nil, nil)
	}
	return authority.authority.Acquire(ctx)
}

func (authority *systemFirewallGlobalAuthority) Release(ctx context.Context) error {
	if authority == nil || authority.authority == nil {
		return nil
	}
	return authority.authority.Release(ctx)
}

func realPersistentGenerationAuthorityDeps() persistentGenerationAuthorityDeps {
	return persistentGenerationAuthorityDeps{
		openGlobalAuthority: func(ctx context.Context) (firewallGlobalAuthority, error) {
			authority, err := authorityfs.OpenFirewall(ctx)
			if err != nil {
				return nil, err
			}
			return &systemFirewallGlobalAuthority{authority: authority}, nil
		},
		openRecordStore: func(ctx context.Context) (generationAuthorityStore, error) {
			return openGenerationRecordStore(
				ctx,
				realGenerationRecordStoreDeps(),
				productionGenerationRecordStorePolicy(),
			)
		},
		readBootID: readSystemBootID,
	}
}

func readSystemBootID(ctx context.Context) (_ string, returnErr error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	fd, err := unix.Open(systemBootIDPath, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return "", err
	}
	defer func() {
		if err := unix.Close(fd); err != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("close host boot ID: %w", err))
		}
	}()
	value := make([]byte, 38)
	used := 0
	for used < len(value) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		count, readErr := unix.Read(fd, value[used:])
		if count < 0 || count > len(value)-used {
			return "", fmt.Errorf("host boot ID returned invalid read count %d", count)
		}
		used += count
		if readErr != nil && !errors.Is(readErr, unix.EINTR) {
			return "", readErr
		}
		if count == 0 && readErr == nil {
			break
		}
	}
	if used != 37 || value[36] != '\n' {
		return "", fmt.Errorf("host boot ID has unexpected length or delimiter")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	bootID := string(value[:36])
	if !generationBootIDPattern.MatchString(bootID) {
		return "", fmt.Errorf("host boot ID is not canonical lowercase UUID text")
	}
	return bootID, nil
}
