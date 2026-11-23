package jailer

import (
	"context"

	"mvmctl/internal/lib/guestcontrolauth"
)

// GuestControlPrepareAction prepares only root-generated credentials for one exact pending cold VM.
const GuestControlPrepareAction = "jailer-guest-control-prepare"

type GuestControlPrepareRequest struct {
	VMID      string `json:"vm_id"`
	CacheRoot string `json:"cache_root"`
}

func (request GuestControlPrepareRequest) Validate() error {
	if !vmIDPattern.MatchString(request.VMID) {
		return instanceValidationError("Guest Control preparation requires an exact VM ID")
	}
	_, err := parseManagedCacheLocator(request.CacheRoot)
	return err
}

// GuestControlReference identifies immutable root authority; possession alone grants nothing.
type GuestControlReference struct {
	SourceVMID string `json:"source_vm_id"`
	Version    string `json:"version"`
}

// GuestControlPrepareResult is sensitive internal provisioning data, never a public API/CLI result.
// AgentMaterial contains NO host private key. Do not log or pass it through process arguments.
type GuestControlPrepareResult struct {
	VMID           string                `json:"vm_id"`
	Reference      GuestControlReference `json:"reference"`
	NeedsProvision bool                  `json:"needs_provision"`
	AgentMaterial  []byte                `json:"agent_material,omitempty"`
}

func (result GuestControlPrepareResult) Validate(request GuestControlPrepareRequest) error {
	if err := request.Validate(); err != nil {
		return err
	}
	if result.VMID != request.VMID || !vmIDPattern.MatchString(result.Reference.SourceVMID) ||
		!isCanonicalManagedResourceID(result.Reference.Version, 64) {
		return instanceValidationError("Guest Control preparation returned an invalid binding")
	}
	if result.NeedsProvision {
		if result.Reference.SourceVMID != request.VMID {
			return instanceValidationError("cold Guest Control preparation returned another source")
		}
		if _, err := guestcontrolauth.DecodeAgentMaterial(result.AgentMaterial); err != nil {
			return instanceValidationError("Guest Control preparation returned invalid agent material")
		}
	} else if len(result.AgentMaterial) != 0 {
		return instanceValidationError("bound Guest Control preparation returned unexpected secret material")
	}
	return nil
}

type GuestControlCredentialClient struct{ transport *TrustedReleaseClient }

func NewGuestControlCredentialClient() *GuestControlCredentialClient {
	return &GuestControlCredentialClient{transport: NewTrustedReleaseClient()}
}

func (client *GuestControlCredentialClient) Prepare(ctx context.Context, request GuestControlPrepareRequest,
) (GuestControlPrepareResult, error) {
	if err := request.Validate(); err != nil {
		return GuestControlPrepareResult{}, err
	}
	return exchangeTrustedRelease(ctx, client.transport, GuestControlPrepareAction, request, nil, 0,
		func(result GuestControlPrepareResult) error { return result.Validate(request) })
}
