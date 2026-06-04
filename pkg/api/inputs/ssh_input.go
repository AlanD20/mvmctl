package inputs

import (
	"context"
	"fmt"
	"os"

	"mvmctl/internal/core/config"
	"mvmctl/internal/core/key"
	"mvmctl/internal/core/vm"
	"mvmctl/internal/infra/errs"
	"mvmctl/internal/infra/model"
	"mvmctl/internal/infra/validators"
)

// SSHInput matches Python's SSHInput dataclass.
//
//	@dataclass
//	class SSHInput:
//	    identifier: str
//	    user: str | None = None
//	    key: Path | None = None
//	    cmd: str | None = None
//	    timeout: int | None = None
type SSHInput struct {
	Identifier string  `json:"identifier"`
	User       *string `json:"user,omitempty"`
	Key        *string `json:"key,omitempty"`
	Cmd        *string `json:"cmd,omitempty"`
	Timeout    *int    `json:"timeout,omitempty"`
}

// ResolvedSSHInput matches Python's ResolvedSSHInput (frozen dataclass).
//
//	@dataclass(frozen=True)
//	class ResolvedSSHInput:
//	    target_ip: str
//	    user: str
//	    key: Path | None
//	    cmd: str | None
//	    timeout: int | None
type ResolvedSSHInput struct {
	TargetIP string
	User     string
	Key      *string
	Cmd      *string
	Timeout  *int
}

// SSHRequest matches Python's SSHRequest.
//
// Resolve SSHInput against the database.
type SSHRequest struct {
	cfg    *config.Service
	input  SSHInput
	result *ResolvedSSHInput
	vm     *model.VM
}

// NewSSHRequest creates a new SSHRequest.
func NewSSHRequest(inputs SSHInput, cfg *config.Service) *SSHRequest {
	return &SSHRequest{
		cfg:   cfg,
		input: inputs,
	}
}

// Result returns the resolved input, or nil if resolve() has not been called.

// Resolve resolves all inputs to explicit values.
// Matches Python's SSHRequest.resolve().
func (r *SSHRequest) Resolve(
	ctx context.Context,
	vmRepo vm.Repository,
	keyRepo key.Repository,
) (*ResolvedSSHInput, error) {
	targetIP, err := r.resolveTarget(ctx, vmRepo)
	if err != nil {
		return nil, err
	}

	user, err := r.resolveUser(ctx)
	if err != nil {
		return nil, err
	}

	sshKey, err := r.resolveKey(ctx, keyRepo)
	if err != nil {
		return nil, err
	}

	r.result = &ResolvedSSHInput{
		TargetIP: targetIP,
		User:     user,
		Key:      sshKey,
		Cmd:      r.input.Cmd,
		Timeout:  r.input.Timeout,
	}

	// Validate
	if err := r.ensureValidate(); err != nil {
		return nil, err
	}

	return r.result, nil
}

func (r *SSHRequest) ensureValidate() error {
	if r.result == nil {
		return &errs.DomainError{
			Code:    errs.CodeSSHError,
			Op:      "ssh",
			Message: "resolve() must be called before validation",
			Class:   errs.ClassValidation,
		}
	}

	if !validators.IsIPAddress(r.result.TargetIP) {
		return &errs.DomainError{
			Code:    errs.CodeSSHError,
			Op:      "ssh",
			Message: fmt.Sprintf("Invalid IP address: %s", r.result.TargetIP),
			Class:   errs.ClassValidation,
		}
	}

	if err := validators.ValidateSSHUsername(r.result.User); err != nil {
		return &errs.DomainError{
			Code:    errs.CodeSSHError,
			Op:      "ssh",
			Message: err.Error(),
			Class:   errs.ClassValidation,
		}
	}

	if r.result.Key != nil && *r.result.Key != "" {
		if _, err := os.Stat(*r.result.Key); os.IsNotExist(err) {
			return &errs.DomainError{
				Code:    errs.CodeKeyNotFound,
				Op:      "ssh",
				Message: fmt.Sprintf("SSH key not found: %s", *r.result.Key),
				Class:   errs.ClassValidation,
			}
		}
	}

	return nil
}

// resolveTarget resolves the target to an IP address.
// Matches Python's SSHRequest._resolve_target().
func (r *SSHRequest) resolveTarget(ctx context.Context, vmRepo vm.Repository) (string, error) {
	target := r.input.Identifier

	if target == "" {
		return "", &errs.DomainError{
			Code:    errs.CodeSSHError,
			Op:      "ssh",
			Message: "Provide a VM identifier (name, ID prefix, IP, or MAC address)",
			Class:   errs.ClassValidation,
		}
	}

	// Try to resolve as a VM entity
	vmResolver := vm.NewResolver(vmRepo)
	vmEntity, err := vmResolver.Resolve(ctx, target)
	if err == nil && vmEntity != nil && vmEntity.IPv4 != "" {
		r.vm = vmEntity
		return vmEntity.IPv4, nil
	}

	// Fallback: use raw identifier (e.g., IP for a VM not in DB)
	return target, nil
}

// resolveUser resolves the SSH user.
// Matches Python's SSHRequest._resolve_user().
func (r *SSHRequest) resolveUser(ctx context.Context) (string, error) {
	if r.input.User != nil && *r.input.User != "" {
		return *r.input.User, nil
	}
	// Check VM's stored ssh_user
	if r.vm != nil && r.vm.SSHUser != nil && *r.vm.SSHUser != "" {
		return *r.vm.SSHUser, nil
	}
	user, _ := r.cfg.GetString(ctx, "defaults.vm", "ssh_user")
	return user, nil
}

// resolveKey resolves SSH private key path via the key domain.
// Matches Python's SSHRequest._resolve_key().
func (r *SSHRequest) resolveKey(ctx context.Context, keyRepo key.Repository) (*string, error) {
	keyResolver := key.NewResolver(keyRepo)

	if r.input.Key != nil && *r.input.Key != "" {
		keyStr := *r.input.Key

		// 1a. Try as registered key name via key resolver
		keyItem, err := keyResolver.Resolve(ctx, keyStr)
		if err == nil && keyItem.PrivateKeyPath != nil && *keyItem.PrivateKeyPath != "" {
			if _, err := os.Stat(*keyItem.PrivateKeyPath); err == nil {
				return keyItem.PrivateKeyPath, nil
			}
		}

		// 1b. Try as direct filesystem path — validate private key content
		if fi, err := os.Stat(keyStr); err == nil && !fi.IsDir() {
			content, err := os.ReadFile(keyStr)
			if err == nil && key.IsPrivateKey(string(content)) {
				return &keyStr, nil
			}
		}

		return nil, &errs.DomainError{
			Code:    errs.CodeSSHError,
			Op:      "ssh",
			Message: fmt.Sprintf("Key '%s' not found or is not a valid private key", keyStr),
			Class:   errs.ClassValidation,
		}
	}

	// 2. No key provided — check VM's stored ssh_keys (these are IDs)
	if r.vm != nil {
		for _, keyID := range r.vm.SSHKeys {
			keyItem, err := keyResolver.ByID(ctx, keyID)
			if err == nil && keyItem.PrivateKeyPath != nil && *keyItem.PrivateKeyPath != "" {
				if _, err := os.Stat(*keyItem.PrivateKeyPath); err == nil {
					return keyItem.PrivateKeyPath, nil
				}
			}
		}
	}

	// 3. Fall back to default keys
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
