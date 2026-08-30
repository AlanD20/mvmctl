package network

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"mvmctl/pkg/errs"
)

func abortTopologyPrepare(
	ctx context.Context,
	preparation *topologyPreparation,
	stage string,
	cause error,
) (*topologyPreparation, error) {
	// CRITICAL: Cancellation at the failure point must not suppress rollback. The retained typed lease and identities
	// bound every cleanup effect; context.WithoutCancel does not broaden the cleanup target.
	cleanupErr := preparation.Cleanup(context.WithoutCancel(ctx))
	if cleanupErr != nil {
		return preparation, topologyPrepareError(
			stage,
			preparation.entity(),
			errors.Join(cause, cleanupErr),
			true,
		)
	}
	return nil, topologyPrepareError(stage, preparation.entity(), cause, false)
}

func abortTopologyPreparePreservingPrimary(
	ctx context.Context,
	preparation *topologyPreparation,
	stage string,
	primary error,
) (*topologyPreparation, error) {
	// CRITICAL: A lock returned with an error remains authority owned by this transaction. Rollback is detached from
	// caller cancellation, while an already-classified DomainError remains the authoritative failure returned upstream.
	cleanupErr := preparation.Cleanup(context.WithoutCancel(ctx))
	domainErr := errs.AsDomainError(primary)
	if domainErr == nil {
		if cleanupErr != nil {
			return preparation, topologyPrepareError(
				stage,
				preparation.entity(),
				errors.Join(primary, cleanupErr),
				true,
			)
		}
		return nil, topologyPrepareError(stage, preparation.entity(), primary, false)
	}

	slog.Error(
		"network namespace topology transaction failed",
		"stage", stage,
		"vm_id", preparation.entity(),
		"error", primary,
	)
	if cleanupErr == nil {
		return nil, domainErr
	}
	if domainErr.Details == nil {
		domainErr.Details = make(map[string]any)
	}
	domainErr.Details["cleanup_required"] = true
	domainErr.Message += "; failed to release network topology authority: " + cleanupErr.Error()
	domainErr.Err = errors.Join(domainErr.Err, cleanupErr)
	return preparation, domainErr
}

func topologyPrepareError(stage, vmID string, cause error, cleanupRequired bool) *errs.DomainError {
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
	return nil
}

func validateTopologyIdentities(topology Topology, names topologyNames) error {
	if err := validateTopologyKey(topology.key); err != nil {
		return err
	}
	if topology.managedBridge.name != names.managedBridge ||
		topology.managedBridge.ifIndex <= 0 ||
		!validNamespaceIdentity(topology.managedBridge.namespace) {
		return errors.New("managed bridge identity is invalid")
	}
	if err := validateNamespaceHandle(topology.handle, names.namespaceHandle); err != nil {
		return err
	}
	if topology.managedBridge.namespace == topology.handle.identity {
		return errors.New("managed bridge is not in the host namespace")
	}
	if err := validateLinkIdentity(
		topology.localBridge,
		names.localBridge,
		topology.handle.identity,
		"namespace-local bridge",
	); err != nil {
		return err
	}
	if err := validateLinkIdentity(
		topology.tap,
		names.tap,
		topology.handle.identity,
		"namespace-local TAP",
	); err != nil {
		return err
	}
	if err := validateVethPair(
		topology.veth,
		names,
		topology.managedBridge.namespace,
		topology.handle.identity,
	); err != nil {
		return err
	}
	// Linux ifindexes are namespace-local. Reject collisions only among links sharing one pinned namespace.
	if duplicateIfIndex(topology.localBridge.ifIndex, topology.tap.ifIndex, topology.veth.peer.ifIndex) {
		return errors.New("vm namespace link ifindexes are not unique")
	}
	if topology.managedBridge.ifIndex == topology.veth.host.ifIndex {
		return errors.New("host namespace link ifindexes are not unique")
	}
	return nil
}

func validateCommittedTopology(preparation *topologyPreparation) error {
	if preparation == nil {
		return errors.New("topology preparation is nil")
	}
	if preparation.state != topologyPreparationCommitted {
		return errors.New("topology preparation is not committed")
	}
	if preparation.lock == nil || preparation.pin == nil {
		return errors.New("topology authority lease is missing")
	}
	if !preparation.namespaceCreated ||
		!preparation.localBridgeMade ||
		!preparation.tapMade ||
		!preparation.vethMade {
		return errors.New("topology preparation is partial")
	}
	if err := validateTopologyKey(preparation.topology.key); err != nil {
		return err
	}

	names := deriveTopologyNames(preparation.topology.key)
	lockedBridge := preparation.lock.ManagedBridge()
	if err := validateNetworkLock(
		preparation.lock.Key(),
		lockedBridge,
		preparation.topology.key.Network(),
		names.managedBridge,
	); err != nil {
		return err
	}
	if preparation.topology.managedBridge != lockedBridge {
		return errors.New("topology managed bridge no longer matches retained network authority")
	}

	pinnedHandle := preparation.pin.Handle()
	if err := validateNamespaceHandle(pinnedHandle, names.namespaceHandle); err != nil {
		return err
	}
	if preparation.topology.handle != pinnedHandle {
		return errors.New("topology namespace handle no longer matches retained pin")
	}
	return validateTopologyIdentities(preparation.topology, names)
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

func topologyAccessError(vmID string) *errs.DomainError {
	options := []errs.ErrorOption{errs.WithClass(errs.ClassInternal)}
	if vmID != "" {
		options = append(options, errs.WithEntity(vmID))
	}
	return errs.New(
		errs.CodeNetworkError,
		"network topology authority is unavailable outside the committed state",
		options...,
	)
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
