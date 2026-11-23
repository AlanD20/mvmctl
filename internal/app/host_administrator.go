package app

import (
	"context"
	"io"
	"os"
	"strings"

	"github.com/spf13/pflag"

	"mvmctl/internal/core/host"
	"mvmctl/pkg/errs"
)

// IsHostAdministratorInvocation reserves init/reset before ordinary initialization.
func IsHostAdministratorInvocation(args []string) bool {
	return reservesHostCommand(args, "init") || reservesHostCommand(args, "reset")
}

// HostAdministratorOptions carries parsed administrator options to the fixed entry point.
type HostAdministratorOptions struct {
	Action   string
	Operator string
	Force    bool
	JSON     bool
	Help     bool
	Usage    string
}

// ParseHostAdministrator uses pflag in the restricted bootstrap path, without wiring the ordinary application.
func ParseHostAdministrator(args []string) (HostAdministratorOptions, error) {
	var options HostAdministratorOptions
	var operators []string
	flags := pflag.NewFlagSet("host administration", pflag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringArrayVar(&operators, "operator", nil, "Non-root operator for a direct root invocation")
	flags.BoolVar(&options.Force, "force", false, "Confirm host reset")
	flags.BoolVar(&options.JSON, "json", false, "Emit one structured JSON response")
	flags.BoolVarP(&options.Help, "help", "h", false, "Show help")

	if err := flags.Parse(args); err != nil {
		return options, errs.WrapMsg(errs.CodeValidationFailed, "invalid host administrator flags", err)
	}
	if flags.NArg() != 2 || flags.Arg(0) != "host" || (flags.Arg(1) != "init" && flags.Arg(1) != "reset") ||
		(flags.ArgsLenAtDash() >= 0 && flags.ArgsLenAtDash() != 2) {
		return options, errs.New(errs.CodeValidationFailed, "expected host init or host reset")
	}
	options.Action = flags.Arg(1)
	if len(operators) > 1 || (len(operators) == 1 && (operators[0] == "" || strings.HasPrefix(operators[0], "-"))) {
		return options, errs.New(errs.CodeValidationFailed, "--operator requires exactly one non-root account")
	}
	if len(operators) == 1 {
		options.Operator = operators[0]
	}
	if options.Action == "init" && flags.Changed("force") {
		return options, errs.New(errs.CodeValidationFailed, "--force is only accepted for host reset")
	}
	if options.Help {
		options.Usage = "Usage: mvm host " + options.Action + " [flags]\n\n"
		if options.Action == "init" {
			flags.Lookup("force").Hidden = true
		}
		options.Usage += flags.FlagUsages()
	} else if options.Action == "reset" && !options.Force {
		return options, errs.New(errs.CodeValidationFailed, "host reset requires --force")
	}
	return options, nil
}

// HostAdministratorResult is the typed administrator setup outcome.
type HostAdministratorResult = host.AdministratorInitResult

// RunHostAdministrator executes only fixed administration from the reserved entry point.
func RunHostAdministrator(ctx context.Context, options HostAdministratorOptions) (*HostAdministratorResult, error) {
	if options.Help || (options.Action != "init" && (options.Action != "reset" || !options.Force)) {
		return nil, errs.New(errs.CodeValidationFailed, "invalid host administrator operation")
	}
	identity, err := host.ResolveAdministratorOperator(options.Operator, options.Action)
	if err != nil {
		return nil, err
	}
	if err := sanitizeSystemInstallEnvironment(systemInstallDeps{
		clearEnvironment: os.Clearenv,
		setEnvironment:   os.Setenv,
	}); err != nil {
		return nil, err
	}
	if options.Action == "reset" {
		return nil, host.AdministratorReset(ctx, identity)
	}
	return host.AdministratorInit(ctx, identity)
}
