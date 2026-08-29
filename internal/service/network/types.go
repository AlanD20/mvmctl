package network

import "context"

type namespaceIdentity struct {
	device uint64
	inode  uint64
}

type namespaceHandle struct {
	path     namespaceHandlePath
	identity namespaceIdentity
	ownerUID uint32
	ownerGID uint32
	mode     uint32
}

type linkIdentity struct {
	name      linkName
	ifIndex   int
	namespace namespaceIdentity
}

type vethPairIdentity struct {
	host linkIdentity
	peer linkIdentity
}

// NamespaceIdentity is the pinned device/inode identity of a network namespace.
type NamespaceIdentity struct {
	Device uint64
	Inode  uint64
}

// NamespaceHandle is a derived root-owned namespace reference and its pinned identity.
type NamespaceHandle struct {
	Path     string
	Identity NamespaceIdentity
	OwnerUID uint32
	OwnerGID uint32
	Mode     uint32
}

// LinkIdentity is the exact kernel identity of a derived link in a pinned namespace.
type LinkIdentity struct {
	Name      string
	IfIndex   int
	Namespace NamespaceIdentity
}

// VethPairIdentity identifies both ends of the owner-bound veth pair.
type VethPairIdentity struct {
	Host LinkIdentity
	Peer LinkIdentity
}

// TopologyRecord is the serializable identity contract for root authority persistence and Jailer registration.
type TopologyRecord struct {
	OwnerUID      uint32
	VMID          string
	NetworkID     string
	Namespace     NamespaceHandle
	ManagedBridge LinkIdentity
	LocalBridge   LinkIdentity
	TAP           LinkIdentity
	VethPair      VethPairIdentity
}

type vmLockCapability struct{}

// vmLaunchLease is deliberately private until the Jailer launch authority transfers its concrete held VM lock into
// this package. A caller cannot satisfy this boundary with identity values and a forgeable boolean.
type vmLaunchLease struct {
	ownerUID OwnerUID
	vmID     VMID
	lock     *vmLockCapability
}

// NetworkLocker acquires the final lock class for one root-owned managed network. A non-nil lock returned with an
// error remains held—for example, when acquisition succeeds but record verification fails—and transfers release
// responsibility to the caller.
type NetworkLocker interface {
	LockNetwork(ctx context.Context, key networkKey) (NetworkLock, error)
}

// NetworkLock retains the verified network record and exact managed bridge through topology creation or cleanup.
type NetworkLock interface {
	Key() networkKey
	ManagedBridge() linkIdentity
	Release(ctx context.Context) error
}

// NamespacePin retains the root-owned namespace handle opened by the effect adapter.
type NamespacePin interface {
	Handle() namespaceHandle
	Release(ctx context.Context) error
}

type namespaceCreateSpec struct {
	key  topologyKey
	path namespaceHandlePath
}

type localBridgeCreateSpec struct {
	name linkName
}

type tapCreateSpec struct {
	name     linkName
	ownerUID OwnerUID
}

type vethCreateSpec struct {
	hostName linkName
	peerName linkName
}

// TopologyEffects is the closed root-side effect boundary. Each create method creates exactly the named resource down;
// link activation and nftables policy are deliberately separate later transaction stages.
//
// CRITICAL: A create method that mutates the host before returning an error must return the exact surviving pin or
// identity. A nil result with an error certifies that the adapter removed its partial mutation before returning. This
// lets the transaction record progress before inspecting the error and makes every mutation rollback-visible.
type TopologyEffects interface {
	CreateNamespace(ctx context.Context, spec namespaceCreateSpec) (NamespacePin, error)
	CreateLocalBridgeDown(
		ctx context.Context,
		namespace NamespacePin,
		spec localBridgeCreateSpec,
	) (*linkIdentity, error)
	CreateTAPDown(ctx context.Context, namespace NamespacePin, spec tapCreateSpec) (*linkIdentity, error)
	CreateVethPairDown(
		ctx context.Context,
		namespace NamespacePin,
		spec vethCreateSpec,
	) (*vethPairIdentity, error)
	AttachNamespaceVethToLocalBridge(
		ctx context.Context,
		namespace NamespacePin,
		veth vethPairIdentity,
		bridge linkIdentity,
	) error
	AttachTAPToLocalBridge(
		ctx context.Context,
		namespace NamespacePin,
		tap linkIdentity,
		bridge linkIdentity,
	) error
	AttachHostVethToManagedBridge(
		ctx context.Context,
		veth vethPairIdentity,
		bridge linkIdentity,
	) error
	DeleteVethPair(ctx context.Context, namespace NamespacePin, veth vethPairIdentity) error
	DeleteTAP(ctx context.Context, namespace NamespacePin, tap linkIdentity) error
	DeleteLocalBridge(ctx context.Context, namespace NamespacePin, bridge linkIdentity) error
	DeleteNamespace(ctx context.Context, namespace NamespacePin, identity namespaceIdentity) error
}

// Topology is the exact derived and kernel-returned identity needed for durable authority and reconciliation.
type Topology struct {
	key           topologyKey
	handle        namespaceHandle
	managedBridge linkIdentity
	localBridge   linkIdentity
	tap           linkIdentity
	veth          vethPairIdentity
}

// Key returns the immutable owner, VM, and network identity.
func (topology Topology) Key() topologyKey { return topology.key }

// NamespaceHandle returns the derived root-owned namespace reference and pinned identity.
func (topology Topology) NamespaceHandle() namespaceHandle { return topology.handle }

// ManagedBridge returns the exact root-verified host bridge identity.
func (topology Topology) ManagedBridge() linkIdentity { return topology.managedBridge }

// LocalBridge returns the namespace-local bridge identity.
func (topology Topology) LocalBridge() linkIdentity { return topology.localBridge }

// TAP returns the namespace-local TAP identity.
func (topology Topology) TAP() linkIdentity { return topology.tap }

// VethPair returns both exact veth identities.
func (topology Topology) VethPair() vethPairIdentity { return topology.veth }

// Export returns the serializable identity contract for authority persistence and Jailer registration.
func (topology Topology) Export() TopologyRecord {
	return TopologyRecord{
		OwnerUID:      uint32(topology.key.ownerUID),
		VMID:          string(topology.key.vmID),
		NetworkID:     string(topology.key.networkID),
		Namespace:     exportNamespaceHandle(topology.handle),
		ManagedBridge: exportLinkIdentity(topology.managedBridge),
		LocalBridge:   exportLinkIdentity(topology.localBridge),
		TAP:           exportLinkIdentity(topology.tap),
		VethPair: VethPairIdentity{
			Host: exportLinkIdentity(topology.veth.host),
			Peer: exportLinkIdentity(topology.veth.peer),
		},
	}
}

func exportNamespaceIdentity(identity namespaceIdentity) NamespaceIdentity {
	return NamespaceIdentity{Device: identity.device, Inode: identity.inode}
}

func exportNamespaceHandle(handle namespaceHandle) NamespaceHandle {
	return NamespaceHandle{
		Path:     handle.path.String(),
		Identity: exportNamespaceIdentity(handle.identity),
		OwnerUID: handle.ownerUID,
		OwnerGID: handle.ownerGID,
		Mode:     handle.mode,
	}
}

func exportLinkIdentity(identity linkIdentity) LinkIdentity {
	return LinkIdentity{
		Name:      identity.name.String(),
		IfIndex:   identity.ifIndex,
		Namespace: exportNamespaceIdentity(identity.namespace),
	}
}
