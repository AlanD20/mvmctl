package image

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"

	"mvmctl/internal/infra"
	loopmountsvc "mvmctl/internal/service/loopmount"
	"mvmctl/pkg/errs"
)

// inspectStagedImage keeps caller paths in the normal process. The receiver sees only
// the canonical staging resource identity and independently admits its fixed rootfs leaf.
func inspectStagedImage(
	ctx context.Context,
	imagePath, imageID, fsType string,
) (osType string, err error) {
	return inspectStagedImageSource(ctx, imagePath, imageID, fsType, imageInspectionOrdinary)
}

func inspectVMRootfsStagedImage(
	ctx context.Context,
	imagePath, imageID, fsType string,
) (osType string, err error) {
	return inspectStagedImageSource(ctx, imagePath, imageID, fsType, imageInspectionVMRootfs)
}

type imageInspectionSource uint8

const (
	imageInspectionOrdinary imageInspectionSource = iota
	imageInspectionVMRootfs
)

func inspectStagedImageSource(
	ctx context.Context,
	imagePath, imageID, fsType string,
	source imageInspectionSource,
) (osType string, err error) {
	stage, err := openStagedImage(ctx, imagePath, imageID, fsType, true)
	if err != nil {
		return "", err
	}
	completed := false
	preparationFailed := false
	defer func() { err = errors.Join(err, stage.close(completed || preparationFailed)) }()
	if stage.recovery != nil {
		// This hint selects the same requested deblob policy on retry. It never establishes
		// completion: optimization still replays the exact request against root's own receipt.
		return stage.recovery.OSType, nil
	}
	if err := stage.prepareInspectionSource(ctx, source, fsType); err != nil {
		preparationFailed = true
		return "", err
	}
	stage.cleanupKnown = false
	result, err := loopmountsvc.NewClient().InspectImage(ctx, stage.request)
	if err != nil {
		return "", err
	}
	stage.cleanupKnown = true
	completed = true
	return result.OSType, nil
}

type stagedImage struct {
	request           loopmountsvc.ImageInspectRequest
	source            *os.File
	sourceInfo        os.FileInfo
	sourcePath        string
	cache             *os.Root
	parent            *os.Root
	root              *os.Root
	publication       *os.Root
	lock              *os.File
	recovery          *stagedImageRecovery
	cleanupKnown      bool
	callerCleanupOnly bool
	manifestMade      bool
	lockMade          bool
	rootfsIdentity    stagedImageIdentity
	manifestIdentity  stagedImageIdentity
	directoryIdentity stagedImageIdentity
	directoryMade     bool
	rootfsMade        bool
}

func prepareStagedImage(ctx context.Context, imagePath, imageID, fsType string) (_ *stagedImage, err error) {
	return openStagedImage(ctx, imagePath, imageID, fsType, false)
}

func openStagedImage(
	ctx context.Context,
	imagePath, imageID, fsType string,
	allowRecovery bool,
) (_ *stagedImage, err error) {
	canonical, err := filepath.Abs(imagePath)
	if err != nil {
		return nil, errs.WrapMsg(errs.CodeImageError, "resolve image staging source", err)
	}
	stage := &stagedImage{sourcePath: canonical, cleanupKnown: true}
	defer func() {
		if err != nil {
			err = errors.Join(err, stage.close(true))
		}
		var domainErr *errs.DomainError
		if err != nil && !errors.As(err, &domainErr) {
			err = errs.WrapMsg(errs.CodeImageError, "clean up image staging inspection", err)
		}
	}()
	cacheDir, err := infra.GetCacheDir()
	if err != nil {
		return nil, errs.WrapMsg(errs.CodeImageError, "resolve image staging cache", err)
	}
	publication, err := os.OpenRoot(filepath.Dir(canonical))
	if err != nil {
		return nil, errs.WrapMsg(errs.CodeImageError, "open original image directory", err)
	}
	stage.publication = publication
	source, err := publication.OpenFile(filepath.Base(canonical), os.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, errs.WrapMsg(errs.CodeImageError, "open image for staging", err)
	}
	stage.source = source
	info, err := source.Stat()
	if err != nil {
		return nil, errs.WrapMsg(errs.CodeImageError, "inspect image staging source", err)
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 {
		return nil, errs.New(errs.CodeImageError, "image staging requires a nonempty regular file")
	}
	stage.sourceInfo = info
	request := loopmountsvc.ImageInspectRequest{
		CacheLocator:       cacheDir,
		ImageID:            imageID,
		ExpectedImageBytes: uint64(info.Size()),
		Filesystem:         loopmountsvc.Filesystem(fsType),
	}
	if err := request.Validate(); err != nil {
		return nil, err
	}
	stage.request = request
	cache, err := os.OpenRoot(cacheDir)
	if err != nil {
		return nil, errs.WrapMsg(errs.CodeImageError, "open image staging cache", err)
	}
	stage.cache = cache
	if err := cache.MkdirAll("images/staging", 0700); err != nil {
		return nil, errs.WrapMsg(errs.CodeImageError, "create image staging parent", err)
	}
	parent, err := cache.OpenRoot("images/staging")
	if err != nil {
		return nil, errs.WrapMsg(errs.CodeImageError, "open image staging parent", err)
	}
	stage.parent = parent
	if err := parent.Mkdir(imageID, 0700); err != nil {
		if !allowRecovery || !errors.Is(err, os.ErrExist) {
			return nil, errs.WrapMsg(errs.CodeImageError, "create exclusive image staging resource", err)
		}
		stage.cleanupKnown = false
	} else {
		stage.directoryMade = true
	}
	directoryInfo, err := parent.Lstat(imageID)
	if err != nil || directoryInfo == nil || !directoryInfo.IsDir() || directoryInfo.Mode().Perm() != 0700 {
		return nil, errs.WrapMsg(errs.CodeImageError, "image staging directory has unsafe metadata", err)
	}
	root, err := parent.OpenRoot(imageID)
	if err != nil {
		return nil, errs.WrapMsg(errs.CodeImageError, "open image staging resource", err)
	}
	stage.root = root
	stage.directoryIdentity, err = stagedDirectoryIdentity(root)
	if err != nil {
		return nil, err
	}
	if stage.directoryIdentity.UID != uint32(os.Geteuid()) || stage.directoryIdentity.Mode&07777 != 0700 {
		return nil, errs.New(errs.CodeImageError, "image staging directory is not caller-owned and private")
	}
	if err := stage.lockStaging(ctx, stage.directoryMade); err != nil {
		return nil, err
	}
	if !stage.directoryMade {
		if err := stage.loadRecovery(ctx); err != nil {
			return nil, err
		}
		return stage, nil
	}
	target, err := root.OpenFile("rootfs.img", os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return nil, errs.WrapMsg(errs.CodeImageError, "create image staging rootfs", err)
	}
	stage.rootfsMade = true
	stage.rootfsIdentity, err = stagedFileIdentity(target)
	if err != nil {
		return nil, errors.Join(err, target.Close())
	}
	copyErr := copyInspectionImage(ctx, source, target, info.Size())
	if copyErr == nil {
		stage.rootfsIdentity, copyErr = stagedFileIdentity(target)
	}
	closeErr := target.Close()
	if err := errors.Join(copyErr, closeErr); err != nil {
		return nil, errs.WrapMsg(errs.CodeImageError, "populate image staging rootfs", err)
	}
	after, err := source.Stat()
	if err != nil {
		return nil, errs.WrapMsg(errs.CodeImageError, "reinspect image staging source", err)
	}
	beforeIdentity, beforeErr := stagedInfoIdentity(info)
	afterIdentity, afterErr := stagedInfoIdentity(after)
	if beforeErr != nil || afterErr != nil || beforeIdentity != afterIdentity {
		return nil, errs.New(errs.CodeImageError, "image changed while preparing staging inspection")
	}
	if err := syncStagedDirectory(ctx, root); err != nil {
		return nil, err
	}
	if err := syncStagedDirectory(ctx, parent); err != nil {
		return nil, err
	}
	return stage, nil
}

func (stage *stagedImage) close(remove bool) error {
	var err error
	remove = remove && (stage.cleanupKnown || stage.callerCleanupOnly)
	if remove {
		err = stage.removePublishedOriginal(context.Background())
	}
	if stage.root != nil {
		if remove && err == nil && stage.cleanupKnown && stage.rootfsMade {
			err = errors.Join(err, stage.removeKnownLeaf(context.Background(), "rootfs.img", stage.rootfsIdentity))
		}
		if remove && err == nil && stage.manifestMade {
			err = errors.Join(err, stage.removeRecoveryManifest(context.Background()))
		}
		if remove && err == nil && stage.lockMade {
			err = errors.Join(err, stage.removeLock(context.Background()))
		}
		if remove {
			err = errors.Join(err, syncStagedDirectory(context.Background(), stage.root))
		}
		err = errors.Join(err, stage.root.Close())
	}
	if stage.lock != nil {
		err = errors.Join(err, stage.lock.Close())
	}
	if stage.parent != nil {
		if remove && err == nil && stage.directoryMade {
			err = stage.removeKnownDirectory(context.Background())
		}
		err = errors.Join(err, stage.parent.Close())
	}
	if stage.cache != nil {
		err = errors.Join(err, stage.cache.Close())
	}
	if stage.source != nil {
		err = errors.Join(err, stage.source.Close())
	}
	if stage.publication != nil {
		err = errors.Join(err, stage.publication.Close())
	}
	if err != nil {
		return errs.WrapMsg(errs.CodeImageError, "close managed image staging resource", err)
	}
	return nil
}

func optimizeStagedImage(
	ctx context.Context,
	imagePath, imageID, fsType, osType string,
	deblob, shrink bool,
) (returnErr error) {
	stage, err := openStagedImage(ctx, imagePath, imageID, fsType, true)
	if err != nil {
		return err
	}
	// Retain the exact staging inode on failure so the durable root transaction can be replayed.
	// The original extracted image is replaced only after an authoritative completed result.
	completed := false
	defer func() {
		cleanupErr := stage.close(completed)
		returnErr = errors.Join(returnErr, cleanupErr)
		if returnErr != nil {
			returnErr = errors.Join(returnErr, errs.New(
				errs.CodeImageError,
				"managed image shrink staging disposition",
				errs.WithDetails(
					map[string]any{"image_id": imageID, "staging_retained": !completed || cleanupErr != nil},
				),
			))
		}
	}()
	if err := stage.prepareRecovery(ctx, osType, deblob, shrink); err != nil {
		return err
	}
	if err := stage.resumeOptimization(ctx); err != nil {
		return err
	}
	completed = true
	return nil
}

func copyInspectionImage(ctx context.Context, source, target *os.File, size int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// Reflinks preserve sparse images without copying their full logical size.
	if err := unix.IoctlFileClone(int(target.Fd()), int(source.Fd())); err == nil {
		return target.Sync()
	} else if !errors.Is(err, unix.EOPNOTSUPP) && !errors.Is(err, unix.EXDEV) &&
		!errors.Is(err, unix.ENOTTY) && !errors.Is(err, unix.EINVAL) {
		return err
	}
	buffer := make([]byte, 1<<20)
	zeroes := make([]byte, len(buffer))
	for remaining := size; remaining > 0; {
		if err := ctx.Err(); err != nil {
			return err
		}
		chunk := buffer[:min(int64(len(buffer)), remaining)]
		if _, err := io.ReadFull(source, chunk); err != nil {
			return err
		}
		if bytes.Equal(chunk, zeroes[:len(chunk)]) {
			if _, err := target.Seek(int64(len(chunk)), io.SeekCurrent); err != nil {
				return err
			}
		} else if _, err := target.Write(chunk); err != nil {
			return err
		}
		remaining -= int64(len(chunk))
	}
	if err := target.Truncate(size); err != nil {
		return err
	}
	return target.Sync()
}
