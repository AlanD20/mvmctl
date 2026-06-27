package inputs

import (
	"context"
	"fmt"

	"mvmctl/internal/core/vm"
	"mvmctl/internal/core/vsock"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/lib/validators"
	"mvmctl/pkg/errs"
)

// ExecInput holds the input for executing a command inside a VM via vsock.
type ExecInput struct {
	Identifier string            `json:"target"            yaml:"target"`
	Command    string            `json:"command,omitempty" yaml:"cmd,omitempty"` // empty = interactive shell
	User       string            `json:"user"              yaml:"user"`
	Timeout    int               `json:"timeout"           yaml:"timeout"`
	Port       int               `json:"port"              yaml:"port"`
	Env        map[string]string `json:"env,omitempty"     yaml:"env,omitempty"`
	NoSync     bool              `json:"no_sync,omitempty" yaml:"no_sync,omitempty"`
}

// ResolvedExecInput holds the resolved VM and vsock config for exec operations.
type ResolvedExecInput struct {
	VM        *model.VMItem
	VsockItem *model.VsockConfigItem
	User      string
}

// Validate checks that the exec input has a valid identifier.
func (i *ExecInput) Validate() error {
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
func (i *ExecInput) Resolve(
	ctx context.Context,
	vmRepo vm.Repository,
	vsockRepo vsock.Repository,
) (*ResolvedExecInput, error) {
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
	return &ResolvedExecInput{
		VM:        vmItem,
		VsockItem: effective,
		User:      user,
	}, nil
}
