package network

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strconv"

	"mvmctl/pkg/errs"
)

const (
	linuxInterfaceNameMax  = 15
	namespaceAuthorityRoot = "/run/mvmctl/authority"
	namespaceHandleMode    = uint32(0o444)
)

var (
	vmIDPattern      = regexp.MustCompile(`^[a-f0-9]{32}$`)
	networkIDPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
)

// OwnerUID is the authenticated non-root owner of a managed VM and network.
type OwnerUID uint32

// VMID is an immutable mvmctl VM identity.
type VMID string

// NetworkID is an immutable mvmctl network identity.
type NetworkID string

type topologyKey struct {
	ownerUID  OwnerUID
	vmID      VMID
	networkID NetworkID
}

// namespaceKey identifies one owner's VM namespace independently of managed network placement.
type namespaceKey struct {
	ownerUID OwnerUID
	vmID     VMID
}

type networkKey struct {
	ownerUID  OwnerUID
	networkID NetworkID
}

type namespaceHandlePath string

type linkName string

type topologyNames struct {
	namespaceHandle namespaceHandlePath
	managedBridge   linkName
	hostVeth        linkName
	namespaceVeth   linkName
	localBridge     linkName
	tap             linkName
}

func newTopologyKey(ownerUID uint32, vmID, networkID string) (topologyKey, error) {
	if ownerUID == 0 {
		return topologyKey{}, topologyValidationError("owner UID must identify a non-root user")
	}
	if !vmIDPattern.MatchString(vmID) {
		return topologyKey{}, topologyValidationError("vm ID must be 32 lowercase hexadecimal characters")
	}
	if !networkIDPattern.MatchString(networkID) {
		return topologyKey{}, topologyValidationError("network ID must be 64 lowercase hexadecimal characters")
	}
	return topologyKey{ownerUID: OwnerUID(ownerUID), vmID: VMID(vmID), networkID: NetworkID(networkID)}, nil
}

func newNamespaceKey(ownerUID uint32, vmID string) (namespaceKey, error) {
	if ownerUID == 0 {
		return namespaceKey{}, topologyValidationError("owner UID must identify a non-root user")
	}
	if !vmIDPattern.MatchString(vmID) {
		return namespaceKey{}, topologyValidationError("vm ID must be 32 lowercase hexadecimal characters")
	}
	return namespaceKey{ownerUID: OwnerUID(ownerUID), vmID: VMID(vmID)}, nil
}

// OwnerUID returns the authenticated owner identity.
func (key topologyKey) OwnerUID() OwnerUID { return key.ownerUID }

// VMID returns the immutable VM identity.
func (key topologyKey) VMID() VMID { return key.vmID }

// NetworkID returns the immutable network identity.
func (key topologyKey) NetworkID() NetworkID { return key.networkID }

// Network returns the owner-bound network lock key.
func (key topologyKey) Network() networkKey {
	return networkKey{ownerUID: key.ownerUID, networkID: key.networkID}
}

// OwnerUID returns the authenticated owner identity.
func (key networkKey) OwnerUID() OwnerUID { return key.ownerUID }

// NetworkID returns the immutable network identity.
func (key networkKey) NetworkID() NetworkID { return key.networkID }

// String returns a derived link name. It is intended for root-side adapters and diagnostics, not caller input.
func (name linkName) String() string { return string(name) }

// String returns a derived namespace-handle path. It is intended for Jailer handoff, not caller input.
func (path namespaceHandlePath) String() string { return string(path) }

func validateTopologyKey(key topologyKey) error {
	_, err := newTopologyKey(uint32(key.ownerUID), string(key.vmID), string(key.networkID))
	return err
}

func validateNamespaceKey(key namespaceKey) error {
	_, err := newNamespaceKey(uint32(key.ownerUID), string(key.vmID))
	return err
}

func validateNetworkKey(key networkKey) error {
	if key.ownerUID == 0 {
		return topologyValidationError("network owner UID must identify a non-root user")
	}
	if !networkIDPattern.MatchString(string(key.networkID)) {
		return topologyValidationError("network ID must be 64 lowercase hexadecimal characters")
	}
	return nil
}

func deriveTopologyNames(key topologyKey) topologyNames {
	return topologyNames{
		namespaceHandle: deriveNamespaceHandle(namespaceKey{ownerUID: key.ownerUID, vmID: key.vmID}),
		managedBridge:   deriveNetworkLinkName("mvb", key.Network(), "managed-bridge"),
		hostVeth:        deriveVMLinkName("mvh", key.ownerUID, key.vmID, "host-veth"),
		namespaceVeth:   deriveVMLinkName("mvn", key.ownerUID, key.vmID, "namespace-veth"),
		localBridge:     deriveVMLinkName("mvl", key.ownerUID, key.vmID, "local-bridge"),
		tap:             deriveVMLinkName("mvt", key.ownerUID, key.vmID, "tap"),
	}
}

func deriveNamespaceHandle(key namespaceKey) namespaceHandlePath {
	uid := strconv.FormatUint(uint64(key.ownerUID), 10)
	return namespaceHandlePath(namespaceAuthorityRoot + "/" + uid + "/netns/" + string(key.vmID))
}

func deriveNetworkLinkName(prefix string, key networkKey, role string) linkName {
	// NUL separators prevent ambiguous concatenations from producing the same preimage. Prefix plus twelve hexadecimal
	// characters fits Linux IFNAMSIZ while the complete immutable identity remains in the authority record.
	value := fmt.Sprintf("%d\x00%s\x00%s", key.ownerUID, key.networkID, role)
	return hashedLinkName(prefix, value)
}

func deriveVMLinkName(prefix string, ownerUID OwnerUID, vmID VMID, role string) linkName {
	value := fmt.Sprintf("%d\x00%s\x00%s", ownerUID, vmID, role)
	return hashedLinkName(prefix, value)
}

func hashedLinkName(prefix, value string) linkName {
	digest := sha256.Sum256([]byte(value))
	return linkName(prefix + hex.EncodeToString(digest[:])[:linuxInterfaceNameMax-len(prefix)])
}

func topologyValidationError(message string) *errs.DomainError {
	return errs.New(errs.CodeValidationFailed, message)
}
