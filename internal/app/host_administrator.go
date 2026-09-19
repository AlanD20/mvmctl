package app

import (
	"context"
	"os"
	"strings"

	"mvmctl/internal/core/host"
	"mvmctl/pkg/errs"
)

// IsHostAdministratorInvocation reserves init/reset even when flags are malformed,
// so they cannot enter ordinary initialization with root authority.
func IsHostAdministratorInvocation(args []string) bool {
	if len(args) == 0 {
		return false
	}
	leadingFlag := strings.HasPrefix(args[0], "-")
	for index, arg := range args {
		if arg == "help" {
			return false
		}
		if arg != "host" {
			continue
		}
		if index > 0 && !leadingFlag {
			return false
		}
		for _, action := range args[index+1:] {
			if action == "init" || action == "reset" {
				return true
			}
		}
		return false
	}
	return false
}

// HostAdministratorResult is the typed administrator setup outcome.
type HostAdministratorResult = host.AdministratorInitResult

// RunHostAdministrator executes only fixed host administration, before user
// configuration, cache paths, SQLite, or ordinary service construction.
func RunHostAdministrator(ctx context.Context, args []string) (*HostAdministratorResult, error) {
	filtered := make([]string, 0, len(args))
	for _, arg := range args {
		if arg != "--json" {
			filtered = append(filtered, arg)
		}
	}
	args = filtered
	if len(args) < 2 || args[0] != "host" || (args[1] != "init" && args[1] != "reset") {
		return nil, errs.New(errs.CodeValidationFailed, "expected host init or host reset")
	}
	operator := ""
	force := false
	for index := 2; index < len(args); index++ {
		switch args[index] {
		case "--operator":
			if operator != "" || index+1 >= len(args) {
				return nil, errs.New(errs.CodeValidationFailed, "--operator requires exactly one non-root account")
			}
			index++
			operator = args[index]
		case "--force":
			if force || args[1] != "reset" {
				return nil, errs.New(errs.CodeValidationFailed, "--force is accepted once for host reset")
			}
			force = true
		default:
			return nil, errs.New(errs.CodeValidationFailed, "unsupported administrator argument")
		}
	}
	if args[1] == "reset" && !force {
		return nil, errs.New(errs.CodeValidationFailed, "host reset requires --force")
	}
	identity, err := host.ResolveAdministratorOperator(operator)
	if err != nil {
		return nil, err
	}
	if err := sanitizeSystemInstallEnvironment(systemInstallDeps{
		clearEnvironment: os.Clearenv,
		setEnvironment:   os.Setenv,
	}); err != nil {
		return nil, err
	}
	if args[1] == "reset" {
		return nil, host.AdministratorReset(ctx, identity)
	}
	return host.AdministratorInit(ctx, identity)
}
