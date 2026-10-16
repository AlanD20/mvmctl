package agent

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"mvmctl/pkg/errs"
)

const destinationProcessWaitDelay = 2 * time.Second

type destinationAccount struct {
	username string
	homeDir  string
	uid      uint32
	gid      uint32
	groups   []uint32
}

type destinationAccountResolver interface {
	Resolve(ctx context.Context, targetUser string) (destinationAccount, error)
}

type systemDestinationAccountResolver struct{}

func (systemDestinationAccountResolver) Resolve(
	ctx context.Context,
	targetUser string,
) (destinationAccount, error) {
	if err := ctx.Err(); err != nil {
		return destinationAccount{}, errs.WrapMsg(
			errs.CodeProcessError,
			"resolve VM-exec target account",
			err,
			errs.WithClass(errs.ClassInternal),
		)
	}
	account, err := user.Lookup(targetUser)
	if err != nil {
		return destinationAccount{}, errs.New(errs.CodeProcessError, "resolve VM-exec target account")
	}
	groupIDs, err := account.GroupIds()
	if err != nil {
		return destinationAccount{}, errs.New(errs.CodeProcessError, "resolve VM-exec target groups")
	}
	if err := ctx.Err(); err != nil {
		return destinationAccount{}, errs.WrapMsg(
			errs.CodeProcessError,
			"resolve VM-exec target account",
			err,
			errs.WithClass(errs.ClassInternal),
		)
	}
	return destinationAccountFromUser(targetUser, account, groupIDs)
}

func destinationAccountFromUser(
	targetUser string,
	account *user.User,
	groupIDs []string,
) (destinationAccount, error) {
	if account == nil || account.Username != targetUser {
		return destinationAccount{}, errs.New(errs.CodeProcessError, "resolve exact VM-exec target account")
	}
	uid, err := parseDestinationID(account.Uid)
	if err != nil || uid == 0 {
		return destinationAccount{}, errs.New(errs.CodeProcessError, "VM-exec target account must have a nonzero UID")
	}
	gid, err := parseDestinationID(account.Gid)
	if err != nil {
		return destinationAccount{}, errs.New(errs.CodeProcessError, "resolve VM-exec target primary group")
	}
	if account.HomeDir == "" || strings.ContainsRune(account.HomeDir, 0) ||
		!filepath.IsAbs(account.HomeDir) || filepath.Clean(account.HomeDir) != account.HomeDir {
		return destinationAccount{}, errs.New(errs.CodeProcessError, "resolve VM-exec target home")
	}

	groups := make([]uint32, 0, len(groupIDs))
	seen := make(map[uint32]struct{}, len(groupIDs))
	for _, rawGroupID := range groupIDs {
		groupID, parseErr := parseDestinationID(rawGroupID)
		if parseErr != nil {
			return destinationAccount{}, errs.New(errs.CodeProcessError, "resolve VM-exec supplementary groups")
		}
		if _, exists := seen[groupID]; exists {
			continue
		}
		seen[groupID] = struct{}{}
		groups = append(groups, groupID)
	}
	return destinationAccount{
		username: account.Username,
		homeDir:  account.HomeDir,
		uid:      uid,
		gid:      gid,
		groups:   groups,
	}, nil
}

func parseDestinationID(value string) (uint32, error) {
	parsed, err := strconv.ParseUint(value, 10, 32)
	return uint32(parsed), err
}

type systemDestinationExecution struct {
	accounts      destinationAccountResolver
	processes     destinationProcessRunner
	processGroups destinationProcessGroupKiller
}

type destinationProcessRunner interface {
	Run(command *exec.Cmd) error
}

type systemDestinationProcessRunner struct{}

func (systemDestinationProcessRunner) Run(command *exec.Cmd) error {
	return command.Run()
}

type destinationProcessGroupKiller interface {
	Kill(pid int, signal unix.Signal) error
}

type systemDestinationProcessGroupKiller struct{}

func (systemDestinationProcessGroupKiller) Kill(pid int, signal unix.Signal) error {
	return unix.Kill(pid, signal)
}

func newDestinationExecutionEffect() destinationExecutionEffect {
	return &systemDestinationExecution{
		accounts:      systemDestinationAccountResolver{},
		processes:     systemDestinationProcessRunner{},
		processGroups: systemDestinationProcessGroupKiller{},
	}
}

func (effect *systemDestinationExecution) Execute(
	ctx context.Context,
	targetUser string,
	command string,
	stdout io.Writer,
	stderr io.Writer,
) (int, error) {
	account, err := effect.accounts.Resolve(ctx, targetUser)
	if err != nil {
		return -1, err
	}
	process := newDestinationCommand(ctx, account, command, stdout, stderr, effect.processGroupKiller())
	if err := effect.processRunner().Run(process); err != nil {
		var exitError interface{ ExitCode() int }
		if errors.As(err, &exitError) {
			exitCode := exitError.ExitCode()
			if exitCode >= 0 && exitCode <= 255 {
				return exitCode, nil
			}
		}
		return -1, errs.New(errs.CodeProcessError, "VM-exec destination command failed")
	}
	return 0, nil
}

func (effect *systemDestinationExecution) processRunner() destinationProcessRunner {
	if effect.processes == nil {
		return systemDestinationProcessRunner{}
	}
	return effect.processes
}

func (effect *systemDestinationExecution) processGroupKiller() destinationProcessGroupKiller {
	if effect.processGroups == nil {
		return systemDestinationProcessGroupKiller{}
	}
	return effect.processGroups
}

func newDestinationCommand(
	ctx context.Context,
	account destinationAccount,
	command string,
	stdout io.Writer,
	stderr io.Writer,
	processGroups destinationProcessGroupKiller,
) *exec.Cmd {
	// SECURITY: The destination account is resolved to an exact nonzero numeric credential before this boundary.
	// The caller controls only the shell command; environment, working directory, credentials, and process-group
	// cancellation are fixed here.
	process := exec.CommandContext(ctx, "/bin/sh", "-c", command)
	process.Dir = account.homeDir
	process.Env = []string{
		"HOME=" + account.homeDir,
		"LOGNAME=" + account.username,
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"SHELL=/bin/sh",
		"USER=" + account.username,
	}
	process.Stdout = stdout
	process.Stderr = stderr
	process.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
		Credential: &syscall.Credential{
			Uid:    account.uid,
			Gid:    account.gid,
			Groups: append([]uint32(nil), account.groups...),
		},
	}
	process.Cancel = func() error {
		if process.Process == nil {
			return os.ErrProcessDone
		}
		if err := processGroups.Kill(-process.Process.Pid, unix.SIGKILL); err != nil {
			if errors.Is(err, unix.ESRCH) {
				return os.ErrProcessDone
			}
			return err
		}
		return nil
	}
	process.WaitDelay = destinationProcessWaitDelay
	return process
}
