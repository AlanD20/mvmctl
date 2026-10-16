package model

// VMRuntimeStatusUpdate projects one authoritative observation into the user-owned VM database.
type VMRuntimeStatusUpdate struct {
	VMID   string   `json:"vm_id" db:"id"`
	Status VMStatus `json:"status" db:"status"`
}
