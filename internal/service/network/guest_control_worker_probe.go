package network

import (
	"context"
	"errors"

	"golang.org/x/sys/unix"

	"mvmctl/internal/lib/authorityfs"
)

// HasCurrentGuestControlDelegations is a read-only startup hint beneath genuine current VM authority.
// It creates no directories, retains no network/global lock and grants no feature authority. Bootstrap
// and each request still reopen the exact network/resource/grant admission before using this hint.
func (server *FloatingIPServer) HasCurrentGuestControlDelegations(
	ctx context.Context, hold *authorityfs.TopologyHold, incarnation FloatingIPVMIncarnation,
) (_ bool, returnErr error) {
	if ctx == nil || hold == nil || !bootIDPattern.MatchString(incarnation.BootID) ||
		incarnation.LaunchGeneration == 0 ||
		incarnation.ExecutionUID == 0 {
		return false, networkAuthorityValidationError("worker grant probe requires current VM authority")
	}
	uid, vm, err := hold.Binding()
	if err != nil {
		return false, err
	}
	boot, err := readCurrentBootID(ctx)
	if err != nil {
		return false, err
	}
	if boot != incarnation.BootID || uid == incarnation.ExecutionUID {
		return false, networkAuthorityValidationError("worker grant probe incarnation is invalid")
	}
	root, err := openHostResetNetworkStore(ctx)
	if err != nil {
		return false, err
	}
	defer func() { returnErr = errors.Join(returnErr, root.Release(context.WithoutCancel(ctx))) }()
	if root == nil {
		return false, nil
	}
	owner, err := root.openUID(ctx, uid, false)
	if errors.Is(err, unix.ENOENT) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer func() { returnErr = errors.Join(returnErr, owner.Release(context.WithoutCancel(ctx))) }()
	grants, err := openFloatingIPDelegationStore(ctx, owner, false)
	if err != nil {
		return false, err
	}
	defer func() { returnErr = errors.Join(returnErr, grants.release(context.WithoutCancel(ctx))) }()
	records, err := grants.list(ctx)
	if err != nil {
		return false, err
	}
	return currentGuestControlDelegationExists(records, uid, vm.String(), incarnation), nil
}

func currentGuestControlDelegationExists(
	records []floatingIPDelegationRecord,
	uid uint32,
	vmID string,
	incarnation FloatingIPVMIncarnation,
) bool {
	for _, r := range records {
		if r.State == floatingIPDelegationActive && r.OwnerUID == uid && r.VMID == vmID &&
			r.BootID == incarnation.BootID &&
			r.LaunchGeneration == incarnation.LaunchGeneration &&
			r.ExecutionUID == incarnation.ExecutionUID {
			return true
		}
	}
	return false
}
