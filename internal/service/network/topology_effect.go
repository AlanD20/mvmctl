package network

import "context"

type namespaceTopologyEffect interface {
	CreateNamespace(context.Context, namespaceCreateSpec) (NamespacePin, error)
	DeleteNamespace(context.Context, NamespacePin) error
}

type topologyLinkEffect interface {
	CreateLocalBridgeDown(context.Context, NamespacePin, localBridgeCreateSpec) (*linkIdentity, error)
	CreateTAPDown(context.Context, NamespacePin, tapCreateSpec) (*linkIdentity, error)
	CreateVethPairDown(context.Context, NamespacePin, vethCreateSpec) (*vethPairIdentity, error)
	AttachNamespaceVethToLocalBridge(
		context.Context,
		NamespacePin,
		vethPairIdentity,
		linkIdentity,
	) error
	AttachTAPToLocalBridge(context.Context, NamespacePin, linkIdentity, linkIdentity) error
	AttachHostVethToManagedBridge(context.Context, vethPairIdentity, linkIdentity) error
	DeleteVethPair(context.Context, NamespacePin, vethPairIdentity) error
	DeleteTAP(context.Context, NamespacePin, linkIdentity) error
	DeleteLocalBridge(context.Context, NamespacePin, linkIdentity) error
}

// topologyEffect is the private production composition of the namespace and link effect adapters.
type topologyEffect struct {
	namespace namespaceTopologyEffect
	link      topologyLinkEffect
}

var _ TopologyEffects = (*topologyEffect)(nil)

func newTopologyEffect() *topologyEffect {
	namespace := newNamespaceEffect()
	return &topologyEffect{namespace: namespace, link: newLinkEffect(namespace)}
}

func (effect *topologyEffect) CreateNamespace(
	ctx context.Context,
	spec namespaceCreateSpec,
) (NamespacePin, error) {
	return effect.namespace.CreateNamespace(ctx, spec)
}

func (effect *topologyEffect) CreateLocalBridgeDown(
	ctx context.Context,
	namespace NamespacePin,
	spec localBridgeCreateSpec,
) (*linkIdentity, error) {
	return effect.link.CreateLocalBridgeDown(ctx, namespace, spec)
}

func (effect *topologyEffect) CreateTAPDown(
	ctx context.Context,
	namespace NamespacePin,
	spec tapCreateSpec,
) (*linkIdentity, error) {
	return effect.link.CreateTAPDown(ctx, namespace, spec)
}

func (effect *topologyEffect) CreateVethPairDown(
	ctx context.Context,
	namespace NamespacePin,
	spec vethCreateSpec,
) (*vethPairIdentity, error) {
	return effect.link.CreateVethPairDown(ctx, namespace, spec)
}

func (effect *topologyEffect) AttachNamespaceVethToLocalBridge(
	ctx context.Context,
	namespace NamespacePin,
	veth vethPairIdentity,
	bridge linkIdentity,
) error {
	return effect.link.AttachNamespaceVethToLocalBridge(ctx, namespace, veth, bridge)
}

func (effect *topologyEffect) AttachTAPToLocalBridge(
	ctx context.Context,
	namespace NamespacePin,
	tap linkIdentity,
	bridge linkIdentity,
) error {
	return effect.link.AttachTAPToLocalBridge(ctx, namespace, tap, bridge)
}

func (effect *topologyEffect) AttachHostVethToManagedBridge(
	ctx context.Context,
	veth vethPairIdentity,
	bridge linkIdentity,
) error {
	return effect.link.AttachHostVethToManagedBridge(ctx, veth, bridge)
}

func (effect *topologyEffect) DeleteVethPair(
	ctx context.Context,
	namespace NamespacePin,
	veth vethPairIdentity,
) error {
	return effect.link.DeleteVethPair(ctx, namespace, veth)
}

func (effect *topologyEffect) DeleteTAP(
	ctx context.Context,
	namespace NamespacePin,
	tap linkIdentity,
) error {
	return effect.link.DeleteTAP(ctx, namespace, tap)
}

func (effect *topologyEffect) DeleteLocalBridge(
	ctx context.Context,
	namespace NamespacePin,
	bridge linkIdentity,
) error {
	return effect.link.DeleteLocalBridge(ctx, namespace, bridge)
}

func (effect *topologyEffect) DeleteNamespace(ctx context.Context, namespace NamespacePin) error {
	return effect.namespace.DeleteNamespace(ctx, namespace)
}
