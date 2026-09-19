package jailer

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"os"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/sys/unix"

	"mvmctl/internal/lib/authorityfs"
)

// vmExecutionIdentity is deliberately distinct from instanceCaller: ownership authorizes
// operations, whereas these credentials confine exactly one VM's host execution.
type vmExecutionIdentity struct {
	uid uint32
	gid uint32
}

type vmExecutionIdentityLease struct {
	state *vmExecutionIdentityLeaseState
}

type vmExecutionIdentityLeaseState struct {
	// mu protects the pinned reservation, identity and close-once lifecycle across copies.
	mu         sync.Mutex
	fd         int
	record     executionIdentityRecord
	released   bool
	releaseErr error
}

type executionIdentityRecord struct {
	Schema   uint32  `json:"schema"`
	OwnerUID uint32  `json:"owner_uid"`
	VMID     string  `json:"vm_id"`
	Name     string  `json:"account_name"`
	Nonce    string  `json:"nonce"`
	Phase    string  `json:"phase"`
	UID      *uint32 `json:"execution_uid,omitempty"`
	GID      *uint32 `json:"execution_gid,omitempty"`
}

const (
	executionIdentityPending = "pending"
	executionIdentityReady   = "ready"
)

// prepareExecutionIdentity reserves a host-managed locked system account for this locked VM.
// CRITICAL: The VM lock proves operator ownership. The allocator lock serializes durable intent
// and account creation; the host account tools allocate their normal finite system-ID space.
// Account deletion is intentionally absent: stopping a VM does not prove its persistent backing,
// ACL grants or snapshots have disappeared. Release never returns these credentials to the pool.
func (lease *vmAuthorityLease) prepareExecutionIdentity(
	ctx context.Context, caller instanceCaller, vmID string,
) (*vmExecutionIdentityLease, error) {
	return lease.admitExecutionIdentity(ctx, caller, vmID, nil)
}

// reopenExecutionIdentity admits only the already allocated credentials recorded for this VM.
// Recovery never creates an account or interprets a pending reservation as execution authority.
func (lease *vmAuthorityLease) reopenExecutionIdentity(
	ctx context.Context, caller instanceCaller, vmID string, expected vmExecutionIdentity,
) (*vmExecutionIdentityLease, error) {
	if expected.uid == 0 || expected.gid == 0 {
		return nil, instanceStateError(vmID, "recorded execution identity is absent")
	}
	return lease.admitExecutionIdentity(ctx, caller, vmID, &expected)
}

func (lease *vmAuthorityLease) admitExecutionIdentity(
	ctx context.Context, caller instanceCaller, vmID string, expected *vmExecutionIdentity,
) (result *vmExecutionIdentityLease, returnErr error) {
	if ctx == nil || os.Geteuid() != 0 || os.Getegid() != 0 || lease == nil || lease.state == nil {
		return nil, instanceStateError(vmID, "execution identity requires root VM authority")
	}
	if err := validateInstanceCaller(caller); err != nil {
		return nil, err
	}
	defer func() {
		if returnErr != nil && result != nil {
			returnErr = errors.Join(returnErr, result.Release(context.WithoutCancel(ctx)))
			result = nil
		}
	}()
	key, err := authorityfs.NewVMKey(vmID)
	if err != nil {
		return nil, instanceValidationError("execution identity VM ID is invalid")
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released || !state.active || state.value == nil || state.ownerUID != caller.uid || state.key != key {
		return nil, instanceStateError(vmID, "execution identity does not match held VM ownership")
	}
	store, err := openExecutionIdentityStore(ctx, expected == nil)
	if err != nil {
		return nil, err
	}
	defer func() { returnErr = errors.Join(returnErr, store.close(context.WithoutCancel(ctx))) }()
	record, found, err := store.read(ctx, vmID)
	if err != nil {
		return nil, err
	}
	if found {
		if record.OwnerUID != caller.uid || record.VMID != vmID {
			return nil, unauthorizedInstanceError(vmID)
		}
		if record.Phase != executionIdentityReady {
			return nil, instanceStateError(
				vmID,
				"execution account allocation is incomplete; retained intent requires administrator recovery",
			)
		}
		if expected != nil && (*record.UID != expected.uid || *record.GID != expected.gid) {
			return nil, instanceStateError(vmID, "execution reservation differs from recorded credentials")
		}
		if err := requireExecutionIdentityAccount(ctx, record, caller); err != nil {
			return nil, err
		}
	} else {
		if expected != nil {
			return nil, instanceStateError(vmID, "recorded execution reservation is missing")
		}
		if err := requireExecutionIdentityHostTools(ctx); err != nil {
			return nil, err
		}
		name, err := executionIdentityAccountName(vmID)
		if err != nil {
			return nil, err
		}
		if err := requireExecutionIdentityNameAbsent(ctx, name); err != nil {
			return nil, err
		}
		nonce := make([]byte, 16)
		if _, err := rand.Read(nonce); err != nil {
			return nil, instanceAtomicError("create execution identity allocation nonce", err)
		}
		record = executionIdentityRecord{
			Schema: 1, OwnerUID: caller.uid, VMID: vmID, Name: name,
			Nonce: hex.EncodeToString(nonce), Phase: executionIdentityPending,
		}
		// A crash after this point is never interpreted as permission to adopt a same-named
		// account. The pending record survives command failure and ambiguous publication.
		if err := store.write(ctx, record, false); err != nil {
			return nil, err
		}
		identity, err := createExecutionIdentityAccount(ctx, record, caller)
		if err != nil {
			return nil, err
		}
		record.Phase, record.UID, record.GID = executionIdentityReady, &identity.uid, &identity.gid
		if err := store.requireDistinct(ctx, record); err != nil {
			return nil, err
		}
		if err := store.write(ctx, record, true); err != nil {
			return nil, err
		}
	}
	if err := store.requireDistinct(ctx, record); err != nil {
		return nil, err
	}
	fd, err := store.pin(ctx, record)
	if err != nil {
		return nil, err
	}
	return &vmExecutionIdentityLease{state: &vmExecutionIdentityLeaseState{fd: fd, record: record}}, nil
}

// Identity returns admitted execution credentials while retaining their durable reservation.
func (lease *vmExecutionIdentityLease) Identity(ctx context.Context) (vmExecutionIdentity, error) {
	if ctx == nil || lease == nil || lease.state == nil {
		return vmExecutionIdentity{}, instanceStateError("", "execution identity lease is unavailable")
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released || state.fd < 0 || state.record.Phase != executionIdentityReady {
		return vmExecutionIdentity{}, instanceStateError(state.record.VMID, "execution identity lease is released")
	}
	if err := ctx.Err(); err != nil {
		return vmExecutionIdentity{}, err
	}
	if err := inspectExecutionIdentityRecordFD(ctx, state.fd); err != nil {
		return vmExecutionIdentity{}, err
	}
	return vmExecutionIdentity{uid: *state.record.UID, gid: *state.record.GID}, nil
}

// Release closes the pinned reservation once. It never deletes or unlocks the host account.
func (lease *vmExecutionIdentityLease) Release(ctx context.Context) error {
	if lease == nil || lease.state == nil {
		return nil
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if !state.released {
		state.releaseErr = unix.Close(state.fd)
		state.fd = -1
		state.released = true
	}
	return state.releaseErr
}

func executionIdentityAccountName(vmID string) (string, error) {
	if !vmIDPattern.MatchString(vmID) {
		return "", instanceValidationError("execution identity VM ID is invalid")
	}
	raw, err := hex.DecodeString(vmID)
	if err != nil {
		return "", instanceValidationError("execution identity VM ID is invalid")
	}
	// Full 128-bit VM identity fits the common 32-character account-name limit without truncation.
	return "mvm-" + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw)), nil
}

func (record executionIdentityRecord) comment() string {
	return "mvmctl-execution-v1/" + strconv.FormatUint(
		uint64(record.OwnerUID),
		10,
	) + "/" + record.VMID + "/" + record.Nonce
}

func validateExecutionIdentityRecord(record executionIdentityRecord) error {
	name, err := executionIdentityAccountName(record.VMID)
	if err != nil || record.Schema != 1 || record.OwnerUID == 0 || record.Name != name || len(record.Nonce) != 32 {
		return instanceAtomicError("execution identity reservation is invalid", err)
	}
	nonce, err := hex.DecodeString(record.Nonce)
	if err != nil || hex.EncodeToString(nonce) != record.Nonce {
		return instanceAtomicError("execution identity reservation nonce is invalid", err)
	}
	switch record.Phase {
	case executionIdentityPending:
		if record.UID != nil || record.GID != nil {
			return instanceAtomicError("pending execution identity claims allocated credentials", nil)
		}
	case executionIdentityReady:
		if record.UID == nil || record.GID == nil || *record.UID == 0 || *record.GID == 0 ||
			*record.UID == ^uint32(0) || *record.GID == ^uint32(0) || *record.UID == record.OwnerUID {
			return instanceAtomicError("allocated execution identity is invalid", nil)
		}
	default:
		return instanceAtomicError("execution identity reservation phase is invalid", nil)
	}
	return nil
}
