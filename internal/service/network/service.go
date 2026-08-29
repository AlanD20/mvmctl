package network

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"mvmctl/pkg/errs"
)

// Service coordinates one fixed per-VM namespace topology without owning transport, filesystem, or netlink details.
type Service struct {
	locker  NetworkLocker
	effects TopologyEffects
}

// TopologyLease retains the network lock and pinned namespace identity across firewall installation, link activation,
// Jailer handoff, and durable launch registration. It is not safe for concurrent use.
type TopologyLease struct {
	effects TopologyEffects
	lock    NetworkLock
	pin     NamespacePin

	topology Topology

	namespaceCreated bool
	localBridgeMade  bool
	tapMade          bool
	vethMade         bool
	complete         bool
	cleaned          bool
	released         bool
}

// NewService constructs the closed topology transaction service.
func NewService(locker NetworkLocker, effects TopologyEffects) *Service {
	if locker == nil {
		panic("network topology locker is required")
	}
	if effects == nil {
		panic("network topology effects are required")
	}
	return &Service{locker: locker, effects: effects}
}

// Create creates the namespace, namespace-local bridge, TAP, and veth pair down and attaches them to their exact
// derived bridges. The returned lease deliberately retains the final network lock and namespace pin. No method in this
// package brings links up, installs firewall policy, or launches Jailer.
func (service *Service) Create(
	ctx context.Context,
	launch VMLaunchLease,
	networkID NetworkID,
) (*TopologyLease, error) {
	if err := ctx.Err(); err != nil {
		return nil, service.createError("admit topology transaction", "", err, false)
	}
	if launch == nil {
		return nil, service.createError(
			"admit topology transaction",
			"",
			topologyValidationError("VM launch lease is required"),
			false,
		)
	}
	if !launch.NetworkVMLockHeld() {
		return nil, service.createError(
			"admit topology transaction",
			string(launch.NetworkVMID()),
			topologyValidationError("VM launch lease is not active"),
			false,
		)
	}
	key, err := NewTopologyKey(
		uint32(launch.NetworkOwnerUID()),
		string(launch.NetworkVMID()),
		string(networkID),
	)
	if err != nil {
		return nil, service.createError("validate topology identity", string(launch.NetworkVMID()), err, false)
	}
	names := deriveTopologyNames(key)

	// CRITICAL: The VM authority owns the earlier lock classes. This is the only acquisition performed here, preserving
	// release -> global index -> VM -> network without a callback or a generic lock operation.
	lock, err := service.locker.LockNetwork(ctx, key.Network())
	if err != nil {
		return nil, service.createError("acquire network topology lock", string(key.vmID), err, false)
	}
	lease := &TopologyLease{
		effects: service.effects,
		lock:    lock,
		topology: Topology{
			key: key,
		},
	}
	if lock == nil {
		return service.abortCreate(ctx, lease, "verify network topology lock", errors.New("network lock is nil"))
	}
	if err := validateNetworkLock(lock, key.Network(), names.managedBridge); err != nil {
		return service.abortCreate(ctx, lease, "verify network topology lock", err)
	}
	lease.topology.managedBridge = lock.ManagedBridge()

	pin, err := service.effects.CreateNamespace(ctx, namespaceCreateSpec{key: key, path: names.namespaceHandle})
	if err != nil {
		return service.abortCreate(ctx, lease, "create VM network namespace", err)
	}
	if pin == nil {
		return service.abortCreate(
			ctx,
			lease,
			"pin VM network namespace",
			errors.New("namespace effect returned a nil pin"),
		)
	}
	lease.pin = pin
	lease.namespaceCreated = true
	lease.topology.handle = pin.Handle()
	if err := validateNamespaceHandle(lease.topology.handle, names.namespaceHandle); err != nil {
		return service.abortCreate(ctx, lease, "verify VM network namespace pin", err)
	}
	if lease.topology.managedBridge.namespace == lease.topology.handle.identity {
		return service.abortCreate(
			ctx,
			lease,
			"verify managed bridge namespace",
			errors.New("managed bridge is not in the host namespace"),
		)
	}

	localBridge, err := service.effects.CreateLocalBridgeDown(
		ctx,
		pin,
		localBridgeCreateSpec{name: names.localBridge},
	)
	if err != nil {
		return service.abortCreate(ctx, lease, "create namespace-local bridge down", err)
	}
	lease.localBridgeMade = true
	lease.topology.localBridge = localBridge
	if err := validateLinkIdentity(
		localBridge,
		names.localBridge,
		lease.topology.handle.identity,
		"namespace-local bridge",
	); err != nil {
		return service.abortCreate(ctx, lease, "verify namespace-local bridge", err)
	}

	tap, err := service.effects.CreateTAPDown(
		ctx,
		pin,
		tapCreateSpec{name: names.tap, ownerUID: key.ownerUID},
	)
	if err != nil {
		return service.abortCreate(ctx, lease, "create namespace-local TAP down", err)
	}
	lease.tapMade = true
	lease.topology.tap = tap
	if err := validateLinkIdentity(tap, names.tap, lease.topology.handle.identity, "namespace-local TAP"); err != nil {
		return service.abortCreate(ctx, lease, "verify namespace-local TAP", err)
	}
	if tap.ifIndex == localBridge.ifIndex {
		return service.abortCreate(ctx, lease, "verify namespace-local TAP", errors.New("TAP reuses bridge ifindex"))
	}

	veth, err := service.effects.CreateVethPairDown(
		ctx,
		pin,
		vethCreateSpec{hostName: names.hostVeth, peerName: names.namespaceVeth},
	)
	if err != nil {
		return service.abortCreate(ctx, lease, "create VM veth pair down", err)
	}
	lease.vethMade = true
	lease.topology.veth = veth
	if err := validateVethPair(
		veth,
		names,
		lease.topology.managedBridge.namespace,
		lease.topology.handle.identity,
	); err != nil {
		return service.abortCreate(ctx, lease, "verify VM veth pair", err)
	}
	if duplicateIfIndex(
		localBridge.ifIndex,
		tap.ifIndex,
		veth.host.ifIndex,
		veth.peer.ifIndex,
	) {
		return service.abortCreate(ctx, lease, "verify VM link identities", errors.New("link ifindexes are not unique"))
	}

	if err := service.effects.AttachNamespaceVethToLocalBridge(ctx, pin, veth, localBridge); err != nil {
		return service.abortCreate(ctx, lease, "attach namespace veth to local bridge", err)
	}
	if err := service.effects.AttachTAPToLocalBridge(ctx, pin, tap, localBridge); err != nil {
		return service.abortCreate(ctx, lease, "attach TAP to local bridge", err)
	}
	if err := service.effects.AttachHostVethToManagedBridge(
		ctx,
		veth,
		lease.topology.managedBridge,
	); err != nil {
		return service.abortCreate(ctx, lease, "attach host veth to managed bridge", err)
	}

	lease.complete = true
	return lease, nil
}

// Topology returns the exact identities established by the transaction.
func (lease *TopologyLease) Topology() Topology {
	if lease == nil {
		return Topology{}
	}
	return lease.topology
}

// Release hands off a complete topology without deleting it. It is idempotent and closes the namespace pin before
// releasing the later network lock.
func (lease *TopologyLease) Release(ctx context.Context) error {
	if lease == nil || lease.released {
		return nil
	}
	if !lease.complete && !lease.cleaned {
		return topologyRemoveError(
			lease.entity(),
			"cannot release incomplete topology; cleanup is required",
			nil,
		)
	}
	if err := lease.releasePin(ctx); err != nil {
		slog.Error("network topology namespace pin release failed", "vm_id", lease.entity(), "error", err)
		return topologyRemoveError(lease.entity(), "failed to release network namespace pin", err)
	}
	if err := lease.releaseLock(ctx); err != nil {
		slog.Error("network topology lock release failed", "vm_id", lease.entity(), "error", err)
		return topologyRemoveError(lease.entity(), "failed to release network topology lock", err)
	}
	lease.released = true
	return nil
}

// Cleanup removes only the exact identities created by this lease, in reverse dependency order. Successful steps are
// remembered, making retry and repeated cleanup idempotent without probing caller-controlled names.
func (lease *TopologyLease) Cleanup(ctx context.Context) error {
	if lease == nil || lease.cleaned {
		return nil
	}
	if lease.released {
		return topologyRemoveError(
			lease.entity(),
			"cannot clean topology after its authority lease was released",
			nil,
		)
	}
	if lease.vethMade {
		if err := lease.effects.DeleteVethPair(ctx, lease.pin, lease.topology.veth); err != nil {
			return lease.cleanupFailure("delete VM veth pair", err)
		}
		lease.vethMade = false
	}
	if lease.tapMade {
		if err := lease.effects.DeleteTAP(ctx, lease.pin, lease.topology.tap); err != nil {
			return lease.cleanupFailure("delete namespace-local TAP", err)
		}
		lease.tapMade = false
	}
	if lease.localBridgeMade {
		if err := lease.effects.DeleteLocalBridge(ctx, lease.pin, lease.topology.localBridge); err != nil {
			return lease.cleanupFailure("delete namespace-local bridge", err)
		}
		lease.localBridgeMade = false
	}
	if lease.namespaceCreated {
		if err := lease.effects.DeleteNamespace(ctx, lease.pin, lease.topology.handle.identity); err != nil {
			return lease.cleanupFailure("delete VM network namespace", err)
		}
		lease.namespaceCreated = false
	}
	lease.cleaned = true
	if err := lease.Release(ctx); err != nil {
		lease.cleaned = false
		return err
	}
	return nil
}

func (service *Service) abortCreate(
	ctx context.Context,
	lease *TopologyLease,
	stage string,
	cause error,
) (*TopologyLease, error) {
	// CRITICAL: Cancellation at the failure point must not suppress rollback. The retained typed lease and identities
	// bound every cleanup effect; context.WithoutCancel does not broaden the cleanup target.
	cleanupErr := lease.Cleanup(context.WithoutCancel(ctx))
	if cleanupErr != nil {
		combined := errors.Join(cause, cleanupErr)
		return lease, service.createError(stage, lease.entity(), combined, true)
	}
	return nil, service.createError(stage, lease.entity(), cause, false)
}

func (service *Service) createError(stage, vmID string, cause error, cleanupRequired bool) *errs.DomainError {
	message := "failed to " + stage
	if cause == nil {
		cause = errors.New(message)
	}
	slog.Error("network namespace topology transaction failed", "stage", stage, "vm_id", vmID, "error", cause)
	options := []errs.ErrorOption{errs.WithClass(errs.ClassInternal)}
	if vmID != "" {
		options = append(options, errs.WithEntity(vmID))
	}
	if cleanupRequired {
		options = append(options, errs.WithDetails(map[string]any{"cleanup_required": true}))
	}
	return errs.WrapMsg(errs.CodeNetworkCreateFailed, message, cause, options...)
}

func (lease *TopologyLease) cleanupFailure(stage string, cause error) *errs.DomainError {
	slog.Error("network namespace topology cleanup failed", "stage", stage, "vm_id", lease.entity(), "error", cause)
	return topologyRemoveError(lease.entity(), "failed to "+stage, cause)
}

func (lease *TopologyLease) releasePin(ctx context.Context) error {
	if lease.pin == nil {
		return nil
	}
	if err := lease.pin.Release(ctx); err != nil {
		return err
	}
	lease.pin = nil
	return nil
}

func (lease *TopologyLease) releaseLock(ctx context.Context) error {
	if lease.lock == nil {
		return nil
	}
	if err := lease.lock.Release(ctx); err != nil {
		return err
	}
	lease.lock = nil
	return nil
}

func (lease *TopologyLease) entity() string {
	if lease == nil {
		return ""
	}
	return string(lease.topology.key.vmID)
}

func validateNetworkLock(lock NetworkLock, expected networkKey, expectedName linkName) error {
	if err := validateNetworkKey(lock.Key()); err != nil {
		return err
	}
	if lock.Key() != expected {
		return errors.New("network lock identity does not match requested topology")
	}
	bridge := lock.ManagedBridge()
	if bridge.name != expectedName {
		return errors.New("managed bridge name does not match derived network identity")
	}
	if bridge.ifIndex <= 0 || !validNamespaceIdentity(bridge.namespace) {
		return errors.New("managed bridge kernel identity is invalid")
	}
	return nil
}

func validateNamespaceHandle(handle namespaceHandle, expectedPath namespaceHandlePath) error {
	if handle.path != expectedPath {
		return errors.New("namespace handle path does not match derived VM identity")
	}
	if handle.ownerUID != 0 || handle.ownerGID != 0 || handle.mode != namespaceHandleMode {
		return errors.New("namespace handle is not root-owned with the required mode")
	}
	if !validNamespaceIdentity(handle.identity) {
		return errors.New("namespace handle identity is invalid")
	}
	return nil
}

func validateLinkIdentity(
	identity linkIdentity,
	expectedName linkName,
	expectedNamespace namespaceIdentity,
	description string,
) error {
	if identity.name != expectedName {
		return fmt.Errorf("%s name does not match derived identity", description)
	}
	if identity.ifIndex <= 0 {
		return fmt.Errorf("%s ifindex is invalid", description)
	}
	if identity.namespace != expectedNamespace || !validNamespaceIdentity(identity.namespace) {
		return fmt.Errorf("%s namespace identity is invalid", description)
	}
	return nil
}

func validateVethPair(
	veth vethPairIdentity,
	names topologyNames,
	hostNamespace namespaceIdentity,
	vmNamespace namespaceIdentity,
) error {
	if err := validateLinkIdentity(veth.host, names.hostVeth, hostNamespace, "host veth"); err != nil {
		return err
	}
	if err := validateLinkIdentity(veth.peer, names.namespaceVeth, vmNamespace, "namespace veth"); err != nil {
		return err
	}
	if veth.host.ifIndex == veth.peer.ifIndex {
		return errors.New("veth pair reuses one ifindex")
	}
	return nil
}

func validNamespaceIdentity(identity namespaceIdentity) bool {
	return identity.device != 0 && identity.inode != 0
}

func duplicateIfIndex(values ...int) bool {
	seen := make(map[int]struct{}, len(values))
	for _, value := range values {
		if _, exists := seen[value]; exists {
			return true
		}
		seen[value] = struct{}{}
	}
	return false
}

func topologyRemoveError(vmID, message string, cause error) *errs.DomainError {
	options := []errs.ErrorOption{errs.WithClass(errs.ClassInternal)}
	if vmID != "" {
		options = append(options, errs.WithEntity(vmID))
	}
	if cause == nil {
		return errs.New(errs.CodeNetworkRemoveFailed, message, options...)
	}
	return errs.WrapMsg(errs.CodeNetworkRemoveFailed, message, cause, options...)
}
