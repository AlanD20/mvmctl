package model

// VsockConfigItem is the DB record for per-VM vsock configuration.
// Matches Python's VsockConfigItem dataclass exactly.
type VsockConfigItem struct {
	ID       string `json:"id"        db:"id"`
	VmID     string `json:"vm_id"     db:"vm_id"`
	GuestCID int    `json:"guest_cid" db:"guest_cid"` // unique constraint
	UDSPath  string `json:"uds_path"  db:"uds_path"`
	Port     int    `json:"port"      db:"port"`
	Token    string `json:"token"     db:"token"`
}

// VsockConfig is the Firecracker JSON config section for the vsock device.
// Embedded in FirecrackerVMConfig as an optional field.
type VsockConfig struct {
	GuestCID int    `json:"guest_cid"`
	UDSPath  string `json:"uds_path"`
}
