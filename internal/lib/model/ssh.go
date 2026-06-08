package model

// ── ConnectionInfo ──

// ConnectionInfo holds SSH connection parameters.
type ConnectionInfo struct {
	Host    string `json:"host"`
	User    string `json:"user"`
	KeyPath string `json:"key_path,omitempty"`
}
