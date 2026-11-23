package image

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/unix"

	"mvmctl/internal/lib/system"
	"mvmctl/pkg/errs"
)

func (stage *stagedImage) prepareInspectionSource(
	ctx context.Context,
	source imageInspectionSource,
	fsType string,
) error {
	switch source {
	case imageInspectionOrdinary:
		return nil
	case imageInspectionVMRootfs:
		if fsType != "ext4" {
			return nil
		}
	default:
		return errs.New(errs.CodeImageError, "unknown image inspection source")
	}
	return stage.replayVMInspectionJournal(ctx)
}

func (stage *stagedImage) replayVMInspectionJournal(ctx context.Context) (returnErr error) {
	if err := ctx.Err(); err != nil {
		return errs.WrapMsg(errs.CodeImageError, "prepare VM inspection journal", err)
	}
	// Never mutate a retained optimization transaction or an adopted staging directory.
	if !stage.directoryMade || !stage.rootfsMade || stage.recovery != nil || !stage.cleanupKnown {
		return errs.New(errs.CodeImageError, "VM journal preparation requires a new owned inspection copy")
	}
	file, err := stage.root.OpenFile("rootfs.img", os.O_RDWR|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return errs.WrapMsg(errs.CodeImageError, "open VM inspection journal copy", err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			returnErr = errors.Join(
				returnErr,
				errs.WrapMsg(errs.CodeImageError, "close VM inspection journal copy", err),
			)
		}
	}()
	before, err := stage.vmJournalIdentity(file)
	if err != nil || before != stage.rootfsIdentity {
		stage.cleanupKnown = false
		return errs.WrapMsg(errs.CodeImageError, "VM inspection copy changed before journal replay", err)
	}
	// DefaultRunner waits for the child before returning, including cancellation. The proc
	// reference retains this exact inode without giving the child an operator pathname.
	result, runErr := system.DefaultRunner.Run(ctx, []string{
		"/usr/sbin/e2fsck", "-p", "-E", "journal_only",
		fmt.Sprintf("/proc/%d/fd/%d", os.Getpid(), file.Fd()),
	}, system.RunCmdOpts{
		Capture: true, Check: false, Cwd: "/", Timeout: 5 * time.Minute,
		Env: map[string]string{"LANG": "C", "LC_ALL": "C"}, ReplaceEnv: true,
		MaxStdoutBytes: 4096, MaxStderrBytes: 4096,
	})
	// A failed or canceled tool may already have replayed journal blocks. Refresh only the
	// admitted node's metadata so exact cleanup remains available; never adopt a replacement.
	after, identityErr := stage.vmJournalIdentity(file)
	if identityErr != nil || !before.sameNode(after) || before.Size != after.Size || !after.callerFile() {
		stage.cleanupKnown = false
		return errs.WrapMsg(errs.CodeImageError, "VM inspection copy changed during journal replay",
			errors.Join(runErr, identityErr, ctx.Err()))
	}
	stage.rootfsIdentity = after
	if runErr != nil || ctx.Err() != nil || result == nil || (result.ExitCode != 0 && result.ExitCode != 1) {
		return errs.WrapMsg(errs.CodeImageError, "VM inspection journal replay did not complete successfully",
			errors.Join(runErr, ctx.Err()))
	}
	if err := file.Sync(); err != nil {
		return errs.WrapMsg(errs.CodeImageError, "sync VM inspection journal copy", err)
	}
	return nil
}

func (stage *stagedImage) vmJournalIdentity(file *os.File) (stagedImageIdentity, error) {
	identity, err := stagedFileIdentity(file)
	if err != nil {
		return stagedImageIdentity{}, err
	}
	info, err := stage.root.Lstat("rootfs.img")
	if err != nil {
		return stagedImageIdentity{}, err
	}
	leaf, err := stagedInfoIdentity(info)
	if err != nil || leaf != identity || !identity.callerFile() || identity.Size != stage.sourceInfo.Size() {
		return stagedImageIdentity{}, errs.New(errs.CodeImageError, "VM inspection journal copy identity is not exact")
	}
	return identity, nil
}
