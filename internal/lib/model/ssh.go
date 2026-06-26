package model

import "time"

// --- ConnectionInfo ---

// ConnectionInfo holds SSH connection parameters.
type ConnectionInfo struct {
	Host         string        `json:"host"`
	User         string        `json:"user"`
	KeyPath      string        `json:"key_path,omitempty"`
	ProbeTimeout time.Duration `json:"-"` // how long to wait for SSH readiness; 0 = use default (10s)
}
