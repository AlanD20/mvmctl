package loopmount

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"golang.org/x/sys/unix"
)

const (
	rootfsGuestDirectoryFlags = unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW
	rootfsGuestInspectFlags   = unix.O_PATH | unix.O_CLOEXEC | unix.O_NOFOLLOW
	rootfsGuestReadFlags      = unix.O_RDONLY | unix.O_NONBLOCK | unix.O_CLOEXEC | unix.O_NOFOLLOW
	rootfsGuestTempFlags      = unix.O_WRONLY | unix.O_CREAT | unix.O_EXCL | unix.O_CLOEXEC | unix.O_NOFOLLOW
	rootfsGuestResolveFlags   = unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS |
		unix.RESOLVE_NO_MAGICLINKS | unix.RESOLVE_NO_XDEV
	maxRootfsGuestSymlinkTargetBytes = 4096
)

type rootfsGuestTreeDeps struct {
	openAt2    func(context.Context, int, string, *unix.OpenHow) (int, error)
	mkdirAt    func(context.Context, int, string, uint32) error
	fstat      func(context.Context, int, *unix.Stat_t) error
	fstatAt    func(context.Context, int, string, *unix.Stat_t, int) error
	pread      func(context.Context, int, []byte, int64) (int, error)
	pwrite     func(context.Context, int, []byte, int64) (int, error)
	fchown     func(context.Context, int, int, int) error
	fchmod     func(context.Context, int, uint32) error
	fsync      func(context.Context, int) error
	renameAt   func(context.Context, int, string, int, string) error
	symlinkAt  func(context.Context, string, int, string) error
	readlinkAt func(context.Context, int, string, []byte) (int, error)
	unlinkAt   func(context.Context, int, string, int) error
	close      func(context.Context, int) error
	tempName   func(context.Context) (string, error)
}

type rootfsGuestTree struct {
	deps    rootfsGuestTreeDeps
	rootFD  int
	tracker *rootfsProvisionTracker
}

func (tree rootfsGuestTree) readOptional(
	ctx context.Context,
	path string,
	limit int,
) (_ []byte, unavailable bool, returnErr error) {
	parentFD, leaf, err := tree.openParent(ctx, path)
	if err != nil {
		return nil, false, err
	}
	defer func() {
		returnErr = appendRootfsProvisionCleanupError(
			returnErr,
			"close managed guest file parent",
			tree.deps.close(context.WithoutCancel(ctx), parentFD),
		)
	}()
	fd, err := tree.deps.openAt2(ctx, parentFD, leaf, &unix.OpenHow{
		Flags: rootfsGuestReadFlags, Resolve: rootfsGuestResolveFlags,
	})
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil, true, nil
		}
		return nil, false, rootfsAtomicError("open managed guest file", err)
	}
	defer func() {
		returnErr = appendRootfsProvisionCleanupError(
			returnErr,
			"close managed guest file",
			tree.deps.close(context.WithoutCancel(ctx), fd),
		)
	}()
	content, err := readStableRootfsGuestFile(ctx, tree.deps, fd, limit)
	if err != nil {
		return nil, false, err
	}
	return content, false, nil
}

func (tree rootfsGuestTree) ensureDirectory(
	ctx context.Context,
	path string,
	mode uint32,
	uid uint32,
	gid uint32,
) (_ bool, returnErr error) {
	components, err := rootfsGuestPathComponents(path)
	if err != nil {
		return false, err
	}
	currentFD := tree.rootFD
	currentOwned := false
	changed := false
	defer func() {
		if !currentOwned {
			return
		}
		returnErr = appendRootfsProvisionCleanupError(
			returnErr,
			"close managed guest directory",
			tree.deps.close(context.WithoutCancel(ctx), currentFD),
		)
	}()
	for index, component := range components {
		fd, openErr := tree.deps.openAt2(ctx, currentFD, component, &unix.OpenHow{
			Flags: rootfsGuestDirectoryFlags, Resolve: rootfsGuestResolveFlags,
		})
		created := false
		if openErr != nil && errors.Is(openErr, unix.ENOENT) && index == len(components)-1 {
			if err := tree.deps.mkdirAt(ctx, currentFD, component, mode); err != nil {
				return changed, rootfsAtomicError("create fixed managed guest directory", err)
			}
			tree.tracker.markModified()
			changed = true
			created = true
			if err := tree.deps.fsync(ctx, currentFD); err != nil {
				return changed, rootfsCleanupError("sync fixed managed guest directory parent", err)
			}
			fd, openErr = tree.deps.openAt2(ctx, currentFD, component, &unix.OpenHow{
				Flags: rootfsGuestDirectoryFlags, Resolve: rootfsGuestResolveFlags,
			})
		}
		if openErr != nil {
			return changed, rootfsAtomicError("open fixed managed guest directory", openErr)
		}
		if currentOwned {
			ancestorFD := currentFD
			currentFD = fd
			if err := tree.deps.close(context.WithoutCancel(ctx), ancestorFD); err != nil {
				return changed, rootfsCleanupError("close managed guest directory ancestor", err)
			}
		} else {
			currentFD = fd
			currentOwned = true
		}
		if index != len(components)-1 {
			continue
		}
		var stat unix.Stat_t
		if err := tree.deps.fstat(ctx, fd, &stat); err != nil {
			return changed, rootfsAtomicError("inspect fixed managed guest directory", err)
		}
		if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
			return changed, rootfsAtomicError("fixed managed guest directory has unsafe type", nil)
		}
		if stat.Uid != uid || stat.Gid != gid {
			if err := tree.deps.fchown(ctx, fd, int(uid), int(gid)); err != nil {
				return changed, rootfsAtomicError("set fixed managed guest directory owner", err)
			}
			tree.tracker.markModified()
			changed = true
		}
		if stat.Mode&07777 != mode {
			if err := tree.deps.fchmod(ctx, fd, mode); err != nil {
				return changed, rootfsAtomicError("set fixed managed guest directory mode", err)
			}
			tree.tracker.markModified()
			changed = true
		}
		if created || changed {
			if err := tree.deps.fsync(ctx, fd); err != nil {
				return changed, rootfsCleanupError("sync fixed managed guest directory", err)
			}
		}
	}
	return changed, nil
}

func (tree rootfsGuestTree) replaceFile(
	ctx context.Context,
	file rootfsProvisionFile,
) (_ bool, returnErr error) {
	if len(file.content) > maxRootfsAgentBytes || file.mode&^uint32(07777) != 0 {
		return false, rootfsAtomicError("managed guest file content or mode exceeds policy", nil)
	}
	parentFD, leaf, err := tree.openParent(ctx, file.path)
	if err != nil {
		return false, err
	}
	defer func() {
		returnErr = appendRootfsProvisionCleanupError(
			returnErr,
			"close managed guest file parent",
			tree.deps.close(context.WithoutCancel(ctx), parentFD),
		)
	}()
	equal, err := rootfsGuestFileAlreadyEqual(ctx, tree.deps, parentFD, leaf, file)
	if err != nil {
		return false, err
	}
	if equal {
		return false, nil
	}
	tempName, err := tree.deps.tempName(ctx)
	if err != nil || !validRootfsGuestTempName(tempName) {
		return false, rootfsAtomicError("derive managed guest temporary leaf", err)
	}
	tempFD, err := tree.deps.openAt2(ctx, parentFD, tempName, &unix.OpenHow{
		Flags: rootfsGuestTempFlags, Mode: 0600, Resolve: rootfsGuestResolveFlags,
	})
	if err != nil {
		return false, rootfsAtomicError("create managed guest temporary file", err)
	}
	tempPresent := true
	defer func() {
		cleanupCtx := context.WithoutCancel(ctx)
		returnErr = appendRootfsProvisionCleanupError(
			returnErr,
			"close managed guest temporary file",
			tree.deps.close(cleanupCtx, tempFD),
		)
		if tempPresent {
			returnErr = appendRootfsProvisionCleanupError(
				returnErr,
				"remove managed guest temporary file",
				tree.deps.unlinkAt(cleanupCtx, parentFD, tempName, 0),
			)
		}
	}()
	tree.tracker.markModified()
	if err := inspectInitialRootfsGuestTemp(ctx, tree.deps, tempFD); err != nil {
		return true, err
	}
	if err := tree.deps.fchown(ctx, tempFD, int(file.uid), int(file.gid)); err != nil {
		return true, rootfsAtomicError("set managed guest temporary file owner", err)
	}
	if err := tree.deps.fchmod(ctx, tempFD, file.mode); err != nil {
		return true, rootfsAtomicError("set managed guest temporary file mode", err)
	}
	if err := writeAllRootfsGuestFile(ctx, tree.deps, tempFD, file.content); err != nil {
		return true, err
	}
	if err := tree.deps.fsync(ctx, tempFD); err != nil {
		return true, rootfsCleanupError("sync managed guest temporary file", err)
	}
	if err := inspectFinalRootfsGuestTemp(ctx, tree.deps, tempFD, file); err != nil {
		return true, err
	}
	if err := tree.deps.renameAt(ctx, parentFD, tempName, parentFD, leaf); err != nil {
		return true, rootfsAtomicError("publish managed guest file", err)
	}
	tempPresent = false
	if err := tree.deps.fsync(ctx, parentFD); err != nil {
		return true, rootfsCleanupError("sync managed guest file parent", err)
	}
	equal, err = rootfsGuestFileAlreadyEqual(ctx, tree.deps, parentFD, leaf, file)
	if err != nil {
		return true, err
	}
	if !equal {
		return true, rootfsAtomicError("published managed guest file readback does not match", nil)
	}
	return true, nil
}

func (tree rootfsGuestTree) fixedPathHasType(
	ctx context.Context,
	path string,
	wantType uint32,
) (_ bool, returnErr error) {
	parentFD, leaf, err := tree.openParent(ctx, path)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return false, nil
		}
		return false, err
	}
	defer func() {
		returnErr = appendRootfsProvisionCleanupError(
			returnErr,
			"close fixed guest evidence parent",
			tree.deps.close(context.WithoutCancel(ctx), parentFD),
		)
	}()
	fd, err := tree.deps.openAt2(ctx, parentFD, leaf, &unix.OpenHow{
		Flags: rootfsGuestInspectFlags, Resolve: rootfsGuestResolveFlags,
	})
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return false, nil
		}
		return false, rootfsAtomicError("open fixed guest layout evidence", err)
	}
	defer func() {
		returnErr = appendRootfsProvisionCleanupError(
			returnErr,
			"close fixed guest layout evidence",
			tree.deps.close(context.WithoutCancel(ctx), fd),
		)
	}()
	var stat unix.Stat_t
	if err := tree.deps.fstat(ctx, fd, &stat); err != nil {
		return false, rootfsAtomicError("inspect fixed guest layout evidence", err)
	}
	if stat.Mode&unix.S_IFMT != wantType {
		return false, rootfsAtomicError("fixed guest layout evidence has an unsafe type", nil)
	}
	return true, nil
}

func (tree rootfsGuestTree) replaceAgentEnablementLink(
	ctx context.Context,
	layout rootfsAgentInitLayout,
) (bool, error) {
	switch layout {
	case rootfsAgentInitSystemd:
		return tree.replaceFixedSymlink(
			ctx,
			rootfsAgentSystemdWantsLink,
			rootfsAgentSystemdWantsTarget,
		)
	case rootfsAgentInitOpenRC:
		return tree.replaceFixedSymlink(
			ctx,
			rootfsAgentOpenRCRunlevelLink,
			rootfsAgentOpenRCRunlevelTarget,
		)
	default:
		return false, rootfsAtomicError("managed guest agent init layout is unsupported", nil)
	}
}

// CRITICAL: Callers cannot choose a guest path or target; only the two fixed agent enablement links reach this helper.
func (tree rootfsGuestTree) replaceFixedSymlink(
	ctx context.Context,
	path string,
	target string,
) (_ bool, returnErr error) {
	if !validRootfsAgentEnablementLink(path, target) {
		return false, rootfsAtomicError("managed guest agent enablement link is outside policy", nil)
	}
	parentFD, leaf, err := tree.openParent(ctx, path)
	if err != nil {
		return false, err
	}
	defer func() {
		returnErr = appendRootfsProvisionCleanupError(
			returnErr,
			"close managed guest agent link parent",
			tree.deps.close(context.WithoutCancel(ctx), parentFD),
		)
	}()
	equal, err := rootfsGuestSymlinkAlreadyEqual(ctx, tree.deps, parentFD, leaf, target)
	if err != nil {
		return false, err
	}
	if equal {
		return false, nil
	}
	tempName, err := tree.deps.tempName(ctx)
	if err != nil || !validRootfsGuestTempName(tempName) {
		return false, rootfsAtomicError("derive managed guest link temporary leaf", err)
	}
	if err := tree.deps.symlinkAt(ctx, target, parentFD, tempName); err != nil {
		return false, rootfsAtomicError("create managed guest agent temporary link", err)
	}
	tempPresent := true
	defer func() {
		if !tempPresent {
			return
		}
		returnErr = appendRootfsProvisionCleanupError(
			returnErr,
			"remove managed guest agent temporary link",
			tree.deps.unlinkAt(context.WithoutCancel(ctx), parentFD, tempName, 0),
		)
	}()
	tree.tracker.markModified()
	equal, err = rootfsGuestSymlinkAlreadyEqual(ctx, tree.deps, parentFD, tempName, target)
	if err != nil {
		return true, err
	}
	if !equal {
		return true, rootfsAtomicError("managed guest agent temporary link does not match", nil)
	}
	if err := tree.deps.renameAt(ctx, parentFD, tempName, parentFD, leaf); err != nil {
		return true, rootfsAtomicError("publish managed guest agent enablement link", err)
	}
	tempPresent = false
	if err := tree.deps.fsync(ctx, parentFD); err != nil {
		return true, rootfsCleanupError("sync managed guest agent link parent", err)
	}
	equal, err = rootfsGuestSymlinkAlreadyEqual(ctx, tree.deps, parentFD, leaf, target)
	if err != nil {
		return true, err
	}
	if !equal {
		return true, rootfsAtomicError("published managed guest agent enablement link does not match", nil)
	}
	return true, nil
}

func (tree rootfsGuestTree) openParent(
	ctx context.Context,
	path string,
) (_ int, _ string, returnErr error) {
	components, err := rootfsGuestPathComponents(path)
	if err != nil || len(components) < 2 {
		return -1, "", rootfsAtomicError("managed guest file path is invalid", err)
	}
	currentFD := tree.rootFD
	currentOwned := false
	defer func() {
		if returnErr == nil || !currentOwned {
			return
		}
		returnErr = appendRootfsProvisionCleanupError(
			returnErr,
			"close rejected managed guest parent",
			tree.deps.close(context.WithoutCancel(ctx), currentFD),
		)
	}()
	for _, component := range components[:len(components)-1] {
		fd, openErr := tree.deps.openAt2(ctx, currentFD, component, &unix.OpenHow{
			Flags: rootfsGuestDirectoryFlags, Resolve: rootfsGuestResolveFlags,
		})
		if openErr != nil {
			return -1, "", rootfsAtomicError("open managed guest file parent", openErr)
		}
		if currentOwned {
			ancestorFD := currentFD
			currentFD = fd
			if closeErr := tree.deps.close(context.WithoutCancel(ctx), ancestorFD); closeErr != nil {
				return -1, "", rootfsCleanupError("close managed guest parent ancestor", closeErr)
			}
		} else {
			currentFD = fd
			currentOwned = true
		}
	}
	return currentFD, components[len(components)-1], nil
}

func rootfsGuestPathComponents(path string) ([]string, error) {
	canonical, err := canonicalRootfsGuestPath(path)
	if err != nil {
		return nil, rootfsAtomicError("managed guest path is invalid", err)
	}
	return strings.Split(canonical, "/"), nil
}

func readStableRootfsGuestFile(
	ctx context.Context,
	deps rootfsGuestTreeDeps,
	fd int,
	limit int,
) ([]byte, error) {
	if limit < 0 {
		return nil, rootfsAtomicError("managed guest file byte limit is invalid", nil)
	}
	var before unix.Stat_t
	if err := deps.fstat(ctx, fd, &before); err != nil {
		return nil, rootfsAtomicError("inspect managed guest file", err)
	}
	if before.Mode&unix.S_IFMT != unix.S_IFREG || before.Nlink != 1 || before.Size < 0 || before.Size > int64(limit) {
		return nil, rootfsAtomicError("managed guest file is not a bounded one-link regular file", nil)
	}
	content := make([]byte, int(before.Size))
	for offset := 0; offset < len(content); {
		if err := ctx.Err(); err != nil {
			return nil, rootfsAtomicError("read managed guest file", err)
		}
		count, readErr := deps.pread(ctx, fd, content[offset:], int64(offset))
		if count < 0 || count > len(content)-offset {
			return nil, rootfsAtomicError("read managed guest file made invalid progress", nil)
		}
		offset += count
		if readErr != nil {
			return nil, rootfsAtomicError("read managed guest file", readErr)
		}
		if count == 0 {
			return nil, rootfsAtomicError("read managed guest file", io.ErrNoProgress)
		}
	}
	var probe [1]byte
	count, readErr := deps.pread(ctx, fd, probe[:], int64(len(content)))
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return nil, rootfsAtomicError("probe managed guest file", readErr)
	}
	if count != 0 {
		return nil, rootfsAtomicError("managed guest file grew while reading", nil)
	}
	var after unix.Stat_t
	if err := deps.fstat(ctx, fd, &after); err != nil {
		return nil, rootfsAtomicError("reinspect managed guest file", err)
	}
	if inspectRootfsFileIdentity(before) != inspectRootfsFileIdentity(after) {
		return nil, rootfsAtomicError("managed guest file changed while reading", nil)
	}
	return content, nil
}

func rootfsGuestFileAlreadyEqual(
	ctx context.Context,
	deps rootfsGuestTreeDeps,
	parentFD int,
	leaf string,
	file rootfsProvisionFile,
) (_ bool, returnErr error) {
	fd, err := deps.openAt2(ctx, parentFD, leaf, &unix.OpenHow{
		Flags: rootfsGuestReadFlags, Resolve: rootfsGuestResolveFlags,
	})
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return false, nil
		}
		if errors.Is(err, unix.ELOOP) {
			var stat unix.Stat_t
			if statErr := deps.fstatAt(ctx, parentFD, leaf, &stat, unix.AT_SYMLINK_NOFOLLOW); statErr != nil {
				return false, rootfsAtomicError("inspect managed guest symlink leaf", statErr)
			}
			if stat.Mode&unix.S_IFMT == unix.S_IFLNK && stat.Nlink == 1 {
				return false, nil
			}
		}
		return false, rootfsAtomicError("open existing managed guest file", err)
	}
	defer func() {
		returnErr = appendRootfsProvisionCleanupError(
			returnErr,
			"close existing managed guest file",
			deps.close(context.WithoutCancel(ctx), fd),
		)
	}()
	var stat unix.Stat_t
	if err := deps.fstat(ctx, fd, &stat); err != nil {
		return false, rootfsAtomicError("inspect existing managed guest file", err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 || stat.Size < 0 ||
		stat.Size > maxRootfsAgentBytes {
		return false, rootfsAtomicError("existing managed guest leaf has unsafe type or size", nil)
	}
	content, err := readStableRootfsGuestFile(ctx, deps, fd, maxRootfsAgentBytes)
	if err != nil {
		return false, err
	}
	return stat.Mode&07777 == file.mode && stat.Uid == file.uid && stat.Gid == file.gid &&
		bytes.Equal(content, file.content), nil
}

func rootfsGuestSymlinkAlreadyEqual(
	ctx context.Context,
	deps rootfsGuestTreeDeps,
	parentFD int,
	leaf string,
	wantTarget string,
) (bool, error) {
	var stat unix.Stat_t
	if err := deps.fstatAt(ctx, parentFD, leaf, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return false, nil
		}
		return false, rootfsAtomicError("inspect managed guest agent enablement link", err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFLNK || stat.Nlink != 1 {
		return false, rootfsAtomicError("managed guest agent enablement leaf has an unsafe type", nil)
	}
	buffer := make([]byte, maxRootfsGuestSymlinkTargetBytes+1)
	count, err := deps.readlinkAt(ctx, parentFD, leaf, buffer)
	if err != nil {
		return false, rootfsAtomicError("read managed guest agent enablement link", err)
	}
	if count < 0 || count > maxRootfsGuestSymlinkTargetBytes || count > len(buffer) {
		return false, rootfsAtomicError("managed guest agent enablement target is unbounded", nil)
	}
	return string(buffer[:count]) == wantTarget, nil
}

func inspectInitialRootfsGuestTemp(ctx context.Context, deps rootfsGuestTreeDeps, fd int) error {
	var stat unix.Stat_t
	if err := deps.fstat(ctx, fd, &stat); err != nil {
		return rootfsAtomicError("inspect managed guest temporary file", err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 || stat.Size != 0 {
		return rootfsAtomicError("managed guest temporary file has unsafe initial metadata", nil)
	}
	return nil
}

func inspectFinalRootfsGuestTemp(
	ctx context.Context,
	deps rootfsGuestTreeDeps,
	fd int,
	file rootfsProvisionFile,
) error {
	var stat unix.Stat_t
	if err := deps.fstat(ctx, fd, &stat); err != nil {
		return rootfsAtomicError("inspect finalized managed guest temporary file", err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 || stat.Size != int64(len(file.content)) ||
		stat.Mode&07777 != file.mode || stat.Uid != file.uid || stat.Gid != file.gid {
		return rootfsAtomicError("managed guest temporary file has unsafe final metadata", nil)
	}
	return nil
}

func writeAllRootfsGuestFile(
	ctx context.Context,
	deps rootfsGuestTreeDeps,
	fd int,
	content []byte,
) error {
	for offset := 0; offset < len(content); {
		if err := ctx.Err(); err != nil {
			return rootfsAtomicError("write managed guest temporary file", err)
		}
		count, writeErr := deps.pwrite(ctx, fd, content[offset:], int64(offset))
		if count < 0 || count > len(content)-offset {
			return rootfsAtomicError("write managed guest temporary file made invalid progress", nil)
		}
		offset += count
		if writeErr != nil {
			return rootfsAtomicError("write managed guest temporary file", writeErr)
		}
		if count == 0 {
			return rootfsAtomicError("write managed guest temporary file", io.ErrNoProgress)
		}
	}
	return nil
}

func validRootfsGuestTempName(name string) bool {
	if len(name) != len(".mvmctl-provision-")+32 || !strings.HasPrefix(name, ".mvmctl-provision-") {
		return false
	}
	for _, character := range name[len(".mvmctl-provision-"):] {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func validateRootfsGuestTreeDeps(deps rootfsGuestTreeDeps) error {
	if deps.openAt2 == nil || deps.mkdirAt == nil || deps.fstat == nil || deps.fstatAt == nil || deps.pread == nil ||
		deps.pwrite == nil || deps.fchown == nil || deps.fchmod == nil || deps.fsync == nil || deps.renameAt == nil ||
		deps.symlinkAt == nil || deps.readlinkAt == nil || deps.unlinkAt == nil || deps.close == nil ||
		deps.tempName == nil {
		return rootfsAtomicError("managed guest tree dependencies are incomplete", nil)
	}
	return nil
}

func appendRootfsProvisionCleanupError(primary error, message string, cause error) error {
	if cause == nil {
		return primary
	}
	if primary == nil {
		return rootfsCleanupError(message, cause)
	}
	return appendRootfsCleanupError(primary, message, fmt.Errorf("%s: %w", message, cause))
}
