package host

import (
	"context"
	"errors"
	"os"
	"os/user"
	"slices"
	"time"

	"golang.org/x/sys/unix"

	"mvmctl/internal/infra"
	"mvmctl/internal/lib/hostadmission"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/lib/system"
	"mvmctl/pkg/errs"
)

// AdministratorInitResult is the closed result of administrator host setup.
type AdministratorInitResult struct {
	Changes          []*model.HostStateChangeItem `json:"changes"`
	UserAddedToGroup bool                         `json:"user_added_to_group"`
	SessionHasGroup  bool                         `json:"session_has_group"`
}

// AdministratorInit performs only compiled host mutations using root-owned
// journal authority. It never constructs a user database or recursively chowns a cache.
// CRITICAL: operator is admitted before environment sanitization by the early entry.
func AdministratorInit(
	ctx context.Context,
	operator AdministratorOperator,
) (result *AdministratorInitResult, returnErr error) {
	if os.Geteuid() != 0 || operator.uid == "" || operator.name == "" {
		return nil, errs.New(errs.CodePrivilegeRequired, "administrator operator admission is required")
	}
	if err := verifyCanonicalSystemBinaryForSudoers(); err != nil {
		return nil, err
	}
	if err := guardAdministratorSudo(ctx); err != nil {
		return nil, err
	}
	admission, err := hostadmission.AcquireAdministrator(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { returnErr = administratorCloseError(returnErr, admission.Release(context.WithoutCancel(ctx))) }()
	dir, err := openAdministratorDirectory(ctx, []string{"var", "lib", "mvmctl", "host"}, -1)
	if err != nil {
		return nil, administratorFailure("open root-owned administrator state", err)
	}
	defer func() { returnErr = administratorCloseError(returnErr, dir.Close()) }()
	journal, err := loadAdministratorJournal(dir)
	if err != nil {
		return nil, administratorFailure("read administrator journal", err)
	}
	if journal != nil && journal.ResetState == "restoring" {
		return nil, errs.New(errs.CodeHostInitFailed, "complete the interrupted host reset before initializing")
	}
	if journal != nil {
		if err := validateSystemBinaryInstallSudoers(journal.AppliedSudoers); err != nil {
			return nil, errs.WrapMsg(errs.CodeHostInitFailed,
				"administrator journal records an incompatible sudoers policy; clean installation is required", err)
		}
	}
	if journal == nil || journal.ResetState == "complete" {
		journal, err = prepareAdministratorJournal(ctx, operator)
		if err != nil {
			return nil, administratorFailure("snapshot administrator state", err)
		}
		if err := saveAdministratorJournal(ctx, dir, journal); err != nil {
			return nil, administratorFailure("persist administrator pre-mutation state", err)
		}
	}
	result = &AdministratorInitResult{Changes: []*model.HostStateChangeItem{}}
	if journal.Initialized {
		group, err := user.LookupGroup(infra.MVMUnixGroup)
		if err != nil || journal.GroupGID == nil || group.Gid != *journal.GroupGID {
			return result, administratorFailure("initialized operator group identity changed", err)
		}
		result, err = enrollAdministratorOperator(ctx, dir, operator, journal, result)
		if err != nil {
			return result, err
		}
		return result, admission.Enable(ctx)
	}
	if journal.Operator != operator.name || journal.UID != operator.uid || journal.GID != operator.gid {
		return nil, errs.New(
			errs.CodeHostInitFailed,
			"complete the interrupted global host initialization with its original operator before enrolling another account",
		)
	}
	// A durable pending step survives command failure and interruption. Retrying
	// converges through idempotent operations without replacing the original snapshot.
	for journal.Completed < 6 {
		if journal.Completed == 0 && journal.Pending && journal.GroupGID == nil {
			if _, err := user.LookupGroup(infra.MVMUnixGroup); err == nil {
				return result, errs.New(errs.CodeHostInitFailed,
					"interrupted group creation has no committed identity; refusing to adopt an existing group")
			}
		}
		journal.Pending = true
		if err := saveAdministratorJournal(ctx, dir, journal); err != nil {
			return result, administratorFailure("persist administrator mutation intent", err)
		}
		changes, err := applyAdministratorStep(ctx, operator, journal.Completed)
		result.Changes = append(result.Changes, changes...)
		if err != nil {
			return result, administratorFailure("apply administrator mutation", err)
		}
		if journal.Completed == 0 {
			group, err := user.LookupGroup(infra.MVMUnixGroup)
			if err != nil {
				return result, administratorFailure("record initialized group identity", err)
			}
			if journal.GroupGID != nil && *journal.GroupGID != group.Gid {
				return result, errs.New(
					errs.CodeHostInitFailed,
					"operator group identity changed during initialization",
				)
			}
			gid := group.Gid
			journal.GroupGID = &gid
		}
		journal.Completed++
		journal.Pending = false
		journal.Initialized = journal.Completed == 6
		if err := saveAdministratorJournal(context.WithoutCancel(ctx), dir, journal); err != nil {
			return result, administratorFailure("persist administrator mutation outcome", err)
		}
	}
	result.UserAddedToGroup = !journal.MemberExisted
	result, err = enrollAdministratorOperator(ctx, dir, operator, journal, result)
	if err != nil {
		return result, err
	}
	return result, admission.Enable(ctx)
}

func prepareAdministratorJournal(ctx context.Context, operator AdministratorOperator) (*administratorJournal, error) {
	forward, err := GetIPForwardStatus(ctx)
	if err != nil {
		return nil, err
	}
	if forward != "0" && forward != "1" {
		return nil, errors.New("invalid current IPv4 forwarding value")
	}
	dir, err := openAdministratorDirectory(ctx, []string{"etc", "sysctl.d"}, -1)
	if err != nil {
		return nil, err
	}
	data, readErr := readAdministratorFile(dir, "mvmctl.conf", 0644)
	closeErr := dir.Close()
	if readErr != nil && !errors.Is(readErr, unix.ENOENT) {
		return nil, errors.Join(readErr, closeErr)
	}
	if closeErr != nil {
		return nil, closeErr
	}
	var original *string
	if readErr == nil {
		value := string(data)
		original = &value
	}
	sudoersDir, err := openAdministratorDirectory(ctx, []string{"etc", "sudoers.d"}, -1)
	if err != nil {
		return nil, err
	}
	sudoersData, sudoersErr := readAdministratorFile(sudoersDir, "mvm", infra.SudoersPerm)
	sudoersCloseErr := sudoersDir.Close()
	if sudoersErr != nil && !errors.Is(sudoersErr, unix.ENOENT) {
		return nil, errors.Join(sudoersErr, sudoersCloseErr)
	}
	if sudoersCloseErr != nil {
		return nil, sudoersCloseErr
	}
	var originalSudoers *string
	if sudoersErr == nil {
		value := string(sudoersData)
		originalSudoers = &value
	}
	groupExisted, memberExisted, err := administratorMembership(operator)
	if err != nil {
		return nil, err
	}
	var groupGID *string
	if groupExisted {
		group, err := user.LookupGroup(infra.MVMUnixGroup)
		if err != nil {
			return nil, err
		}
		gid := group.Gid
		groupGID = &gid
	}
	return &administratorJournal{
		Version: 2, Operator: operator.name, UID: operator.uid, GID: operator.gid,
		OriginalForward: forward, OriginalSysctl: original, OriginalSudoers: originalSudoers,
		GroupExisted: groupExisted, MemberExisted: memberExisted, GroupGID: groupGID,
		AppliedSudoers: GenerateSudoersContent(infra.MVMUnixGroup),
	}, nil
}

func administratorMembership(operator AdministratorOperator) (bool, bool, error) {
	group, err := user.LookupGroup(infra.MVMUnixGroup)
	if err != nil {
		var unknown user.UnknownGroupError
		if errors.As(err, &unknown) {
			return false, false, nil
		}
		return false, false, err
	}
	account, err := user.LookupId(operator.uid)
	if err != nil {
		return false, false, err
	}
	if account.Username != operator.name || account.Gid != operator.gid {
		return false, false, errors.New("operator identity changed")
	}
	groups, err := account.GroupIds()
	if err != nil {
		return false, false, err
	}
	return true, slices.Contains(groups, group.Gid), nil
}

func applyAdministratorStep(
	ctx context.Context,
	operator AdministratorOperator,
	step int,
) ([]*model.HostStateChangeItem, error) {
	var changes []*model.HostStateChangeItem
	switch step {
	case 0, 1:
		groupExists, memberExists, err := administratorMembership(operator)
		if err != nil {
			return nil, err
		}
		if step == 0 {
			if groupExists {
				return changes, nil
			}
			err = runAdministratorCommand(ctx, []string{"/usr/sbin/groupadd", "--system", infra.MVMUnixGroup})
			if err == nil {
				changes = append(changes, &model.HostStateChangeItem{
					Setting: "group:" + infra.MVMUnixGroup, Mechanism: "groupadd", AppliedValue: infra.MVMUnixGroup,
				})
			}
		} else {
			if !groupExists {
				return nil, errors.New("operator group disappeared")
			}
			if memberExists {
				return changes, nil
			}
			err = runAdministratorCommand(ctx, []string{"/usr/sbin/usermod", "-aG", infra.MVMUnixGroup, operator.name})
			if err == nil {
				changes = append(changes, &model.HostStateChangeItem{
					Setting: "group_member:" + operator.name, Mechanism: "usermod",
					AppliedValue: operator.name + ":" + infra.MVMUnixGroup,
				})
			}
		}
		return changes, err
	case 2:
		change, err := EnableIPForward(ctx)
		if change != nil {
			changes = append(changes, change)
		}
		return changes, err
	case 3:
		dir, err := openAdministratorDirectory(ctx, []string{"etc", "sysctl.d"}, -1)
		if err != nil {
			return nil, err
		}
		data, readErr := readAdministratorFile(dir, "mvmctl.conf", 0644)
		if readErr != nil && !errors.Is(readErr, unix.ENOENT) {
			return nil, errors.Join(readErr, dir.Close())
		}
		content := []byte("net.ipv4.ip_forward = 1\n")
		if string(data) == string(content) {
			return changes, dir.Close()
		}
		err = writeAdministratorFile(ctx, dir, "mvmctl.conf", content, 0644)
		err = errors.Join(err, dir.Close())
		if err == nil {
			changes = append(changes, &model.HostStateChangeItem{
				Setting: "sysctl_persist_file", Mechanism: "file_create", AppliedValue: infra.DefaultSysctlConfPath,
			})
		}
		return changes, err
	case 4:
		changes, _, err := NewService(nil).EnsureKVMModules(ctx, "", 0)
		return changes, err
	case 5:
		changed, err := NewService(nil).ConfigureSudoers(ctx)
		if changed {
			changes = append(changes, &model.HostStateChangeItem{
				Setting: "sudoers_dropin", Mechanism: "file_create", AppliedValue: infra.SudoersDropInPath(),
			})
		}
		return changes, err
	default:
		return nil, errors.New("unknown administrator mutation step")
	}
}

func runAdministratorCommand(ctx context.Context, argv []string) error {
	result, err := system.DefaultRunner.Run(ctx, argv, system.RunCmdOpts{
		Check: true, Capture: true, Timeout: 30 * time.Second,
		MaxStdoutBytes: 4096, MaxStderrBytes: 4096, ReplaceEnv: true,
		Env: map[string]string{"PATH": "/usr/sbin:/usr/bin:/sbin:/bin", "HOME": "/root", "LANG": "C", "LC_ALL": "C"},
	})
	if err != nil {
		return err
	}
	if result == nil || !result.Success() {
		return errors.New("administrator command did not succeed")
	}
	return nil
}

func guardAdministratorSudo(ctx context.Context) (returnErr error) {
	dir, err := openAdministratorDirectory(ctx, []string{"etc", "sudoers.d"}, -1)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, dir.Close()) }()
	data, err := readAdministratorFile(dir, "mvm", infra.SudoersPerm)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return err
	}
	return validateSystemBinaryInstallSudoers(string(data))
}

func administratorFailure(operation string, cause error) error {
	return errs.WrapMsg(errs.CodeHostInitFailed, operation, cause,
		errs.WithDetails(map[string]any{"journal_retained": true}))
}

func administratorCloseError(primary, closeErr error) error {
	if closeErr == nil {
		return primary
	}
	return administratorFailure("close administrator state", errors.Join(primary, closeErr))
}
