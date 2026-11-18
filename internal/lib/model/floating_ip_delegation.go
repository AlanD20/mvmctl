package model

import "time"

// FloatingIPDelegationItem projects stored root permission, never session readiness or guest identity authority.
type FloatingIPDelegationItem struct {
	ID               string        `json:"id" db:"id"`
	NetworkID        string        `json:"network_id" db:"network_id"`
	ResourceID       string        `json:"resource_id" db:"resource_id"`
	VMID             string        `json:"vm_id" db:"vm_id"`
	BootID           string        `json:"boot_id" db:"boot_id"`
	ExecutionUID     uint32        `json:"execution_uid" db:"execution_uid"`
	LaunchGeneration uint64        `json:"launch_generation" db:"launch_generation"`
	Method           string        `json:"method" db:"method"`
	LeaseDuration    time.Duration `json:"lease_duration_ns" db:"lease_duration_ns"`
	Revision         uint64        `json:"revision" db:"revision"`
	State            string        `json:"state" db:"state"`
}
