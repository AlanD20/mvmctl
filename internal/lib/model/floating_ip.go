package model

// FloatingIPItem is a display projection of root-owned floating-IP authority.
// It is not a SQLite ownership record or permission to configure a guest address.
type FloatingIPItem struct {
	ID                    string `json:"id" db:"id"`
	NetworkID             string `json:"network_id" db:"network_id"`
	Name                  string `json:"name" db:"name"`
	IPv4                  string `json:"ipv4" db:"ipv4"`
	Generation            uint64 `json:"generation" db:"generation"`
	Retired               bool   `json:"retired" db:"retired"`
	Phase                 string `json:"phase" db:"-"`
	OwnerVMID             string `json:"owner_vm_id" db:"-"`
	OwnerOrigin           string `json:"owner_origin" db:"-"`
	Lifetime              string `json:"lifetime" db:"-"`
	OwnerBootID           string `json:"owner_boot_id" db:"-"`
	OwnerLaunchGeneration uint64 `json:"owner_launch_generation" db:"-"`
	AuthorityState        string `json:"authority_state" db:"-"`
	LeaseRemainingMillis  uint64 `json:"lease_remaining_ms" db:"-"`
	AliasSynchronization  string `json:"alias_synchronization" db:"-"`
}
