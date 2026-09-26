package jailer

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"

	"mvmctl/internal/lib/authorityfs"
	"mvmctl/internal/lib/system"
)

// retireExecutionIdentity consumes the VM removal coordinator's exact resource-absence proof.
// CRITICAL: No caller flag, path or numeric identity can authorize account deletion. The same
// VM lock remains held across resource absence, account admission, durable retirement and effects.
// A failure preserves retiring authority; retry reconstructs the proof and repeats task absence.
func (lease *vmAuthorityLease) retireExecutionIdentity(
	ctx context.Context, proof *vmExecutionRetirementProof,
) (returnErr error) {
	if ctx == nil || os.Geteuid() != 0 || os.Getegid() != 0 || lease == nil || lease.state == nil ||
		proof == nil || proof.state == nil {
		return instanceStateError("", "execution retirement requires root VM removal authority")
	}
	retirement := proof.state
	retirement.mu.Lock()
	defer retirement.mu.Unlock()
	if retirement.vmLock == nil || retirement.vmLock.state != lease.state {
		return instanceStateError(retirement.vmID, "execution retirement proof has a different VM lock")
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	key, err := authorityfs.NewVMKey(retirement.vmID)
	if err != nil || state.released || !state.active || state.value == nil ||
		state.ownerUID != retirement.ownerUID || state.key != key {
		return instanceStateError(retirement.vmID, "execution retirement VM authority is unavailable")
	}
	if retirement.consumed {
		return nil
	}
	store, err := openExecutionIdentityStore(ctx, false)
	if err != nil {
		return err
	}
	if store == nil {
		return instanceStateError(retirement.vmID, "execution retirement reservation store disappeared")
	}
	defer func() { returnErr = errors.Join(returnErr, store.close(context.WithoutCancel(ctx))) }()
	record, found, err := store.read(ctx, retirement.vmID)
	if err != nil || !found {
		return instanceAtomicError("execution retirement reservation is missing", err)
	}
	if record.OwnerUID != retirement.ownerUID || record.VMID != retirement.vmID || record.UID == nil ||
		record.GID == nil ||
		*record.UID != retirement.identity.uid ||
		*record.GID != retirement.identity.gid {
		return instanceStateError(retirement.vmID, "execution retirement proof does not match its reservation")
	}
	if record.Phase == executionIdentityRetired {
		retirement.consumed = true
		return nil
	}
	if record.Phase != executionIdentityReady && record.Phase != executionIdentityRetiring {
		return instanceStateError(
			retirement.vmID,
			"execution allocation outcome is ambiguous; refusing account removal",
		)
	}
	if err := requireExecutionIdentityProcessAbsence(ctx, retirement.identity); err != nil {
		return err
	}
	if record.Phase == executionIdentityReady {
		if err := requireExecutionIdentityAccountRecord(ctx, record); err != nil {
			return err
		}
		record.Phase = executionIdentityRetiring
		if err := store.write(ctx, record, true); err != nil {
			return err
		}
	}
	if err := removeExecutionIdentityAccount(ctx, record); err != nil {
		return err
	}
	record.Phase = executionIdentityRetired
	if err := store.write(ctx, record, true); err != nil {
		return err
	}
	retirement.consumed = true
	return nil
}

func removeExecutionIdentityAccount(ctx context.Context, record executionIdentityRecord) error {
	_, found, err := lookupExecutionIdentityAccount(ctx, "passwd", record.Name)
	if err != nil {
		return err
	}
	if found {
		if err := requireExecutionIdentityAccountRecord(ctx, record); err != nil {
			return err
		}
		if err := requireExecutionIdentityProcessAbsence(
			ctx,
			vmExecutionIdentity{uid: *record.UID, gid: *record.GID},
		); err != nil {
			return err
		}
		// Never force deletion or request home/mail removal. The account manager retains its
		// own concurrent-account locking and refuses a still-used account.
		result, err := system.DefaultRunner.Run(ctx,
			[]string{"/usr/sbin/userdel", "--", record.Name}, executionIdentityCommandOptions())
		if err != nil || result == nil || !result.Success() {
			return instanceAtomicError("remove isolated execution account; retirement intent retained", err)
		}
	}
	if err := requireExecutionIdentityUserAbsent(ctx, record); err != nil {
		return err
	}
	group, found, err := lookupExecutionIdentityAccount(ctx, "group", record.Name)
	if err != nil {
		return err
	}
	if found {
		if err := requireExecutionIdentityAccountRow(record, "group", group); err != nil {
			return err
		}
		numeric, found, err := lookupExecutionIdentityAccount(ctx, "group", strconv.FormatUint(uint64(*record.GID), 10))
		if err != nil || !found {
			return instanceAtomicError("execution retirement group no longer resolves", err)
		}
		if err := requireExecutionIdentityAccountRow(record, "group", numeric); err != nil {
			return err
		}
		if err := requireExecutionIdentityProcessAbsence(
			ctx,
			vmExecutionIdentity{uid: *record.UID, gid: *record.GID},
		); err != nil {
			return err
		}
		result, err := system.DefaultRunner.Run(ctx,
			[]string{"/usr/sbin/groupdel", "--", record.Name}, executionIdentityCommandOptions())
		if err != nil || result == nil || !result.Success() {
			return instanceAtomicError("remove isolated execution group; retirement intent retained", err)
		}
	}
	if _, found, err := lookupExecutionIdentityAccount(ctx, "group", record.Name); err != nil || found {
		return instanceAtomicError("execution retirement group absence is unproved", err)
	}
	raw, err := readExecutionIdentityAccountFile(ctx, "group", false)
	if err != nil {
		return err
	}
	for _, row := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(row, record.Name+":") {
			return instanceAtomicError("execution retirement left a local group record", nil)
		}
	}
	if err := requireExecutionIdentityUserAbsent(ctx, record); err != nil {
		return err
	}
	return requireExecutionIdentityProcessAbsence(ctx, vmExecutionIdentity{uid: *record.UID, gid: *record.GID})
}

func requireExecutionIdentityUserAbsent(ctx context.Context, record executionIdentityRecord) error {
	if _, found, err := lookupExecutionIdentityAccount(ctx, "passwd", record.Name); err != nil || found {
		return instanceAtomicError("execution retirement account absence is unproved", err)
	}
	for _, database := range []string{"passwd", "shadow"} {
		raw, err := readExecutionIdentityAccountFile(ctx, database, false)
		if err != nil {
			return err
		}
		for _, row := range strings.Split(string(raw), "\n") {
			if strings.HasPrefix(row, record.Name+":") {
				return instanceAtomicError("execution retirement left a local account record", nil)
			}
		}
	}
	return nil
}
