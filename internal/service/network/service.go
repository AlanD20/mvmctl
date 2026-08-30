package network

import (
	"context"
	"errors"
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
	topologyPreparationCommitted
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
		return nil, topologyPrepareError("admit topology transaction", "", err, false)
	}
	if launch == nil {
		return nil, topologyPrepareError(
			"admit topology transaction",
			"",
			topologyValidationError("vm launch lease is required"),
			false,
		)
	}
	if launch.lock == nil {
		return nil, topologyPrepareError(
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
		return nil, topologyPrepareError("validate topology identity", string(launch.vmID), err, false)
	}
	names := deriveTopologyNames(key)

	// CRITICAL: The VM authority owns the earlier lock classes. This is the only acquisition performed here, preserving
	// release -> global index -> VM -> network without a callback or a generic lock operation.
	lock, err := service.locker.LockNetwork(ctx, key.Network())
	preparation := &topologyPreparation{
		effects: service.effects,
		lock:    lock,
		topology: Topology{
			key: key,
		},
		state: topologyPreparationBuilding,
	}
	if err != nil {
		return abortTopologyPreparePreservingPrimary(ctx, preparation, "acquire network topology lock", err)
	}
	if lock == nil {
		return abortTopologyPrepare(
			ctx,
			preparation,
			"verify network topology lock",
			errors.New("network lock is nil"),
		)
	}
	managedBridge := lock.ManagedBridge()
	if err := validateNetworkLock(lock.Key(), managedBridge, key.Network(), names.managedBridge); err != nil {
		return abortTopologyPrepare(ctx, preparation, "verify network topology lock", err)
	}
	preparation.topology.managedBridge = managedBridge

	pin, err := service.effects.CreateNamespace(ctx, namespaceCreateSpec{key: key, path: names.namespaceHandle})
	if pin != nil {
		preparation.pin = pin
		preparation.namespaceCreated = true
		preparation.topology.handle = pin.Handle()
	}
	if err != nil {
		return abortTopologyPrepare(ctx, preparation, "create VM network namespace", err)
	}
	if pin == nil {
		return abortTopologyPrepare(
			ctx,
			preparation,
			"pin VM network namespace",
			errors.New("namespace effect returned a nil pin"),
		)
	}
	if err := validateNamespaceHandle(preparation.topology.handle, names.namespaceHandle); err != nil {
		return abortTopologyPrepare(ctx, preparation, "verify VM network namespace pin", err)
	}
	if preparation.topology.managedBridge.namespace == preparation.topology.handle.identity {
		return abortTopologyPrepare(
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
		return abortTopologyPrepare(ctx, preparation, "create namespace-local bridge down", err)
	}
	if localBridge == nil {
		return abortTopologyPrepare(
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
		return abortTopologyPrepare(ctx, preparation, "verify namespace-local bridge", err)
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
		return abortTopologyPrepare(ctx, preparation, "create namespace-local TAP down", err)
	}
	if tap == nil {
		return abortTopologyPrepare(
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
		return abortTopologyPrepare(ctx, preparation, "verify namespace-local TAP", err)
	}
	if tap.ifIndex == localBridge.ifIndex {
		return abortTopologyPrepare(
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
		return abortTopologyPrepare(ctx, preparation, "create VM veth pair down", err)
	}
	if veth == nil {
		return abortTopologyPrepare(
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
		return abortTopologyPrepare(ctx, preparation, "verify VM veth pair", err)
	}
	if duplicateIfIndex(
		localBridge.ifIndex,
		tap.ifIndex,
		veth.host.ifIndex,
		veth.peer.ifIndex,
	) {
		return abortTopologyPrepare(
			ctx,
			preparation,
			"verify VM link identities",
			errors.New("link ifindexes are not unique"),
		)
	}

	if err := service.effects.AttachNamespaceVethToLocalBridge(ctx, pin, *veth, *localBridge); err != nil {
		return abortTopologyPrepare(ctx, preparation, "attach namespace veth to local bridge", err)
	}
	if err := service.effects.AttachTAPToLocalBridge(ctx, pin, *tap, *localBridge); err != nil {
		return abortTopologyPrepare(ctx, preparation, "attach TAP to local bridge", err)
	}
	if err := service.effects.AttachHostVethToManagedBridge(
		ctx,
		*veth,
		preparation.topology.managedBridge,
	); err != nil {
		return abortTopologyPrepare(ctx, preparation, "attach host veth to managed bridge", err)
	}

	preparation.state = topologyPreparationCommitted
	return preparation, nil
}

// Topology returns the exact identities only while every preparation stage is complete and its authority remains held.
func (preparation *topologyPreparation) Topology() (Topology, error) {
	if preparation == nil ||
		preparation.state != topologyPreparationCommitted ||
		preparation.lock == nil ||
		preparation.pin == nil ||
		!preparation.namespaceCreated ||
		!preparation.localBridgeMade ||
		!preparation.tapMade ||
		!preparation.vethMade {
		vmID := ""
		if preparation != nil {
			vmID = preparation.entity()
		}
		err := topologyAccessError(vmID)
		slog.Error("network topology authority access rejected", "vm_id", vmID, "error", err)
		return Topology{}, err
	}
	return preparation.topology, nil
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
