package inputs

import (
	"context"
	"fmt"
	"mvmctl/internal/core/config"
	"mvmctl/internal/core/key"
	"mvmctl/internal/core/vm"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/lib/validators"
	"mvmctl/pkg/errs"
	"os"
)

// SSHInput specifies SSH input.
type SSHInput struct {
	Identifier string            `json:"target"            yaml:"target"`
	User       *string           `json:"user,omitempty"    yaml:"user,omitempty"`
	Key        *string           `json:"key,omitempty"     yaml:"key,omitempty"`
	Cmd        *string           `json:"cmd,omitempty"     yaml:"cmd,omitempty"`
	Timeout    *int              `json:"timeout,omitempty" yaml:"timeout,omitempty"`
	Env        map[string]string `json:"env,omitempty"     yaml:"env,omitempty"`
}

// ResolvedSSHInput specifies resolved SSH input.
type ResolvedSSHInput struct {
	TargetIP string
	User     string
	Key      *string
	Cmd      *string
	Timeout  *int
}

// Validate checks that the SSH input has a target identifier.
func (i *SSHInput) Validate() error {
	if i.Identifier == "" {
		return errs.New(
			errs.CodeSSHError,
			"Provide a VM identifier (name, ID prefix, IP, or MAC address)",
			errs.WithClass(errs.ClassValidation),
		)
	}
	return nil
}

// Resolve resolves the SSH input against the database and config.
// Returns a fully resolved SSH input with target IP, user, and key path.
func (i *SSHInput) Resolve(
	ctx context.Context,
	cfg *config.Service,
	vmRepo vm.Repository,
	keyRepo key.Repository,
) (*ResolvedSSHInput, error) {
	if err := i.Validate(); err != nil {
		return nil, err
	}
	targetIP, vmEntity, err := resolveTarget(ctx, i.Identifier, vmRepo)
	if err != nil {
		return nil, err
	}
	user, err := resolveUser(ctx, i.User, vmEntity, cfg)
	if err != nil {
		return nil, err
	}
	sshKey, err := resolveSSHKey(ctx, i.Key, vmEntity, keyRepo)
	if err != nil {
		return nil, err
	}
	// Validate resolved values.
	if !validators.IsIPAddress(targetIP) {
		return nil, errs.New(
			errs.CodeSSHError,
			fmt.Sprintf("Invalid IP address: %s", targetIP),
			errs.WithClass(errs.ClassValidation),
		)
	}
	if err := validators.SSHUsername(user); err != nil {
		return nil, errs.New(errs.CodeSSHError, err.Error(), errs.WithClass(errs.ClassValidation))
	}
	if sshKey != nil && *sshKey != "" {
		if _, err := os.Stat(*sshKey); os.IsNotExist(err) {
			return nil, errs.NotFound(errs.CodeKeyNotFound, fmt.Sprintf("SSH key not found: %s", *sshKey))
		}
	}
	return &ResolvedSSHInput{
		TargetIP: targetIP,
		User:     user,
		Key:      sshKey,
		Cmd:      i.Cmd,
		Timeout:  i.Timeout,
	}, nil
}

// resolveTarget resolves the target to an IP address and optionally a VM entity.
// Tries VM resolver first, falls back to raw identifier (e.g., IP for a VM not in DB).
func resolveTarget(ctx context.Context, identifier string, vmRepo vm.Repository) (string, *model.VMItem, error) {
	vmResolver := vm.NewResolver(vmRepo)
	vmEntity, err := vmResolver.Resolve(ctx, identifier)
	if err == nil && vmEntity != nil && vmEntity.IPv4 != "" {
		return vmEntity.IPv4, vmEntity, nil
	}
	// Fallback: use raw identifier
	return identifier, nil, nil
}

// resolveUser resolves the SSH user from input, VM, or config default.
func resolveUser(ctx context.Context, inputUser *string, vmEntity *model.VMItem, cfg *config.Service) (string, error) {
	if inputUser != nil && *inputUser != "" {
		return *inputUser, nil
	}
	if vmEntity != nil && vmEntity.SSHUser != nil && *vmEntity.SSHUser != "" {
		return *vmEntity.SSHUser, nil
	}
	user, _ := cfg.GetString(ctx, "defaults.vm", "ssh_user")
	return user, nil
}

// resolveSSHKey resolves SSH private key path via the key domain.
func resolveSSHKey(
	ctx context.Context,
	inputKey *string,
	vmEntity *model.VMItem,
	keyRepo key.Repository,
) (*string, error) {
	keyResolver := key.NewResolver(keyRepo)
	if inputKey != nil && *inputKey != "" {
		keyStr := *inputKey
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
		return nil, errs.New(
			errs.CodeSSHError,
			fmt.Sprintf("Key '%s' not found or is not a valid private key", keyStr),
			errs.WithClass(errs.ClassValidation),
		)
	}
	// 2. No key provided — check VM's stored ssh_keys
	// SSHKeys stores key NAMES; use the key resolver which handles
	// resolution by name, ID, or .pub file path.
	if vmEntity != nil {
		for _, keyName := range vmEntity.SSHKeys {
			keyItem, err := keyResolver.Resolve(ctx, keyName)
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
