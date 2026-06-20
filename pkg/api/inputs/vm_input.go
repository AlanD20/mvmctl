package inputs

import (
	"context"
	"fmt"
	"strings"

	"mvmctl/internal/core/vm"
	"mvmctl/internal/core/vsock"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/lib/validators"
	"mvmctl/pkg/errs"
)

// VMInput specifies VM input for identifying existing VMs.
type VMInput struct {
	Identifiers []string `json:"identifiers"`
	Force       bool     `json:"force"`
}

// Validate checks that the VM input has valid identifiers.
func (i *VMInput) Validate() error {
	if len(i.Identifiers) == 0 {
		return fmt.Errorf("at least one VM identifier is required")
	}
	for _, identifier := range i.Identifiers {
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

// Resolve resolves all identifiers in the input to VMItem objects.
// Delegates to vm.Resolver.ResolveMany for batch resolution with
// deduplication and error collection.
func (i *VMInput) Resolve(ctx context.Context, repo vm.Repository) ([]*model.VMItem, error) {
	if err := i.Validate(); err != nil {
		return nil, err
	}
	resolver := vm.NewResolver(repo)
	result := resolver.ResolveMany(ctx, i.Identifiers)
	if result == nil || (len(result.Errors) > 0 && len(result.VMs) == 0) {
		return nil, errs.NotFound(
			errs.CodeVMNotFound,
			fmt.Sprintf("Could not resolve any VMs: %s", strings.Join(result.Errors, ", ")),
		)
	}
	if len(result.Errors) > 0 {
		return result.VMs, fmt.Errorf("partial resolve failures: %s", strings.Join(result.Errors, "; "))
	}
	return result.VMs, nil
}

// --- VMExecInput ---
// VMExecInput holds the input for executing a command inside a VM via vsock.
type VMExecInput struct {
	Identifier string            `json:"target"            yaml:"target"`
	Command    string            `json:"command,omitempty" yaml:"cmd,omitempty"` // empty = interactive shell
	User       string            `json:"user"              yaml:"user"`
	Timeout    int               `json:"timeout"           yaml:"timeout"`
	Port       int               `json:"port"              yaml:"port"`
	Env        map[string]string `json:"env,omitempty"     yaml:"env,omitempty"`
}

// ResolvedVMExecInput holds the resolved VM and vsock config for exec operations.
type ResolvedVMExecInput struct {
	VM        *model.VMItem
	VsockItem *model.VsockConfigItem
	User      string
}

// Validate checks that the VM exec input has a valid identifier.
func (i *VMExecInput) Validate() error {
	if i.Identifier == "" {
		return fmt.Errorf("VM identifier is required")
	}
	if validators.ValidMACRegex.MatchString(i.Identifier) {
		if err := validators.MAC(i.Identifier); err != nil {
			return errs.New(errs.CodeVMResolveFailed, fmt.Sprintf("Invalid MAC address: %s", i.Identifier))
		}
	} else if validators.IsIPAddress(i.Identifier) {
		if err := validators.IPv4Address(i.Identifier, "guest IP", true, "", ""); err != nil {
			return errs.New(errs.CodeVMResolveFailed, fmt.Sprintf("Invalid guest IP: %s", i.Identifier))
		}
	} else {
		if err := validators.EntityName(i.Identifier, "VM", 63); err != nil {
			return errs.New(errs.CodeVMResolveFailed, fmt.Sprintf("Invalid VM identifier: %s", i.Identifier))
		}
	}
	return nil
}

// Resolve resolves the VM and vsock configuration for exec operations.
func (i *VMExecInput) Resolve(
	ctx context.Context,
	vmRepo vm.Repository,
	vsockRepo vsock.Repository,
) (*ResolvedVMExecInput, error) {
	if err := i.Validate(); err != nil {
		return nil, err
	}
	// Resolve VM
	vmResolver := vm.NewResolver(vmRepo)
	vmItem, err := vmResolver.Resolve(ctx, i.Identifier)
	if err != nil {
		return nil, errs.WrapMsg(errs.CodeVMNotFound, fmt.Sprintf("vm not found: %s", i.Identifier), err)
	}
	// Resolve vsock config
	vsockItem, err := vsockRepo.GetByVMID(ctx, vmItem.ID)
	if err != nil {
		return nil, errs.WrapMsg(
			errs.CodeVsockNotFound,
			fmt.Sprintf("failed to get vsock config for vm '%s'", vmItem.Name),
			err,
		)
	}
	if vsockItem == nil {
		return nil, errs.New(
			errs.CodeVsockNotFound,
			fmt.Sprintf("vm '%s' has no vsock agent configured. Create with --vsock-port to enable.", vmItem.Name),
			errs.WithClass(errs.ClassValidation),
		)
	}
	// Determine port: input overrides config default
	port := vsockItem.Port
	if i.Port > 0 {
		port = i.Port
	}
	// Build effective vsock config with resolved port
	effective := &model.VsockConfigItem{
		ID:               vsockItem.ID,
		VmID:             vsockItem.VmID,
		GuestCID:         vsockItem.GuestCID,
		UDSPath:          vsockItem.UDSPath,
		Port:             port,
		Token:            vsockItem.Token,
		AgentVersion:     vsockItem.AgentVersion,
		Upgrading:        vsockItem.Upgrading,
		UpgradeStartedAt: vsockItem.UpgradeStartedAt,
	}
	// Resolve user: input or empty (caller defaults from config)
	user := i.User
	return &ResolvedVMExecInput{
		VM:        vmItem,
		VsockItem: effective,
		User:      user,
	}, nil
}
