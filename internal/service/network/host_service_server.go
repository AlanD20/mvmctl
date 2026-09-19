package network

import (
	"context"

	"mvmctl/internal/infra"
	"mvmctl/internal/service/firewall"
	"mvmctl/pkg/errs"
)

// HostServiceServer authorizes only NoCloud on an authenticated owner's managed gateway.
type HostServiceServer struct{}

// NewHostServiceServer constructs the closed NoCloud authority boundary.
func NewHostServiceServer() *HostServiceServer { return &HostServiceServer{} }

// InspectHostService resolves network facts without granting service access.
func (server *HostServiceServer) InspectHostService(ctx context.Context, ownerUID uint32, request HostServiceRequest) (_ HostServiceResult, returnErr error) {
	lease, err := acquireHostServiceNetwork(ctx, ownerUID, request)
	if err != nil {
		return HostServiceResult{}, err
	}
	defer func() {
		returnErr = appendNetworkAuthorityError(returnErr, "release NoCloud network", lease.Release(context.WithoutCancel(ctx)))
	}()
	return hostServiceResult(lease), nil
}

// ApplyHostService derives the fixed grant exclusively from root-owned network authority.
// CRITICAL: ownerUID comes from the authenticated dispatcher, never caller-supplied SQL or JSON.
func (server *HostServiceServer) ApplyHostService(ctx context.Context, ownerUID uint32, request HostServiceRequest) (_ HostServiceResult, returnErr error) {
	authority, err := beginHostServiceChange(ctx, ownerUID, request)
	if err != nil {
		return HostServiceResult{}, err
	}
	defer func() { returnErr = authority.release(ctx, returnErr) }()
	if err := authority.firewall.ApplyHostService(ctx, noCloudServiceIntent(request)); err != nil {
		return HostServiceResult{}, err
	}
	return hostServiceResult(authority.network), nil
}

// RemoveHostService proves exact revocation or absence in the root generation.
func (server *HostServiceServer) RemoveHostService(ctx context.Context, ownerUID uint32, request HostServiceRequest) (_ HostServiceResult, returnErr error) {
	authority, err := beginHostServiceChange(ctx, ownerUID, request)
	if err != nil {
		return HostServiceResult{}, err
	}
	defer func() { returnErr = authority.release(ctx, returnErr) }()
	if err := authority.firewall.RemoveHostService(ctx, noCloudServiceIntent(request)); err != nil {
		return HostServiceResult{}, err
	}
	return hostServiceResult(authority.network), nil
}

func acquireHostServiceNetwork(ctx context.Context, ownerUID uint32, request HostServiceRequest) (*managedNetworkLease, error) {
	if ctx == nil || ownerUID == 0 {
		return nil, errs.New(errs.CodeValidationFailed, "authenticated non-root NoCloud owner and context are required")
	}
	if err := request.Validate(); err != nil {
		return nil, err
	}
	lease, err := (managedNetworkLocker{}).LockNetwork(ctx,
		networkKey{ownerUID: OwnerUID(ownerUID), networkID: NetworkID(request.NetworkID)})
	if err != nil {
		return nil, err
	}
	return lease.(*managedNetworkLease), nil
}

func beginHostServiceChange(ctx context.Context, ownerUID uint32, request HostServiceRequest) (*trafficPolicyAuthority, error) {
	lease, err := acquireHostServiceNetwork(ctx, ownerUID, request)
	if err != nil {
		return nil, err
	}
	authority := &trafficPolicyAuthority{ownerUID: ownerUID, network: lease}
	record := lease.record
	authority.firewall, err = firewall.BeginPolicyChange(ctx, lease.mutation, firewall.PolicyBinding{
		OwnerUID: ownerUID, NetworkID: string(record.networkID), BootID: record.bootID,
		HostNamespace: firewall.NamespaceIdentity{Device: record.managedBridge.namespace.device, Inode: record.managedBridge.namespace.inode},
	})
	if err != nil {
		return nil, authority.release(ctx, err)
	}
	return authority, nil
}

func noCloudServiceIntent(request HostServiceRequest) firewall.HostServiceIntent {
	return firewall.HostServiceIntent{ID: request.serviceID(), SourceNetworkID: request.NetworkID,
		Protocol: firewall.TCP, Ports: firewall.PortInterval{First: infra.NoCloudNetPort, Last: infra.NoCloudNetPort}}
}

func hostServiceResult(lease *managedNetworkLease) HostServiceResult {
	return HostServiceResult{NetworkID: string(lease.record.networkID),
		Gateway: lease.record.configuration.l3.gateway.value.String(), Port: infra.NoCloudNetPort}
}
