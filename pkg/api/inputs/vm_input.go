package inputs

import (
	"context"
	"fmt"
	"strings"

	"mvmctl/internal/core/vm"
	"mvmctl/internal/enricher"
	"mvmctl/internal/infra/errs"
	"mvmctl/internal/infra/model"
	"mvmctl/internal/infra/validators"

	"github.com/jmoiron/sqlx"
)

// VMInput matches Python's VMInput dataclass.
//
//	@dataclass
//	class VMInput:
//	    identifiers: list[str] = field(default_factory=list)
//	    force: bool | None = None
type VMInput struct {
	Identifiers []string `json:"identifiers"`
	Force       bool     `json:"force"`
}

// ResolvedVMInput matches Python's ResolvedVMInput (frozen dataclass).
//
//	@dataclass(frozen=True)
//	class ResolvedVMInput:
//	    vms: list[VMInstanceItem]
//	    force: bool
type ResolvedVMInput struct {
	VMs   []*model.VM
	Force bool
}

// VMRequest matches Python's VMRequest.
//
// Request to resolve a VM by name, ID, IP, or MAC.
// Python version creates VMResolver with include=["image","kernel","network","network.leases","volumes","binary"].
type VMRequest struct {
	db       *sqlx.DB
	input    VMInput
	result   *ResolvedVMInput
	resolver *vm.Resolver
	enricher *enricher.Enricher
}

// NewVMRequest creates a new VMRequest.
// Accepts optional enricher for enriching resolved VMs with related data.
func NewVMRequest(inputs VMInput, db *sqlx.DB, vmRepo vm.Repository, enricher *enricher.Enricher) *VMRequest {
	return &VMRequest{
		db:       db,
		input:    inputs,
		resolver: vm.NewResolver(vmRepo),
		enricher: enricher,
	}
}

// Result returns the resolved input, or nil if resolve() has not been called.

// Resolve resolves VM identifiers to VMInstanceItem records.
// Matches Python's VMRequest.resolve().
func (r *VMRequest) Resolve(ctx context.Context) (*ResolvedVMInput, error) {
	if len(r.input.Identifiers) == 0 {
		return nil, &errs.DomainError{
			Code:    errs.CodeVMNotFound,
			Op:      "vm",
			Message: "No VM identifiers provided",
			Class:   errs.ClassValidation,
		}
	}
	if err := r.validateIdentifiers(); err != nil {
		return nil, err
	}

	result := r.resolver.ResolveMany(ctx, r.input.Identifiers)
	if result == nil {
		return nil, &errs.DomainError{
			Code:    errs.CodeVMNotFound,
			Op:      "vm",
			Message: "Could not resolve any VMs",
			Class:   errs.ClassValidation,
		}
	}

	if len(result.Errors) > 0 && len(result.VMs) == 0 {
		return nil, &errs.DomainError{
			Code:    errs.CodeVMNotFound,
			Op:      "vm",
			Message: fmt.Sprintf("Could not resolve any VMs: %s", strings.Join(result.Errors, ", ")),
			Class:   errs.ClassValidation,
		}
	}

	// Enrich resolved VMs with related data (image, kernel, network, volumes, binary).
	// Matches Python's VMResolver(include=["image","kernel","network","network.leases","volumes","binary"]).
	if r.enricher != nil && len(result.VMs) > 0 {
		_ = r.enricher.EnrichVM(ctx, result.VMs, "kernel", "image", "binary", "network", "network.leases", "volumes")
	}

	r.result = &ResolvedVMInput{
		VMs:   result.VMs,
		Force: r.input.Force,
	}

	return r.result, nil
}

// validateIdentifiers validates each identifier based on detected type.
// Matches Python's VMRequest._validate_identifiers().
func (r *VMRequest) validateIdentifiers() error {
	for _, identifier := range r.input.Identifiers {
		if validators.IsMAC(identifier) {
			if err := validators.MAC(identifier); err != nil {
				return &errs.DomainError{
					Code:    errs.CodeVMResolveFailed,
					Op:      "vm",
					Message: fmt.Sprintf("Invalid MAC address: %s", identifier),
					Class:   errs.ClassValidation,
				}
			}
		} else if validators.IsIPAddress(identifier) {
			if err := validators.IPv4Address(identifier, "guest IP", true, "", ""); err != nil {
				return &errs.DomainError{
					Code:    errs.CodeVMResolveFailed,
					Op:      "vm",
					Message: fmt.Sprintf("Invalid guest IP: %s", identifier),
					Class:   errs.ClassValidation,
				}
			}
		} else {
			// Name or ID — validate as entity name
			if err := validators.EntityName(identifier, "VM", 63); err != nil {
				return &errs.DomainError{
					Code:    errs.CodeVMResolveFailed,
					Op:      "vm",
					Message: fmt.Sprintf("Invalid VM identifier: %s", identifier),
					Class:   errs.ClassValidation,
				}
			}
		}
	}
	return nil
}
