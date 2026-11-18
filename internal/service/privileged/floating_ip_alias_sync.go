package privileged

import (
	"context"
	"errors"
	"time"

	"mvmctl/internal/lib/crypto"
	"mvmctl/internal/lib/guestcontrolwire"
	"mvmctl/internal/lib/hostadmission"
	"mvmctl/internal/service/jailer"
	"mvmctl/internal/service/network"
)

func observeGuestControlAliases(
	ctx context.Context,
	p jailer.GuestControlPrincipal,
) (_ bool, _ []guestcontrolwire.FloatingIPAlias, returnErr error) {
	gate, err := hostadmission.AcquireOperation(ctx)
	if err != nil {
		return false, nil, err
	}
	defer func() { returnErr = errors.Join(returnErr, gate.Release(context.WithoutCancel(ctx))) }()
	if err := authorizeCaller(callerIdentity{uid: p.OwnerUID, gid: p.OwnerGID}, realAuthorizationDeps()); err != nil {
		return false, nil, err
	}
	admission, err := jailer.NewFloatingIPAdmissionServer().
		Open(ctx, p.OwnerUID, p.OwnerGID, jailer.FloatingIPAdmissionRequest{VMID: p.VMID})
	if err != nil {
		return false, nil, err
	}
	defer func() { returnErr = errors.Join(returnErr, admission.Release(context.WithoutCancel(ctx))) }()
	identity, err := admission.Identity(ctx)
	if err != nil {
		return false, nil, err
	}
	if err := requireGuestControlRequestIncarnation(p, identity); err != nil {
		return false, nil, err
	}
	hold, err := admission.RetainTopologyHold(ctx)
	if err != nil {
		return false, nil, err
	}
	defer func() { returnErr = errors.Join(returnErr, hold.Release(context.WithoutCancel(ctx))) }()
	vm, err := network.NewFloatingIPServer().
		OpenVM(ctx, hold, network.FloatingIPVMIncarnation{BootID: p.BootID, LaunchGeneration: p.LaunchGeneration, ExecutionUID: p.ExecutionUID})
	if err != nil {
		return false, nil, err
	}
	defer func() { returnErr = errors.Join(returnErr, vm.Release(context.WithoutCancel(ctx))) }()
	aliases, err := vm.OperatorFloatingIPAliases(ctx)
	if err != nil {
		return false, nil, err
	}
	active, err := vm.HasActiveGuestControlDelegations(ctx)
	return active, aliases, err
}

// waitOperatorFloatingIPAliasSync never retains VM, worker, network or firewall locks while waiting.
func waitOperatorFloatingIPAliasSync(ctx context.Context, uid, gid uint32, vmID string) error {
	return waitOperatorFloatingIPAliasAcknowledgement(ctx, uid, gid, vmID, false, "")
}
func waitOperatorFloatingIPAliasCleanup(ctx context.Context, uid, gid uint32, vmID, baseline string) error {
	return waitOperatorFloatingIPAliasAcknowledgement(ctx, uid, gid, vmID, true, baseline)
}

func waitOperatorFloatingIPAliasAcknowledgement(
	ctx context.Context,
	uid, gid uint32,
	vmID string,
	cleanup bool,
	baseline string,
) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		complete, err := operatorFloatingIPAliasSynchronized(ctx, uid, gid, vmID, cleanup, baseline)
		if err != nil {
			return err
		}
		if complete {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func operatorFloatingIPAliasSynchronized(
	ctx context.Context,
	uid, gid uint32,
	vmID string,
	cleanup bool,
	baseline string,
) (_ bool, returnErr error) {
	admission, err := jailer.NewFloatingIPAdmissionServer().
		Open(ctx, uid, gid, jailer.FloatingIPAdmissionRequest{VMID: vmID})
	if err != nil {
		return false, err
	}
	defer func() { returnErr = errors.Join(returnErr, admission.Release(context.WithoutCancel(ctx))) }()
	identity, err := admission.Identity(ctx)
	if err != nil {
		return false, err
	}
	hold, err := admission.RetainTopologyHold(ctx)
	if err != nil {
		return false, err
	}
	defer func() { returnErr = errors.Join(returnErr, hold.Release(context.WithoutCancel(ctx))) }()
	vm, err := network.NewFloatingIPServer().
		OpenVM(ctx, hold, network.FloatingIPVMIncarnation{BootID: identity.BootID, LaunchGeneration: identity.LaunchGeneration, ExecutionUID: identity.ExecutionUID})
	if err != nil {
		return false, err
	}
	aliases, observeErr := vm.OperatorFloatingIPAliases(ctx)
	releaseErr := vm.Release(context.WithoutCancel(ctx))
	if err := errors.Join(observeErr, releaseErr); err != nil {
		return false, err
	}
	// Release the network/global suffix before taking the sibling worker record lock.
	worker, err := jailer.NewGuestControlWorkerServer().Lock(ctx, admission)
	if err != nil {
		return false, err
	}
	defer func() { returnErr = errors.Join(returnErr, worker.Release(context.WithoutCancel(ctx))) }()
	ack, found, err := worker.FloatingIPAliasAcknowledgement(ctx)
	if cleanup && len(aliases) == 0 {
		ack, found, err = worker.FloatingIPAliasCleanupAcknowledgement(ctx)
	}
	if err != nil || !found {
		return false, err
	}
	if cleanup && ack.SnapshotID == baseline {
		return false, nil
	}
	digest, err := (guestcontrolwire.FloatingIPAliasSnapshot{ID: crypto.UUIDV4(), Aliases: aliases}).Digest()
	if err != nil {
		return false, err
	}
	return ack.Digest == digest, nil
}

// operatorFloatingIPAliasBaseline captures freshness before release; absence is an explicit empty baseline.
func operatorFloatingIPAliasBaseline(ctx context.Context, uid, gid uint32, vmID string) (_ string, returnErr error) {
	if vmID == "" {
		return "", nil
	}
	admission, err := jailer.NewFloatingIPAdmissionServer().
		Open(ctx, uid, gid, jailer.FloatingIPAdmissionRequest{VMID: vmID})
	if err != nil {
		return "", err
	}
	defer func() { returnErr = errors.Join(returnErr, admission.Release(context.WithoutCancel(ctx))) }()
	worker, err := jailer.NewGuestControlWorkerServer().Lock(ctx, admission)
	if err != nil {
		return "", err
	}
	defer func() { returnErr = errors.Join(returnErr, worker.Release(context.WithoutCancel(ctx))) }()
	ack, found, err := worker.FloatingIPAliasAcknowledgement(ctx)
	if err == nil && !found {
		ack, found, err = worker.FloatingIPAliasCleanupAcknowledgement(ctx)
	}
	if err != nil {
		return "", err
	}
	if !found {
		return "", nil
	}
	return ack.SnapshotID, nil
}
