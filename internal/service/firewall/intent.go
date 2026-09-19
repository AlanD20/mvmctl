package firewall

import "net/netip"

// NamespaceIdentity binds observation to one boot-scoped network namespace.
type NamespaceIdentity struct {
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
}

// LinkIdentity is root-observed replacement evidence, never a public interface selector.
type LinkIdentity struct {
	Name            string `json:"name"`
	IfIndex         int    `json:"ifindex"`
	NamespaceDevice uint64 `json:"namespace_device"`
	NamespaceInode  uint64 `json:"namespace_inode"`
}

// EndpointBinding is resolved by the root network service while its mutation lock is held.
// CRITICAL: This type is internal service intent and must never be decoded from caller requests.
type EndpointBinding struct {
	OwnerUID      uint32            `json:"owner_uid"`
	VMID          string            `json:"vm_id"`
	NetworkID     string            `json:"network_id"`
	BootID        string            `json:"boot_id"`
	HostNamespace NamespaceIdentity `json:"host_namespace"`
	Prefix        netip.Prefix      `json:"prefix"`
	Gateway       netip.Addr        `json:"gateway"`
	IPv4          netip.Addr        `json:"ipv4"`
	MAC           [6]byte           `json:"mac"`
	ManagedBridge LinkIdentity      `json:"managed_bridge"`
	HostVeth      LinkIdentity      `json:"host_veth"`
}

// NetworkIntent is a complete root-resolved managed-network firewall projection.
type NetworkIntent struct {
	OwnerUID        uint32       `json:"owner_uid"`
	NetworkID       string       `json:"network_id"`
	HostBridge      string       `json:"host_bridge"`
	Subnet          netip.Prefix `json:"subnet"`
	HostIPv4        netip.Addr   `json:"host_ipv4"`
	UplinkInterface string       `json:"uplink_interface"`
	Masquerade      bool         `json:"masquerade"`
}

// Protocol is a closed traffic-policy transport protocol.
type Protocol uint8

const (
	// TCP selects TCP traffic only.
	TCP Protocol = 6
	// UDP selects UDP traffic only.
	UDP Protocol = 17
)

// PortInterval is an inclusive destination-port range.
type PortInterval struct {
	First uint16 `json:"first"`
	Last  uint16 `json:"last"`
}

// VMTrafficGrant permits one root-resolved VM pair.
type VMTrafficGrant struct {
	ID              string       `json:"id"`
	SourceVMID      string       `json:"source_vm_id"`
	DestinationVMID string       `json:"destination_vm_id"`
	Protocol        Protocol     `json:"protocol"`
	Ports           PortInterval `json:"ports"`
}

// NetworkTrafficGrant permits a managed network to reach one VM.
type NetworkTrafficGrant struct {
	ID              string       `json:"id"`
	SourceNetworkID string       `json:"source_network_id"`
	DestinationVMID string       `json:"destination_vm_id"`
	Protocol        Protocol     `json:"protocol"`
	Ports           PortInterval `json:"ports"`
}

// HostServiceIntent permits one managed network to reach a fixed host service.
type HostServiceIntent struct {
	ID              string       `json:"id"`
	SourceNetworkID string       `json:"source_network_id"`
	Protocol        Protocol     `json:"protocol"`
	Ports           PortInterval `json:"ports"`
}

// Intent is the full host-global graph stored beside the authoritative generation record.
// A graph is admitted only when its compiler-derived identity agrees with that record.
type Intent struct {
	Endpoints     []EndpointBinding     `json:"endpoints"`
	Networks      []NetworkIntent       `json:"networks"`
	VMGrants      []VMTrafficGrant      `json:"vm_grants"`
	NetworkGrants []NetworkTrafficGrant `json:"network_grants"`
	HostServices  []HostServiceIntent   `json:"host_services"`
}

func (intent Intent) compile() (compiledGeneration, error) {
	resolved := generationIntent{}
	for _, endpoint := range intent.Endpoints {
		if err := validateEndpointBinding(endpoint, false); err != nil {
			return compiledGeneration{}, err
		}
		resolved.endpoints = append(resolved.endpoints, endpointIntent{
			id: endpoint.VMID, networkID: endpoint.NetworkID, hostVeth: endpoint.HostVeth.Name,
			ipv4: endpoint.IPv4, mac: macAddress(endpoint.MAC),
		})
	}
	for _, network := range intent.Networks {
		resolved.networks = append(resolved.networks, networkIntent{
			id: network.NetworkID, hostBridge: network.HostBridge, subnet: network.Subnet,
			hostIPv4: network.HostIPv4, uplinkInterface: network.UplinkInterface, masquerade: network.Masquerade,
		})
	}
	for _, grant := range intent.VMGrants {
		resolved.vmGrants = append(resolved.vmGrants, vmTrafficGrant{
			id: grant.ID, sourceVMID: grant.SourceVMID, destinationVMID: grant.DestinationVMID,
			protocol: protocol(grant.Protocol), ports: portInterval{first: grant.Ports.First, last: grant.Ports.Last},
		})
	}
	for _, grant := range intent.NetworkGrants {
		resolved.networkGrants = append(resolved.networkGrants, networkTrafficGrant{
			id: grant.ID, sourceNetworkID: grant.SourceNetworkID, destinationVMID: grant.DestinationVMID,
			protocol: protocol(grant.Protocol), ports: portInterval{first: grant.Ports.First, last: grant.Ports.Last},
		})
	}
	for _, service := range intent.HostServices {
		resolved.hostServices = append(resolved.hostServices, hostServiceIntent{
			id:              service.ID,
			sourceNetworkID: service.SourceNetworkID,
			protocol: protocol(
				service.Protocol,
			),
			ports: portInterval{first: service.Ports.First, last: service.Ports.Last},
		})
	}
	return compileGeneration(resolved)
}

func validateEndpointBinding(binding EndpointBinding, preparing bool) error {
	if !generationBootIDPattern.MatchString(binding.BootID) || binding.HostNamespace.Device == 0 ||
		binding.HostNamespace.Inode == 0 || !validResourceID(binding.VMID) || !validResourceID(binding.NetworkID) ||
		!validIPv4Prefix(binding.Prefix) || !usableIPv4Address(binding.Prefix, binding.Gateway) ||
		!usableIPv4Address(binding.Prefix, binding.IPv4) || binding.IPv4 == binding.Gateway ||
		binding.MAC == [6]byte{} || binding.MAC[0]&1 != 0 {
		return generationAuthorityError("firewall endpoint binding is incomplete", nil, nil)
	}
	for _, link := range []LinkIdentity{binding.ManagedBridge, binding.HostVeth} {
		if !validInterfaceName(link.Name) || link.NamespaceDevice != binding.HostNamespace.Device ||
			link.NamespaceInode != binding.HostNamespace.Inode || link.IfIndex < 0 {
			return generationAuthorityError("firewall endpoint link binding is invalid", nil, nil)
		}
	}
	if binding.ManagedBridge.IfIndex == 0 || (!preparing && binding.HostVeth.IfIndex == 0) {
		return generationAuthorityError("firewall endpoint has no observed link identity", nil, nil)
	}
	return nil
}
