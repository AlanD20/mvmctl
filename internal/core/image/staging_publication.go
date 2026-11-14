package image

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"

	"mvmctl/internal/lib/crypto"
	"mvmctl/pkg/errs"
)

func (stage *stagedImage) publishRecoveredImage(ctx context.Context) error {
	record := stage.recovery
	name := filepath.Base(stage.sourcePath)
	current, err := stage.publicationIdentity(name)
	if err != nil {
		return err
	}
	if record.Phase == stagedPhasePublishing || record.Phase == stagedPhasePublished {
		if record.Publication.samePublishedFile(current) {
			if err := syncStagedDirectory(ctx, stage.publication); err != nil {
				return err
			}
			record.Phase = stagedPhasePublished
			return stage.saveRecovery(ctx)
		}
		if record.Phase == stagedPhasePublished {
			return errs.New(errs.CodeImageError, "published image was replaced after recorded completion")
		}
	}
	if current != record.Source {
		return errs.New(errs.CodeImageError, "original image changed before optimization publication")
	}
	if record.Phase == stagedPhaseReady {
		if err := stage.preparePublication(ctx); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	prepared, err := stage.publicationIdentity(record.PublicationLeaf)
	if err != nil || !record.Publication.samePublishedFile(prepared) {
		return errs.WrapMsg(errs.CodeImageError, "prepared publication candidate changed", err)
	}
	current, err = stage.publicationIdentity(name)
	if err != nil || current != record.Source {
		return errs.WrapMsg(errs.CodeImageError, "original image changed during publication preparation", err)
	}
	// Exchange retains the original under the already-recorded temporary name until all
	// publication durability steps succeed. An error after rename cannot discard its bytes.
	directory, err := stage.publication.Open(".")
	if err != nil {
		return errs.WrapMsg(errs.CodeImageError, "open image publication exchange directory", err)
	}
	exchangeErr := unix.Renameat2(
		int(directory.Fd()), record.PublicationLeaf, int(directory.Fd()), name, unix.RENAME_EXCHANGE,
	)
	if err := errors.Join(exchangeErr, directory.Close()); err != nil {
		return errs.WrapMsg(errs.CodeImageError, "publish completed image optimization", err)
	}
	if err := syncStagedDirectory(ctx, stage.publication); err != nil {
		return err
	}
	record.Phase = stagedPhasePublished
	return stage.saveRecovery(ctx)
}

func (stage *stagedImage) removePublishedOriginal(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	record := stage.recovery
	if record == nil || record.Phase != stagedPhasePublished {
		return nil
	}
	current, err := stage.publicationIdentity(filepath.Base(stage.sourcePath))
	if err != nil || !record.Publication.samePublishedFile(current) {
		return errs.WrapMsg(errs.CodeImageError, "published image changed before original cleanup", err)
	}
	original, err := stage.publicationIdentity(record.PublicationLeaf)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !record.Source.samePublishedFile(original) {
		return errs.WrapMsg(errs.CodeImageError, "retained publication original identity changed", err)
	}
	if err := stage.publication.Remove(record.PublicationLeaf); err != nil {
		return errs.WrapMsg(errs.CodeImageError, "remove durably replaced image original", err)
	}
	return syncStagedDirectory(ctx, stage.publication)
}

func (stage *stagedImage) publicationIdentity(name string) (stagedImageIdentity, error) {
	info, err := stage.publication.Lstat(name)
	if err != nil {
		return stagedImageIdentity{}, errs.WrapMsg(errs.CodeImageError, "inspect image publication identity", err)
	}
	return stagedInfoIdentity(info)
}

func (stage *stagedImage) preparePublication(ctx context.Context) (returnErr error) {
	record := stage.recovery
	result, err := stage.root.OpenFile("rootfs.img", os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return errs.WrapMsg(errs.CodeImageError, "open completed image optimization result", err)
	}
	defer func() { returnErr = errors.Join(returnErr, result.Close()) }()
	before, err := stagedFileIdentity(result)
	if err != nil || before != *record.ResultRootfs {
		return errs.WrapMsg(errs.CodeImageError, "completed image result changed before publication copy", err)
	}
	temporary := ".mvm-image-" + crypto.UUIDV4()
	target, err := stage.publication.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_RDWR|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return errs.WrapMsg(errs.CodeImageError, "create image publication candidate", err)
	}
	initial, initialErr := stagedFileIdentity(target)
	retained := false
	defer func() {
		if retained || initialErr != nil {
			return
		}
		current, err := stage.publicationIdentity(temporary)
		if err != nil {
			returnErr = errors.Join(returnErr, err)
			return
		}
		if !initial.sameNode(current) || current.Links != 1 {
			returnErr = errors.Join(returnErr,
				errs.New(errs.CodeImageError, "failed publication candidate identity changed; retained"))
			return
		}
		returnErr = errors.Join(returnErr, stage.publication.Remove(temporary))
	}()
	if initialErr != nil {
		return errors.Join(initialErr, target.Close())
	}
	copyErr := copyInspectionImage(ctx, result, target, int64(record.Result.ImageBytes))
	identity, identityErr := stagedFileIdentity(target)
	if err := errors.Join(copyErr, identityErr, target.Close()); err != nil {
		return errs.WrapMsg(errs.CodeImageError, "copy completed image optimization result", err)
	}
	after, err := stagedFileIdentity(result)
	if err != nil || after != before {
		return errs.WrapMsg(errs.CodeImageError, "completed image result changed during publication copy", err)
	}
	if err := syncStagedDirectory(ctx, stage.publication); err != nil {
		return err
	}
	record.PublicationLeaf = temporary
	record.Publication = &identity
	record.Phase = stagedPhasePublishing
	// Once record publication may have begun, keep the exact candidate even when fsync fails.
	// A retry can then reconcile either durable manifest state without guessing another file.
	retained = true
	return stage.saveRecovery(ctx)
}
