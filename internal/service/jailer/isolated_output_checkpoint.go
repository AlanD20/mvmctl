package jailer

import (
	"context"

	"golang.org/x/sys/unix"
)

// grantExecutionOutputAccess records the exact published log/metrics before the
// first ACL mutation, so a crashed launcher retains named revocation authority.
func (lease *launchLease) grantExecutionOutputAccess(ctx context.Context) error {
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released || !state.registrationComplete || state.record.lifecycle != instanceLifecycleLaunching || state.persistentOutputs == nil {
		return instanceStateError(state.record.vmID, "execution output access requires durable launching authority")
	}
	outputs := state.persistentOutputs.state
	outputs.mu.Lock()
	identities := &instanceOutputIdentities{}
	for _, output := range outputs.outputs {
		if output.name == managedPersistentConsole {
			continue
		}
		var stat unix.Stat_t
		if err := unix.Fstat(output.fd, &stat); err != nil {
			outputs.mu.Unlock()
			return instanceAtomicError("pin execution output identity", err)
		}
		identity := instanceObjectIdentity{device: uint64(stat.Dev), inode: stat.Ino}
		switch output.name {
		case managedPersistentLog:
			identities.log = identity
		case managedPersistentMetrics:
			identities.metrics = &identity
		}
	}
	outputs.mu.Unlock()
	record := cloneInstanceRecord(state.record)
	record.launch.outputIdentity = identities
	if err := validateInstanceRecord(record); err != nil {
		return err
	}
	err := state.directories.writeRecord(ctx, record)
	if err == nil || instanceRecordWasReplaced(err) {
		state.record = record
	}
	if err != nil {
		return err
	}
	return grantIsolatedPersistentOutputs(ctx, state.persistentOutputs, record.launch.executionIdentity.uid)
}
