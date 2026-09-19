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

// EndpointReservation retains the immutable root-owned VM identity while its topology is stopped.
// Kernel link and namespace observations deliberately do not survive here as activation authority.
type EndpointReservation struct {
	OwnerUID  uint32       `json:"owner_uid"`
	VMID      string       `json:"vm_id"`
	NetworkID string       `json:"network_id"`
	Prefix    netip.Prefix `json:"prefix"`
	Gateway   netip.Addr   `json:"gateway"`
	IPv4      netip.Addr   `json:"ipv4"`
	MAC       [6]byte      `json:"mac"`
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
	ActiveNetworks []string              `json:"active_networks"`
	Reservations   []EndpointReservation `json:"reservations"`
	Endpoints      []EndpointBinding     `json:"endpoints"`
	Networks       []NetworkIntent       `json:"networks"`
	VMGrants       []VMTrafficGrant      `json:"vm_grants"`
	NetworkGrants  []NetworkTrafficGrant `json:"network_grants"`
	HostServices   []HostServiceIntent   `json:"host_services"`
}

func (intent Intent) compile() (compiledGeneration, error) {
	if err := intent.validateDurablePolicies(); err != nil {
		return compiledGeneration{}, err
	}
	resolved := generationIntent{}
	active := make(map[string]struct{}, len(intent.Endpoints))
	activeNetworks := make(map[string]struct{}, len(intent.ActiveNetworks))
	for _, id := range intent.ActiveNetworks {
		activeNetworks[id] = struct{}{}
	}
	for _, endpoint := range intent.Endpoints {
		if err := validateEndpointBinding(endpoint, false); err != nil {
			return compiledGeneration{}, err
		}
		resolved.endpoints = append(resolved.endpoints, endpointIntent{
			id: endpoint.VMID, networkID: endpoint.NetworkID, hostVeth: endpoint.HostVeth.Name,
			ipv4: endpoint.IPv4, mac: macAddress(endpoint.MAC),
		})
		active[endpoint.VMID] = struct{}{}
	}
	for _, network := range intent.Networks {
		if _, found := activeNetworks[network.NetworkID]; !found {
			continue
		}
		resolved.networks = append(resolved.networks, networkIntent{
			id: network.NetworkID, hostBridge: network.HostBridge, subnet: network.Subnet,
			hostIPv4: network.HostIPv4, uplinkInterface: network.UplinkInterface, masquerade: network.Masquerade,
		})
	}
	for _, grant := range intent.VMGrants {
		_, sourceActive := active[grant.SourceVMID]
		_, destinationActive := active[grant.DestinationVMID]
		if !sourceActive || !destinationActive {
			continue
		}
		resolved.vmGrants = append(resolved.vmGrants, vmTrafficGrant{
			id: grant.ID, sourceVMID: grant.SourceVMID, destinationVMID: grant.DestinationVMID,
			protocol: protocol(grant.Protocol), ports: portInterval{first: grant.Ports.First, last: grant.Ports.Last},
		})
	}
	for _, grant := range intent.NetworkGrants {
		if _, sourceActive := activeNetworks[grant.SourceNetworkID]; !sourceActive {
			continue
		}
		if _, destinationActive := active[grant.DestinationVMID]; !destinationActive {
			continue
		}
		resolved.networkGrants = append(resolved.networkGrants, networkTrafficGrant{
			id: grant.ID, sourceNetworkID: grant.SourceNetworkID, destinationVMID: grant.DestinationVMID,
			protocol: protocol(grant.Protocol), ports: portInterval{first: grant.Ports.First, last: grant.Ports.Last},
		})
	}
	for _, service := range intent.HostServices {
		if _, sourceActive := activeNetworks[service.SourceNetworkID]; !sourceActive {
			continue
		}
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

func endpointReservation(binding EndpointBinding) EndpointReservation {
	return EndpointReservation{
		OwnerUID: binding.OwnerUID, VMID: binding.VMID, NetworkID: binding.NetworkID,
		Prefix: binding.Prefix, Gateway: binding.Gateway, IPv4: binding.IPv4, MAC: binding.MAC,
	}
}

func validateEndpointReservation(reservation EndpointReservation) error {
	if reservation.OwnerUID == 0 || !validResourceID(reservation.VMID) || !validResourceID(reservation.NetworkID) ||
		!validIPv4Prefix(reservation.Prefix) || !usableIPv4Address(reservation.Prefix, reservation.Gateway) ||
		!usableIPv4Address(reservation.Prefix, reservation.IPv4) || reservation.IPv4 == reservation.Gateway ||
		reservation.MAC == [6]byte{} || reservation.MAC[0]&1 != 0 {
		return generationAuthorityError("durable firewall endpoint reservation is invalid", nil, nil)
	}
	return nil
}

// validateDurablePolicies admits the complete desired inventory before compiling its active projection.
// A stopped endpoint may suppress a rule, but never bypass policy grammar or ownership admission.
func (intent Intent) validateDurablePolicies() error {
	networks := make(map[string]NetworkIntent, len(intent.Networks))
	for _, network := range intent.Networks {
		if network.OwnerUID == 0 || !validResourceID(network.NetworkID) || !validIPv4Prefix(network.Subnet) ||
			!usableIPv4Address(network.Subnet, network.HostIPv4) {
			return invalidGeneration("durable network logical authority is invalid")
		}
		if _, found := networks[network.NetworkID]; found {
			return invalidGeneration("duplicate durable network intent")
		}
		networks[network.NetworkID] = network
	}
	activeNetworks := make(map[string]struct{}, len(intent.ActiveNetworks))
	for _, id := range intent.ActiveNetworks {
		if _, found := networks[id]; !found {
			return invalidGeneration("active network has no durable intent")
		}
		if _, found := activeNetworks[id]; found {
			return invalidGeneration("duplicate active network identity")
		}
		activeNetworks[id] = struct{}{}
	}
	reservations := make(map[string]EndpointReservation, len(intent.Reservations))
	addresses := make(map[string]string, len(intent.Reservations))
	macs := make(map[[6]byte]string, len(intent.Reservations))
	for _, reservation := range intent.Reservations {
		if err := validateEndpointReservation(reservation); err != nil {
			return err
		}
		if _, found := reservations[reservation.VMID]; found {
			return invalidGeneration("duplicate durable VM reservation")
		}
		network, found := networks[reservation.NetworkID]
		if !found || network.OwnerUID != reservation.OwnerUID || network.Subnet != reservation.Prefix ||
			network.HostIPv4 != reservation.Gateway {
			return invalidGeneration("durable VM reservation differs from its network authority")
		}
		address := reservation.NetworkID + "/" + reservation.IPv4.String()
		if _, found := addresses[address]; found {
			return invalidGeneration("duplicate durable VM IPv4 reservation")
		}
		if _, found := macs[reservation.MAC]; found {
			return invalidGeneration("duplicate durable VM MAC reservation")
		}
		reservations[reservation.VMID] = reservation
		addresses[address] = reservation.VMID
		macs[reservation.MAC] = reservation.VMID
	}
	for _, endpoint := range intent.Endpoints {
		if _, found := activeNetworks[endpoint.NetworkID]; !found {
			return invalidGeneration("active endpoint has no active network")
		}
		reservation, found := reservations[endpoint.VMID]
		if !found || reservation != endpointReservation(endpoint) {
			return invalidGeneration("active VM endpoint differs from durable reservation")
		}
	}
	ids := make(map[string]struct{}, len(intent.VMGrants)+len(intent.NetworkGrants)+len(intent.HostServices))
	for _, grant := range intent.VMGrants {
		if err := validateGrantBase(
			grant.ID,
			protocol(grant.Protocol),
			portInterval{first: grant.Ports.First, last: grant.Ports.Last},
			ids,
		); err != nil {
			return err
		}
		source, sourceFound := reservations[grant.SourceVMID]
		destination, destinationFound := reservations[grant.DestinationVMID]
		if !sourceFound || !destinationFound || source.VMID == destination.VMID ||
			source.OwnerUID != destination.OwnerUID {
			return invalidGeneration("durable VM grant references an unowned endpoint pair")
		}
	}
	for _, grant := range intent.NetworkGrants {
		if err := validateGrantBase(
			grant.ID,
			protocol(grant.Protocol),
			portInterval{first: grant.Ports.First, last: grant.Ports.Last},
			ids,
		); err != nil {
			return err
		}
		source, sourceFound := networks[grant.SourceNetworkID]
		destination, destinationFound := reservations[grant.DestinationVMID]
		if !sourceFound || !destinationFound || source.OwnerUID != destination.OwnerUID {
			return invalidGeneration("durable network grant references an unowned endpoint")
		}
	}
	for _, service := range intent.HostServices {
		if err := validateGrantBase(
			service.ID,
			protocol(service.Protocol),
			portInterval{first: service.Ports.First, last: service.Ports.Last},
			ids,
		); err != nil {
			return err
		}
		if _, found := networks[service.SourceNetworkID]; !found {
			return invalidGeneration("host service references an absent network")
		}
	}
	return nil
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
