package inputs

import (
	"context"
	"fmt"
	"os"
	"strings"

	"mvmctl/internal/core/config"
	"mvmctl/internal/core/key"
	"mvmctl/internal/core/vm"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/lib/system"
	"mvmctl/pkg/errs"
)

// CPInput matches Python's CPInput dataclass.
//
//	@dataclass
//	class CPInput:
//	    sources: list[str]
//	    dst: str
//	    user: str | None = None
//	    key: str | None = None
//	    force: bool = False
type CPInput struct {
	Sources []string `json:"sources"        yaml:"src"`
	Dst     string   `json:"dst"            yaml:"-"`
	User    *string  `json:"user,omitempty" yaml:"user,omitempty"`
	Key     *string  `json:"key,omitempty"  yaml:"key,omitempty"`
	Force   bool     `json:"force"          yaml:"force"`
}

// ResolvedCPInfo matches Python's ResolvedCPInfo dataclass.
//
//	@dataclass
//	class ResolvedCPInfo:
//	    identifier: str
//	    ip: str
//	    user: str
//	    key_path: str | None
//	    remote_path: str
//	    is_directory: bool | None = None
//	    total_bytes: int | None = None
type ResolvedCPInfo struct {
	Identifier  string
	IP          string
	User        string
	KeyPath     *string
	RemotePath  string
	IsDirectory *bool
	TotalBytes  *int64
}

// ResolvedCPInput matches Python's ResolvedCPInput (frozen dataclass).
//
//	@dataclass(frozen=True)
//	class ResolvedCPInput:
//	    direction: str  # "host_to_vm" | "vm_to_host" | "vm_to_vm"
//	    local_paths: list[str] | None = None
//	    src_info: ResolvedCPInfo | None = None
//	    dst_info: ResolvedCPInfo | None = None
//	    force: bool = False
type ResolvedCPInput struct {
	Direction  string
	LocalPaths []string
	SrcInfo    *ResolvedCPInfo
	DstInfo    *ResolvedCPInfo
	Force      bool
}

// CPRequest matches Python's CPRequest.
//
// Resolve CPInput against the database and filesystem.
type CPRequest struct {
	cfg    *config.Service
	input  CPInput
	result *ResolvedCPInput
}

// NewCPRequest creates a new CPRequest.
func NewCPRequest(inputs CPInput, cfg *config.Service) *CPRequest {
	return &CPRequest{
		cfg:   cfg,
		input: inputs,
	}
}

// ParseVMPath parses a "vm:path" string into VM identifier and remote path.
// Matches Python's CPService._parse_vm_path().
func ParseVMPath(path string) (vmIdent, remotePath string) {
	vmIdent, remotePath, found := strings.Cut(path, ":")
	if !found {
		return "", path
	}
	return vmIdent, remotePath
}

// Result returns the resolved input, or nil if resolve() has not been called.

// Resolve resolves all inputs to explicit values.
// Matches Python's CPRequest.resolve().
func (r *CPRequest) Resolve(
	ctx context.Context,
	vmRepo vm.Repository,
	keyRepo key.Repository,
) (*ResolvedCPInput, error) {
	sources := make([]string, len(r.input.Sources))
	for i, src := range r.input.Sources {
		sources[i] = system.ExpandTilde(src)
	}
	dstVM, dstPath := ParseVMPath(r.input.Dst)

	var srcInfo, dstInfo *ResolvedCPInfo
	var localPaths []string
	direction := ""

	if len(sources) > 1 {
		// Multi-source only works for host -> VM
		if dstVM == "" {
			return nil, errs.New(
				errs.CodeCPMultiSourceNoVMDest,
				"Multiple sources require a VM destination (use vm_name:/path format)",
			)
		}
		direction = "host_to_vm"
		localPaths = sources
		var err error
		dstInfo, err = r.resolveVMSide(ctx, dstVM, dstPath, false, vmRepo, keyRepo)
		if err != nil {
			return nil, err
		}
	} else {
		// Single source — determine direction by parsing source
		srcPath := sources[0]
		srcVM, srcRemotePath := ParseVMPath(srcPath)

		if srcVM != "" && dstVM != "" {
			direction = "vm_to_vm"
		} else if srcVM != "" {
			direction = "vm_to_host"
		} else if dstVM != "" {
			direction = "host_to_vm"
		} else {
			return nil, errs.New(
				errs.CodeCPNoVMSpecified,
				"At least one path must reference a VM (use vm_name:/path format)",
			)
		}

		switch direction {
		case "host_to_vm":
			localPaths = []string{srcPath}
			var err error
			dstInfo, err = r.resolveVMSide(ctx, dstVM, dstPath, false, vmRepo, keyRepo)
			if err != nil {
				return nil, err
			}
		case "vm_to_host":
			var err error
			srcInfo, err = r.resolveVMSide(ctx, srcVM, srcRemotePath, true, vmRepo, keyRepo)
			if err != nil {
				return nil, err
			}
			localPaths = []string{dstPath}
		case "vm_to_vm":
			var err error
			srcInfo, err = r.resolveVMSide(ctx, srcVM, srcRemotePath, true, vmRepo, keyRepo)
			if err != nil {
				return nil, err
			}
			dstInfo, err = r.resolveVMSide(ctx, dstVM, dstPath, false, vmRepo, keyRepo)
			if err != nil {
				return nil, err
			}
		}
	}

	r.result = &ResolvedCPInput{
		Direction:  direction,
		LocalPaths: localPaths,
		SrcInfo:    srcInfo,
		DstInfo:    dstInfo,
		Force:      r.input.Force,
	}

	return r.result, nil
}

// resolveVMSide resolves a VM-side path to connection info.
// Matches Python's CPRequest._resolve_vm_side().
func (r *CPRequest) resolveVMSide(
	ctx context.Context,
	vmIdent, remotePath string,
	isSource bool,
	vmRepo vm.Repository,
	keyRepo key.Repository,
) (*ResolvedCPInfo, error) {
	vmEntity, err := r.resolveVM(ctx, vmIdent, vmRepo)
	if err != nil {
		return nil, err
	}

	user := r.resolveUser(ctx, vmEntity)
	keyPath, err := r.resolveKey(ctx, vmEntity, keyRepo)
	if err != nil {
		return nil, err
	}

	if vmEntity.IPv4 == "" {
		return nil, errs.New(
			errs.CodeCPVMNoIP,
			fmt.Sprintf("VM '%s' has no IP address assigned", vmIdent),
			errs.WithClass(errs.ClassValidation),
		)
	}

	return &ResolvedCPInfo{
		Identifier: vmIdent,
		IP:         vmEntity.IPv4,
		User:       user,
		KeyPath:    keyPath,
		RemotePath: remotePath,
	}, nil
}

// resolveVM resolves a VM by name, IP, MAC, or ID prefix.
// Matches Python's CPRequest._resolve_vm().
func (r *CPRequest) resolveVM(ctx context.Context, identifier string, vmRepo vm.Repository) (*model.VM, error) {
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

// resolveUser resolves the SSH user for copy.
// Matches Python's CPRequest._resolve_user().
func (r *CPRequest) resolveUser(ctx context.Context, vmEntity *model.VM) string {
	if r.input.User != nil && *r.input.User != "" {
		return *r.input.User
	}
	if vmEntity.SSHUser != nil && *vmEntity.SSHUser != "" {
		return *vmEntity.SSHUser
	}
	s, _ := r.cfg.GetString(ctx, "defaults.vm", "ssh_user")
	return s
}

// resolveKey resolves SSH private key path for copy.
// Matches Python's CPRequest._resolve_key().
func (r *CPRequest) resolveKey(ctx context.Context, vmEntity *model.VM, keyRepo key.Repository) (*string, error) {
	keyResolver := key.NewResolver(keyRepo)

	if r.input.Key != nil && *r.input.Key != "" {
		keyStr := *r.input.Key

		// Try as registered key name
		keyItem, err := keyResolver.Resolve(ctx, keyStr)
		if err == nil && keyItem.PrivateKeyPath != nil && *keyItem.PrivateKeyPath != "" {
			if _, err := os.Stat(*keyItem.PrivateKeyPath); err == nil {
				return keyItem.PrivateKeyPath, nil
			}
		}

		// Try as filesystem path — validate private key content
		if fi, err := os.Stat(keyStr); err == nil && !fi.IsDir() {
			content, err := os.ReadFile(keyStr)
			if err == nil && key.IsPrivateKey(string(content)) {
				return &keyStr, nil
			}
		}

		return nil, errs.New(
			errs.CodeSSHError,
			fmt.Sprintf("Key '%s' not found or is not a valid private key", keyStr),
			errs.WithClass(errs.ClassValidation),
		)
	}

	// Check VM's stored ssh_keys (these are names, but check by ID first as well)
	for _, keyName := range vmEntity.SSHKeys {
		keyItem, err := keyResolver.ByID(ctx, keyName)
		if err != nil && errs.IsNotFound(err) {
			// Fall back to name lookup — SSHKeys stores names, not fingerprints
			keyItem, err = keyResolver.ByName(ctx, keyName)
		}
		if err == nil && keyItem.PrivateKeyPath != nil && *keyItem.PrivateKeyPath != "" {
			if _, err := os.Stat(*keyItem.PrivateKeyPath); err == nil {
				return keyItem.PrivateKeyPath, nil
			}
		}
	}

	// Fall back to default keys
	defaults, err := keyRepo.GetDefaults(ctx)
	if err == nil {
		for _, keyItem := range defaults {
			if keyItem.PrivateKeyPath != nil && *keyItem.PrivateKeyPath != "" {
				if _, err := os.Stat(*keyItem.PrivateKeyPath); err == nil {
					return keyItem.PrivateKeyPath, nil
				}
			}
		}
	}

	return nil, nil
}
