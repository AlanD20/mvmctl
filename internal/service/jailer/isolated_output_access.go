package jailer

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"

	"golang.org/x/sys/unix"
)

const (
	isolatedOutputAccessACLName  = "system.posix_acl_access"
	isolatedOutputACLVersion     = 2
	isolatedOutputACLUserObject  = 0x01
	isolatedOutputACLUser        = 0x02
	isolatedOutputACLGroupObject = 0x04
	isolatedOutputACLMask        = 0x10
	isolatedOutputACLOther       = 0x20
	isolatedOutputACLUndefinedID = ^uint32(0)
	isolatedOutputACLBytes       = 4 + 5*8
)

// grantIsolatedPersistentOutputs admits exactly one execution UID on the retained log and
// metrics files. The caller retains the durable launch and VM lock; both children remain blocked.
// CRITICAL: Mode 0660 represents the named-user ACL mask, not owning-group access. The owning
// group and other ACL entries stay zero. The console receives an inherited fd and needs no grant.
func grantIsolatedPersistentOutputs(
	ctx context.Context, lease *managedVMPersistentOutputLease, executionUID uint32,
) error {
	if ctx == nil || lease == nil || lease.state == nil {
		return instanceStateError("", "isolated output grant requires retained output authority")
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released || !state.ready || executionUID == 0 || executionUID == isolatedOutputACLUndefinedID ||
		executionUID == state.owner.uid || executionUID != state.executionUID {
		return managedVMResourceValidationError("isolated output credentials do not match durable launch authority")
	}
	acl := isolatedPersistentOutputACL(executionUID)
	for index := range state.outputs {
		output := &state.outputs[index]
		if output.name == managedPersistentConsole {
			continue
		}
		if output.name != managedPersistentLog && output.name != managedPersistentMetrics {
			return managedVMResourceValidationError("isolated output grant has an unexpected destination")
		}
		stat, err := inspectManagedPersistentOutputObject(ctx, state, output.fd, false, 1)
		if err != nil {
			return annotatePersistentOutputError(err, state)
		}
		output.isolatedGrant = true
		if stat.Mode == unix.S_IFREG|0660 {
			continue
		}
		state.mutationAttempted = true
		// CREATE refuses an ACL that appeared since admission. Existing admitted grants need
		// no replacement, so an unknown user ACL is never intentionally overwritten.
		if err := state.deps.setAccessACL(ctx, output.fd, acl, unix.XATTR_CREATE); err != nil {
			state.outcomeUnknown = true
			return annotatePersistentOutputError(
				persistentOutputError("grant isolated persistent output access", err),
				state,
			)
		}
		state.modified = true
		if err := state.deps.fsync(context.WithoutCancel(ctx), output.fd); err != nil {
			state.durabilityUncertain = true
			return annotatePersistentOutputError(
				persistentOutputError("sync isolated persistent output grant", err),
				state,
			)
		}
		stat, err = inspectManagedPersistentOutputObject(ctx, state, output.fd, false, 1)
		if err != nil {
			return annotatePersistentOutputError(err, state)
		}
		if stat.Mode != unix.S_IFREG|0660 {
			return managedVMResourceValidationError("isolated persistent output grant disappeared")
		}
	}
	return nil
}

// CALLER MUST HOLD: state.mu. The fixed file role and root-record execution UID determine
// the only accepted extended ACL; 0600 files must have no hidden extended ACL at all.
func inspectIsolatedPersistentOutputACL(
	ctx context.Context, state *managedVMPersistentOutputState, fd int, mode uint16,
) (bool, error) {
	if state.deps.getAccessACL == nil {
		return false, managedVMResourceAtomicError("persistent output ACL admission is unavailable", nil)
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	var buffer [isolatedOutputACLBytes]byte
	count, err := state.deps.getAccessACL(ctx, fd, buffer[:])
	if errors.Is(err, unix.ENODATA) && mode == unix.S_IFREG|0600 {
		return false, nil
	}
	if err != nil {
		return false, persistentOutputError("inspect persistent output access ACL", err)
	}
	if mode != unix.S_IFREG|0660 || count != len(buffer) || state.executionUID == 0 ||
		state.executionUID == isolatedOutputACLUndefinedID || state.executionUID == state.owner.uid {
		return false, managedVMResourceValidationError("persistent output mode or access ACL is not admitted")
	}
	permitted := false
	for _, output := range state.outputs {
		if output.fd == fd && (output.name == managedPersistentLog || output.name == managedPersistentMetrics) {
			permitted = true
		}
	}
	if !permitted || !bytes.Equal(buffer[:], isolatedPersistentOutputACL(state.executionUID)) {
		return false, managedVMResourceValidationError(
			"persistent output access ACL differs from its execution identity",
		)
	}
	return true, nil
}

func isolatedPersistentOutputACL(executionUID uint32) []byte {
	return encodeIsolatedOutputACL([]isolatedOutputACLEntry{
		{tag: isolatedOutputACLUserObject, permissions: 6, id: isolatedOutputACLUndefinedID},
		{tag: isolatedOutputACLUser, permissions: 6, id: executionUID},
		{tag: isolatedOutputACLGroupObject, id: isolatedOutputACLUndefinedID},
		{tag: isolatedOutputACLMask, permissions: 6, id: isolatedOutputACLUndefinedID},
		{tag: isolatedOutputACLOther, id: isolatedOutputACLUndefinedID},
	})
}

type isolatedOutputACLEntry struct {
	tag         uint16
	permissions uint16
	id          uint32
}

func encodeIsolatedOutputACL(entries []isolatedOutputACLEntry) []byte {
	raw := make([]byte, 4+len(entries)*8)
	binary.LittleEndian.PutUint32(raw[:4], isolatedOutputACLVersion)
	for index, entry := range entries {
		offset := 4 + index*8
		binary.LittleEndian.PutUint16(raw[offset:offset+2], entry.tag)
		binary.LittleEndian.PutUint16(raw[offset+2:offset+4], entry.permissions)
		binary.LittleEndian.PutUint32(raw[offset+4:offset+8], entry.id)
	}
	return raw
}

// revokeIsolatedPersistentOutputLease is used only after exact process termination and reap.
// A mismatched ACL stays untouched and retains cleanup authority for a later explicit recovery.
func revokeIsolatedPersistentOutputLease(ctx context.Context, lease *managedVMPersistentOutputLease) error {
	if ctx == nil || lease == nil || lease.state == nil {
		return instanceStateError("", "isolated output revocation requires retained authority")
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released {
		return instanceStateError("", "isolated output revocation authority is released")
	}
	// Linux reduces this minimal ACL to ordinary 0600 in the same ACL update. Removing the
	// xattr directly would retain mode 0660 and accidentally grant the owning group access.
	privateACL := encodeIsolatedOutputACL([]isolatedOutputACLEntry{
		{tag: isolatedOutputACLUserObject, permissions: 6, id: isolatedOutputACLUndefinedID},
		{tag: isolatedOutputACLGroupObject, id: isolatedOutputACLUndefinedID},
		{tag: isolatedOutputACLOther, id: isolatedOutputACLUndefinedID},
	})
	for index := range state.outputs {
		output := &state.outputs[index]
		if !output.isolatedGrant {
			continue
		}
		stat, err := inspectManagedPersistentOutputObject(ctx, state, output.fd, false, 1)
		if err != nil {
			return annotatePersistentOutputError(err, state)
		}
		if stat.Mode == unix.S_IFREG|0600 {
			output.isolatedGrant = false
			continue
		}
		if output.name != managedPersistentLog && output.name != managedPersistentMetrics {
			return managedVMResourceValidationError("isolated output revocation has an unexpected destination")
		}
		state.mutationAttempted = true
		if err := state.deps.setAccessACL(ctx, output.fd, privateACL, unix.XATTR_REPLACE); err != nil {
			state.outcomeUnknown = true
			return annotatePersistentOutputError(
				persistentOutputError("revoke isolated persistent output access", err),
				state,
			)
		}
		state.modified = true
		if err := state.deps.fsync(context.WithoutCancel(ctx), output.fd); err != nil {
			state.durabilityUncertain = true
			return annotatePersistentOutputError(
				persistentOutputError("sync isolated persistent output revocation", err),
				state,
			)
		}
		stat, err = inspectManagedPersistentOutputObject(ctx, state, output.fd, false, 1)
		if err != nil {
			return annotatePersistentOutputError(err, state)
		}
		if stat.Mode != unix.S_IFREG|0600 {
			return managedVMResourceValidationError("persistent output access was not revoked")
		}
		output.isolatedGrant = false
	}
	return nil
}

// revokeIsolatedPersistentOutputs reopens only historically recorded log/metrics inodes after
// the lifecycle owner proves task absence. It performs no publication, truncation or directory
// creation. Missing or replaced output files remain ambiguous rather than authorizing a new inode.
func revokeIsolatedPersistentOutputs(ctx context.Context, record instanceRecord) (returnErr error) {
	if ctx == nil {
		return instanceStateError(record.vmID, "isolated output cleanup context is required")
	}
	if err := validateInstanceRecord(record); err != nil {
		return err
	}
	if record.launch == nil || record.launch.outputIdentity == nil {
		return nil
	}
	if record.lifecycle != instanceLifecycleCleaning {
		return instanceStateError(record.vmID, "isolated output revocation requires cleaning authority")
	}
	caller := instanceCaller{uid: record.ownerUID, gid: record.launch.ownerGID}
	cache, err := repinManagedCache(ctx, realManagedCacheDeps(), productionManagedCachePolicy(),
		caller, record.launch.cacheLocator, record.launch.cacheIdentity)
	if err != nil {
		return err
	}
	outputs := []managedPersistentOutput{{name: managedPersistentLog, fd: -1}}
	if record.launch.outputIdentity.metrics != nil {
		outputs = append(outputs, managedPersistentOutput{name: managedPersistentMetrics, fd: -1})
	}
	state := &managedVMPersistentOutputState{
		deps: realManagedPersistentOutputDeps(), cache: cache, owner: caller,
		executionUID: record.launch.executionIdentity.uid, vmDirectoryFD: -1, outputs: outputs,
	}
	lease := &managedVMPersistentOutputLease{state: state}
	defer func() { returnErr = errors.Join(returnErr, lease.Release(context.WithoutCancel(ctx))) }()
	state.mu.Lock()
	err = admitManagedPersistentOutputs(ctx, state, record.vmID)
	if err == nil {
		for _, output := range state.outputs {
			expected := record.launch.outputIdentity.log
			if output.name == managedPersistentMetrics {
				expected = *record.launch.outputIdentity.metrics
			}
			if err = requireIsolatedPersistentOutputIdentity(ctx, state, output, expected); err != nil {
				break
			}
		}
	}
	state.mu.Unlock()
	if err != nil {
		return err
	}
	if err := revokeIsolatedPersistentOutputLease(ctx, lease); err != nil {
		return err
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	for _, output := range state.outputs {
		expected := record.launch.outputIdentity.log
		if output.name == managedPersistentMetrics {
			expected = *record.launch.outputIdentity.metrics
		}
		if err := requireIsolatedPersistentOutputIdentity(ctx, state, output, expected); err != nil {
			return err
		}
	}
	return nil
}

// CALLER MUST HOLD: state.mu. The historical device/inode pair is recorded before ACL effects.
func requireIsolatedPersistentOutputIdentity(
	ctx context.Context, state *managedVMPersistentOutputState,
	output managedPersistentOutput, expected instanceObjectIdentity,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if output.fd < 0 || expected.inode == 0 {
		return managedVMResourceValidationError("recorded isolated output is missing")
	}
	var pinned, named unix.Stat_t
	if err := unix.Fstat(output.fd, &pinned); err != nil {
		return persistentOutputError("inspect recorded isolated output", err)
	}
	if err := unix.Fstatat(state.vmDirectoryFD, output.name.basename(), &named, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return persistentOutputError("recheck recorded isolated output name", err)
	}
	if uint64(pinned.Dev) != expected.device || pinned.Ino != expected.inode ||
		uint64(named.Dev) != expected.device || named.Ino != expected.inode ||
		pinned.Mode&unix.S_IFMT != unix.S_IFREG || named.Mode != pinned.Mode ||
		pinned.Nlink != 1 || named.Nlink != 1 || pinned.Uid != state.owner.uid || named.Uid != state.owner.uid ||
		pinned.Gid != state.owner.gid || named.Gid != state.owner.gid {
		return managedVMResourceValidationError("isolated output differs from its recorded inode or fixed binding")
	}
	return nil
}
