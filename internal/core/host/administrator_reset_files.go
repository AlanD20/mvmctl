package host

import (
	"context"
	"errors"
	"os"
	"time"

	"golang.org/x/sys/unix"

	"mvmctl/internal/infra"
	"mvmctl/internal/lib/system"
)

const resetAppliedSysctl = "net.ipv4.ip_forward = 1\n"

type resetFiles struct {
	sysctl  *os.File
	sudoers *os.File
}

func openResetFiles(ctx context.Context, journal *administratorJournal) (_ *resetFiles, returnErr error) {
	files := &resetFiles{}
	defer func() {
		if returnErr != nil {
			returnErr = errors.Join(returnErr, files.close())
		}
	}()
	var err error
	files.sysctl, err = openAdministratorDirectory(ctx, []string{"etc", "sysctl.d"}, -1)
	if err != nil {
		return nil, err
	}
	files.sudoers, err = openAdministratorDirectory(ctx, []string{"etc", "sudoers.d"}, -1)
	if err != nil {
		return nil, err
	}
	if err := admitResetFile(
		files.sysctl,
		"mvmctl.conf",
		0644,
		resetAppliedSysctl,
		journal.OriginalSysctl,
	); err != nil {
		return nil, err
	}
	if err := admitResetFile(
		files.sudoers,
		"mvm",
		infra.SudoersPerm,
		journal.AppliedSudoers,
		journal.OriginalSudoers,
	); err != nil {
		return nil, err
	}
	// CRITICAL: transitional grants can bypass admission. Neither root identity
	// nor the global gate authorizes reset while those raw grants remain active.
	data, err := readAdministratorFile(files.sudoers, "mvm", infra.SudoersPerm)
	if errors.Is(err, unix.ENOENT) && journal.ResetState == "restoring" &&
		journal.ResetStep == 3 && journal.OriginalSudoers == nil {
		return files, nil
	}
	if err != nil {
		return nil, err
	}
	if err := requireMarkerOnlyResetPolicy(string(data)); err != nil {
		return nil, err
	}
	if journal.OriginalSudoers != nil {
		if err := requireMarkerOnlyResetPolicy(*journal.OriginalSudoers); err != nil {
			return nil, err
		}
	}
	forward, err := GetIPForwardStatus(ctx)
	if err != nil {
		return nil, err
	}
	if forward != "1" && forward != journal.OriginalForward {
		return nil, errors.New("IPv4 forwarding changed after initialization")
	}
	return files, nil
}

func (files *resetFiles) close() error {
	var result error
	if files.sudoers != nil {
		result = errors.Join(result, files.sudoers.Close())
		files.sudoers = nil
	}
	if files.sysctl != nil {
		result = errors.Join(result, files.sysctl.Close())
		files.sysctl = nil
	}
	return result
}

func admitResetFile(dir *os.File, name string, mode uint32, applied string, original *string) error {
	data, err := readAdministratorFile(dir, name, mode)
	if errors.Is(err, unix.ENOENT) && original == nil {
		return nil
	}
	if err != nil {
		return err
	}
	if string(data) != applied && (original == nil || string(data) != *original) {
		return errors.New("managed host file differs from both recorded applied and original contents")
	}
	return nil
}

func restoreResetFile(
	ctx context.Context, dir *os.File, name string, mode uint32, applied string, original *string,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := admitResetFile(dir, name, mode, applied, original); err != nil {
		return err
	}
	data, err := readAdministratorFile(dir, name, mode)
	if errors.Is(err, unix.ENOENT) && original == nil {
		return dir.Sync()
	}
	if err != nil {
		return err
	}
	if original != nil {
		if string(data) == *original {
			return dir.Sync()
		}
		return writeAdministratorFile(ctx, dir, name, []byte(*original), mode)
	}
	if err := unix.Unlinkat(int(dir.Fd()), name, 0); err != nil {
		return err
	}
	return dir.Sync()
}

func (files *resetFiles) restoreSysctl(ctx context.Context, journal *administratorJournal) error {
	current, err := GetIPForwardStatus(ctx)
	if err != nil {
		return err
	}
	if current != journal.OriginalForward {
		if current != "1" {
			return errors.New("IPv4 forwarding changed before restoration")
		}
		if err := runAdministratorCommand(
			ctx,
			[]string{"/usr/sbin/sysctl", "-w", sysctlKey + "=" + journal.OriginalForward},
		); err != nil {
			return err
		}
	}
	return restoreResetFile(ctx, files.sysctl, "mvmctl.conf", 0644, resetAppliedSysctl, journal.OriginalSysctl)
}

func (files *resetFiles) restoreSudoers(ctx context.Context, journal *administratorJournal) error {
	if journal.OriginalSudoers != nil {
		result, err := system.DefaultRunner.Run(ctx, []string{"/usr/sbin/visudo", "-c", "-f", "-"}, system.RunCmdOpts{
			Check:          true,
			Capture:        true,
			Input:          *journal.OriginalSudoers,
			Timeout:        15 * time.Second,
			MaxStdoutBytes: 4096,
			MaxStderrBytes: 4096,
			ReplaceEnv:     true,
			Env: map[string]string{
				"PATH":   "/usr/sbin:/usr/bin:/sbin:/bin",
				"HOME":   "/root",
				"LANG":   "C",
				"LC_ALL": "C",
			},
		})
		if err != nil {
			return err
		}
		if result == nil || !result.Success() {
			return errors.New("original sudoers did not pass validation")
		}
	}
	return restoreResetFile(
		ctx,
		files.sudoers,
		"mvm",
		infra.SudoersPerm,
		journal.AppliedSudoers,
		journal.OriginalSudoers,
	)
}
