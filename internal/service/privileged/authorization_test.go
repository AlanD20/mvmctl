package privileged

import (
	"errors"
	"os/user"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/infra"
	"mvmctl/pkg/errs"
)

func TestAuthorizeCaller_AcceptsCurrentUIDBoundMembership(t *testing.T) {
	tests := map[string]struct {
		user     *user.User
		groupIDs []string
	}{
		"supplementary_group": {
			user:     &user.User{Uid: "1000", Gid: "1000", Username: "alice"},
			groupIDs: []string{"1000", "2000"},
		},
		"primary_group": {
			user:     &user.User{Uid: "1000", Gid: "2000", Username: "alice"},
			groupIDs: []string{"1000"},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			requestedUID := ""
			err := authorizeCaller(callerIdentity{uid: 1000, gid: 3000}, authorizationDeps{
				lookupUserID: func(uid string) (*user.User, error) {
					requestedUID = uid
					return tc.user, nil
				},
				lookupGroup: func(name string) (*user.Group, error) {
					assert.Equal(t, infra.MVMUnixGroup, name)
					return &user.Group{Name: name, Gid: "2000"}, nil
				},
				groupIDs: func(got *user.User) ([]string, error) {
					assert.Same(t, tc.user, got)
					return tc.groupIDs, nil
				},
			})
			require.NoError(t, err)
			assert.Equal(t, "1000", requestedUID)
		})
	}
}

// Rationale: SUDO_GID describes the caller's launch context, not current mvm
// authorization. Removing the user from mvm must revoke the next invocation.
func TestAuthorizeCaller_RejectsMissingCurrentMembershipDespiteSudoGID(t *testing.T) {
	err := authorizeCaller(callerIdentity{uid: 1000, gid: 2000}, authorizationDeps{
		lookupUserID: func(string) (*user.User, error) {
			return &user.User{Uid: "1000", Gid: "1000", Username: "alice"}, nil
		},
		lookupGroup: func(string) (*user.Group, error) {
			return &user.Group{Name: infra.MVMUnixGroup, Gid: "2000"}, nil
		},
		groupIDs: func(*user.User) ([]string, error) { return []string{"1000", "3000"}, nil },
	})
	require.Error(t, err)
	assert.ErrorContains(t, err, "is not a current member")
	assert.Equal(t, errs.CodePrivilegeRequired, errs.AsDomainError(err).Code)
}

func TestAuthorizeCaller_FailsClosedOnNSSAndIdentityErrors(t *testing.T) {
	tests := map[string]struct {
		mutate  func(*authorizationDeps)
		wantErr string
	}{
		"user_lookup_failure": {
			mutate: func(deps *authorizationDeps) {
				deps.lookupUserID = func(string) (*user.User, error) { return nil, errors.New("nss unavailable") }
			},
			wantErr: "look up invoking UID",
		},
		"uid_mismatch": {
			mutate: func(deps *authorizationDeps) {
				deps.lookupUserID = func(string) (*user.User, error) {
					return &user.User{Uid: "1001", Gid: "1000", Username: "mallory"}, nil
				}
			},
			wantErr: "does not match requested UID",
		},
		"group_lookup_failure": {
			mutate: func(deps *authorizationDeps) {
				deps.lookupGroup = func(string) (*user.Group, error) { return nil, errors.New("group unavailable") }
			},
			wantErr: "look up authorization group",
		},
		"invalid_group_id": {
			mutate: func(deps *authorizationDeps) {
				deps.lookupGroup = func(string) (*user.Group, error) {
					return &user.Group{Name: infra.MVMUnixGroup, Gid: "not-a-gid"}, nil
				}
			},
			wantErr: "invalid authorization group ID",
		},
		"root_authorization_group": {
			mutate: func(deps *authorizationDeps) {
				deps.lookupGroup = func(string) (*user.Group, error) {
					return &user.Group{Name: infra.MVMUnixGroup, Gid: "0"}, nil
				}
			},
			wantErr: "authorization group must identify a non-root group",
		},
		"membership_lookup_failure": {
			mutate: func(deps *authorizationDeps) {
				deps.groupIDs = func(*user.User) ([]string, error) { return nil, errors.New("membership unavailable") }
			},
			wantErr: "look up current group membership",
		},
		"invalid_membership_group_id": {
			mutate: func(deps *authorizationDeps) {
				deps.groupIDs = func(*user.User) ([]string, error) { return []string{"bad-gid"}, nil }
			},
			wantErr: "invalid current group membership",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			deps := memberAuthorizationDeps()
			tc.mutate(&deps)
			err := authorizeCaller(callerIdentity{uid: 1000, gid: 1000}, deps)
			require.Error(t, err)
			assert.ErrorContains(t, err, tc.wantErr)
			assert.Equal(t, errs.CodePrivilegeRequired, errs.AsDomainError(err).Code)
		})
	}
}

func memberAuthorizationDeps() authorizationDeps {
	return authorizationDeps{
		lookupUserID: func(string) (*user.User, error) {
			return &user.User{Uid: "1000", Gid: "1000", Username: "alice"}, nil
		},
		lookupGroup: func(string) (*user.Group, error) {
			return &user.Group{Name: infra.MVMUnixGroup, Gid: "2000"}, nil
		},
		groupIDs: func(*user.User) ([]string, error) { return []string{"1000", "2000"}, nil },
	}
}
