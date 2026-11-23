package app

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strings"

	"mvmctl/internal/core/host"
	"mvmctl/internal/infra"
	"mvmctl/pkg/errs"
)

const (
	systemInstallPATH   = "/usr/sbin:/usr/bin:/sbin:/bin"
	systemInstallHOME   = "/root"
	systemInstallLocale = "C"
)

type systemInstallDeps struct {
	clearEnvironment    func()
	setEnvironment      func(string, string) error
	installSystemBinary func(context.Context) (bool, error)
}

// IsHostInstallSystemBinaryInvocation reserves every argument sequence that
// selects the administrator bootstrap command. Validation remains exact inside
// RunHostInstallSystemBinary so malformed invocations cannot fall through to
// normal application initialization.
func IsHostInstallSystemBinaryInvocation(args []string) bool {
	return reservesHostCommand(args, "install-system")
}

// reservesHostCommand only routes a reserved operation. Its parser decides whether the arguments are valid.
func reservesHostCommand(args []string, action string) bool {
	if len(args) == 0 {
		return false
	}
	leadingFlag := strings.HasPrefix(args[0], "-")
	for index, arg := range args {
		if arg == "help" {
			return false
		}
		if arg == "host" {
			return (index == 0 || leadingFlag) && slices.Contains(args[index+1:], action)
		}
	}
	return false
}

// RunHostInstallSystemBinary performs and reports the minimal administrator
// bootstrap without resolving user state or constructing the normal application.
func RunHostInstallSystemBinary(ctx context.Context, args []string) error {
	changed, err := hostInstallSystemBinary(ctx, args, systemInstallDeps{
		clearEnvironment:    os.Clearenv,
		setEnvironment:      os.Setenv,
		installSystemBinary: host.InstallSystemBinary,
	})
	reportHostInstallSystemBinary(changed, err)
	return err
}

func hostInstallSystemBinary(
	ctx context.Context,
	args []string,
	deps systemInstallDeps,
) (bool, error) {
	if len(args) != 2 || args[0] != "host" || args[1] != "install-system" {
		return false, errs.New(
			errs.CodeValidationFailed,
			"host install-system accepts no flags or arguments",
		)
	}
	if err := ctx.Err(); err != nil {
		return false, errs.WrapMsg(errs.CodeInternal, "system installation context ended", err)
	}
	if err := sanitizeSystemInstallEnvironment(deps); err != nil {
		return false, err
	}
	return deps.installSystemBinary(ctx)
}

func sanitizeSystemInstallEnvironment(deps systemInstallDeps) error {
	deps.clearEnvironment()
	fixedValues := [...]struct {
		key   string
		value string
	}{
		{key: "PATH", value: systemInstallPATH},
		{key: "HOME", value: systemInstallHOME},
		{key: "LANG", value: systemInstallLocale},
		{key: "LC_ALL", value: systemInstallLocale},
	}
	for _, entry := range fixedValues {
		if err := deps.setEnvironment(entry.key, entry.value); err != nil {
			return errs.WrapMsg(
				errs.CodeInternal,
				fmt.Sprintf("set fixed system installation environment %s", entry.key),
				err,
			)
		}
	}
	return nil
}

func reportHostInstallSystemBinary(changed bool, err error) {
	if err != nil {
		if !changed {
			return
		}
		durabilityUncertain := false
		if domainErr := errs.AsDomainError(err); domainErr != nil {
			durabilityUncertain, _ = domainErr.Details["durability_uncertain"].(bool)
		}
		if durabilityUncertain {
			slog.Warn(
				"system binary was replaced but directory durability could not be confirmed",
				"path", infra.SystemBinaryPath,
			)
			return
		}
		slog.Warn(
			"system binary was replaced but final completion checks failed",
			"path", infra.SystemBinaryPath,
		)
		return
	}
	if changed {
		slog.Info("installed trusted system binary", "path", infra.SystemBinaryPath)
		return
	}
	slog.Info("system binary already current", "path", infra.SystemBinaryPath)
}
