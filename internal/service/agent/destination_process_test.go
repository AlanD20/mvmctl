package agent

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"os/user"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/pkg/errs"
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
		"nil account": {
			account:  nil,
			groups:   []string{"1000"},
			wantText: "exact",
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
		"overflowing UID": {
			account:  &user.User{Username: "builder", Uid: "4294967296", Gid: "1000", HomeDir: "/home/builder"},
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
		"overflowing supplementary group": {
			account:  &user.User{Username: "builder", Uid: "1000", Gid: "1000", HomeDir: "/home/builder"},
			groups:   []string{"4294967296"},
			wantText: "supplementary",
		},
		"empty home": {
			account:  &user.User{Username: "builder", Uid: "1000", Gid: "1000"},
			groups:   []string{"1000"},
			wantText: "home",
		},
		"NUL home": {
			account:  &user.User{Username: "builder", Uid: "1000", Gid: "1000", HomeDir: "/home/builder\x00/root"},
			groups:   []string{"1000"},
			wantText: "home",
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
	want := destinationAccount{
		username: "builder",
		homeDir:  "/home/builder",
		uid:      1001,
		gid:      1002,
		groups:   []uint32{1002, 1003},
	}
	if diff := cmp.Diff(want, account, cmp.AllowUnexported(destinationAccount{})); diff != "" {
		t.Fatalf("destination account mismatch (-want +got):\n%s", diff)
	}
}

func TestDestinationCommandUsesExactCredentialAndSanitizedEnvironment(t *testing.T) {
	account := destinationAccount{
		username: "builder",
		homeDir:  "/home/builder",
		uid:      1001,
		gid:      1002,
		groups:   []uint32{1002, 1003},
	}
	killer := &fakeDestinationProcessGroupKiller{}
	command := newDestinationCommand(t.Context(), account, "printf secret", io.Discard, io.Discard, killer)

	if diff := cmp.Diff([]string{"/bin/sh", "-c", "printf secret"}, command.Args); diff != "" {
		t.Errorf("command args mismatch (-want +got):\n%s", diff)
	}
	assert.Equal(t, "/home/builder", command.Dir)
	wantEnv := []string{
		"HOME=/home/builder",
		"LOGNAME=builder",
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"SHELL=/bin/sh",
		"USER=builder",
	}
	if diff := cmp.Diff(wantEnv, command.Env); diff != "" {
		t.Errorf("command environment mismatch (-want +got):\n%s", diff)
	}
	require.NotNil(t, command.SysProcAttr)
	assert.True(t, command.SysProcAttr.Setpgid)
	require.NotNil(t, command.SysProcAttr.Credential)
	assert.Equal(t, uint32(1001), command.SysProcAttr.Credential.Uid)
	assert.Equal(t, uint32(1002), command.SysProcAttr.Credential.Gid)
	if diff := cmp.Diff([]uint32{1002, 1003}, command.SysProcAttr.Credential.Groups); diff != "" {
		t.Errorf("supplementary groups mismatch (-want +got):\n%s", diff)
	}
	assert.NotNil(t, command.Cancel)
	assert.Equal(t, 2*time.Second, command.WaitDelay)
}

type recordingDestinationAccountResolver struct {
	account    destinationAccount
	err        error
	targetUser string
}

func (resolver *recordingDestinationAccountResolver) Resolve(
	_ context.Context,
	targetUser string,
) (destinationAccount, error) {
	resolver.targetUser = targetUser
	return resolver.account, resolver.err
}

type fakeDestinationProcessRunner struct {
	run func(*exec.Cmd) error
}

func (runner fakeDestinationProcessRunner) Run(command *exec.Cmd) error {
	return runner.run(command)
}

type fakeDestinationProcessGroupKiller struct {
	pid    int
	signal unix.Signal
	err    error
}

func (killer *fakeDestinationProcessGroupKiller) Kill(pid int, signal unix.Signal) error {
	killer.pid = pid
	killer.signal = signal
	return killer.err
}

type fakeDestinationExitError struct {
	code int
}

func (exitError fakeDestinationExitError) Error() string { return "destination exited" }
func (exitError fakeDestinationExitError) ExitCode() int { return exitError.code }

func TestDestinationExecutionRunsConfiguredCommandAndForwardsOutput(t *testing.T) {
	account := destinationAccount{
		username: "builder",
		homeDir:  "/home/builder",
		uid:      1001,
		gid:      1002,
		groups:   []uint32{1002, 1003},
	}
	var captured *exec.Cmd
	resolver := &recordingDestinationAccountResolver{account: account}
	runner := fakeDestinationProcessRunner{run: func(command *exec.Cmd) error {
		captured = command
		_, stdoutErr := command.Stdout.Write([]byte{0, 0xff, 'o', 'k'})
		require.NoError(t, stdoutErr)
		_, stderrErr := command.Stderr.Write([]byte{0, 0xfe, 'e', 'r', 'r'})
		require.NoError(t, stderrErr)
		return nil
	}}
	effect := &systemDestinationExecution{
		accounts:      resolver,
		processes:     runner,
		processGroups: &fakeDestinationProcessGroupKiller{},
	}
	var stdout, stderr bytes.Buffer

	exitCode, err := effect.Execute(t.Context(), "builder", "printf secret", &stdout, &stderr)

	require.NoError(t, err)
	assert.Equal(t, 0, exitCode)
	assert.Equal(t, "builder", resolver.targetUser)
	require.NotNil(t, captured)
	wantCommand := destinationCommandSecurityProjection{
		args: []string{"/bin/sh", "-c", "printf secret"},
		dir:  "/home/builder",
		env: []string{
			"HOME=/home/builder",
			"LOGNAME=builder",
			"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
			"SHELL=/bin/sh",
			"USER=builder",
		},
		uid:     1001,
		gid:     1002,
		groups:  []uint32{1002, 1003},
		setpgid: true,
	}
	gotCommand := destinationCommandProjection(captured)
	if diff := cmp.Diff(
		wantCommand,
		gotCommand,
		cmp.AllowUnexported(destinationCommandSecurityProjection{}),
	); diff != "" {
		t.Errorf("executed command security projection mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]byte{0, 0xff, 'o', 'k'}, stdout.Bytes()); diff != "" {
		t.Errorf("stdout mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]byte{0, 0xfe, 'e', 'r', 'r'}, stderr.Bytes()); diff != "" {
		t.Errorf("stderr mismatch (-want +got):\n%s", diff)
	}
}

func TestDestinationExecutionMapsExitAndGenericFailures(t *testing.T) {
	tests := map[string]struct {
		runErr       error
		wantExitCode int
		wantErr      bool
	}{
		"generic start failure": {runErr: assert.AnError, wantExitCode: -1, wantErr: true},
		"valid nonzero exit":    {runErr: fakeDestinationExitError{code: 7}, wantExitCode: 7},
		"invalid negative exit": {runErr: fakeDestinationExitError{code: -1}, wantExitCode: -1, wantErr: true},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			resolver := &recordingDestinationAccountResolver{account: destinationAccount{
				username: "builder", homeDir: "/home/builder", uid: 1001, gid: 1002,
			}}
			effect := &systemDestinationExecution{
				accounts:      resolver,
				processes:     fakeDestinationProcessRunner{run: func(*exec.Cmd) error { return test.runErr }},
				processGroups: &fakeDestinationProcessGroupKiller{},
			}
			exitCode, err := effect.Execute(t.Context(), "builder", "printf secret", io.Discard, io.Discard)
			assert.Equal(t, test.wantExitCode, exitCode)
			if test.wantErr {
				require.Error(t, err)
				var domainErr *errs.DomainError
				require.ErrorAs(t, err, &domainErr)
				assert.Equal(t, errs.CodeProcessError, domainErr.Code)
				assert.NotContains(t, err.Error(), "printf secret")
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestDestinationCommandCancellationKillsTheProcessGroup(t *testing.T) {
	killer := &fakeDestinationProcessGroupKiller{}
	command := newDestinationCommand(
		t.Context(),
		destinationAccount{username: "builder", homeDir: "/home/builder", uid: 1001, gid: 1002},
		"sleep 30",
		io.Discard,
		io.Discard,
		killer,
	)
	command.Process = &os.Process{Pid: 4242}
	require.NoError(t, command.Cancel())
	assert.Equal(t, -4242, killer.pid)
	assert.Equal(t, unix.SIGKILL, killer.signal)

	killer.err = unix.ESRCH
	assert.ErrorIs(t, command.Cancel(), os.ErrProcessDone)
}

type destinationCommandSecurityProjection struct {
	args    []string
	dir     string
	env     []string
	uid     uint32
	gid     uint32
	groups  []uint32
	setpgid bool
}

func destinationCommandProjection(command *exec.Cmd) destinationCommandSecurityProjection {
	projection := destinationCommandSecurityProjection{
		args: append([]string(nil), command.Args...),
		dir:  command.Dir,
		env:  append([]string(nil), command.Env...),
	}
	if command.SysProcAttr != nil {
		projection.setpgid = command.SysProcAttr.Setpgid
		if command.SysProcAttr.Credential != nil {
			projection.uid = command.SysProcAttr.Credential.Uid
			projection.gid = command.SysProcAttr.Credential.Gid
			projection.groups = append([]uint32(nil), command.SysProcAttr.Credential.Groups...)
		}
	}
	return projection
}

func TestDestinationAccountResolverWrapsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := (systemDestinationAccountResolver{}).Resolve(ctx, "builder")

	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	var domainErr *errs.DomainError
	require.ErrorAs(t, err, &domainErr)
	assert.Equal(t, errs.CodeProcessError, domainErr.Code)
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
