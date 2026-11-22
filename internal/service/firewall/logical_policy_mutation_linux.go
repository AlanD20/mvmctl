package firewall

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	"mvmctl/internal/lib/authorityfs"
)

// logicalPolicyMutation may replace policy and label records. It borrows the
// topology projection from committed intent and has no link or network authority.
type logicalPolicyMutation struct {
	store    *generationRecordStore
	kernel   generationKernelApplication
	lease    *persistentGenerationLease
	binding  generationRecordBinding
	before   Intent
	compiled compiledGeneration
	raw      []byte
	evidence generationRecordFileEvidence
}

func withLogicalPolicyMutation(
	ctx context.Context, mutate func(*logicalPolicyMutation) error,
) (returnErr error) {
	global, err := authorityfs.OpenFirewall(ctx)
	if err != nil {
		return preserveGenerationAuthorityError("open logical firewall mutation authority", err)
	}
	cleanup := context.WithoutCancel(ctx)
	defer func() { returnErr = errors.Join(returnErr, global.Release(cleanup)) }()
	lock, err := global.Acquire(ctx)
	if err != nil {
		return preserveGenerationAuthorityError("lock logical firewall mutation authority", err)
	}
	defer func() { returnErr = errors.Join(returnErr, lock.Release(cleanup)) }()
	kernel, err := retainHostResetNamespace(ctx)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, kernel.Release(cleanup)) }()
	identity := kernel.state.identity
	if _, err := verifyPinnedGenerationNamespace(ctx, kernel, identity); err != nil {
		return err
	}
	store, err := openGenerationRecordStore(ctx, realGenerationRecordStoreDeps(), productionGenerationRecordStorePolicy())
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, store.Release(cleanup)) }()
	bootID, err := readSystemBootID(ctx)
	if err != nil {
		return err
	}
	binding := generationRecordBinding{bootID: bootID, namespace: identity}
	record, evidence, found, err := store.read(ctx)
	if err != nil {
		return err
	}
	switch {
	case !found:
		_, _, _, intentFound, err := store.readIntent(ctx)
		if err != nil {
			return err
		}
		if intentFound {
			return invalidGeneration("firewall intent exists without a generation record")
		}
		record, evidence, err = initializeEmptyGeneration(ctx, kernel, store, binding)
		if err != nil {
			return err
		}
	case record.binding.bootID != binding.bootID:
		record, evidence, err = retirePriorBootGeneration(ctx, kernel, store, binding, record, evidence)
		if err != nil {
			return err
		}
	}
	lease := &persistentGenerationLease{state: &persistentGenerationLeaseState{
		global: &systemFirewallGlobalAuthority{authority: global}, lock: lock, store: store, record: cloneGenerationRecord(record), evidence: evidence, active: true,
	}}
	transaction, err := loadLogicalPolicyMutation(ctx, store, kernel, lease, record, binding)
	if err != nil {
		return err
	}
	return mutate(transaction)
}

func loadLogicalPolicyMutation(
	ctx context.Context, store *generationRecordStore, kernel generationKernelApplication,
	lease *persistentGenerationLease, record generationRecord, binding generationRecordBinding,
) (*logicalPolicyMutation, error) {
	if record.binding != binding {
		return nil, invalidGeneration("logical firewall mutation requires a matching generation binding")
	}
	journal, raw, evidence, found, err := store.readIntent(ctx)
	if err != nil {
		return nil, err
	}
	namespace := NamespaceIdentity{Device: binding.namespace.device, Inode: binding.namespace.inode}
	if found && (journal.BootID != binding.bootID || journal.HostNamespace != namespace) {
		return nil, invalidGeneration("logical firewall journal differs from current binding")
	}
	before, err := journal.Before.compile()
	if err != nil {
		return nil, err
	}
	target, err := journal.Target.compile()
	if err != nil {
		return nil, err
	}
	current, compiled := journal.Target, target
	switch record.lifecycle {
	case generationRecordApplying:
		// A crashed topology transaction needs its concrete network lease.
		if err := requireLogicalPolicyDelta(journal.Before, journal.Target); err != nil {
			return nil, err
		}
		if err := lease.applyCompiledGeneration(ctx, kernel, before, target); err != nil {
			return nil, err
		}
	case generationRecordCommitted:
		if record.committed == generationRecordIdentityFromCompiled(target) {
			break
		}
		if record.committed != generationRecordIdentityFromCompiled(before) {
			return nil, invalidGeneration("logical firewall intent differs from committed generation")
		}
		current, compiled = journal.Before, before
	default:
		return nil, invalidGeneration("logical firewall generation lifecycle is invalid")
	}
	return &logicalPolicyMutation{store, kernel, lease, binding, current, compiled, raw, evidence}, nil
}

func (mutation *logicalPolicyMutation) publish(ctx context.Context, target Intent, execOwner uint32) error {
	if err := requireLogicalPolicyDelta(mutation.before, target); err != nil {
		return err
	}
	compiled, err := target.compile()
	if err != nil {
		return err
	}
	if err := verifyExactCommittedGeneration(ctx, mutation.kernel, mutation.compiled); err != nil {
		return err
	}
	sameProjection := generationRecordIdentityFromCompiled(mutation.compiled) ==
		generationRecordIdentityFromCompiled(compiled)
	namespace := NamespaceIdentity{Device: mutation.binding.namespace.device, Inode: mutation.binding.namespace.inode}
	journal := intentJournal{
		SchemaVersion: intentJournalSchema, BootID: mutation.binding.bootID, HostNamespace: namespace,
		Before: mutation.before, Target: target,
	}
	if sameProjection {
		// The journal replacement commits the metadata. Both sides must agree
		// because the kernel generation cannot distinguish the revisions.
		journal.Before = target
	}
	// Persist a denial before accepting a label or exec transition that can
	// change this owner's index. A crash leaves new admissions denied until
	// a subsequent logical transaction rebuilds from committed authority.
	var projection execPolicyProjection
	publishExec := execOwner != 0 && (mutation.before.hasExecPolicies(execOwner) || target.hasExecPolicies(execOwner))
	if publishExec {
		projection, err = target.projectExecPolicies(execOwner)
		if err != nil {
			return err
		}
		if err := mutation.store.writeExecPolicyIndex(ctx, execOwner, execPolicyProjection{}, false); err != nil {
			return err
		}
	}
	if _, _, err := mutation.store.writeIntent(ctx, mutation.raw, mutation.evidence, journal); err != nil {
		return err
	}
	if !sameProjection {
		if err := mutation.lease.applyCompiledGeneration(ctx, mutation.kernel, mutation.compiled, compiled); err != nil {
			return err
		}
	}
	if publishExec {
		return mutation.store.writeExecPolicyIndex(ctx, execOwner, projection, true)
	}
	return nil
}

func (mutation *logicalPolicyMutation) recoverExecPolicyIndex(ctx context.Context, ownerUID uint32) error {
	status, found, err := mutation.store.execPolicyIndexState(ctx, ownerUID)
	if err != nil {
		return err
	}
	if !found {
		if mutation.before.hasExecPolicies(ownerUID) {
			return invalidGeneration("committed exec policies lack their authorization index")
		}
		return nil
	}
	if status == execPolicyIndexCommitted {
		return nil
	}
	projection, err := mutation.before.projectExecPolicies(ownerUID)
	if err != nil {
		return err
	}
	if err := verifyExactCommittedGeneration(ctx, mutation.kernel, mutation.compiled); err != nil {
		return err
	}
	return mutation.store.writeExecPolicyIndex(ctx, ownerUID, projection, true)
}

func requireLogicalPolicyDelta(before, target Intent) error {
	before.VMLabels, target.VMLabels = nil, nil
	before.ExecPolicies, target.ExecPolicies = nil, nil
	before.LabelTrafficPolicies, target.LabelTrafficPolicies = nil, nil
	left, err := json.Marshal(before)
	if err != nil {
		return err
	}
	right, err := json.Marshal(target)
	if err != nil {
		return err
	}
	if !bytes.Equal(left, right) {
		return invalidGeneration("firewall transition requires concrete network mutation authority")
	}
	return nil
}
