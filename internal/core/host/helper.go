package host

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"

	"mvmctl/internal/infra"
	"mvmctl/internal/infra/system"
)

// ── PrivilegeHelper ──
// Matches Python's HostPrivilegeHelper class.
type PrivilegeHelper struct{}

func NewPrivilegeHelper() *PrivilegeHelper {
	return &PrivilegeHelper{}
}

// CheckPrivileges checks privileges; if lacking, returns an error with structured details.
// This is a pure check — no console output.
//
// Matches Python's HostPrivilegeHelper.check_privileges().
func (h *PrivilegeHelper) CheckPrivileges(binary string, operationDescription string) error {
	opStr := ""
	if operationDescription != "" {
		opStr = fmt.Sprintf(" for: %s", operationDescription)
	}

	var missingBinaries []string
	if _, err := exec.LookPath(binary); err != nil {
		if _, err := os.Stat(binary); os.IsNotExist(err) {
			missingBinaries = append(missingBinaries, binary)
		}
	}

	if os.Getuid() == 0 {
		return nil
	}

	// Lookup the mvm group using Go's stdlib os/user.LookupGroup (NSS-compatible)
	grpInfo, err := user.LookupGroup(infra.MVMUnixGroup)
	if err != nil {
		// Group does not exist
		details := &PrivilegeDetails{
			Message: fmt.Sprintf(
				"Group '%s' does not exist. Run 'sudo mvm host init' to set up privilege management.",
				infra.MVMUnixGroup,
			),
			MissingBinaries: missingBinaries,
			Suggestions: []string{
				fmt.Sprintf("Run with sudo: sudo %s ...", infra.CLIName),
				"Configure persistent access: sudo mvm host init",
				fmt.Sprintf("Then log out and back in, or run: newgrp %s", infra.MVMUnixGroup),
			},
		}
		return NewPrivilegeError(
			fmt.Sprintf("Elevated privileges required%s", opStr),
			details,
		)
	}

	currentUser, err := user.Current()
	if err != nil {
		return privilegeError(fmt.Sprintf("Elevated privileges required%s", opStr))
	}

	username := currentUser.Username

	// Check if user is in group via supplementary OR primary group
	isSupplementaryMember := false
	members, parseErr := system.GroupMembersViaNSS(infra.MVMUnixGroup)
	if parseErr == nil {
		for _, m := range members {
			if m == username {
				isSupplementaryMember = true
				break
			}
		}
	}
	isPrimaryGroup := currentUser.Gid == grpInfo.Gid
	userInGroup := isSupplementaryMember || isPrimaryGroup

	if !userInGroup {
		msg := fmt.Sprintf(
			"User '%s' is not in the '%s' group. Run 'sudo mvm host init' to configure privileges, then 'newgrp %s' or log out and back in.",
			username, infra.MVMUnixGroup, infra.MVMUnixGroup,
		)
		details := &PrivilegeDetails{
			Message:         msg,
			MissingBinaries: missingBinaries,
			Suggestions: []string{
				fmt.Sprintf("Run with sudo: sudo %s ...", infra.CLIName),
				"Configure persistent access: sudo mvm host init",
				fmt.Sprintf("Then log out and back in, or run: newgrp %s", infra.MVMUnixGroup),
			},
		}
		return NewPrivilegeError(
			fmt.Sprintf("Elevated privileges required%s", opStr),
			details,
		)
	}

	// User is in group per NSS (gr_mem) — but check if THIS process has the credentials
	if !h.SessionHasGroup() {
		msg := fmt.Sprintf(
			"Your user is in the '%s' group, but your current session does not have the group active yet. Please log out and log back in, or run: newgrp %s",
			infra.MVMUnixGroup, infra.MVMUnixGroup,
		)
		details := &PrivilegeDetails{
			Message:         msg,
			MissingBinaries: missingBinaries,
			Suggestions: []string{
				fmt.Sprintf("Run with sudo: sudo %s ...", infra.CLIName),
				fmt.Sprintf("Activate group in current session: newgrp %s", infra.MVMUnixGroup),
				"Or log out and back in for group membership to take effect",
			},
		}
		return NewPrivilegeError(
			fmt.Sprintf("Elevated privileges required%s", opStr),
			details,
		)
	}

	return nil
}

// SessionHasGroup checks if the current process has the mvm group GID active in its credentials.
// Uses os.Getgroups(), os.Getgid(), and os.Getegid().
func (h *PrivilegeHelper) SessionHasGroup() bool {
	g, err := user.LookupGroup(infra.MVMUnixGroup)
	if err != nil {
		return false
	}

	processGIDs := make(map[string]bool)
	groups, _ := os.Getgroups()
	for _, gid := range groups {
		processGIDs[fmt.Sprintf("%d", gid)] = true
	}
	processGIDs[fmt.Sprintf("%d", os.Getgid())] = true
	processGIDs[fmt.Sprintf("%d", os.Getegid())] = true

	return processGIDs[g.Gid]
}

// InMvmGroup checks if the current user is a member of the mvm group.
func (h *PrivilegeHelper) InMvmGroup() bool {
	g, err := user.LookupGroup(infra.MVMUnixGroup)
	if err != nil {
		return false
	}
	currentUser, err := user.Current()
	if err != nil {
		return false
	}
	members, parseErr := system.GroupMembersViaNSS(infra.MVMUnixGroup)
	if parseErr != nil {
		return false
	}
	for _, m := range members {
		if m == currentUser.Username {
			return true
		}
	}
	// Also check if primary group matches
	if currentUser.Gid == g.Gid {
		return true
	}
	return false
}

// IsRoot returns true if running as root (matches Python's os.getuid() == 0).
func (h *PrivilegeHelper) IsRoot() bool {
	return os.Getuid() == 0
}

// RequireRoot returns an error if not running as root.
func (h *PrivilegeHelper) RequireRoot() error {
	if !h.IsRoot() {
		return privilegeError("this command requires root privileges",
			&PrivilegeDetails{
				Message:     "Run the command with sudo or as root.",
				Suggestions: []string{"Run the command with sudo or as root."},
			},
		)
	}
	return nil
}

// SudoersDropInPath returns the path to the mvm sudoers drop-in file.
func SudoersDropInPath() string {
	return fmt.Sprintf("/etc/sudoers.d/%s", infra.CLIName)
}
