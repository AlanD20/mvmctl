package inputs

import (
	"context"
	"fmt"
	"mvmctl/internal/core/vm"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/lib/validators"
	"mvmctl/pkg/errs"
	"strings"

	"github.com/jmoiron/sqlx"
)

// VMInput specifies v m input.
type VMInput struct {
	Identifiers []string `json:"identifiers"`
	Force       bool     `json:"force"`
}

// ResolvedVMInput specifies resolved v m input.
type ResolvedVMInput struct {
	VMs   []*model.VMItem
	Force bool
}

// VMRequest specifies v m request.
// Request to resolve a VM by name, ID, IP, or MAC.
// Create VMResolver with full enrichment (image, kernel, network, volumes, binary).
type VMRequest struct {
	db       *sqlx.DB
	input    VMInput
	result   *ResolvedVMInput
	resolver *vm.Resolver
}

// NewVMRequest creates a new VMRequest.
func NewVMRequest(inputs VMInput, db *sqlx.DB, vmRepo vm.Repository) *VMRequest {
	return &VMRequest{
		db:       db,
		input:    inputs,
		resolver: vm.NewResolver(vmRepo),
	}
}

// Result returns the resolved input, or nil if resolve() has not been called.
// Resolve resolves VM identifiers to VMInstanceItem records.
func (r *VMRequest) Resolve(ctx context.Context) (*ResolvedVMInput, error) {
	if len(r.input.Identifiers) == 0 {
		return nil, errs.NotFound(errs.CodeVMNotFound, "No VM identifiers provided")
	}
	if err := r.validateIdentifiers(); err != nil {
		return nil, err
	}
	result := r.resolver.ResolveMany(ctx, r.input.Identifiers)
	if result == nil {
		return nil, errs.NotFound(errs.CodeVMNotFound, "Could not resolve any VMs")
	}
	if len(result.Errors) > 0 && len(result.VMs) == 0 {
		return nil, errs.NotFound(
			errs.CodeVMNotFound,
			fmt.Sprintf("Could not resolve any VMs: %s", strings.Join(result.Errors, ", ")),
		)
	}
	r.result = &ResolvedVMInput{
		VMs:   result.VMs,
		Force: r.input.Force,
	}
	return r.result, nil
}

// --- VMExecInput ---
// VMExecInput holds the input for executing a command inside a VM via vsock.
type VMExecInput struct {
	Identifier string `json:"target"            yaml:"target"`
	Command    string `json:"command,omitempty" yaml:"cmd,omitempty"` // empty = interactive shell
	User       string `json:"user"              yaml:"user"`
	Timeout    int    `json:"timeout"           yaml:"timeout"`
	Port       int    `json:"port"              yaml:"port"`
}

// validateIdentifiers validates each identifier based on detected type.
func (r *VMRequest) validateIdentifiers() error {
	for _, identifier := range r.input.Identifiers {
		if validators.ValidMACRegex.MatchString(identifier) {
			if err := validators.MAC(identifier); err != nil {
				return errs.New(errs.CodeVMResolveFailed, fmt.Sprintf("Invalid MAC address: %s", identifier))
			}
		} else if validators.IsIPAddress(identifier) {
			if err := validators.IPv4Address(identifier, "guest IP", true, "", ""); err != nil {
				return errs.New(errs.CodeVMResolveFailed, fmt.Sprintf("Invalid guest IP: %s", identifier))
			}
		} else {
			// Name or ID — validate as entity name
			if err := validators.EntityName(identifier, "VM", 63); err != nil {
				return errs.New(errs.CodeVMResolveFailed, fmt.Sprintf("Invalid VM identifier: %s", identifier))
			}
		}
	}
	return nil
}
