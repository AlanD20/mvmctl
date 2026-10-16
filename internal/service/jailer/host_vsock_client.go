package jailer

import (
	"context"
	"net"

	"mvmctl/internal/lib/system"
)

// HostVsockOpenAction selects a fixed private host-to-guest socket handoff.
const HostVsockOpenAction = system.HostVsockOpenAction

// HostVsockOpenRequest selects only a canonical VM identity.
type HostVsockOpenRequest = system.HostVsockOpenRequest

// HostVsockClient obtains authenticated host-to-guest connections without exposing private socket paths.
type HostVsockClient struct{}

// NewHostVsockClient constructs the fixed named privileged connection client.
func NewHostVsockClient() *HostVsockClient { return &HostVsockClient{} }

// Open returns an owned base UDS connection. Guest CONNECT and token exchange remain caller responsibilities.
func (*HostVsockClient) Open(ctx context.Context, vmID string) (*net.UnixConn, error) {
	return system.OpenPrivilegedHostVsock(ctx, vmID)
}
