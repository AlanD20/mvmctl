package model

// TrafficProtocol is the only IP transport protocol granted by traffic policies.
type TrafficProtocol string

const (
	// TrafficProtocolTCP grants TCP traffic.
	TrafficProtocolTCP TrafficProtocol = "tcp"
	// TrafficProtocolUDP grants UDP traffic.
	TrafficProtocolUDP TrafficProtocol = "udp"
)

// ManagedNetworkRelation describes the current placement of both endpoints of a traffic policy.
// It is resolved runtime data and is never persisted as policy identity because VM placement may change.
type ManagedNetworkRelation string

const (
	// ManagedNetworkRelationSame means both endpoints currently share one managed network.
	ManagedNetworkRelationSame ManagedNetworkRelation = "same-network"
	// ManagedNetworkRelationCross means the endpoints currently use different managed networks.
	ManagedNetworkRelationCross ManagedNetworkRelation = "cross-network"
)

// NetworkToVMTrafficAllowIntent grants one managed network access to one exact VM guest port.
// The port is reached through the VM's managed guest address. This intent cannot make a service bound only to guest
// loopback reachable, grant host command execution, or describe a compiled firewall rule.
type NetworkToVMTrafficAllowIntent struct {
	ID                   string          `json:"id"                     db:"id"`
	SourceNetworkID      string          `json:"source_network_id"      db:"source_network_id"`
	DestinationVMID      string          `json:"destination_vm_id"      db:"destination_vm_id"`
	Protocol             TrafficProtocol `json:"protocol"               db:"protocol"`
	DestinationPortStart int             `json:"destination_port_start" db:"destination_port_start"`
	DestinationPortEnd   int             `json:"destination_port_end"   db:"destination_port_end"`
	CreatedAt            string          `json:"created_at"             db:"created_at"`
	UpdatedAt            string          `json:"updated_at"             db:"updated_at"`
}

// VMToVMTrafficAllowIntent grants one exact VM access to another exact VM guest port.
// The port is reached through the destination VM's managed guest address. This intent applies to the IP data plane
// only; same-network placement grants nothing without an exact allow intent.
type VMToVMTrafficAllowIntent struct {
	ID                   string          `json:"id"                     db:"id"`
	SourceVMID           string          `json:"source_vm_id"           db:"source_vm_id"`
	DestinationVMID      string          `json:"destination_vm_id"      db:"destination_vm_id"`
	Protocol             TrafficProtocol `json:"protocol"               db:"protocol"`
	DestinationPortStart int             `json:"destination_port_start" db:"destination_port_start"`
	DestinationPortEnd   int             `json:"destination_port_end"   db:"destination_port_end"`
	CreatedAt            string          `json:"created_at"             db:"created_at"`
	UpdatedAt            string          `json:"updated_at"             db:"updated_at"`
}

// VMToVMExecAllowIntent grants one exact source VM access to one exact destination VM user through the vsock relay.
// It is directional, independent of managed-network placement, and never grants IP traffic or VM-to-host execution.
type VMToVMExecAllowIntent struct {
	ID              string `json:"id"                db:"id"`
	SourceVMID      string `json:"source_vm_id"      db:"source_vm_id"`
	DestinationVMID string `json:"destination_vm_id" db:"destination_vm_id"`
	TargetUser      string `json:"target_user"       db:"target_user"`
	CreatedAt       string `json:"created_at"        db:"created_at"`
	UpdatedAt       string `json:"updated_at"        db:"updated_at"`
}

// HostToVMExecIdentity is the resolved authorization identity for an ordinary host-originated exec request.
// Host authorization is owner-bound runtime state and is not represented by a VM-to-VM exec policy row.
type HostToVMExecIdentity struct {
	OwnerUID        uint32 `json:"owner_uid"         db:"owner_uid"`
	DestinationVMID string `json:"destination_vm_id" db:"destination_vm_id"`
	TargetUser      string `json:"target_user"       db:"target_user"`
}

// VMToVMExecIdentity is the resolved identity authenticated by the host vsock relay.
// The relay binds OwnerUID from root-owned instance state; callers cannot use this type to authorize VM-to-host exec.
type VMToVMExecIdentity struct {
	OwnerUID        uint32 `json:"owner_uid"         db:"owner_uid"`
	SourceVMID      string `json:"source_vm_id"      db:"source_vm_id"`
	DestinationVMID string `json:"destination_vm_id" db:"destination_vm_id"`
	TargetUser      string `json:"target_user"       db:"target_user"`
}
