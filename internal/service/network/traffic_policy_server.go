package network

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/netip"
	"slices"

	"mvmctl/internal/lib/authorityfs"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/service/firewall"
	"mvmctl/pkg/errs"
)

// TrafficPolicyServer resolves policy identities exclusively through root-owned topology and generation records.
type TrafficPolicyServer struct{}

// NewTrafficPolicyServer creates the closed root traffic-policy authority.
func NewTrafficPolicyServer() *TrafficPolicyServer { return &TrafficPolicyServer{} }

// ApplyNetworkTrafficPolicy installs a grant only after its owner-bound destination and source network are admitted.
// CRITICAL: ownerUID comes from the authenticated dispatcher, never the request body.
func (server *TrafficPolicyServer) ApplyNetworkTrafficPolicy(
	ctx context.Context, ownerUID uint32, request NetworkTrafficPolicyRequest,
) (_ TrafficPolicyMutationResult, returnErr error) {
	if err := request.Validate(); err != nil {
		return TrafficPolicyMutationResult{}, err
	}
	authority, err := acquireTrafficPolicyAuthority(ctx, ownerUID, []string{request.DestinationVMID})
	if err != nil {
		return TrafficPolicyMutationResult{}, err
	}
	defer func() { returnErr = authority.release(ctx, returnErr) }()
	if err := authority.begin(ctx, request.SourceNetworkID); err != nil {
		return TrafficPolicyMutationResult{}, err
	}
	if err := authority.firewall.ApplyNetworkTrafficGrant(ctx, firewall.NetworkTrafficGrant{
		ID:              request.PolicyID(),
		SourceNetworkID: request.SourceNetworkID,
		DestinationVMID: request.DestinationVMID,
		Protocol: trafficFirewallProtocol(
			request.Protocol,
		),
		Ports: firewall.PortInterval{First: request.PortStart, Last: request.PortEnd},
	}); err != nil {
		return TrafficPolicyMutationResult{}, err
	}
	return TrafficPolicyMutationResult{PolicyID: request.PolicyID()}, nil
}

// ApplyVMTrafficPolicy installs a grant using the source network resolved under both exact VM locks.
// CRITICAL: ownerUID is authenticated by the privileged dispatcher.
func (server *TrafficPolicyServer) ApplyVMTrafficPolicy(
	ctx context.Context, ownerUID uint32, request VMTrafficPolicyRequest,
) (_ TrafficPolicyMutationResult, returnErr error) {
	if err := request.Validate(); err != nil {
		return TrafficPolicyMutationResult{}, err
	}
	authority, err := acquireTrafficPolicyAuthority(
		ctx,
		ownerUID,
		[]string{request.SourceVMID, request.DestinationVMID},
	)
	if err != nil {
		return TrafficPolicyMutationResult{}, err
	}
	defer func() { returnErr = authority.release(ctx, returnErr) }()
	if err := authority.begin(ctx, authority.topologies[request.SourceVMID].NetworkID); err != nil {
		return TrafficPolicyMutationResult{}, err
	}
	if err := authority.firewall.ApplyVMTrafficGrant(ctx, firewall.VMTrafficGrant{
		ID:              request.PolicyID(),
		SourceVMID:      request.SourceVMID,
		DestinationVMID: request.DestinationVMID,
		Protocol: trafficFirewallProtocol(
			request.Protocol,
		),
		Ports: firewall.PortInterval{First: request.PortStart, Last: request.PortEnd},
	}); err != nil {
		return TrafficPolicyMutationResult{}, err
	}
	return TrafficPolicyMutationResult{PolicyID: request.PolicyID()}, nil
}

// RemoveNetworkTrafficPolicy proves revocation or exact absence before acknowledging local projection deletion.
// CRITICAL: request fields identify intent only; the root graph supplies ownership and applied grant identity.
func (server *TrafficPolicyServer) RemoveNetworkTrafficPolicy(
	ctx context.Context, ownerUID uint32, request NetworkTrafficPolicyRequest,
) (_ TrafficPolicyMutationResult, returnErr error) {
	if err := request.Validate(); err != nil {
		return TrafficPolicyMutationResult{}, err
	}
	authority, err := acquireTrafficPolicyAuthority(ctx, ownerUID, []string{request.DestinationVMID})
	if err != nil {
		return TrafficPolicyMutationResult{}, err
	}
	defer func() { returnErr = authority.release(ctx, returnErr) }()
	if err := authority.begin(ctx, request.SourceNetworkID); err != nil {
		return TrafficPolicyMutationResult{}, err
	}
	if err := authority.firewall.RemoveNetworkTrafficGrant(ctx, request.PolicyID()); err != nil {
		return TrafficPolicyMutationResult{}, err
	}
	return TrafficPolicyMutationResult{PolicyID: request.PolicyID()}, nil
}

// RemoveVMTrafficPolicy proves exact directional revocation before acknowledging local projection deletion.
// CRITICAL: ownerUID is authenticated by the dispatcher and both topology identities are root-owned.
func (server *TrafficPolicyServer) RemoveVMTrafficPolicy(
	ctx context.Context, ownerUID uint32, request VMTrafficPolicyRequest,
) (_ TrafficPolicyMutationResult, returnErr error) {
	if err := request.Validate(); err != nil {
		return TrafficPolicyMutationResult{}, err
	}
	authority, err := acquireTrafficPolicyAuthority(
		ctx,
		ownerUID,
		[]string{request.SourceVMID, request.DestinationVMID},
	)
	if err != nil {
		return TrafficPolicyMutationResult{}, err
	}
	defer func() { returnErr = authority.release(ctx, returnErr) }()
	if err := authority.begin(ctx, authority.topologies[request.SourceVMID].NetworkID); err != nil {
		return TrafficPolicyMutationResult{}, err
	}
	if err := authority.firewall.RemoveVMTrafficGrant(ctx, request.PolicyID()); err != nil {
		return TrafficPolicyMutationResult{}, err
	}
	return TrafficPolicyMutationResult{PolicyID: request.PolicyID()}, nil
}

// trafficPolicyAuthority is invocation-local; no capability or descriptor escapes the synchronous root mutation.
type trafficPolicyAuthority struct {
	ownerUID   uint32
	jailer     *authorityfs.Jailer
	index      *authorityfs.Lock
	owner      *authorityfs.JailerUID
	vms        []*authorityfs.JailerVM
	storeRoot  *networkStoreRoot
	store      *networkStoreUID
	journals   []*topologyJournal
	topologies map[string]topologyJournalRecord
	networks   []*managedNetworkLease
	firewall   *firewall.MutationLease
}

func acquireTrafficPolicyAuthority(
	ctx context.Context, ownerUID uint32, vmIDs []string,
) (_ *trafficPolicyAuthority, returnErr error) {
	if ctx == nil || ownerUID == 0 {
		return nil, errs.New(errs.CodeValidationFailed, "authenticated non-root policy owner and context are required")
	}
	authority := &trafficPolicyAuthority{
		ownerUID:   ownerUID,
		topologies: make(map[string]topologyJournalRecord, len(vmIDs)),
	}
	defer func() {
		if returnErr != nil {
			returnErr = authority.release(ctx, returnErr)
		}
	}()
	var err error
	authority.jailer, err = authorityfs.OpenJailer(ctx)
	if err != nil {
		return nil, err
	}
	authority.index, err = authority.jailer.AcquireIndex(ctx)
	if err != nil {
		return nil, err
	}
	authority.owner, err = authority.jailer.OpenUID(ctx, ownerUID)
	if err != nil {
		return nil, err
	}
	ordered := slices.Clone(vmIDs)
	slices.Sort(ordered)
	ordered = slices.Compact(ordered)
	for _, id := range ordered {
		key, err := authorityfs.NewVMKey(id)
		if err != nil {
			return nil, err
		}
		vm, err := authority.owner.AcquireVM(ctx, key)
		if err != nil {
			return nil, err
		}
		authority.vms = append(authority.vms, vm)
	}
	indexErr := authority.index.Release(context.WithoutCancel(ctx))
	authority.index = nil
	if indexErr != nil {
		return nil, indexErr
	}
	authority.storeRoot, err = openNetworkStoreRoot(ctx, realNetworkStoreDeps(), productionNetworkStorePolicy())
	if err != nil {
		return nil, err
	}
	authority.store, err = authority.storeRoot.openUID(ctx, ownerUID, false)
	if err != nil {
		return nil, err
	}
	boot, err := readCurrentBootID(ctx)
	if err != nil {
		return nil, err
	}
	for _, id := range ordered {
		journal, err := openTopologyJournal(ctx, authority.store, VMID(id))
		if err != nil {
			return nil, err
		}
		authority.journals = append(authority.journals, journal)
		record, found, err := journal.read(ctx)
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, errs.NotFound(
				errs.CodePolicyEndpointInvalid,
				"traffic policy requires an existing root-owned VM topology; unlaunched endpoint admission is unavailable",
				errs.WithEntity(id),
			)
		}
		if record.Lifecycle != topologyActive && record.Lifecycle != topologyCleaned {
			return nil, errs.New(errs.CodePolicyEndpointInvalid,
				"traffic policy endpoint requires completed topology cleanup or activation", errs.WithEntity(id))
		}
		if record.Lifecycle == topologyActive && record.BootID != boot {
			return nil, errs.New(errs.CodePolicyEndpointInvalid,
				"active traffic policy endpoint requires current-boot recovery", errs.WithEntity(id))
		}
		authority.topologies[id] = record
	}
	return authority, nil
}

func (authority *trafficPolicyAuthority) begin(ctx context.Context, sourceNetworkID string) error {
	networkIDs := []string{sourceNetworkID}
	for _, record := range authority.topologies {
		networkIDs = append(networkIDs, record.NetworkID)
	}
	slices.Sort(networkIDs)
	networkIDs = slices.Compact(networkIDs)
	networks := make(map[string]*managedNetworkLease, len(networkIDs))
	for _, id := range networkIDs {
		lock, err := (managedNetworkLocker{}).LockNetwork(ctx,
			networkKey{ownerUID: OwnerUID(authority.ownerUID), networkID: NetworkID(id)})
		if err != nil {
			return err
		}
		lease := lock.(*managedNetworkLease)
		authority.networks = append(authority.networks, lease)
		networks[id] = lease
	}

	// CRITICAL: Admit durable guest facts against the exact current network records while both VM and network
	// locks are held. A cleaned journal provides no reusable kernel link or namespace authority.
	reservations := make([]firewall.EndpointReservation, 0, len(authority.topologies))
	vmIDs := make([]string, 0, len(authority.topologies))
	for id := range authority.topologies {
		vmIDs = append(vmIDs, id)
	}
	slices.Sort(vmIDs)
	for _, id := range vmIDs {
		record := authority.topologies[id]
		network := networks[record.NetworkID].record
		encoded, err := encodeNetworkAuthorityRecord(network)
		if err != nil {
			return err
		}
		digest := sha256.Sum256(encoded)
		if record.OwnerUID != network.ownerUID || record.NetworkDigest != hex.EncodeToString(digest[:]) ||
			record.Prefix != network.configuration.l3.prefix.value.String() ||
			record.Gateway != network.configuration.l3.gateway.value.String() {
			return networkAuthorityCorruptError("policy topology differs from its retained root network authority", nil)
		}
		if record.Lifecycle != topologyCleaned {
			continue
		}
		key, err := newTopologyKey(record.OwnerUID, record.VMID, record.NetworkID)
		if err != nil {
			return err
		}
		address, err := netip.ParseAddr(record.GuestIPv4)
		if err != nil {
			return err
		}
		l3, err := deriveEndpointL3(key, network.configuration.l3, endpointL3Input{
			guestAddress: address, requestedMAC: &record.GuestMAC,
		})
		if err != nil {
			return err
		}
		reservations = append(reservations, firewall.EndpointReservation{
			OwnerUID: record.OwnerUID, VMID: record.VMID, NetworkID: record.NetworkID,
			Prefix: l3.network.prefix.value, Gateway: l3.network.gateway.value,
			IPv4: l3.guestAddress.value, MAC: [6]byte(l3.mac),
		})
	}

	// Each network registration consumes that network's concrete lock. All earlier network locks stay held;
	// only the later firewall suffix is acquired and released between complete generation changes.
	for _, lease := range authority.networks {
		if err := authority.beginFirewall(ctx, lease); err != nil {
			return err
		}
		err := authority.firewall.RegisterNetwork(ctx, trafficPolicyNetworkIntent(lease.record))
		releaseErr := authority.firewall.Release(context.WithoutCancel(ctx))
		authority.firewall = nil
		if err = appendNetworkAuthorityError(
			err,
			"release registered policy network firewall",
			releaseErr,
		); err != nil {
			return err
		}
	}
	if err := authority.beginFirewall(ctx, networks[sourceNetworkID]); err != nil {
		return err
	}
	for _, reservation := range reservations {
		if err := authority.firewall.RegisterStoppedEndpoint(ctx, reservation); err != nil {
			return err
		}
	}
	return nil
}

func (authority *trafficPolicyAuthority) beginFirewall(ctx context.Context, network *managedNetworkLease) error {
	if err := network.mutation.Verify(ctx); err != nil {
		return err
	}
	fd, identity, err := network.host.descriptor()
	if err != nil || identity != network.record.managedBridge.namespace {
		return networkAuthorityCorruptError("policy network host namespace changed", err)
	}
	if err := inspectManagedBridgeL3(ctx, newDirectRouteNetlink(), fd, network.record); err != nil {
		return err
	}
	if network.uplink != nil {
		if err := network.uplink.revalidate(ctx); err != nil {
			return err
		}
	}
	record := network.record
	authority.firewall, err = firewall.BeginPolicyChange(ctx, network.mutation, firewall.PolicyBinding{
		OwnerUID: authority.ownerUID, NetworkID: string(record.networkID), BootID: record.bootID,
		HostNamespace: firewall.NamespaceIdentity{
			Device: record.managedBridge.namespace.device, Inode: record.managedBridge.namespace.inode,
		},
	})
	return err
}

func trafficPolicyNetworkIntent(record networkAuthorityRecord) firewall.NetworkIntent {
	intent := firewall.NetworkIntent{
		OwnerUID: record.ownerUID, NetworkID: string(record.networkID), HostBridge: record.managedBridge.name.String(),
		Subnet: record.configuration.l3.prefix.value, HostIPv4: record.configuration.l3.gateway.value,
		Masquerade: record.configuration.nat,
	}
	if record.configuration.uplink != nil {
		intent.UplinkInterface = record.configuration.uplink.link.name.String()
	}
	return intent
}

func (authority *trafficPolicyAuthority) release(ctx context.Context, primary error) error {
	cleanup := context.WithoutCancel(ctx)
	if authority.firewall != nil {
		primary = appendNetworkAuthorityError(
			primary,
			"release policy firewall mutation",
			authority.firewall.Release(cleanup),
		)
	}
	for index := len(authority.networks) - 1; index >= 0; index-- {
		primary = appendNetworkAuthorityError(
			primary,
			"release policy network",
			authority.networks[index].Release(cleanup),
		)
	}
	for index := len(authority.journals) - 1; index >= 0; index-- {
		primary = appendNetworkAuthorityError(
			primary,
			"release policy topology journal",
			authority.journals[index].store.Release(cleanup),
		)
	}
	if authority.store != nil {
		primary = appendNetworkAuthorityError(
			primary,
			"release policy topology owner",
			authority.store.Release(cleanup),
		)
	}
	if authority.storeRoot != nil {
		primary = appendNetworkAuthorityError(
			primary,
			"release policy topology root",
			authority.storeRoot.Release(cleanup),
		)
	}
	for index := len(authority.vms) - 1; index >= 0; index-- {
		primary = appendNetworkAuthorityError(primary, "release policy VM lock", authority.vms[index].Release(cleanup))
	}
	if authority.owner != nil {
		primary = appendNetworkAuthorityError(primary, "release policy VM owner", authority.owner.Release(cleanup))
	}
	if authority.index != nil {
		primary = appendNetworkAuthorityError(
			primary,
			"release policy ownership index",
			authority.index.Release(cleanup),
		)
	}
	if authority.jailer != nil {
		primary = appendNetworkAuthorityError(
			primary,
			"release policy authority root",
			authority.jailer.Release(cleanup),
		)
	}
	return primary
}

func trafficFirewallProtocol(protocol model.TrafficProtocol) firewall.Protocol {
	if protocol == model.TrafficProtocolTCP {
		return firewall.TCP
	}
	return firewall.UDP
}
