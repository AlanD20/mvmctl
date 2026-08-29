package network

import (
	"errors"
	"fmt"

	"mvmctl/pkg/errs"
)

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

func topologyAccessError(vmID string) *errs.DomainError {
	options := []errs.ErrorOption{errs.WithClass(errs.ClassInternal)}
	if vmID != "" {
		options = append(options, errs.WithEntity(vmID))
	}
	return errs.New(
		errs.CodeNetworkError,
		"network topology authority is unavailable outside the prepared state",
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
