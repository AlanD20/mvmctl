package system

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/user"
	"strings"
	"sync"

	"mvmctl/internal/infra"
)

// _mvmGroupVerified is a per-process cache matching Python's
// _MVM_GROUP_VERIFIED module-level flag.  Group membership is immutable
// within a process lifetime (os.Getgroups() returns what was set at
// login/newgrp time), so we only check once.
var _mvmGroupVerified bool
var _mvmGroupMu sync.Mutex

// RequireMvmGroupMembership matches Python's require_mvm_group_membership().
//
// It warns (via os.Stderr) if:
//  1. The 'mvm' group does not exist.
//  2. The current user is not in the group (supplementary OR primary).
//  3. The current session does not have the group active.
//
// Results are cached per-process because group membership is immutable
// within a process lifetime.
//
// Python's original docstring:
//
//	Warn if user is not in the mvm group, but do NOT block execution.
//	Prints advisory warnings for each missing precondition (group doesn't
//	exist, user not a member, session doesn't have the group active), then
//	lets sudo handle authentication with its normal password prompt.
//
// Uses infra.MVMUnixGroup (which defaults to "mvm" from the CLI binary name).
func RequireMvmGroupMembership() error {
	_mvmGroupMu.Lock()
	if _mvmGroupVerified {
		_mvmGroupMu.Unlock()
		return nil
	}
	_mvmGroupMu.Unlock()

	groupName := infra.MVMUnixGroup

	// Match Python's: import grp; g = grp.getgrnam(MVM_UNIX_GROUP)
	g, err := user.LookupGroup(groupName)
	if err != nil {
		// Python: logger.warning("Group '%s' does not exist. ...")
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

	// Match Python's: user_pw = pwd.getpwuid(os.getuid()); username = user_pw.pw_name
	currentUser, err := user.Current()
	if err != nil {
		_mvmGroupMu.Lock()
		_mvmGroupVerified = true
		_mvmGroupMu.Unlock()
		return fmt.Errorf("get current user: %w", err)
	}
	username := currentUser.Username

	// Get group member list like Python's g.gr_mem
	// Use getent group (NSS-compatible) instead of reading /etc/group directly.
	groupMembers := getGroupMembers(context.Background(), g.Name)

	// -- Check 1: is user a member (supplementary OR primary)? --
	// Python:
	//   is_supplementary_member = username in g.gr_mem
	//   is_primary_group = user_pw.pw_gid == g.gr_gid
	isSupplementaryMember := false
	for _, m := range groupMembers {
		if m == username {
			isSupplementaryMember = true
			break
		}
	}
	isPrimaryGroup := currentUser.Gid == g.Gid

	if !(isSupplementaryMember || isPrimaryGroup) {
		// Python: logger.warning("User '%s' is not in the '%s' group. ...")
		slog.Warn(
			"User is not in the mvm group. Run 'sudo mvm host init' to configure privileges, then 'newgrp' or log out and back in.",
			"user",
			username,
			"group",
			groupName,
		)
	}

	// -- Check 2: does THIS session have the group active? --
	// Python:
	//   process_gids = set(os.getgroups()) | {os.getgid(), os.getegid()}
	//   if g.gr_gid not in process_gids:
	processGIDs := make(map[string]struct{})
	groups, _ := os.Getgroups()
	for _, gid := range groups {
		processGIDs[fmt.Sprintf("%d", gid)] = struct{}{}
	}
	processGIDs[fmt.Sprintf("%d", os.Getgid())] = struct{}{}
	processGIDs[fmt.Sprintf("%d", os.Getegid())] = struct{}{}

	if _, ok := processGIDs[g.Gid]; !ok {
		// Python: logger.warning("Your user is in the '%s' group, but your
		//          current session does not have the group active yet. ...")
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

// getGroupMembers returns the list of members of a Unix group.
// Python's grp.getgrnam() returns a grp.struct_group with a gr_mem field.
// Go's user.LookupGroup() does not provide member lists.
//
// Instead of reading /etc/group directly (which breaks with NSS — LDAP,
// SSSD, etc.), we use `getent group` which queries the configured Name
// Service Switch libraries via libc, matching Python's NSS-compatible
// behavior.
func getGroupMembers(ctx context.Context, groupName string) []string {
	result := RunCmdCompat(ctx, []string{"getent", "group", groupName}, RunCmdOptions{Capture: true})
	if result.Err != nil {
		return nil
	}
	// Format: groupname:password:GID:member1,member2,...
	parts := strings.Split(strings.TrimRight(result.Stdout, "\n"), ":")
	if len(parts) >= 4 {
		membersStr := parts[3]
		if membersStr == "" {
			return nil
		}
		return strings.Split(membersStr, ",")
	}
	return nil
}

// ── OS helpers ──

// IsRoot returns true if the effective user ID is 0 (root).
// Uses os.Geteuid() because the kernel checks the effective UID for
// permission decisions. This correctly detects priviledged access
// via sudo, doas, or setuid binaries.
func IsRoot() bool {
	return os.Geteuid() == 0
}

// ── GroupExists ──
// Uses stdlib os/user.LookupGroup matching Python's grp.getgrnam()
// which resolves through NSS (LDAP, systemd-userdb, etc.).
func GroupExists(groupName string) bool {
	_, err := user.LookupGroup(groupName)
	return err == nil
}

// ── UserInGroup ──
// Checks if username is a member of the given group.
// Uses stdlib os/user.LookupGroup + GroupMembersViaNSS to get member list
// via NSS (LDAP, systemd-userdb, etc.), matching Python's grp.getgrnam().gr_mem.
func UserInGroup(ctx context.Context, username, groupName string) bool {
	_, err := user.LookupGroup(groupName)
	if err != nil {
		return false
	}
	members, err := GroupMembersViaNSS(ctx, groupName)
	if err != nil {
		return false
	}
	for _, m := range members {
		if m == username {
			return true
		}
	}
	return false
}

// ── GroupMembersViaNSS ──
// Returns the member list for a group via NSS (getent).
// This matches Python's grp.getgrnam().gr_mem which resolves through NSS
// (LDAP, systemd-userdb, etc.) instead of parsing /etc/group directly.
//
// TODO: Consolidate with getGroupMembers (unexported) which has similar logic.
func GroupMembersViaNSS(ctx context.Context, groupName string) ([]string, error) {
	result := RunCmdCompat(ctx, []string{"getent", "group", groupName}, RunCmdOptions{Capture: true})
	if result.Err != nil {
		return nil, fmt.Errorf("unable to resolve group %q via NSS: %w", groupName, result.Err)
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

// ResetGroupCache resets the module-level group membership cache.
// Intended for testing only.
func ResetGroupCache() {
	_mvmGroupMu.Lock()
	_mvmGroupVerified = false
	_mvmGroupMu.Unlock()
}
