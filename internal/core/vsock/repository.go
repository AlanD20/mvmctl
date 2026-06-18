// Package vsock provides the vsock domain for guest agent communication.
//
// The vsock domain manages per-VM vsock device configuration (guest CID,
// UDS path, port, auth token) and provides a Client for executing commands
// and interactive shells inside VMs via the Firecracker vsock device.
//
// Domain structure:
// - Client: per-VM protocol client (Exec, Shell, Teardown) — NOT a Controller
// - Repository: data access for VsockConfigItem
// - Resolver: GetByVMID enrichment support
// - protocol.go: unexported UDS dial, CONNECT handshake, JSON framing
package vsock

import (
	"context"

	"mvmctl/internal/lib/model"
)

// Repository is the data access interface for vsock configuration.
type Repository interface {
	// GetByVMID returns the vsock config for a VM. Returns nil, nil if not found.
	GetByVMID(ctx context.Context, vmID string) (*model.VsockConfigItem, error)

	// ListByVMIDs returns vsock configs for multiple VMs. Returns empty slice if none found.
	ListByVMIDs(ctx context.Context, vmIDs []string) ([]*model.VsockConfigItem, error)

	// Upsert creates or updates a vsock config record.
	Upsert(ctx context.Context, item *model.VsockConfigItem) error

	// DeleteByVMID removes the vsock config for a VM. No-op if not found.
	DeleteByVMID(ctx context.Context, vmID string) error

	// SetUpgradeLock sets the upgrade lock for a VM's vsock agent.
	// Returns error if lock is already held (upgrading=1).
	SetUpgradeLock(ctx context.Context, vmID string) error

	// ClearUpgradeLock removes the upgrade lock for a VM's vsock agent.
	ClearUpgradeLock(ctx context.Context, vmID string) error

	// UpdateAgentVersion persists the agent version after a successful upgrade.
	UpdateAgentVersion(ctx context.Context, vmID, version string) error
}
