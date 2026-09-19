package image

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"

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
	defer func() {
		var domainErr *errs.DomainError
		if err != nil && !errors.As(err, &domainErr) {
			err = errs.WrapMsg(errs.CodeImageError, "clean up image staging inspection", err)
		}
	}()
	cacheDir, err := infra.GetCacheDir()
	if err != nil {
		return "", errs.WrapMsg(errs.CodeImageError, "resolve image staging cache", err)
	}
	sourceFD, err := unix.Open(imagePath, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return "", errs.WrapMsg(errs.CodeImageError, "open image for staging", err)
	}
	source := os.NewFile(uintptr(sourceFD), imagePath)
	defer func() { err = errors.Join(err, source.Close()) }()
	info, err := source.Stat()
	if err != nil {
		return "", errs.WrapMsg(errs.CodeImageError, "inspect image staging source", err)
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 {
		return "", errs.New(errs.CodeImageError, "image staging requires a nonempty regular file")
	}
	request := loopmountsvc.ImageInspectRequest{
		CacheLocator:       cacheDir,
		ImageID:            imageID,
		ExpectedImageBytes: uint64(info.Size()),
		Filesystem:         loopmountsvc.Filesystem(fsType),
	}
	if err := request.Validate(); err != nil {
		return "", err
	}
	cache, err := os.OpenRoot(cacheDir)
	if err != nil {
		return "", errs.WrapMsg(errs.CodeImageError, "open image staging cache", err)
	}
	defer func() { err = errors.Join(err, cache.Close()) }()
	if err := cache.MkdirAll("images/staging", 0700); err != nil {
		return "", errs.WrapMsg(errs.CodeImageError, "create image staging parent", err)
	}
	parent, err := cache.OpenRoot("images/staging")
	if err != nil {
		return "", errs.WrapMsg(errs.CodeImageError, "open image staging parent", err)
	}
	defer func() { err = errors.Join(err, parent.Close()) }()
	// CRITICAL: Never adopt, replace, or recursively clean an existing staging resource.
	// Exclusive creation keeps interrupted or concurrent work available for diagnosis.
	if err := parent.Mkdir(imageID, 0700); err != nil {
		return "", errs.WrapMsg(errs.CodeImageError, "create exclusive image staging resource", err)
	}
	defer func() { err = errors.Join(err, parent.Remove(imageID)) }()
	stage, err := parent.OpenRoot(imageID)
	if err != nil {
		return "", errs.WrapMsg(errs.CodeImageError, "open image staging resource", err)
	}
	defer func() { err = errors.Join(err, stage.Close()) }()
	target, err := stage.OpenFile("rootfs.img", os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return "", errs.WrapMsg(errs.CodeImageError, "create image staging rootfs", err)
	}
	defer func() { err = errors.Join(err, stage.Remove("rootfs.img")) }()
	copyErr := copyInspectionImage(ctx, source, target, info.Size())
	closeErr := target.Close()
	if err := errors.Join(copyErr, closeErr); err != nil {
		return "", errs.WrapMsg(errs.CodeImageError, "populate image staging rootfs", err)
	}
	after, err := source.Stat()
	if err != nil {
		return "", errs.WrapMsg(errs.CodeImageError, "reinspect image staging source", err)
	}
	if after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) {
		return "", errs.New(errs.CodeImageError, "image changed while preparing staging inspection")
	}
	result, err := loopmountsvc.NewClient().InspectImage(ctx, request)
	if err != nil {
		return "", err
	}
	return result.OSType, nil
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
