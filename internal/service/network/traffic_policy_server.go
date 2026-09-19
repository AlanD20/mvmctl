package network

import (
	"context"
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
	authority, err := acquireTrafficPolicyAuthority(ctx, ownerUID, []string{request.DestinationVMID}, true)
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
		true,
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
	authority, err := acquireTrafficPolicyAuthority(ctx, ownerUID, []string{request.DestinationVMID}, false)
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
		false,
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
	network    *managedNetworkLease
	firewall   *firewall.MutationLease
}

func acquireTrafficPolicyAuthority(
	ctx context.Context, ownerUID uint32, vmIDs []string, applying bool,
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
				"traffic policy requires an existing root-owned VM topology; stopped or unlaunched endpoint admission is unavailable",
				errs.WithEntity(id),
			)
		}
		if applying && (record.Lifecycle != topologyActive || record.BootID != boot) {
			return nil, errs.New(
				errs.CodePolicyEndpointInvalid,
				"traffic policy installation currently requires an active root-owned endpoint; stopped-VM admission is unavailable",
				errs.WithEntity(id),
			)
		}
		if !applying && record.Lifecycle != topologyActive && record.Lifecycle != topologyCleaned {
			return nil, errs.New(
				errs.CodePolicyEndpointInvalid,
				"traffic policy endpoint requires topology recovery before revocation",
				errs.WithEntity(id),
			)
		}
		authority.topologies[id] = record
	}
	return authority, nil
}

func (authority *trafficPolicyAuthority) begin(ctx context.Context, networkID string) error {
	lock, err := (managedNetworkLocker{}).LockNetwork(
		ctx,
		networkKey{ownerUID: OwnerUID(authority.ownerUID), networkID: NetworkID(networkID)},
	)
	if err != nil {
		return err
	}
	authority.network = lock.(*managedNetworkLease)
	record := authority.network.record
	authority.firewall, err = firewall.BeginPolicyChange(ctx, authority.network.mutation, firewall.PolicyBinding{
		OwnerUID:  authority.ownerUID,
		NetworkID: string(record.networkID),
		BootID:    record.bootID,
		HostNamespace: firewall.NamespaceIdentity{
			Device: record.managedBridge.namespace.device,
			Inode:  record.managedBridge.namespace.inode,
		},
	})
	return err
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
	if authority.network != nil {
		primary = appendNetworkAuthorityError(
			primary,
			"release policy source network",
			authority.network.Release(cleanup),
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
