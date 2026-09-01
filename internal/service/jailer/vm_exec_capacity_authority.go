package jailer

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"math"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"mvmctl/internal/lib/authorityfs"
	"mvmctl/internal/lib/vmexecwire"
	"mvmctl/pkg/errs"
)

const (
	// vmExecCapacityTerminalCleanupGrace is the sum of the protocol's fixed two-second forced-process cleanup bound and
	// fixed two-second terminal-frame write bound. Relay setup consumes the execution budget: later composition must stop
	// the relay at relayStopBoottimeNS, then finish terminal transport and release inside this grace.
	vmExecCapacityTerminalCleanupGrace = 4 * time.Second
	vmExecCapacityLifetimeNS           = uint64(
		vmexecwire.MaxExecutionDuration + vmExecCapacityTerminalCleanupGrace,
	)
	vmExecCapacityIDAttempts = 8
)

type vmExecCapacityPolicy struct {
	maxReservations uint32
}

type vmExecCapacityClock interface {
	boottimeNS(context.Context) (uint64, error)
}

type vmExecCapacityLock interface {
	Release(context.Context) error
}

type vmExecCapacityLockRoot interface {
	acquire(context.Context) (vmExecCapacityLock, error)
	Release(context.Context) error
}

type vmExecCapacityAuthorityDeps struct {
	openStore        func(context.Context) (*vmExecCapacityStore, error)
	openLockRoot     func(context.Context) (vmExecCapacityLockRoot, error)
	currentBootID    func(context.Context) (string, error)
	newReservationID func(context.Context) (vmExecReservationID, error)
	clock            vmExecCapacityClock
}

type vmExecCapacityAuthority struct {
	policy vmExecCapacityPolicy
	deps   vmExecCapacityAuthorityDeps
}

type vmExecCapacityLease struct {
	state *vmExecCapacityLeaseState
}

type vmExecCapacityLeaseState struct {
	// mu guards the terminal release decision. authority, caller, and record are immutable after construction.
	mu         sync.Mutex
	authority  *vmExecCapacityAuthority
	caller     vmExecCapacityCaller
	record     vmExecCapacityRecord
	released   bool
	releaseErr error
}

func newVMExecCapacityAuthority(policy vmExecCapacityPolicy) (*vmExecCapacityAuthority, error) {
	return newVMExecCapacityAuthorityWithDeps(policy, realVMExecCapacityAuthorityDeps())
}

func newVMExecCapacityAuthorityWithDeps(
	policy vmExecCapacityPolicy,
	deps vmExecCapacityAuthorityDeps,
) (*vmExecCapacityAuthority, error) {
	if policy.maxReservations == 0 {
		return nil, vmExecCapacityValidationError("vm-exec capacity maximum must be positive")
	}
	if deps.openStore == nil || deps.openLockRoot == nil || deps.currentBootID == nil ||
		deps.newReservationID == nil || deps.clock == nil {
		return nil, vmExecCapacityCorruptError("vm-exec capacity authority dependencies are incomplete", nil)
	}
	return &vmExecCapacityAuthority{policy: policy, deps: deps}, nil
}

// reserve creates one inert owner- and launch-bound capacity claim. No production relay calls this private seam.
func (authority *vmExecCapacityAuthority) reserve(
	ctx context.Context,
	intent vmExecCapacityIntent,
) (_ *vmExecCapacityLease, returnErr error) {
	if authority == nil {
		return nil, vmExecCapacityCorruptError("vm-exec capacity authority is unavailable", nil)
	}
	if err := validateVMExecCapacityIntent(intent); err != nil {
		return nil, err
	}
	lockRoot, lock, store, err := authority.openLockedStore(ctx)
	if err != nil {
		return nil, err
	}
	var lease *vmExecCapacityLease
	defer func() {
		returnErr = releaseVMExecCapacityOperation(ctx, returnErr, store, lock, lockRoot)
		if lease != nil && returnErr != nil {
			returnErr = annotateVMExecCapacityRecordCreated(returnErr)
		}
	}()

	hostBootID, err := authority.deps.currentBootID(ctx)
	if err != nil {
		return nil, vmExecCapacityEffectError("observe host boot identity for VM-exec capacity", err)
	}
	if !hostBootIDPattern.MatchString(hostBootID) {
		return nil, vmExecCapacityCorruptError("current host boot identity is invalid", nil)
	}
	boottimeNS, err := authority.deps.clock.boottimeNS(ctx)
	if err != nil {
		return nil, vmExecCapacityEffectError("observe CLOCK_BOOTTIME for VM-exec capacity", err)
	}
	if boottimeNS > math.MaxUint64-vmExecCapacityLifetimeNS {
		return nil, vmExecCapacityCorruptError("vm-exec capacity expiration overflows clock_boottime", nil)
	}
	active, err := store.scanAndPrune(ctx, hostBootID, boottimeNS)
	if err != nil {
		return nil, err
	}
	if uint64(len(active)) >= uint64(authority.policy.maxReservations) {
		return nil, vmExecCapacityExhaustedError()
	}

	for range vmExecCapacityIDAttempts {
		reservationID, idErr := authority.deps.newReservationID(ctx)
		if idErr != nil {
			return nil, vmExecCapacityEffectError("generate VM-exec reservation identity", idErr)
		}
		if !vmExecReservationIDPattern.MatchString(reservationID.value) {
			return nil, vmExecCapacityCorruptError("generated VM-exec reservation identity is invalid", nil)
		}
		record, recordErr := newVMExecCapacityRecord(
			intent,
			reservationID,
			hostBootID,
			boottimeNS,
			boottimeNS+vmExecCapacityLifetimeNS,
		)
		if recordErr != nil {
			return nil, recordErr
		}
		createErr := store.createRecord(ctx, record)
		if errors.Is(createErr, unix.EEXIST) {
			domainErr := errs.AsDomainError(createErr)
			if domainErr != nil && domainErr.Details["cleanup_required"] == true {
				return nil, createErr
			}
			continue
		}
		if createErr != nil {
			domainErr := errs.AsDomainError(createErr)
			if domainErr == nil || domainErr.Details["record_created"] != true {
				return nil, createErr
			}
		}
		lease = &vmExecCapacityLease{state: &vmExecCapacityLeaseState{
			authority: authority, caller: intent.caller, record: record,
		}}
		return lease, createErr
	}
	return nil, vmExecCapacityCorruptError("exclusive VM-exec reservation identities exhausted", nil)
}

// release removes only the exact caller-owned reservation. Absence is idempotent.
func (authority *vmExecCapacityAuthority) release(
	ctx context.Context,
	caller vmExecCapacityCaller,
	reservationID vmExecReservationID,
) (returnErr error) {
	if authority == nil {
		return vmExecCapacityCorruptError("vm-exec capacity authority is unavailable", nil)
	}
	if caller.ownerUID == 0 {
		return vmExecCapacityValidationError("vm-exec capacity caller UID must be non-zero")
	}
	if !vmExecReservationIDPattern.MatchString(reservationID.value) {
		return vmExecCapacityValidationError("vm-exec reservation ID is invalid")
	}
	cleanupCtx := context.WithoutCancel(ctx)
	lockRoot, lock, store, err := authority.openLockedStore(cleanupCtx)
	if err != nil {
		return err
	}
	recordDeleted := false
	defer func() {
		returnErr = releaseVMExecCapacityOperation(cleanupCtx, returnErr, store, lock, lockRoot)
		if recordDeleted && returnErr != nil {
			returnErr = annotateVMExecCapacityRecordDeleted(returnErr)
		}
	}()
	record, found, err := store.readRecord(cleanupCtx, reservationID)
	if err != nil || !found {
		return err
	}
	if record.ownerUID != caller.ownerUID {
		return vmExecCapacityUnauthorizedError()
	}
	recordDeleted, err = store.removeRecord(cleanupCtx, reservationID)
	return err
}

func (authority *vmExecCapacityAuthority) openLockedStore(
	ctx context.Context,
) (root vmExecCapacityLockRoot, lock vmExecCapacityLock, store *vmExecCapacityStore, returnErr error) {
	root, err := authority.deps.openLockRoot(ctx)
	if err != nil {
		return nil, nil, nil, vmExecCapacityEffectError("open VM-exec capacity lock authority", err)
	}
	lock, err = root.acquire(ctx)
	if err != nil {
		returnErr = vmExecCapacityEffectError("acquire VM-exec capacity lock", err)
		returnErr = releaseVMExecCapacityOperation(ctx, returnErr, nil, nil, root)
		return nil, nil, nil, returnErr
	}
	store, err = authority.deps.openStore(ctx)
	if err != nil {
		returnErr = vmExecCapacityEffectError("open VM-exec capacity records", err)
		returnErr = releaseVMExecCapacityOperation(ctx, returnErr, nil, lock, root)
		return nil, nil, nil, returnErr
	}
	return root, lock, store, nil
}

// Release removes the reservation exactly once, preserves context values, ignores cancellation, and caches the first
// result across copied lease values.
func (lease *vmExecCapacityLease) Release(ctx context.Context) error {
	if lease == nil || lease.state == nil {
		return nil
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released {
		return state.releaseErr
	}
	cleanupCtx := context.WithoutCancel(ctx)
	if state.authority == nil {
		state.releaseErr = vmExecCapacityCorruptError("vm-exec capacity lease authority is unavailable", nil)
	} else {
		state.releaseErr = state.authority.release(cleanupCtx, state.caller, state.record.reservationID)
	}
	state.released = true
	return state.releaseErr
}

// relayStopBoottimeNS is the absolute CLOCK_BOOTTIME deadline later relay composition must enforce. CLOCK_BOOTTIME
// advances across suspend, so setup and suspend consume this fixed execution budget before the record becomes pruneable.
func (lease *vmExecCapacityLease) relayStopBoottimeNS() (uint64, error) {
	if lease == nil || lease.state == nil {
		return 0, vmExecCapacityCorruptError("vm-exec capacity lease is unavailable", nil)
	}
	created := lease.state.record.createdBoottimeNS
	if created > math.MaxUint64-uint64(vmexecwire.MaxExecutionDuration) {
		return 0, vmExecCapacityCorruptError("vm-exec relay stop time overflows clock_boottime", nil)
	}
	return created + uint64(vmexecwire.MaxExecutionDuration), nil
}

func releaseVMExecCapacityOperation(
	ctx context.Context,
	primary error,
	store *vmExecCapacityStore,
	lock vmExecCapacityLock,
	root vmExecCapacityLockRoot,
) error {
	cleanupCtx := context.WithoutCancel(ctx)
	result := primary
	if store != nil {
		result = appendVMExecCapacityError(result, "release VM-exec capacity store", store.Release(cleanupCtx))
	}
	if lock != nil {
		result = appendVMExecCapacityError(result, "release VM-exec capacity lock", lock.Release(cleanupCtx))
	}
	if root != nil {
		result = appendVMExecCapacityError(result, "release VM-exec capacity lock root", root.Release(cleanupCtx))
	}
	return result
}

func annotateVMExecCapacityRecordCreated(err error) error {
	domainErr := errs.AsDomainError(err)
	if domainErr == nil {
		domainErr = vmExecCapacityEffectError("vm-exec capacity reservation was created", err)
	}
	result := *domainErr
	result.Details = cloneVMExecCapacityDetails(domainErr.Details)
	result.Details["record_created"] = true
	return &result
}

func annotateVMExecCapacityRecordDeleted(err error) error {
	domainErr := errs.AsDomainError(err)
	if domainErr == nil {
		domainErr = vmExecCapacityEffectError("vm-exec capacity reservation was deleted", err)
	}
	result := *domainErr
	result.Details = cloneVMExecCapacityDetails(domainErr.Details)
	result.Details["record_deleted"] = true
	if _, exists := result.Details["durability_uncertain"]; !exists {
		result.Details["durability_uncertain"] = false
	}
	return &result
}

func cloneVMExecCapacityDetails(details map[string]any) map[string]any {
	result := make(map[string]any, len(details)+1)
	for key, value := range details {
		result[key] = value
	}
	return result
}

type systemVMExecCapacityClock struct{}

func (systemVMExecCapacityClock) boottimeNS(ctx context.Context) (uint64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	var now unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_BOOTTIME, &now); err != nil {
		return 0, err
	}
	if now.Sec < 0 || now.Nsec < 0 || now.Nsec >= 1_000_000_000 ||
		uint64(now.Sec) > math.MaxUint64/1_000_000_000 {
		return 0, errors.New("clock_boottime returned an invalid value")
	}
	seconds := uint64(now.Sec) * 1_000_000_000
	if seconds > math.MaxUint64-uint64(now.Nsec) {
		return 0, errors.New("clock_boottime value overflows nanoseconds")
	}
	return seconds + uint64(now.Nsec), nil
}

func randomVMExecReservationID(ctx context.Context) (vmExecReservationID, error) {
	if err := ctx.Err(); err != nil {
		return vmExecReservationID{}, err
	}
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return vmExecReservationID{}, err
	}
	return newVMExecReservationID(hex.EncodeToString(value[:]))
}

func realVMExecCapacityAuthorityDeps() vmExecCapacityAuthorityDeps {
	return vmExecCapacityAuthorityDeps{
		openStore: func(ctx context.Context) (*vmExecCapacityStore, error) {
			return openVMExecCapacityStore(
				ctx,
				realVMExecCapacityStoreDeps(),
				productionVMExecCapacityStorePolicy(),
			)
		},
		openLockRoot: func(ctx context.Context) (vmExecCapacityLockRoot, error) {
			value, err := authorityfs.OpenVMExecCapacity(ctx)
			if err != nil {
				return nil, err
			}
			return &systemVMExecCapacityLockRoot{value: value}, nil
		},
		currentBootID:    readCurrentProcessBootID,
		newReservationID: randomVMExecReservationID,
		clock:            systemVMExecCapacityClock{},
	}
}

type systemVMExecCapacityLockRoot struct {
	value *authorityfs.VMExecCapacity
}

func (root *systemVMExecCapacityLockRoot) acquire(ctx context.Context) (vmExecCapacityLock, error) {
	if root == nil || root.value == nil {
		return nil, errors.New("vm-exec capacity lock root is unavailable")
	}
	value, err := root.value.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	return &systemVMExecCapacityLock{value: value}, nil
}

func (root *systemVMExecCapacityLockRoot) Release(ctx context.Context) error {
	if root == nil || root.value == nil {
		return nil
	}
	err := root.value.Release(context.WithoutCancel(ctx))
	root.value = nil
	return err
}

type systemVMExecCapacityLock struct {
	value *authorityfs.Lock
}

func (lock *systemVMExecCapacityLock) Release(ctx context.Context) error {
	if lock == nil || lock.value == nil {
		return nil
	}
	err := lock.value.Release(context.WithoutCancel(ctx))
	lock.value = nil
	return err
}
