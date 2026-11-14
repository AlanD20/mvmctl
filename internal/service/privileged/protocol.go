package privileged

import (
	"fmt"
	"strings"

	"mvmctl/internal/infra"
	"mvmctl/pkg/errs"
)

const (
	markerPrefix   = "__mvm_privileged_v"
	maxActionBytes = 64
)

// Marker selects the current internal privileged protocol before normal CLI initialization.
const Marker = infra.PrivilegedProtocolMarker

type invocation struct {
	action string
}

// IsInvocation reports whether args select the reserved privileged protocol namespace.
// Unsupported versions stay in this namespace so they cannot fall through to the public CLI.
func IsInvocation(args []string) bool {
	return len(args) > 0 && strings.HasPrefix(args[0], markerPrefix)
}

func parseInvocation(args []string) (invocation, error) {
	if len(args) == 0 || args[0] != Marker {
		return invocation{}, errs.New(
			errs.CodeValidationFailed,
			fmt.Sprintf("unsupported privileged protocol; ask an administrator to upgrade %s", infra.SystemBinaryPath),
		)
	}
	if len(args) != 2 {
		return invocation{}, errs.New(
			errs.CodeValidationFailed,
			"privileged protocol requires exactly one action",
		)
	}
	if !validAction(args[1]) {
		return invocation{}, errs.New(
			errs.CodeValidationFailed,
			"privileged action must be a 1-64 byte ASCII identifier",
		)
	}
	return invocation{action: args[1]}, nil
}

func validAction(action string) bool {
	if action == "" || len(action) > maxActionBytes || !isASCIIAlphaNumeric(action[0]) {
		return false
	}
	for index := range len(action) {
		character := action[index]
		if isASCIIAlphaNumeric(character) || character == '.' || character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}

func isASCIIAlphaNumeric(character byte) bool {
	return (character >= 'a' && character <= 'z') ||
		(character >= 'A' && character <= 'Z') ||
		(character >= '0' && character <= '9')
}
