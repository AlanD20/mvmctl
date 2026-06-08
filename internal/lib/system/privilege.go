package system

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"mvmctl/internal/infra"
	"mvmctl/pkg/errs"
)

// PrivilegeDetails carries structured metadata about a privilege failure,
// matching Python's PrivilegeError rich `details` dict.
type PrivilegeDetails struct {
	Message             string   `json:"message"`
	MissingCapabilities []string `json:"missing_capabilities"`
	MissingBinaries     []string `json:"missing_binaries,omitempty"`
	Suggestions         []string `json:"suggestions,omitempty"`
}

// NewPrivilegeError creates a privilege error with structured PrivilegeDetails.
func NewPrivilegeError(msg string, details *PrivilegeDetails) *errs.DomainError {
	var d map[string]any
	data, _ := json.Marshal(details)
	json.Unmarshal(data, &d)
	if d == nil {
		d = make(map[string]any)
	}
	return errs.New(errs.CodePrivilegeRequired, msg, errs.WithDetails(d))
}

// CheckPrivileges checks privileges; if lacking, returns an error with structured details.
// This is a pure check — no console output.
// Matches Python's HostPrivilegeHelper.check_privileges().
func CheckPrivileges(binary string, operationDescription string) error {
	opStr := ""
	if operationDescription != "" {
		opStr = fmt.Sprintf(" for: %s", operationDescription)
	}

	var missingBinaries []string
	if _, err := DefaultOS.LookPath(binary); err != nil {
		if _, err := DefaultOS.Stat(binary); err != nil && DefaultOS.IsNotExist(err) {
			missingBinaries = append(missingBinaries, binary)
		}
	}

	if IsRoot() {
		return nil
	}

	grpInfo, err := DefaultOS.LookupGroup(infra.MVMUnixGroup)
	if err != nil {
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

	currentUser, err := DefaultOS.Current()
	if err != nil {
		return NewPrivilegeError(fmt.Sprintf("Elevated privileges required%s", opStr),
			&PrivilegeDetails{Message: err.Error()})
	}

	username := currentUser.Username

	isSupplementaryMember := false
	members, parseErr := GroupMembersViaNSS(context.Background(), infra.MVMUnixGroup)
	if parseErr == nil {
		isSupplementaryMember = slices.Contains(members, username)
	}
	isPrimaryGroup := currentUser.Gid == grpInfo.Gid
	userInGroup := isSupplementaryMember || isPrimaryGroup

	if !userInGroup {
		msg := fmt.Sprintf(
			"User '%s' is not in the '%s' group. Run 'sudo mvm host init' to configure privileges, then 'newgrp %s' or log out and back in.",
			username,
			infra.MVMUnixGroup,
			infra.MVMUnixGroup,
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

	if !SessionHasGroup() {
		msg := fmt.Sprintf(
			"Your user is in the '%s' group, but your current session does not have the group active yet. Please log out and log back in, or run: newgrp %s",
			infra.MVMUnixGroup,
			infra.MVMUnixGroup,
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
func SessionHasGroup() bool {
	g, err := DefaultOS.LookupGroup(infra.MVMUnixGroup)
	if err != nil {
		return false
	}

	processGIDs := make(map[string]bool)
	groups, _ := DefaultOS.Getgroups()
	for _, gid := range groups {
		processGIDs[fmt.Sprintf("%d", gid)] = true
	}
	processGIDs[fmt.Sprintf("%d", DefaultOS.Getgid())] = true
	processGIDs[fmt.Sprintf("%d", DefaultOS.Getegid())] = true

	return processGIDs[g.Gid]
}
