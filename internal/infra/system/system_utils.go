package system

import (
	"fmt"
	"os"
	"os/user"
	"runtime"
	"strconv"
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

// GetRealUserIDs returns the real (original) user UID and GID.
// Uses SUDO_UID/SUDO_GID env vars if set (running under sudo).
// Returns (uid, gid, isRoot) where isRoot is true when current user is root.
func GetRealUserIDs() (int, int, bool) {
	uidStr := os.Getenv("SUDO_UID")
	gidStr := os.Getenv("SUDO_GID")
	if uidStr != "" && gidStr != "" {
		uid, err1 := strconv.Atoi(uidStr)
		gid, err2 := strconv.Atoi(gidStr)
		if err1 == nil && err2 == nil {
			return uid, gid, false
		}
	}
	return os.Getuid(), os.Getgid(), os.Getuid() == 0
}

// ChownToRealUser changes file ownership to the real (original) user.
// If running under sudo, uses SUDO_UID/SUDO_GID. Otherwise uses current user.
func ChownToRealUser(pathStr string) {
	uid, gid, _ := GetRealUserIDs()
	_ = os.Chown(pathStr, uid, gid)
}

// FileExists returns true if path exists and is a regular file.
func FileExists(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return !info.IsDir()
}

// MakeExecutable sets the executable permission bit on path.
func MakeExecutable(path string) error {
	return os.Chmod(path, 0755)
}

// TruncateString truncates s to at most maxLen characters.
func TruncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen]
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
