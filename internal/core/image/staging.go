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
	"mvmctl/internal/lib/crypto"
	loopmountsvc "mvmctl/internal/service/loopmount"
	"mvmctl/pkg/errs"
)

// inspectStagedImage keeps caller paths in the normal process. The receiver sees only
// the canonical staging resource identity and independently admits its fixed rootfs leaf.
func inspectStagedImage(
	ctx context.Context,
	imagePath, imageID, fsType string,
) (osType string, err error) {
	stage, err := prepareStagedImage(ctx, imagePath, imageID, fsType)
	if err != nil {
		return "", err
	}
	defer func() { err = errors.Join(err, stage.close(true)) }()
	result, err := loopmountsvc.NewClient().InspectImage(ctx, stage.request)
	if err != nil {
		return "", err
	}
	return result.OSType, nil
}

type stagedImage struct {
	request       loopmountsvc.ImageInspectRequest
	source        *os.File
	sourceInfo    os.FileInfo
	sourcePath    string
	cache         *os.Root
	parent        *os.Root
	root          *os.Root
	directoryMade bool
	rootfsMade    bool
}

func prepareStagedImage(ctx context.Context, imagePath, imageID, fsType string) (_ *stagedImage, err error) {
	stage := &stagedImage{sourcePath: imagePath}
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
	sourceFD, err := unix.Open(imagePath, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, errs.WrapMsg(errs.CodeImageError, "open image for staging", err)
	}
	source := os.NewFile(uintptr(sourceFD), imagePath)
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
	// CRITICAL: Never adopt, replace, or recursively clean an existing staging resource.
	// Exclusive creation keeps interrupted or concurrent work available for diagnosis.
	if err := parent.Mkdir(imageID, 0700); err != nil {
		return nil, errs.WrapMsg(errs.CodeImageError, "create exclusive image staging resource", err)
	}
	stage.directoryMade = true
	root, err := parent.OpenRoot(imageID)
	if err != nil {
		return nil, errs.WrapMsg(errs.CodeImageError, "open image staging resource", err)
	}
	stage.root = root
	target, err := root.OpenFile("rootfs.img", os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return nil, errs.WrapMsg(errs.CodeImageError, "create image staging rootfs", err)
	}
	stage.rootfsMade = true
	copyErr := copyInspectionImage(ctx, source, target, info.Size())
	closeErr := target.Close()
	if err := errors.Join(copyErr, closeErr); err != nil {
		return nil, errs.WrapMsg(errs.CodeImageError, "populate image staging rootfs", err)
	}
	after, err := source.Stat()
	if err != nil {
		return nil, errs.WrapMsg(errs.CodeImageError, "reinspect image staging source", err)
	}
	if after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) {
		return nil, errs.New(errs.CodeImageError, "image changed while preparing staging inspection")
	}
	return stage, nil
}

func (stage *stagedImage) close(remove bool) error {
	var err error
	if stage.root != nil {
		if remove && stage.rootfsMade {
			err = errors.Join(err, stage.root.Remove("rootfs.img"))
		}
		err = errors.Join(err, stage.root.Close())
	}
	if stage.parent != nil {
		if remove && stage.directoryMade {
			err = errors.Join(err, stage.parent.Remove(stage.request.ImageID))
		}
		err = errors.Join(err, stage.parent.Close())
	}
	if stage.cache != nil {
		err = errors.Join(err, stage.cache.Close())
	}
	if stage.source != nil {
		err = errors.Join(err, stage.source.Close())
	}
	if err != nil {
		return errs.WrapMsg(errs.CodeImageError, "close managed image staging resource", err)
	}
	return nil
}

func shrinkStagedImage(ctx context.Context, imagePath, imageID, fsType string) (returnErr error) {
	stage, err := prepareStagedImage(ctx, imagePath, imageID, fsType)
	if err != nil {
		return err
	}
	// Retain the exact staging inode on failure so the durable root transaction can be replayed.
	// The original extracted image is replaced only after an authoritative completed result.
	completed := false
	defer func() {
		returnErr = errors.Join(returnErr, stage.close(completed))
		if returnErr != nil {
			returnErr = errors.Join(returnErr, errs.New(errs.CodeImageError, "managed image shrink staging disposition",
				errs.WithDetails(map[string]any{"image_id": imageID, "staging_retained": !completed})))
		}
	}()
	result, err := loopmountsvc.NewClient().OptimizeImage(ctx, loopmountsvc.ImageOptimizeRequest{
		CacheLocator: stage.request.CacheLocator, ImageID: imageID,
		ExpectedImageBytes: stage.request.ExpectedImageBytes, Filesystem: stage.request.Filesystem,
		Plan: loopmountsvc.ImageOptimizePlan{Shrink: true},
	})
	if err != nil {
		return err
	}
	if err := stage.publishShrink(ctx, result.ImageBytes); err != nil {
		return err
	}
	completed = true
	return nil
}

func (stage *stagedImage) publishShrink(ctx context.Context, size uint64) (returnErr error) {
	result, err := stage.root.Open("rootfs.img")
	if err != nil {
		return errs.WrapMsg(errs.CodeImageError, "open completed image shrink result", err)
	}
	defer func() { returnErr = errors.Join(returnErr, result.Close()) }()
	info, err := result.Stat()
	if err != nil || !info.Mode().IsRegular() || uint64(info.Size()) != size {
		return errs.WrapMsg(errs.CodeImageError, "completed image shrink result metadata changed", err)
	}
	parent, err := os.OpenRoot(filepath.Dir(stage.sourcePath))
	if err != nil {
		return errs.WrapMsg(errs.CodeImageError, "open image publication directory", err)
	}
	defer func() { returnErr = errors.Join(returnErr, parent.Close()) }()
	temporary := ".mvm-image-" + crypto.UUIDV4()
	target, err := parent.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return errs.WrapMsg(errs.CodeImageError, "create image publication file", err)
	}
	published := false
	defer func() {
		if !published {
			returnErr = errors.Join(returnErr, parent.Remove(temporary))
		}
	}()
	copyErr := copyInspectionImage(ctx, result, target, int64(size))
	if err := errors.Join(copyErr, target.Close()); err != nil {
		return errs.WrapMsg(errs.CodeImageError, "copy completed image shrink result", err)
	}
	name := filepath.Base(stage.sourcePath)
	current, err := parent.Stat(name)
	if err != nil || !os.SameFile(current, stage.sourceInfo) || current.Size() != stage.sourceInfo.Size() ||
		!current.ModTime().Equal(stage.sourceInfo.ModTime()) {
		return errs.WrapMsg(errs.CodeImageError, "image publication source changed during shrink", err)
	}
	if err := parent.Rename(temporary, name); err != nil {
		return errs.WrapMsg(errs.CodeImageError, "publish completed image shrink", err)
	}
	published = true
	directory, err := parent.Open(".")
	if err != nil {
		return errs.WrapMsg(errs.CodeImageError, "open completed image publication directory", err)
	}
	return errors.Join(directory.Sync(), directory.Close())
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
