package agent

import (
	"context"
	"io"
	"os/user"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDestinationAccountParsingRejectsPrivilegeAndIdentityFailures(t *testing.T) {
	tests := map[string]struct {
		account  *user.User
		groups   []string
		wantText string
	}{
		"UID zero alias": {
			account:  &user.User{Username: "builder", Uid: "0", Gid: "1000", HomeDir: "/home/builder"},
			groups:   []string{"1000"},
			wantText: "nonzero UID",
		},
		"canonical username mismatch": {
			account:  &user.User{Username: "other", Uid: "1000", Gid: "1000", HomeDir: "/home/other"},
			groups:   []string{"1000"},
			wantText: "exact",
		},
		"invalid UID": {
			account:  &user.User{Username: "builder", Uid: "invalid", Gid: "1000", HomeDir: "/home/builder"},
			groups:   []string{"1000"},
			wantText: "UID",
		},
		"overflowing GID": {
			account:  &user.User{Username: "builder", Uid: "1000", Gid: "4294967296", HomeDir: "/home/builder"},
			groups:   []string{"1000"},
			wantText: "primary group",
		},
		"invalid supplementary group": {
			account:  &user.User{Username: "builder", Uid: "1000", Gid: "1000", HomeDir: "/home/builder"},
			groups:   []string{"invalid"},
			wantText: "supplementary",
		},
		"relative home": {
			account:  &user.User{Username: "builder", Uid: "1000", Gid: "1000", HomeDir: "home/builder"},
			groups:   []string{"1000"},
			wantText: "home",
		},
		"unclean home": {
			account:  &user.User{Username: "builder", Uid: "1000", Gid: "1000", HomeDir: "/home/../root"},
			groups:   []string{"1000"},
			wantText: "home",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := destinationAccountFromUser("builder", test.account, test.groups)
			require.Error(t, err)
			assert.Contains(t, err.Error(), test.wantText)
		})
	}
}

func TestDestinationAccountParsingReturnsNumericDeduplicatedCredential(t *testing.T) {
	account, err := destinationAccountFromUser(
		"builder",
		&user.User{Username: "builder", Uid: "1001", Gid: "1002", HomeDir: "/home/builder"},
		[]string{"1002", "1003", "1002"},
	)
	require.NoError(t, err)
	assert.Equal(t, destinationAccount{
		username: "builder",
		homeDir:  "/home/builder",
		uid:      1001,
		gid:      1002,
		groups:   []uint32{1002, 1003},
	}, account)
}

func TestDestinationCommandUsesExactCredentialAndSanitizedEnvironment(t *testing.T) {
	account := destinationAccount{
		username: "builder",
		homeDir:  "/home/builder",
		uid:      1001,
		gid:      1002,
		groups:   []uint32{1002, 1003},
	}
	command := newDestinationCommand(t.Context(), account, "printf secret", io.Discard, io.Discard)

	assert.Equal(t, []string{"/bin/sh", "-c", "printf secret"}, command.Args)
	assert.Equal(t, "/home/builder", command.Dir)
	assert.Equal(t, []string{
		"HOME=/home/builder",
		"LOGNAME=builder",
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"SHELL=/bin/sh",
		"USER=builder",
	}, command.Env)
	require.NotNil(t, command.SysProcAttr)
	assert.True(t, command.SysProcAttr.Setpgid)
	require.NotNil(t, command.SysProcAttr.Credential)
	assert.Equal(t, uint32(1001), command.SysProcAttr.Credential.Uid)
	assert.Equal(t, uint32(1002), command.SysProcAttr.Credential.Gid)
	assert.Equal(t, []uint32{1002, 1003}, command.SysProcAttr.Credential.Groups)
	assert.NotNil(t, command.Cancel)
	assert.Equal(t, 2*time.Second, command.WaitDelay)
}

type errorDestinationAccountResolver struct {
	err error
}

func (resolver errorDestinationAccountResolver) Resolve(context.Context, string) (destinationAccount, error) {
	return destinationAccount{}, resolver.err
}

func TestDestinationExecutionStopsWhenAccountResolutionFails(t *testing.T) {
	effect := &systemDestinationExecution{accounts: errorDestinationAccountResolver{err: assert.AnError}}
	exitCode, err := effect.Execute(t.Context(), "builder", "printf secret", io.Discard, io.Discard)
	require.Error(t, err)
	assert.Equal(t, -1, exitCode)
	assert.NotContains(t, err.Error(), "printf secret")
}
