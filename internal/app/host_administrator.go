package app

import (
	"context"
	"os"
	"strconv"
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
	filtered, help, err := hostAdministratorArguments(args)
	if err == nil {
		if len(filtered) > 0 && filtered[0] == "help" {
			return false
		}
		if help && len(filtered) >= 2 && filtered[0] == "host" &&
			(filtered[1] == "init" || filtered[1] == "reset") {
			return false
		}
	}
	leadingFlag := strings.HasPrefix(args[0], "-")
	for index, arg := range args {
		if index == 0 && arg == "help" {
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
	filtered, help, err := hostAdministratorArguments(args)
	if err != nil {
		return nil, err
	}
	if help {
		return nil, errs.New(errs.CodeValidationFailed, "help must use the non-mutating command help route")
	}
	args = filtered
	if len(args) < 2 || args[0] != "host" || (args[1] != "init" && args[1] != "reset") {
		return nil, errs.New(errs.CodeValidationFailed, "expected host init or host reset")
	}
	operator := ""
	force := false
	for index := 2; index < len(args); index++ {
		switch args[index] {
		case "--":
			if index != len(args)-1 {
				return nil, errs.New(errs.CodeValidationFailed, "host administration accepts no positional arguments")
			}
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

// hostAdministratorArguments recognizes only the declared global output and help
// flags. Flag values and tokens after -- are never reinterpreted as routing flags.
func hostAdministratorArguments(args []string) ([]string, bool, error) {
	filtered := make([]string, 0, len(args))
	help := false
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if arg == "--" {
			filtered = append(filtered, args[index:]...)
			break
		}
		if arg == "--operator" {
			filtered = append(filtered, arg)
			if index+1 < len(args) {
				index++
				filtered = append(filtered, args[index])
			}
			continue
		}
		if arg == "--json" {
			continue
		}
		if value, found := strings.CutPrefix(arg, "--json="); found {
			if _, err := strconv.ParseBool(value); err != nil {
				return nil, false, errs.New(errs.CodeValidationFailed, "invalid --json boolean value")
			}
			continue
		}
		if arg == "--help" || arg == "-h" {
			help = true
			continue
		}
		value, helpValue := strings.CutPrefix(arg, "--help=")
		if !helpValue {
			value, helpValue = strings.CutPrefix(arg, "-h=")
		}
		if helpValue {
			parsed, err := strconv.ParseBool(value)
			if err != nil {
				return nil, false, errs.New(errs.CodeValidationFailed, "invalid --help boolean value")
			}
			help = parsed
			continue
		}
		filtered = append(filtered, arg)
	}
	return filtered, help, nil
}
