package inputs

import (
	"context"
	"fmt"
	"mvmctl/internal/core/config"
	"mvmctl/internal/core/vm"
	"mvmctl/internal/core/vsock"
	"mvmctl/internal/infra"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/lib/system"
	"mvmctl/pkg/errs"
)

// CPInput specifies c p input.
type CPInput struct {
	Sources []string `json:"sources" yaml:"src"`
	Dest    string   `json:"dest"    yaml:"dest"`
	Force   bool     `json:"force"   yaml:"force"`
}

// ResolvedCPInfo holds resolved target VM info for a copy operation.
type ResolvedCPInfo struct {
	Identifier  string
	RemotePath  string
	IsDirectory *bool
	TotalBytes  *int64
	Vsock       *model.VsockConfigItem
}

// ResolvedCPInput specifies resolved c p input.
type ResolvedCPInput struct {
	Direction  string
	LocalPaths []string
	SrcInfo    *ResolvedCPInfo
	DstInfo    *ResolvedCPInfo
	Force      bool
}

// Validate checks that the copy input has valid sources and destination.
func (i *CPInput) Validate() error {
	if len(i.Sources) == 0 {
		return errs.New(errs.CodeCPError, "At least one source path is required")
	}
	if i.Dest == "" {
		return errs.New(errs.CodeCPError, "A destination path is required")
	}
	return nil
}

// Resolve expands tilde paths and resolves VM identifiers to vsock config.
func (i *CPInput) Resolve(
	ctx context.Context,
	cfg *config.Service,
	vmRepo vm.Repository,
	vsockRepo vsock.Repository,
) (*ResolvedCPInput, error) {
	if err := i.Validate(); err != nil {
		return nil, err
	}
	sources := make([]string, len(i.Sources))
	for i, src := range i.Sources {
		sources[i] = system.ExpandTilde(src)
	}
	dstVM, dstPath := infra.ParseVMPath(i.Dest)
	srcVM, srcRemotePath := infra.ParseVMPath(sources[0])
	var (
		srcInfo, dstInfo *ResolvedCPInfo
		localPaths       []string
		direction        string
		err              error
	)
	// Determine direction.
	multiSource := len(sources) > 1
	switch {
	case multiSource && dstVM == "":
		return nil, errs.New(errs.CodeCPMultiSourceNoVMDest,
			"Multiple sources require a VM destination (use vm_name:/path format)")
	case multiSource || (srcVM == "" && dstVM != ""):
		direction = infra.DirectionHostToVM
		if multiSource {
			localPaths = sources
		} else {
			localPaths = []string{sources[0]}
		}
		dstInfo, err = resolveVMSide(ctx, dstVM, dstPath, vmRepo, vsockRepo)
		if err != nil {
			return nil, err
		}
	case srcVM != "" && dstVM == "":
		direction = infra.DirectionVMToHost
		localPaths = []string{dstPath}
		srcInfo, err = resolveVMSide(ctx, srcVM, srcRemotePath, vmRepo, vsockRepo)
		if err != nil {
			return nil, err
		}
	case srcVM != "" && dstVM != "":
		direction = infra.DirectionVMToVM
		srcInfo, err = resolveVMSide(ctx, srcVM, srcRemotePath, vmRepo, vsockRepo)
		if err != nil {
			return nil, err
		}
		dstInfo, err = resolveVMSide(ctx, dstVM, dstPath, vmRepo, vsockRepo)
		if err != nil {
			return nil, err
		}
	default:
		return nil, errs.New(errs.CodeCPNoVMSpecified,
			"At least one path must reference a VM (use vm_name:/path format)")
	}
	return &ResolvedCPInput{
		Direction:  direction,
		LocalPaths: localPaths,
		SrcInfo:    srcInfo,
		DstInfo:    dstInfo,
		Force:      i.Force,
	}, nil
}

// resolveVMSide resolves a VM-side path to vsock connection config.
func resolveVMSide(
	ctx context.Context,
	vmIdent, remotePath string,
	vmRepo vm.Repository,
	vsockRepo vsock.Repository,
) (*ResolvedCPInfo, error) {
	vmEntity, err := resolveVM(ctx, vmIdent, vmRepo)
	if err != nil {
		return nil, err
	}
	vsockCfg, err := vsockRepo.GetByVMID(ctx, vmEntity.ID)
	if err != nil {
		return nil, errs.Wrap(errs.CodeCPResolveFailed, err)
	}
	if vsockCfg == nil {
		return nil, errs.New(
			errs.CodeCPError,
			fmt.Sprintf("VM '%s' has no vsock configuration — ensure vsock device is enabled", vmIdent),
		)
	}
	return &ResolvedCPInfo{
		Identifier: vmIdent,
		RemotePath: remotePath,
		Vsock:      vsockCfg,
	}, nil
}

// resolveVM resolves a VM by name, IP, MAC, or ID prefix.
func resolveVM(ctx context.Context, identifier string, vmRepo vm.Repository) (*model.VMItem, error) {
	vmResolver := vm.NewResolver(vmRepo)
	vmEntity, err := vmResolver.Resolve(ctx, identifier)
	if err != nil {
		return nil, errs.NotFound(
			errs.CodeCPVMNotFound,
			fmt.Sprintf("Could not resolve VM '%s': %s", identifier, err.Error()),
		)
	}
	if vmEntity == nil {
		return nil, errs.NotFound(errs.CodeCPVMNotFound, fmt.Sprintf("VM '%s' not found", identifier))
	}
	return vmEntity, nil
}
