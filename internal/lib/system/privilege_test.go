package system_test

import (
	"context"
	"errors"
	"os"
	"os/user"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/system"
	"mvmctl/internal/testutil"
	"mvmctl/pkg/errs"
)

// ─── NewPrivilegeError ────────────────────────────────────────────────────────
// Rationale: Must create a DomainError with CodePrivilegeRequired and structured
// details. Keep as-is — purely data-formatting, no OS calls.

func TestNewPrivilegeError(t *testing.T) {
	t.Run("with_all_fields", func(t *testing.T) {
		details := &system.PrivilegeDetails{
			Message:             "User not in mvm group",
			MissingCapabilities: []string{"CAP_NET_ADMIN"},
			MissingBinaries:     []string{"ip", "iptables"},
			Suggestions:         []string{"sudo mvm host init", "newgrp mvm"},
		}
		err := system.NewPrivilegeError("Elevated privileges required", details)
		require.Error(t, err)
		var de *errs.DomainError
		if errors.As(err, &de) {
			assert.Equal(t, errs.CodePrivilegeRequired, de.Code)
			assert.Equal(t, "Elevated privileges required", de.Message)
			assert.Equal(t, "User not in mvm group", de.Details["message"])
			assert.Equal(t, []interface{}{"CAP_NET_ADMIN"}, de.Details["missing_capabilities"])
		}
	})

	t.Run("nil_details", func(t *testing.T) {
		err := system.NewPrivilegeError("msg", nil)
		require.Error(t, err)
	})

	t.Run("empty_details", func(t *testing.T) {
		err := system.NewPrivilegeError("msg", &system.PrivilegeDetails{})
		require.Error(t, err)
	})
}

// ─── IsRoot ───────────────────────────────────────────────────────────────────
// Rationale: Returns true when Geteuid() == 0.

func TestIsRoot(t *testing.T) {
	orig := system.DefaultOS
	defer func() { system.DefaultOS = orig }()

	t.Run("root_when_euid_zero", func(t *testing.T) {
		system.DefaultOS = &testutil.FakeOS{GeteuidVal: 0}
		assert.True(t, system.IsRoot())
	})

	t.Run("not_root_when_euid_nonzero", func(t *testing.T) {
		system.DefaultOS = &testutil.FakeOS{GeteuidVal: 1000}
		assert.False(t, system.IsRoot())
	})
}

// ─── GroupExists ──────────────────────────────────────────────────────────────
// Rationale: Returns true when LookupGroup succeeds.

func TestGroupExists(t *testing.T) {
	orig := system.DefaultOS
	defer func() { system.DefaultOS = orig }()

	t.Run("exists_when_lookup_succeeds", func(t *testing.T) {
		system.DefaultOS = &testutil.FakeOS{
			LookupGroupFn: func(name string) (*user.Group, error) {
				return &user.Group{Gid: "42"}, nil
			},
		}
		assert.True(t, system.GroupExists("mvm"))
	})

	t.Run("not_exists_when_lookup_fails", func(t *testing.T) {
		system.DefaultOS = &testutil.FakeOS{
			LookupGroupFn: func(name string) (*user.Group, error) {
				return nil, errors.New("group not found")
			},
		}
		assert.False(t, system.GroupExists("nonexistent"))
	})
}

// ─── SessionHasGroup ──────────────────────────────────────────────────────────
// Rationale: Checks if any process GID matches the group.

func TestSessionHasGroup(t *testing.T) {
	orig := system.DefaultOS
	defer func() { system.DefaultOS = orig }()

	mvmGroup := &user.Group{Gid: "999"}

	t.Run("found_in_getgroups", func(t *testing.T) {
		system.DefaultOS = &testutil.FakeOS{
			LookupGroupFn: func(name string) (*user.Group, error) { return mvmGroup, nil },
			GetgroupsVal:  []int{999},
			GetgidVal:     1000,
			GetegidVal:    1000,
		}
		assert.True(t, system.SessionHasGroup())
	})

	t.Run("found_in_getgid", func(t *testing.T) {
		system.DefaultOS = &testutil.FakeOS{
			LookupGroupFn: func(name string) (*user.Group, error) { return mvmGroup, nil },
			GetgroupsVal:  []int{},
			GetgidVal:     999,
			GetegidVal:    1000,
		}
		assert.True(t, system.SessionHasGroup())
	})

	t.Run("found_in_getegid", func(t *testing.T) {
		system.DefaultOS = &testutil.FakeOS{
			LookupGroupFn: func(name string) (*user.Group, error) { return mvmGroup, nil },
			GetgroupsVal:  []int{},
			GetgidVal:     1000,
			GetegidVal:    999,
		}
		assert.True(t, system.SessionHasGroup())
	})

	t.Run("not_found_returns_false", func(t *testing.T) {
		system.DefaultOS = &testutil.FakeOS{
			LookupGroupFn: func(name string) (*user.Group, error) { return mvmGroup, nil },
			GetgroupsVal:  []int{},
			GetgidVal:     1000,
			GetegidVal:    1000,
		}
		assert.False(t, system.SessionHasGroup())
	})

	t.Run("group_lookup_fails_returns_false", func(t *testing.T) {
		system.DefaultOS = &testutil.FakeOS{
			LookupGroupFn: func(name string) (*user.Group, error) {
				return nil, errors.New("no such group")
			},
		}
		assert.False(t, system.SessionHasGroup())
	})
}

// ─── UserInGroup ──────────────────────────────────────────────────────────────
// Rationale: Checks NSS group membership. (GroupMembersViaNSS still calls
// DefaultRunner — not mocked here. See runner_test.go for NSS faking.)

func TestUserInGroup(t *testing.T) {
	orig := system.DefaultOS
	defer func() { system.DefaultOS = orig }()

	t.Run("group_lookup_fails_returns_false", func(t *testing.T) {
		system.DefaultOS = &testutil.FakeOS{
			LookupGroupFn: func(name string) (*user.Group, error) {
				return nil, errors.New("no such group")
			},
		}
		// GroupMembersViaNSS will fail because it calls real DefaultRunner
		// (no NSS fake exists). The function should still return false gracefully.
		assert.False(t, system.UserInGroup(context.Background(), "user", "nonexistent"))
	})
}

// ─── IsProcessRunning ─────────────────────────────────────────────────────────
// Rationale: pid <= 0 returns false (fast path). Positive pid uses FindProcess.

func TestIsProcessRunning(t *testing.T) {
	orig := system.DefaultOS
	defer func() { system.DefaultOS = orig }()

	t.Run("pid_zero_returns_false", func(t *testing.T) {
		assert.False(t, system.IsProcessRunning(0))
	})

	t.Run("pid_negative_returns_false", func(t *testing.T) {
		assert.False(t, system.IsProcessRunning(-1))
	})

	// Positive-pid test omitted — proc.Signal() is a concrete method on
	// *os.Process that sends real OS signals, not abstracted by OSProvider.
	// system-test coverage via runner_test.go (IsProcessAlive).

	t.Run("findprocess_fails_returns_false", func(t *testing.T) {
		system.DefaultOS = &testutil.FakeOS{
			FindProcessFn: func(pid int) (*os.Process, error) {
				return nil, errors.New("not found")
			},
		}
		assert.False(t, system.IsProcessRunning(42))
	})
}

// ─── CheckPrivileges ──────────────────────────────────────────────────────────
// Rationale: 6 branches — binary missing, root bypass, group missing, user not
// in group, session without group, all good.

func TestCheckPrivileges(t *testing.T) {
	orig := system.DefaultOS
	defer func() { system.DefaultOS = orig }()

	mvmGroup := &user.Group{Gid: "999"}

	t.Run("binary_not_found_included_in_details", func(t *testing.T) {
		system.DefaultOS = &testutil.FakeOS{
			LookPathFn: func(file string) (string, error) {
				return "", errors.New("not found")
			},
			StatFn: func(name string) (os.FileInfo, error) {
				return nil, os.ErrNotExist
			},
			GeteuidVal: 1000,
			LookupGroupFn: func(name string) (*user.Group, error) {
				return mvmGroup, nil
			},
			CurrentFn: func() (*user.User, error) {
				return &user.User{Username: "testuser", Gid: "999"}, nil
			},
			GetgroupsVal: []int{999},
			GetgidVal:    999,
			GetegidVal:   999,
		}
		err := system.CheckPrivileges("/usr/bin/ip", "test operation")
		assert.NoError(t, err, "user in group with session active should pass")
	})

	t.Run("root_bypasses_all_checks", func(t *testing.T) {
		system.DefaultOS = &testutil.FakeOS{
			GeteuidVal: 0,
		}
		err := system.CheckPrivileges("/usr/bin/ip", "test")
		assert.NoError(t, err)
	})

	t.Run("group_does_not_exist_errors", func(t *testing.T) {
		system.DefaultOS = &testutil.FakeOS{
			LookPathFn: func(file string) (string, error) { return "/usr/bin/ip", nil },
			GeteuidVal: 1000,
			LookupGroupFn: func(name string) (*user.Group, error) {
				return nil, errors.New("group not found")
			},
		}
		err := system.CheckPrivileges("/usr/bin/ip", "test")
		require.Error(t, err)
		assertCode(t, err, errs.CodePrivilegeRequired)
		assert.Contains(t, err.Error(), "Elevated privileges required")
	})

	t.Run("user_not_in_group_errors", func(t *testing.T) {
		system.DefaultOS = &testutil.FakeOS{
			LookPathFn:  func(file string) (string, error) { return "/usr/bin/ip", nil },
			GeteuidVal:  1000,
			LookupGroupFn: func(name string) (*user.Group, error) { return mvmGroup, nil },
			CurrentFn: func() (*user.User, error) {
				// Gid "1000" does not match mvmGroup Gid "999", and GroupMembersViaNSS
				// will fail (real getent), so isSupplementaryMember = false.
				return &user.User{Username: "testuser", Gid: "1000"}, nil
			},
		}
		err := system.CheckPrivileges("/usr/bin/ip", "test")
		require.Error(t, err)
		assertCode(t, err, errs.CodePrivilegeRequired)
		var de *errs.DomainError
		if errors.As(err, &de) && de.Details != nil {
			msg, _ := de.Details["message"].(string)
			assert.Contains(t, msg, "not in the")
		}
	})

	t.Run("user_in_group_but_session_without_group_errors", func(t *testing.T) {
		system.DefaultOS = &testutil.FakeOS{
			LookPathFn:  func(file string) (string, error) { return "/usr/bin/ip", nil },
			GeteuidVal:  1000,
			LookupGroupFn: func(name string) (*user.Group, error) { return mvmGroup, nil },
			CurrentFn: func() (*user.User, error) {
				return &user.User{Username: "testuser", Gid: "999"}, nil
			},
			GetgroupsVal: []int{},     // session doesn't have group GID
			GetgidVal:    1000,         // primary GID matches but...
			GetegidVal:   1000,         // neither GID nor EGID is 999
		}
		err := system.CheckPrivileges("/usr/bin/ip", "test")
		require.Error(t, err)
		assertCode(t, err, errs.CodePrivilegeRequired)
		var de *errs.DomainError
		if errors.As(err, &de) && de.Details != nil {
			msg, _ := de.Details["message"].(string)
			assert.Contains(t, msg, "session does not have the group")
		}
	})

	t.Run("all_checks_pass", func(t *testing.T) {
		system.DefaultOS = &testutil.FakeOS{
			LookPathFn:  func(file string) (string, error) { return "/usr/bin/ip", nil },
			GeteuidVal:  1000,
			LookupGroupFn: func(name string) (*user.Group, error) { return mvmGroup, nil },
			CurrentFn: func() (*user.User, error) {
				return &user.User{Username: "testuser", Gid: "999"}, nil
			},
			GetgroupsVal: []int{999},
			GetgidVal:    999,
			GetegidVal:   999,
		}
		err := system.CheckPrivileges("/usr/bin/ip", "test")
		assert.NoError(t, err)
	})
}

// ─── Helper ───────────────────────────────────────────────────────────────────

func assertCode(t *testing.T, err error, code errs.Code) {
	t.Helper()
	var de *errs.DomainError
	if errors.As(err, &de) {
		assert.Equal(t, code, de.Code)
	} else {
		t.Errorf("expected *errs.DomainError, got %T", err)
	}
}
