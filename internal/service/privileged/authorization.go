package privileged

import (
	"fmt"
	"math"
	"os/user"
	"strconv"

	"mvmctl/internal/infra"
	"mvmctl/pkg/errs"
)

type authorizationDeps struct {
	lookupUserID func(string) (*user.User, error)
	lookupGroup  func(string) (*user.Group, error)
	groupIDs     func(*user.User) ([]string, error)
}

func authorizeCaller(caller callerIdentity, deps authorizationDeps) error {
	uid := strconv.FormatUint(uint64(caller.uid), 10)
	invokingUser, err := deps.lookupUserID(uid)
	if err != nil {
		return errs.New(
			errs.CodePrivilegeRequired,
			fmt.Sprintf("look up invoking UID %s: %v", uid, err),
		)
	}
	resolvedUID, err := parseIdentityNumber(invokingUser.Uid)
	if err != nil || resolvedUID != caller.uid {
		return errs.New(
			errs.CodePrivilegeRequired,
			fmt.Sprintf("resolved user identity does not match requested UID %s", uid),
		)
	}

	authorizationGroup, err := deps.lookupGroup(infra.MVMUnixGroup)
	if err != nil {
		return errs.New(
			errs.CodePrivilegeRequired,
			fmt.Sprintf("look up authorization group %s: %v", infra.MVMUnixGroup, err),
		)
	}
	authorizationGID, err := parseIdentityNumber(authorizationGroup.Gid)
	if err != nil {
		return errs.New(
			errs.CodePrivilegeRequired,
			fmt.Sprintf("invalid authorization group ID for %s", infra.MVMUnixGroup),
		)
	}
	if authorizationGID == 0 {
		return errs.New(
			errs.CodePrivilegeRequired,
			"authorization group must identify a non-root group",
		)
	}

	primaryGID, err := parseIdentityNumber(invokingUser.Gid)
	if err != nil {
		return errs.New(
			errs.CodePrivilegeRequired,
			fmt.Sprintf("invalid primary group ID for invoking UID %s", uid),
		)
	}
	if primaryGID == authorizationGID {
		return nil
	}

	groupIDs, err := deps.groupIDs(invokingUser)
	if err != nil {
		return errs.New(
			errs.CodePrivilegeRequired,
			fmt.Sprintf("look up current group membership for invoking UID %s: %v", uid, err),
		)
	}
	for _, groupID := range groupIDs {
		parsedGroupID, err := parseIdentityNumber(groupID)
		if err != nil {
			return errs.New(
				errs.CodePrivilegeRequired,
				fmt.Sprintf("invalid current group membership for invoking UID %s", uid),
			)
		}
		if parsedGroupID == authorizationGID {
			return nil
		}
	}

	return errs.New(
		errs.CodePrivilegeRequired,
		fmt.Sprintf(
			"invoking UID %s is not a current member of the %s group",
			uid,
			infra.MVMUnixGroup,
		),
	)
}

func parseIdentityNumber(value string) (uint32, error) {
	parsed, err := strconv.ParseUint(value, 10, 32)
	if err != nil || parsed == math.MaxUint32 {
		return 0, fmt.Errorf("invalid identity number %q", value)
	}
	return uint32(parsed), nil
}

func realAuthorizationDeps() authorizationDeps {
	return authorizationDeps{
		lookupUserID: user.LookupId,
		lookupGroup:  user.LookupGroup,
		groupIDs: func(invokingUser *user.User) ([]string, error) {
			return invokingUser.GroupIds()
		},
	}
}
