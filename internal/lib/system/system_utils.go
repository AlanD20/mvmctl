package system

import (
	"fmt"
	"os"
	"os/user"
	"runtime"
)

// CurrentUsername returns the current OS username.
// Checks SUDO_USER env var first when running under sudo.
func CurrentUsername() (string, error) {
	if sudoUser := os.Getenv("SUDO_USER"); sudoUser != "" {
		return sudoUser, nil
	}
	u, err := user.Current()
	if err != nil {
		return "", fmt.Errorf("cannot determine current username: %w", err)
	}
	return u.Username, nil
}

// RuntimeArch returns the CPU architecture using Firecracker's naming convention.
// Maps Go's runtime.GOARCH ("amd64", "arm64") to the names used in Firecracker
// release tarballs ("x86_64", "aarch64"). Returns GOARCH directly for other values.
func RuntimeArch() string {
	switch runtime.GOARCH {
	case "amd64":
		return "x86_64"
	case "arm64":
		return "aarch64"
	default:
		return runtime.GOARCH
	}
}
