package network

import (
	"context"
	"errors"

	"golang.org/x/sys/unix"

	"mvmctl/internal/lib/authorityfs"
	"mvmctl/internal/service/firewall"
)

// ForgetVMEndpoint invalidates stopped endpoint policy only during final VM removal.
// It borrows the caller's real VM hold; the caller retains responsibility for releasing that hold afterward.
// Ordinary stop and topology Cleanup deliberately preserve these immutable reservations and grants.
func ForgetVMEndpoint(ctx context.Context, hold *authorityfs.TopologyHold, launchGeneration uint64) (returnErr error) {
	if ctx == nil || hold == nil || launchGeneration == 0 {
		return networkAuthorityValidationError("endpoint invalidation requires VM hold and launch generation")
	}
	owner, vm, err := hold.Binding()
	if err != nil {
		return err
	}
	journal, err := openOwnedTopologyJournal(ctx, owner, VMID(vm.String()))
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() {
		returnErr = appendNetworkAuthorityError(returnErr, "close invalidated endpoint journal", journal.store.Release(context.WithoutCancel(ctx)))
	}()
	record, found, err := journal.read(ctx)
	if err != nil || !found {
		return err
	}
	if record.Lifecycle != topologyCleaned || record.LaunchGeneration != launchGeneration {
		return networkAuthorityCorruptError("endpoint invalidation requires exact cleaned topology generation", nil)
	}
	key, err := newTopologyKey(owner, vm.String(), record.NetworkID)
	if err != nil {
		return err
	}
	locked, err := (managedNetworkLocker{cleanup: true}).LockNetwork(ctx, key.Network())
	if err != nil {
		return err
	}
	network := locked.(*managedNetworkLease)
	defer func() {
		returnErr = appendNetworkAuthorityError(returnErr, "release invalidated endpoint network", network.Release(context.WithoutCancel(ctx)))
	}()
	if network.record.bootID != record.BootID {
		return networkAuthorityCorruptError("endpoint invalidation network boot identity changed", nil)
	}
	namespace := network.record.managedBridge.namespace
	mutation, err := firewall.BeginPolicyChange(ctx, network.mutation, firewall.PolicyBinding{
		OwnerUID: owner, NetworkID: record.NetworkID, BootID: record.BootID,
		HostNamespace: firewall.NamespaceIdentity{Device: namespace.device, Inode: namespace.inode},
	})
	if err != nil {
		return err
	}
	defer func() {
		returnErr = appendNetworkAuthorityError(returnErr, "release invalidated endpoint firewall", mutation.Release(context.WithoutCancel(ctx)))
	}()
	return mutation.RemoveVMPolicies(ctx, record.VMID)
}
