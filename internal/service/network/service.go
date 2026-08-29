package network

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"mvmctl/pkg/errs"
)

type service struct {
	locker  NetworkLocker
	effects TopologyEffects
}

type topologyPreparationState uint8

const (
	topologyPreparationBuilding topologyPreparationState = iota
	topologyPreparationPrepared
	topologyPreparationCleaning
	topologyPreparationCleaned
)

// topologyPreparation retains the network lock and pinned namespace identity for later firewall, link activation,
// Jailer, and persistence integration. It cannot release a live topology: until those stages exist, Cleanup is the
// only terminal transition. It is not safe for concurrent use.
type topologyPreparation struct {
	effects TopologyEffects
	lock    NetworkLock
	pin     NamespacePin

	topology Topology

	namespaceCreated bool
	localBridgeMade  bool
	tapMade          bool
	vethMade         bool
	state            topologyPreparationState
}

// newService remains private until the complete launch transaction can provide a concrete held VM-lock capability.
func newService(locker NetworkLocker, effects TopologyEffects) *service {
	if locker == nil {
		panic("network topology locker is required")
	}
	if effects == nil {
		panic("network topology effects are required")
	}
	return &service{locker: locker, effects: effects}
}

// prepare creates the namespace, namespace-local bridge, TAP, and veth pair down and attaches them to their exact
// derived bridges. The returned preparation retains the final network lock and namespace pin and cannot be released
// without cleanup. No method in this package brings links up, installs firewall policy, or launches Jailer.
func (service *service) prepare(
	ctx context.Context,
	launch *vmLaunchLease,
	networkID NetworkID,
) (*topologyPreparation, error) {
	if err := ctx.Err(); err != nil {
		return nil, service.prepareError("admit topology transaction", "", err, false)
	}
	if launch == nil {
		return nil, service.prepareError(
			"admit topology transaction",
			"",
			topologyValidationError("vm launch lease is required"),
			false,
		)
	}
	if launch.lock == nil {
		return nil, service.prepareError(
			"admit topology transaction",
			string(launch.vmID),
			topologyValidationError("vm launch lease is not active"),
			false,
		)
	}
	key, err := newTopologyKey(
		uint32(launch.ownerUID),
		string(launch.vmID),
		string(networkID),
	)
	if err != nil {
		return nil, service.prepareError("validate topology identity", string(launch.vmID), err, false)
	}
	names := deriveTopologyNames(key)

	// CRITICAL: The VM authority owns the earlier lock classes. This is the only acquisition performed here, preserving
	// release -> global index -> VM -> network without a callback or a generic lock operation.
	lock, err := service.locker.LockNetwork(ctx, key.Network())
	if err != nil {
		return nil, service.prepareError("acquire network topology lock", string(key.vmID), err, false)
	}
	preparation := &topologyPreparation{
		effects: service.effects,
		lock:    lock,
		topology: Topology{
			key: key,
		},
		state: topologyPreparationBuilding,
	}
	if lock == nil {
		return service.abortPrepare(
			ctx,
			preparation,
			"verify network topology lock",
			errors.New("network lock is nil"),
		)
	}
	managedBridge := lock.ManagedBridge()
	if err := validateNetworkLock(lock.Key(), managedBridge, key.Network(), names.managedBridge); err != nil {
		return service.abortPrepare(ctx, preparation, "verify network topology lock", err)
	}
	preparation.topology.managedBridge = managedBridge

	pin, err := service.effects.CreateNamespace(ctx, namespaceCreateSpec{key: key, path: names.namespaceHandle})
	if pin != nil {
		preparation.pin = pin
		preparation.namespaceCreated = true
		preparation.topology.handle = pin.Handle()
	}
	if err != nil {
		return service.abortPrepare(ctx, preparation, "create VM network namespace", err)
	}
	if pin == nil {
		return service.abortPrepare(
			ctx,
			preparation,
			"pin VM network namespace",
			errors.New("namespace effect returned a nil pin"),
		)
	}
	if err := validateNamespaceHandle(preparation.topology.handle, names.namespaceHandle); err != nil {
		return service.abortPrepare(ctx, preparation, "verify VM network namespace pin", err)
	}
	if preparation.topology.managedBridge.namespace == preparation.topology.handle.identity {
		return service.abortPrepare(
			ctx,
			preparation,
			"verify managed bridge namespace",
			errors.New("managed bridge is not in the host namespace"),
		)
	}

	localBridge, err := service.effects.CreateLocalBridgeDown(
		ctx,
		pin,
		localBridgeCreateSpec{name: names.localBridge},
	)
	if localBridge != nil {
		preparation.localBridgeMade = true
		preparation.topology.localBridge = *localBridge
	}
	if err != nil {
		return service.abortPrepare(ctx, preparation, "create namespace-local bridge down", err)
	}
	if localBridge == nil {
		return service.abortPrepare(
			ctx,
			preparation,
			"create namespace-local bridge down",
			errors.New("local bridge effect returned no identity"),
		)
	}
	if err := validateLinkIdentity(
		*localBridge,
		names.localBridge,
		preparation.topology.handle.identity,
		"namespace-local bridge",
	); err != nil {
		return service.abortPrepare(ctx, preparation, "verify namespace-local bridge", err)
	}

	tap, err := service.effects.CreateTAPDown(
		ctx,
		pin,
		tapCreateSpec{name: names.tap, ownerUID: key.ownerUID},
	)
	if tap != nil {
		preparation.tapMade = true
		preparation.topology.tap = *tap
	}
	if err != nil {
		return service.abortPrepare(ctx, preparation, "create namespace-local TAP down", err)
	}
	if tap == nil {
		return service.abortPrepare(
			ctx,
			preparation,
			"create namespace-local TAP down",
			errors.New("tap effect returned no identity"),
		)
	}
	if err := validateLinkIdentity(
		*tap,
		names.tap,
		preparation.topology.handle.identity,
		"namespace-local TAP",
	); err != nil {
		return service.abortPrepare(ctx, preparation, "verify namespace-local TAP", err)
	}
	if tap.ifIndex == localBridge.ifIndex {
		return service.abortPrepare(
			ctx,
			preparation,
			"verify namespace-local TAP",
			errors.New("tap reuses bridge ifindex"),
		)
	}

	veth, err := service.effects.CreateVethPairDown(
		ctx,
		pin,
		vethCreateSpec{hostName: names.hostVeth, peerName: names.namespaceVeth},
	)
	if veth != nil {
		preparation.vethMade = true
		preparation.topology.veth = *veth
	}
	if err != nil {
		return service.abortPrepare(ctx, preparation, "create VM veth pair down", err)
	}
	if veth == nil {
		return service.abortPrepare(
			ctx,
			preparation,
			"create VM veth pair down",
			errors.New("veth effect returned no identity"),
		)
	}
	if err := validateVethPair(
		*veth,
		names,
		preparation.topology.managedBridge.namespace,
		preparation.topology.handle.identity,
	); err != nil {
		return service.abortPrepare(ctx, preparation, "verify VM veth pair", err)
	}
	if duplicateIfIndex(
		localBridge.ifIndex,
		tap.ifIndex,
		veth.host.ifIndex,
		veth.peer.ifIndex,
	) {
		return service.abortPrepare(
			ctx,
			preparation,
			"verify VM link identities",
			errors.New("link ifindexes are not unique"),
		)
	}

	if err := service.effects.AttachNamespaceVethToLocalBridge(ctx, pin, *veth, *localBridge); err != nil {
		return service.abortPrepare(ctx, preparation, "attach namespace veth to local bridge", err)
	}
	if err := service.effects.AttachTAPToLocalBridge(ctx, pin, *tap, *localBridge); err != nil {
		return service.abortPrepare(ctx, preparation, "attach TAP to local bridge", err)
	}
	if err := service.effects.AttachHostVethToManagedBridge(
		ctx,
		*veth,
		preparation.topology.managedBridge,
	); err != nil {
		return service.abortPrepare(ctx, preparation, "attach host veth to managed bridge", err)
	}

	preparation.state = topologyPreparationPrepared
	return preparation, nil
}

// Topology returns the exact identities established by the transaction.
func (preparation *topologyPreparation) Topology() Topology {
	if preparation == nil {
		return Topology{}
	}
	return preparation.topology
}

// Cleanup removes only the exact identities created by this preparation, in reverse dependency order. Successful
// steps are remembered, making retry and repeated cleanup idempotent without probing caller-controlled names.
func (preparation *topologyPreparation) Cleanup(ctx context.Context) error {
	if preparation == nil || preparation.state == topologyPreparationCleaned {
		return nil
	}
	preparation.state = topologyPreparationCleaning
	if preparation.vethMade {
		if err := preparation.effects.DeleteVethPair(
			ctx,
			preparation.pin,
			preparation.topology.veth,
		); err != nil {
			return preparation.cleanupFailure("delete VM veth pair", err)
		}
		preparation.vethMade = false
	}
	if preparation.tapMade {
		if err := preparation.effects.DeleteTAP(ctx, preparation.pin, preparation.topology.tap); err != nil {
			return preparation.cleanupFailure("delete namespace-local TAP", err)
		}
		preparation.tapMade = false
	}
	if preparation.localBridgeMade {
		if err := preparation.effects.DeleteLocalBridge(
			ctx,
			preparation.pin,
			preparation.topology.localBridge,
		); err != nil {
			return preparation.cleanupFailure("delete namespace-local bridge", err)
		}
		preparation.localBridgeMade = false
	}
	if preparation.namespaceCreated {
		if err := preparation.effects.DeleteNamespace(
			ctx,
			preparation.pin,
			preparation.topology.handle.identity,
		); err != nil {
			return preparation.cleanupFailure("delete VM network namespace", err)
		}
		preparation.namespaceCreated = false
	}
	if err := preparation.releaseAuthorities(ctx); err != nil {
		return err
	}
	preparation.state = topologyPreparationCleaned
	return nil
}

func (service *service) abortPrepare(
	ctx context.Context,
	preparation *topologyPreparation,
	stage string,
	cause error,
) (*topologyPreparation, error) {
	// CRITICAL: Cancellation at the failure point must not suppress rollback. The retained typed lease and identities
	// bound every cleanup effect; context.WithoutCancel does not broaden the cleanup target.
	cleanupErr := preparation.Cleanup(context.WithoutCancel(ctx))
	if cleanupErr != nil {
		combined := errors.Join(cause, cleanupErr)
		return preparation, service.prepareError(stage, preparation.entity(), combined, true)
	}
	return nil, service.prepareError(stage, preparation.entity(), cause, false)
}

func (service *service) prepareError(stage, vmID string, cause error, cleanupRequired bool) *errs.DomainError {
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

func (preparation *topologyPreparation) cleanupFailure(stage string, cause error) *errs.DomainError {
	slog.Error(
		"network namespace topology cleanup failed",
		"stage", stage,
		"vm_id", preparation.entity(),
		"error", cause,
	)
	return topologyRemoveError(preparation.entity(), "failed to "+stage, cause)
}

func (preparation *topologyPreparation) releaseAuthorities(ctx context.Context) error {
	if preparation.pin != nil {
		if err := preparation.pin.Release(ctx); err != nil {
			slog.Error(
				"network topology namespace pin release failed",
				"vm_id", preparation.entity(),
				"error", err,
			)
			return topologyRemoveError(preparation.entity(), "failed to release network namespace pin", err)
		}
		preparation.pin = nil
	}
	if preparation.lock != nil {
		if err := preparation.lock.Release(ctx); err != nil {
			slog.Error(
				"network topology lock release failed",
				"vm_id", preparation.entity(),
				"error", err,
			)
			return topologyRemoveError(preparation.entity(), "failed to release network topology lock", err)
		}
		preparation.lock = nil
	}
	return nil
}

func (preparation *topologyPreparation) entity() string {
	if preparation == nil {
		return ""
	}
	return string(preparation.topology.key.vmID)
}

func validateNetworkLock(
	actualKey networkKey,
	bridge linkIdentity,
	expectedKey networkKey,
	expectedName linkName,
) error {
	if err := validateNetworkKey(actualKey); err != nil {
		return err
	}
	if actualKey != expectedKey {
		return errors.New("network lock identity does not match requested topology")
	}
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
