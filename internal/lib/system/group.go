package system

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"

	"mvmctl/internal/infra"
)

// _mvmGroupVerified is a per-process cache for group membership status.
// Group membership is immutable within a process lifetime (os.Getgroups()
// returns what was set at login/newgrp time), so we only check once.
var _mvmGroupVerified bool
var _mvmGroupMu sync.Mutex

// requireMvmGroupMembership warns if the current user lacks the mvm group,
// but does NOT block execution. Prints advisory warnings for each missing
// precondition (group doesn't exist, user not a member, session doesn't have
// the group active), then lets sudo handle authentication.
//
// Results are cached per-process because group membership is immutable
// within a process lifetime.
//
// Uses infra.MVMUnixGroup (which defaults to "mvm" from the CLI binary name).
func requireMvmGroupMembership() error {
	_mvmGroupMu.Lock()
	if _mvmGroupVerified {
		_mvmGroupMu.Unlock()
		return nil
	}
	_mvmGroupMu.Unlock()

	groupName := infra.MVMUnixGroup

	g, err := DefaultOS.LookupGroup(groupName)
	if err != nil {
		// slog routes to the configured logging infrastructure (stderr + file).
		slog.Warn(
			"Group does not exist. Run 'sudo mvm host init' to set up privilege management and avoid password prompts.",
			"group",
			groupName,
		)
		_mvmGroupMu.Lock()
		_mvmGroupVerified = true
		_mvmGroupMu.Unlock()
		return nil
	}

	currentUser, err := DefaultOS.Current()
	if err != nil {
		_mvmGroupMu.Lock()
		_mvmGroupVerified = true
		_mvmGroupMu.Unlock()
		return fmt.Errorf("get current user: %w", err)
	}
	username := currentUser.Username

	groupMembers, _ := GroupMembersViaNSS(context.Background(), g.Name)

	// --- Check 1: is user a member (supplementary OR primary)? ---
	isSupplementaryMember := slices.Contains(groupMembers, username)
	isPrimaryGroup := currentUser.Gid == g.Gid

	if !(isSupplementaryMember || isPrimaryGroup) {
		slog.Warn(
			"User is not in the mvm group. Run 'sudo mvm host init' to configure privileges, then 'newgrp' or log out and back in.",
			"user",
			username,
			"group",
			groupName,
		)
	}

	// --- Check 2: does THIS session have the group active? ---
	processGIDs := make(map[string]struct{})
	groups, _ := DefaultOS.Getgroups()
	for _, gid := range groups {
		processGIDs[fmt.Sprintf("%d", gid)] = struct{}{}
	}
	processGIDs[fmt.Sprintf("%d", DefaultOS.Getgid())] = struct{}{}
	processGIDs[fmt.Sprintf("%d", DefaultOS.Getegid())] = struct{}{}

	if _, ok := processGIDs[g.Gid]; !ok {
		slog.Warn(
			"User is in the mvm group but current session does not have the group active. Log out and back in, or run 'newgrp'.",
			"group",
			groupName,
		)
	}

	_mvmGroupMu.Lock()
	_mvmGroupVerified = true
	_mvmGroupMu.Unlock()
	return nil
}

// --- OS helpers ---

// IsRoot returns true if the effective user ID is 0 (root).
// Uses os.Geteuid() because the kernel checks the effective UID for
// permission decisions. This correctly detects privileged access
// via sudo, doas, or setuid binaries.
func IsRoot() bool {
	return DefaultOS.Geteuid() == 0
}

// --- GroupExists ---
// Uses stdlib os/user.LookupGroup which resolves through NSS
// (LDAP, systemd-userdb, etc.).
func GroupExists(groupName string) bool {
	_, err := DefaultOS.LookupGroup(groupName)
	return err == nil
}

// --- UserInGroup ---
// Checks if username is a member of the given group.
// Uses stdlib os/user.LookupGroup + GroupMembersViaNSS to get member list
// via NSS (LDAP, systemd-userdb, etc.).
func UserInGroup(ctx context.Context, username, groupName string) bool {
	_, err := DefaultOS.LookupGroup(groupName)
	if err != nil {
		return false
	}
	members, err := GroupMembersViaNSS(ctx, groupName)
	if err != nil {
		return false
	}
	return slices.Contains(members, username)
}

// --- GroupMembersViaNSS ---
// Returns the member list for a group via NSS (getent).
// Resolves through NSS (LDAP, systemd-userdb, etc.) instead of
// parsing /etc/group directly.
func GroupMembersViaNSS(ctx context.Context, groupName string) ([]string, error) {
	result, err := DefaultRunner.Run(ctx, []string{"getent", "group", groupName}, RunCmdOpts{Capture: true})
	if err != nil {
		return nil, fmt.Errorf("unable to resolve group %q via NSS: %w", groupName, err)
	}
	// Parse "groupname:x:gid:member1,member2"
	parts := strings.SplitN(strings.TrimSpace(result.Stdout), ":", 4)
	if len(parts) < 4 || parts[2] == "" {
		return nil, fmt.Errorf("unable to parse group: %s", groupName)
	}
	members := strings.TrimSpace(parts[3])
	if members == "" {
		return nil, nil
	}
	return strings.Split(members, ","), nil
}
