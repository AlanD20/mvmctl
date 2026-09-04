package network

import (
	"context"
	"errors"
	"fmt"
	"maps"

	"golang.org/x/sys/unix"

	"mvmctl/pkg/errs"
)

const loopbackLinkName linkName = "lo"

type linkStateRole uint8

const (
	linkStateLoopback linkStateRole = iota + 1
	linkStateLocalBridge
	linkStateTAP
	linkStateNamespaceVeth
	linkStateHostVeth
)

type retainedL3Namespaces struct {
	vmLease   *linkNamespaceLease
	hostLease *linkNamespaceLease
	vmFD      int
	hostFD    int
	vm        namespaceIdentity
	host      namespaceIdentity
	owner     OwnerUID
}

type exactTopologyLinks struct {
	loopback      routeLinkObservation
	managedBridge routeLinkObservation
	localBridge   routeLinkObservation
	tap           routeLinkObservation
	namespaceVeth routeLinkObservation
	hostVeth      routeLinkObservation
}

func (effect *linkEffect) ConfigureL3Down(
	ctx context.Context,
	namespace NamespacePin,
	topology Topology,
	endpoint endpointL3,
) (returnErr error) {
	if err := validateL3EffectAuthority(namespace, topology, endpoint); err != nil {
		return l3EffectError(topology.managedBridge.name, "validate L3 effect authority", err)
	}
	retained, err := effect.retainL3Namespaces(ctx, namespace)
	if err != nil {
		return l3EffectError(topology.managedBridge.name, "retain L3 namespaces", err)
	}
	defer func() {
		returnErr = appendL3CloseError(returnErr, retained.close(context.WithoutCancel(ctx)))
	}()
	links, err := effect.inspectExactTopologyLinks(ctx, retained, topology, true)
	if err != nil {
		return l3EffectError(topology.managedBridge.name, "inspect down L3 topology", err)
	}
	if err := effect.verifyFabricUnnumbered(ctx, retained, links); err != nil {
		return l3EffectError(topology.managedBridge.name, "verify unnumbered fabric links", err)
	}
	bridgeBookkeeping, err := deriveBridgeKernelBookkeeping(endpoint.network)
	if err != nil {
		return l3EffectError(topology.managedBridge.name, "derive bridge kernel routes", err)
	}
	if err := effect.ensureCanonicalAddressAndRoutes(
		ctx,
		retained.hostFD,
		links.managedBridge,
		endpoint.network.bridgeAddress,
		[]routeExpectation{
			endpoint.network.bridgeConnectedRoute,
			bridgeBookkeeping.local,
			bridgeBookkeeping.networkBroadcast,
			bridgeBookkeeping.subnetBroadcast,
		},
	); err != nil {
		return err
	}
	return effect.ensureCanonicalAddressAndRoutes(
		ctx,
		retained.vmFD,
		links.loopback,
		canonicalLoopbackL3.address,
		[]routeExpectation{canonicalLoopbackL3.localRoute, canonicalLoopbackL3.connectedRoute},
	)
}

func (effect *linkEffect) RaiseLoopback(ctx context.Context, namespace NamespacePin, topology Topology) error {
	return effect.setExactTopologyLinkState(ctx, namespace, topology, linkStateLoopback, true)
}

func (effect *linkEffect) RaiseLocalBridge(ctx context.Context, namespace NamespacePin, topology Topology) error {
	return effect.setExactTopologyLinkState(ctx, namespace, topology, linkStateLocalBridge, true)
}

func (effect *linkEffect) RaiseTAP(ctx context.Context, namespace NamespacePin, topology Topology) error {
	return effect.setExactTopologyLinkState(ctx, namespace, topology, linkStateTAP, true)
}

func (effect *linkEffect) RaiseNamespaceVeth(ctx context.Context, namespace NamespacePin, topology Topology) error {
	return effect.setExactTopologyLinkState(ctx, namespace, topology, linkStateNamespaceVeth, true)
}

func (effect *linkEffect) RaiseHostVeth(ctx context.Context, namespace NamespacePin, topology Topology) error {
	return effect.setExactTopologyLinkState(ctx, namespace, topology, linkStateHostVeth, true)
}

func (effect *linkEffect) LowerHostVeth(ctx context.Context, namespace NamespacePin, topology Topology) error {
	return effect.setExactTopologyLinkState(ctx, namespace, topology, linkStateHostVeth, false)
}

func (effect *linkEffect) LowerNamespaceVeth(ctx context.Context, namespace NamespacePin, topology Topology) error {
	return effect.setExactTopologyLinkState(ctx, namespace, topology, linkStateNamespaceVeth, false)
}

func (effect *linkEffect) LowerTAP(ctx context.Context, namespace NamespacePin, topology Topology) error {
	return effect.setExactTopologyLinkState(ctx, namespace, topology, linkStateTAP, false)
}

func (effect *linkEffect) LowerLocalBridge(ctx context.Context, namespace NamespacePin, topology Topology) error {
	return effect.setExactTopologyLinkState(ctx, namespace, topology, linkStateLocalBridge, false)
}

func (effect *linkEffect) LowerLoopback(ctx context.Context, namespace NamespacePin, topology Topology) error {
	return effect.setExactTopologyLinkState(ctx, namespace, topology, linkStateLoopback, false)
}

func validateL3EffectAuthority(namespace NamespacePin, topology Topology, endpoint endpointL3) error {
	if namespace == nil {
		return errors.New("namespace pin is required")
	}
	names := deriveTopologyNames(topology.key)
	if err := validateTopologyIdentities(topology, names); err != nil {
		return err
	}
	handle := namespace.Handle()
	if err := validateNamespaceHandle(handle, names.namespaceHandle); err != nil {
		return err
	}
	if handle != topology.handle {
		return errors.New("namespace pin does not match retained topology authority")
	}
	return validateEndpointL3(topology.key, endpoint.network, endpoint)
}

func validateLinkStateEffectAuthority(namespace NamespacePin, topology Topology) error {
	if namespace == nil {
		return errors.New("namespace pin is required")
	}
	names := deriveTopologyNames(topology.key)
	if err := validateTopologyIdentities(topology, names); err != nil {
		return err
	}
	handle := namespace.Handle()
	if err := validateNamespaceHandle(handle, names.namespaceHandle); err != nil {
		return err
	}
	if handle != topology.handle {
		return errors.New("namespace pin does not match retained topology authority")
	}
	return nil
}

func (effect *linkEffect) retainL3Namespaces(
	ctx context.Context,
	namespace NamespacePin,
) (*retainedL3Namespaces, error) {
	vmLease, vmFD, vmIdentity, err := effect.retainVM(ctx, namespace)
	if err != nil {
		return nil, err
	}
	hostLease, hostFD, hostIdentity, err := effect.retainHost(ctx)
	if err != nil {
		return nil, errors.Join(err, vmLease.close(context.WithoutCancel(ctx)))
	}
	retained := &retainedL3Namespaces{
		vmLease: vmLease, hostLease: hostLease,
		vmFD: vmFD, hostFD: hostFD, vm: vmIdentity, host: hostIdentity,
	}
	retained.owner, err = vmLease.owner()
	if err != nil {
		return nil, errors.Join(err, retained.close(context.WithoutCancel(ctx)))
	}
	return retained, nil
}

func (retained *retainedL3Namespaces) close(ctx context.Context) error {
	if retained == nil {
		return nil
	}
	var hostErr, vmErr error
	if retained.hostLease != nil {
		hostErr = retained.hostLease.close(ctx)
	}
	if retained.vmLease != nil {
		vmErr = retained.vmLease.close(ctx)
	}
	return errors.Join(hostErr, vmErr)
}

func (effect *linkEffect) inspectExactTopologyLinks(
	ctx context.Context,
	retained *retainedL3Namespaces,
	topology Topology,
	requireDown bool,
) (exactTopologyLinks, error) {
	if topology.handle.identity != retained.vm || topology.managedBridge.namespace != retained.host ||
		topology.veth.host.namespace != retained.host {
		return exactTopologyLinks{}, errors.New("topology links do not match retained namespace descriptors")
	}
	managedBridge, err := effect.inspectRequiredLink(ctx, retained.hostFD, topology.managedBridge.name)
	if err != nil {
		return exactTopologyLinks{}, err
	}
	hostVeth, err := effect.inspectRequiredLink(ctx, retained.hostFD, topology.veth.host.name)
	if err != nil {
		return exactTopologyLinks{}, err
	}
	localBridge, err := effect.inspectRequiredLink(ctx, retained.vmFD, topology.localBridge.name)
	if err != nil {
		return exactTopologyLinks{}, err
	}
	tap, err := effect.inspectRequiredLink(ctx, retained.vmFD, topology.tap.name)
	if err != nil {
		return exactTopologyLinks{}, err
	}
	namespaceVeth, err := effect.inspectRequiredLink(ctx, retained.vmFD, topology.veth.peer.name)
	if err != nil {
		return exactTopologyLinks{}, err
	}
	loopback, err := effect.inspectRequiredLink(ctx, retained.vmFD, loopbackLinkName)
	if err != nil {
		return exactTopologyLinks{}, err
	}
	validationErr := errors.Join(
		validateExactManagedLink(
			managedBridge,
			topology.managedBridge,
			linkKindBridge,
			managedBridgeAlias(topology.managedBridge.name),
			0,
			0,
		),
		validateExactManagedLink(
			hostVeth,
			topology.veth.host,
			linkKindVeth,
			managedVethAlias("veth-host", topology.veth.host.name, topology.veth.peer.name),
			topology.managedBridge.ifIndex,
			0,
		),
		validateExactManagedLink(
			localBridge,
			topology.localBridge,
			linkKindBridge,
			managedBridgeAlias(topology.localBridge.name),
			0,
			0,
		),
		validateExactManagedLink(
			tap,
			topology.tap,
			linkKindTAP,
			managedTAPAlias(topology.tap.name, retained.owner),
			topology.localBridge.ifIndex,
			retained.owner,
		),
		validateExactManagedLink(
			namespaceVeth,
			topology.veth.peer,
			linkKindVeth,
			managedVethAlias("veth-peer", topology.veth.peer.name, topology.veth.host.name),
			topology.localBridge.ifIndex,
			0,
		),
		validateExactLoopback(loopback),
	)
	if hostVeth.peer != namespaceVeth.ifIndex || namespaceVeth.peer != hostVeth.ifIndex {
		validationErr = errors.Join(validationErr, errors.New("veth endpoints are not reciprocal peers"))
	}
	if duplicateIfIndex(loopback.ifIndex, localBridge.ifIndex, tap.ifIndex, namespaceVeth.ifIndex) {
		validationErr = errors.Join(validationErr, errors.New("loopback reuses a retained topology ifindex"))
	}
	if requireDown && (loopback.flags&unix.IFF_UP != 0 || localBridge.flags&unix.IFF_UP != 0 ||
		tap.flags&unix.IFF_UP != 0 || namespaceVeth.flags&unix.IFF_UP != 0 || hostVeth.flags&unix.IFF_UP != 0) {
		validationErr = errors.Join(validationErr, errors.New("topology exposure link is unexpectedly up"))
	}
	if validationErr != nil {
		return exactTopologyLinks{}, validationErr
	}
	return exactTopologyLinks{
		loopback:      loopback,
		managedBridge: managedBridge,
		localBridge:   localBridge,
		tap:           tap,
		namespaceVeth: namespaceVeth,
		hostVeth:      hostVeth,
	}, nil
}

func (effect *linkEffect) inspectRequiredLink(
	ctx context.Context,
	namespaceFD int,
	name linkName,
) (routeLinkObservation, error) {
	observation, err := effect.route.inspectLink(ctx, namespaceFD, name)
	if err != nil {
		return routeLinkObservation{}, fmt.Errorf("inspect exact link %s: %w", name, err)
	}
	if observation == nil {
		return routeLinkObservation{}, fmt.Errorf("exact link %s is absent", name)
	}
	return *observation, nil
}

func validateExactManagedLink(
	observation routeLinkObservation,
	want linkIdentity,
	kind string,
	alias string,
	master int,
	owner OwnerUID,
) error {
	switch {
	case observation.name != want.name:
		return errors.New("link name changed")
	case want.ifIndex <= 0 || observation.ifIndex != want.ifIndex:
		return errors.New("link ifindex changed")
	case observation.kind != kind:
		return errors.New("link kind changed")
	case observation.alias != alias:
		return errors.New("link ownership alias changed")
	case observation.master != master:
		return errors.New("link master changed")
	case kind != linkKindVeth && observation.peer != 0:
		return errors.New("non-veth link has unexpected peer authority")
	}
	if kind == linkKindTAP {
		return validateManagedTAPAuthority(observation, owner, 1)
	}
	return nil
}

func validateExactLoopback(observation routeLinkObservation) error {
	switch {
	case observation.name != loopbackLinkName:
		return errors.New("loopback name changed")
	case observation.ifIndex <= 0:
		return errors.New("loopback ifindex is invalid")
	case observation.deviceType != unix.ARPHRD_LOOPBACK:
		return errors.New("loopback device type changed")
	case observation.kind != "" || observation.alias != "" || observation.master != 0 || observation.peer != 0:
		return errors.New("loopback has unexpected link authority")
	}
	return nil
}

func (effect *linkEffect) verifyFabricUnnumbered(
	ctx context.Context,
	retained *retainedL3Namespaces,
	links exactTopologyLinks,
) error {
	if canonicalFabricL3.ipv4Addresses != zero || canonicalFabricL3.ipv4Routes != zero {
		return errors.New("canonical fabric L3 expectation is invalid")
	}
	targets := [...]struct {
		namespaceFD int
		link        routeLinkObservation
	}{
		{namespaceFD: retained.hostFD, link: links.hostVeth},
		{namespaceFD: retained.vmFD, link: links.namespaceVeth},
		{namespaceFD: retained.vmFD, link: links.localBridge},
		{namespaceFD: retained.vmFD, link: links.tap},
	}
	for _, target := range targets {
		addresses, routes, err := effect.inspectIPv4Authority(ctx, target.namespaceFD, target.link.ifIndex)
		if err != nil {
			return err
		}
		if len(addresses) != int(canonicalFabricL3.ipv4Addresses) ||
			len(routes) != int(canonicalFabricL3.ipv4Routes) {
			return fmt.Errorf("fabric link has unexpected IPv4 authority: %s", target.link.name)
		}
	}
	return nil
}

func (effect *linkEffect) inspectIPv4Authority(
	ctx context.Context,
	namespaceFD int,
	ifIndex int,
) ([]routeAddressObservation, []routeIPv4Observation, error) {
	addresses, addressErr := effect.route.listIPv4Addresses(ctx, namespaceFD, ifIndex)
	routes, routeErr := effect.route.listIPv4Routes(ctx, namespaceFD, ifIndex)
	if err := errors.Join(addressErr, routeErr); err != nil {
		return nil, nil, err
	}
	return addresses, routes, nil
}

func (effect *linkEffect) ensureCanonicalAddressAndRoutes(
	ctx context.Context,
	namespaceFD int,
	link routeLinkObservation,
	address addressExpectation,
	routes []routeExpectation,
) error {
	addressesBefore, routesBefore, err := effect.inspectIPv4Authority(ctx, namespaceFD, link.ifIndex)
	if err != nil {
		return l3EffectError(link.name, "inspect IPv4 authority", err)
	}
	if len(addressesBefore) != 0 || len(routesBefore) != 0 {
		if err := validateExactIPv4Authority(addressesBefore, routesBefore, address, routes, link.ifIndex); err != nil {
			return l3EffectError(link.name, "admit existing IPv4 authority", err)
		}
		return nil
	}

	mutationErr := effect.route.addIPv4Address(ctx, namespaceFD, link.ifIndex, address)
	addressesAfter, routesAfter, inspectionErr := effect.inspectIPv4Authority(
		context.WithoutCancel(ctx),
		namespaceFD,
		link.ifIndex,
	)
	if inspectionErr != nil {
		return l3MutationError(
			link.name,
			"inspect configured IPv4 authority",
			errors.Join(mutationErr, inspectionErr),
			false,
			true,
		)
	}
	validationErr := validateExactIPv4Authority(addressesAfter, routesAfter, address, routes, link.ifIndex)
	if mutationErr != nil && validationErr == nil {
		return l3MutationError(link.name, "configure IPv4 address", mutationErr, true, false)
	}
	if mutationErr != nil && len(addressesAfter) == 0 && len(routesAfter) == 0 {
		return l3MutationError(link.name, "configure IPv4 address", mutationErr, false, false)
	}
	if err := errors.Join(mutationErr, validationErr); err != nil {
		return l3EffectError(link.name, "configure exact IPv4 authority", err)
	}
	return nil
}

func validateExactIPv4Authority(
	addresses []routeAddressObservation,
	routes []routeIPv4Observation,
	address addressExpectation,
	wantRoutes []routeExpectation,
	ifIndex int,
) error {
	if len(addresses) != 1 {
		return errors.New("link does not have exactly one canonical IPv4 address")
	}
	if err := exactAddressObservation(addresses[0], address, ifIndex); err != nil {
		return err
	}
	if len(routes) != len(wantRoutes) {
		return errors.New("link does not have exactly its canonical IPv4 routes")
	}
	remaining := append([]routeIPv4Observation(nil), routes...)
	for _, expectation := range wantRoutes {
		expected, err := routeExpectationToObservation(expectation, ifIndex)
		if err != nil {
			return err
		}
		match := -1
		for index, observation := range remaining {
			if observation == expected {
				match = index
				break
			}
		}
		if match < 0 {
			return errors.New("canonical IPv4 route is absent or changed")
		}
		remaining = append(remaining[:match], remaining[match+1:]...)
	}
	return nil
}

func (effect *linkEffect) setExactTopologyLinkState(
	ctx context.Context,
	namespace NamespacePin,
	topology Topology,
	role linkStateRole,
	up bool,
) (returnErr error) {
	if err := validateLinkStateEffectAuthority(namespace, topology); err != nil {
		return l3EffectError(topology.veth.host.name, "validate link-state effect authority", err)
	}
	retained, err := effect.retainL3Namespaces(ctx, namespace)
	if err != nil {
		return l3EffectError(topology.veth.host.name, "retain link-state namespaces", err)
	}
	defer func() {
		returnErr = appendL3CloseError(returnErr, retained.close(context.WithoutCancel(ctx)))
	}()
	before, err := effect.inspectExactTopologyLinks(ctx, retained, topology, false)
	if err != nil {
		return l3EffectError(topology.veth.host.name, "inspect link-state topology", err)
	}
	namespaceFD, target, err := exactLinkStateTarget(retained, before, role)
	if err != nil {
		return l3EffectError(topology.veth.host.name, "select link-state target", err)
	}
	if linkIsUp(target) == up {
		return nil
	}
	mutationErr := effect.route.setLinkUp(ctx, namespaceFD, target.ifIndex, up)
	after, inspectionErr := effect.inspectExactTopologyLinks(context.WithoutCancel(ctx), retained, topology, false)
	if inspectionErr != nil {
		return l3MutationError(
			target.name,
			"inspect changed link state",
			errors.Join(mutationErr, inspectionErr),
			false,
			true,
		)
	}
	_, afterTarget, targetErr := exactLinkStateTarget(retained, after, role)
	if targetErr == nil && role == linkStateLoopback && afterTarget.ifIndex != target.ifIndex {
		targetErr = errors.New("loopback ifindex changed during link-state mutation")
	}
	if targetErr == nil && linkIsUp(afterTarget) == up {
		if mutationErr != nil {
			return l3MutationError(target.name, "change exact link state", mutationErr, true, false)
		}
		return nil
	}
	if mutationErr != nil && targetErr == nil {
		return l3MutationError(target.name, "change exact link state", mutationErr, false, false)
	}
	return l3EffectError(
		target.name,
		"change exact link state",
		errors.Join(mutationErr, targetErr, errors.New("requested IFF_UP state was not observed")),
	)
}

func exactLinkStateTarget(
	retained *retainedL3Namespaces,
	links exactTopologyLinks,
	role linkStateRole,
) (int, routeLinkObservation, error) {
	switch role {
	case linkStateLoopback:
		return retained.vmFD, links.loopback, nil
	case linkStateLocalBridge:
		return retained.vmFD, links.localBridge, nil
	case linkStateTAP:
		return retained.vmFD, links.tap, nil
	case linkStateNamespaceVeth:
		return retained.vmFD, links.namespaceVeth, nil
	case linkStateHostVeth:
		return retained.hostFD, links.hostVeth, nil
	default:
		return -1, routeLinkObservation{}, errors.New("link-state role is invalid")
	}
}

func linkIsUp(link routeLinkObservation) bool {
	return link.flags&unix.IFF_UP != 0
}

func l3EffectError(name linkName, stage string, cause error) *errs.DomainError {
	if cause == nil {
		cause = errors.New("failed to " + stage)
	}
	return errs.WrapMsg(
		errs.CodeNetworkCreateFailed,
		"failed to "+stage,
		cause,
		errs.WithClass(errs.ClassInternal),
		errs.WithEntity(name.String()),
	)
}

func l3MutationError(
	name linkName,
	stage string,
	cause error,
	committed bool,
	unknown bool,
) *errs.DomainError {
	if cause == nil {
		cause = errors.New("failed to " + stage)
	}
	return errs.WrapMsg(
		errs.CodeNetworkCreateFailed,
		"failed to "+stage,
		cause,
		errs.WithClass(errs.ClassRetryable),
		errs.WithEntity(name.String()),
		errs.WithDetails(map[string]any{
			"mutation_committed":       committed,
			"mutation_outcome_unknown": unknown,
		}),
	)
}

func appendL3CloseError(primary, closeErr error) error {
	if closeErr == nil {
		return primary
	}
	if primary == nil {
		return l3EffectError("", "close retained L3 namespaces", closeErr)
	}
	domainErr := errs.AsDomainError(primary)
	if domainErr == nil {
		return l3EffectError("", "close retained L3 namespaces", errors.Join(primary, closeErr))
	}
	result := *domainErr
	result.Details = maps.Clone(domainErr.Details)
	result.Message += "; failed to close retained L3 namespaces: " + closeErr.Error()
	result.Err = errors.Join(primary, closeErr)
	return &result
}
