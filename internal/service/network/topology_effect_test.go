package network

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTopologyEffectDelegatesClosedBoundaryToPrivateEffects(t *testing.T) {
	t.Parallel()

	namespace := &recordingNamespaceTopologyEffect{pin: &fakeNamespacePin{}}
	link := &recordingTopologyLinkEffect{}
	effect := &topologyEffect{namespace: namespace, link: link}

	pin, err := effect.CreateNamespace(t.Context(), namespaceCreateSpec{})
	require.NoError(t, err)
	bridge, err := effect.CreateLocalBridgeDown(t.Context(), pin, localBridgeCreateSpec{})
	require.NoError(t, err)
	tap, err := effect.CreateTAPDown(t.Context(), pin, tapCreateSpec{})
	require.NoError(t, err)
	veth, err := effect.CreateVethPairDown(t.Context(), pin, vethCreateSpec{})
	require.NoError(t, err)
	require.NoError(t, effect.AttachNamespaceVethToLocalBridge(t.Context(), pin, *veth, *bridge))
	require.NoError(t, effect.AttachTAPToLocalBridge(t.Context(), pin, *tap, *bridge))
	require.NoError(t, effect.AttachHostVethToManagedBridge(t.Context(), *veth, *bridge))
	require.NoError(t, effect.DeleteVethPair(t.Context(), pin, *veth))
	require.NoError(t, effect.DeleteTAP(t.Context(), pin, *tap))
	require.NoError(t, effect.DeleteLocalBridge(t.Context(), pin, *bridge))
	require.NoError(t, effect.DeleteNamespace(t.Context(), pin))

	assert.Equal(t, []string{"create namespace", "delete namespace"}, namespace.calls)
	assert.Equal(t, []string{
		"create local bridge",
		"create TAP",
		"create veth pair",
		"attach namespace veth",
		"attach TAP",
		"attach host veth",
		"delete veth pair",
		"delete TAP",
		"delete local bridge",
	}, link.calls)
}

func TestNewTopologyEffectUsesPrivateNamespaceAndLinkAdapters(t *testing.T) {
	t.Parallel()

	effect := newTopologyEffect()
	_, namespaceOK := effect.namespace.(*namespaceEffect)
	_, linkOK := effect.link.(*linkEffect)
	assert.True(t, namespaceOK)
	assert.True(t, linkOK)
}

type recordingNamespaceTopologyEffect struct {
	pin   NamespacePin
	calls []string
}

func (effect *recordingNamespaceTopologyEffect) CreateNamespace(
	context.Context,
	namespaceCreateSpec,
) (NamespacePin, error) {
	effect.calls = append(effect.calls, "create namespace")
	return effect.pin, nil
}

func (effect *recordingNamespaceTopologyEffect) DeleteNamespace(context.Context, NamespacePin) error {
	effect.calls = append(effect.calls, "delete namespace")
	return nil
}

type recordingTopologyLinkEffect struct {
	calls []string
}

func (effect *recordingTopologyLinkEffect) CreateLocalBridgeDown(
	context.Context,
	NamespacePin,
	localBridgeCreateSpec,
) (*linkIdentity, error) {
	effect.calls = append(effect.calls, "create local bridge")
	return &linkIdentity{}, nil
}

func (effect *recordingTopologyLinkEffect) CreateTAPDown(
	context.Context,
	NamespacePin,
	tapCreateSpec,
) (*linkIdentity, error) {
	effect.calls = append(effect.calls, "create TAP")
	return &linkIdentity{}, nil
}

func (effect *recordingTopologyLinkEffect) CreateVethPairDown(
	context.Context,
	NamespacePin,
	vethCreateSpec,
) (*vethPairIdentity, error) {
	effect.calls = append(effect.calls, "create veth pair")
	return &vethPairIdentity{}, nil
}

func (effect *recordingTopologyLinkEffect) AttachNamespaceVethToLocalBridge(
	context.Context,
	NamespacePin,
	vethPairIdentity,
	linkIdentity,
) error {
	effect.calls = append(effect.calls, "attach namespace veth")
	return nil
}

func (effect *recordingTopologyLinkEffect) AttachTAPToLocalBridge(
	context.Context,
	NamespacePin,
	linkIdentity,
	linkIdentity,
) error {
	effect.calls = append(effect.calls, "attach TAP")
	return nil
}

func (effect *recordingTopologyLinkEffect) AttachHostVethToManagedBridge(
	context.Context,
	vethPairIdentity,
	linkIdentity,
) error {
	effect.calls = append(effect.calls, "attach host veth")
	return nil
}

func (effect *recordingTopologyLinkEffect) DeleteVethPair(
	context.Context,
	NamespacePin,
	vethPairIdentity,
) error {
	effect.calls = append(effect.calls, "delete veth pair")
	return nil
}

func (effect *recordingTopologyLinkEffect) DeleteTAP(
	context.Context,
	NamespacePin,
	linkIdentity,
) error {
	effect.calls = append(effect.calls, "delete TAP")
	return nil
}

func (effect *recordingTopologyLinkEffect) DeleteLocalBridge(
	context.Context,
	NamespacePin,
	linkIdentity,
) error {
	effect.calls = append(effect.calls, "delete local bridge")
	return nil
}
