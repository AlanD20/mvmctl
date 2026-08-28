package jailer

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"

	"golang.org/x/sys/unix"
)

const (
	trustedReleaseStoreExecutableMode      = uint32(0755)
	trustedReleaseExecutableReadBufferSize = 32 * 1024
)

type trustedReleaseExecutables struct {
	deps          trustedReleaseStoreDeps
	firecrackerFD int
	jailerFD      int
	retained      []int
}

type trustedReleaseExecutablePolicy struct {
	name     string
	leaf     string
	expected trustedReleaseExecutable
}

// CRITICAL: Both fixed leaves are opened once through the pinned slot directory. The same descriptors that pass
// metadata, full-content hash, and ELF admission remain retained for the later launch boundary.
func (directory *trustedReleaseDirectory) openExecutables(
	ctx context.Context,
	manifest trustedReleaseManifest,
) (_ *trustedReleaseExecutables, returnErr error) {
	if directory == nil || directory.slotFD < 0 || len(directory.retained) == 0 {
		return nil, trustedReleaseStoreError("installed trusted release directory is not active", nil)
	}
	if err := validateTrustedReleaseManifest(manifest); err != nil {
		return nil, err
	}
	if manifest.slot != directory.slot {
		return nil, trustedReleaseStoreUntrusted(
			"trusted release executable manifest slot does not match its directory",
			nil,
		)
	}
	if err := ctx.Err(); err != nil {
		return nil, trustedReleaseStoreError("open trusted release executables", err)
	}
	source, err := newTrustedReleaseSource(directory.slot)
	if err != nil {
		return nil, trustedReleaseStoreUntrusted("trusted release executable source is invalid", err)
	}

	retained := make([]int, 0, 2)
	defer func() {
		if returnErr == nil {
			return
		}
		returnErr = appendTrustedReleaseStoreError(
			returnErr,
			"close rejected trusted release executables",
			closeTrustedReleaseStoreFDs(ctx, directory.deps, retained),
		)
	}()

	policies := [...]trustedReleaseExecutablePolicy{
		{name: "Firecracker", leaf: trustedReleaseFirecrackerLeaf, expected: manifest.firecracker},
		{name: "Jailer", leaf: trustedReleaseJailerLeaf, expected: manifest.jailer},
	}
	for _, policy := range policies {
		fd, openErr := openTrustedReleaseExecutable(ctx, directory, policy, source)
		if openErr != nil {
			return nil, openErr
		}
		retained = append(retained, fd)
	}

	executables := &trustedReleaseExecutables{
		deps:          directory.deps,
		firecrackerFD: retained[0],
		jailerFD:      retained[1],
		retained:      retained,
	}
	retained = nil
	return executables, nil
}

func openTrustedReleaseExecutable(
	ctx context.Context,
	directory *trustedReleaseDirectory,
	policy trustedReleaseExecutablePolicy,
	source trustedReleaseSource,
) (_ int, returnErr error) {
	if err := ctx.Err(); err != nil {
		return -1, trustedReleaseStoreError("open trusted release "+policy.name+" executable", err)
	}
	openedFD, err := directory.deps.openAt(ctx, directory.slotFD, policy.leaf, trustedReleaseStoreReadFlags, 0)
	if err != nil {
		return -1, classifyTrustedReleaseExecutableOpenError(policy.name, err)
	}
	defer func() {
		if returnErr == nil {
			return
		}
		returnErr = appendTrustedReleaseStoreError(
			returnErr,
			"close rejected trusted release "+policy.name+" executable",
			directory.deps.close(context.WithoutCancel(ctx), openedFD),
		)
	}()

	before, err := inspectTrustedReleaseExecutableDescriptor(ctx, directory, openedFD, policy)
	if err != nil {
		return -1, err
	}
	header, digest, err := hashTrustedReleaseExecutable(ctx, directory.deps, openedFD, policy)
	if err != nil {
		return -1, err
	}
	after, err := inspectTrustedReleaseExecutableDescriptor(ctx, directory, openedFD, policy)
	if err != nil {
		return -1, err
	}
	if !sameTrustedReleaseExecutableStat(before, after) {
		return -1, trustedReleaseStoreUntrusted(
			"trusted release "+policy.name+" executable changed while being read",
			nil,
		)
	}
	if digest != policy.expected.digest {
		return -1, trustedReleaseStoreUntrusted(
			"trusted release "+policy.name+" executable digest does not match its manifest",
			nil,
		)
	}
	if err := validateTrustedReleaseELFHeader(header[:], policy.expected.sizeBytes, source); err != nil {
		return -1, err
	}
	return openedFD, nil
}

func inspectTrustedReleaseExecutableDescriptor(
	ctx context.Context,
	directory *trustedReleaseDirectory,
	fd int,
	policy trustedReleaseExecutablePolicy,
) (unix.Stat_t, error) {
	if err := ctx.Err(); err != nil {
		return unix.Stat_t{}, trustedReleaseStoreError(
			"inspect trusted release "+policy.name+" executable",
			err,
		)
	}
	var stat unix.Stat_t
	if err := directory.deps.fstat(ctx, fd, &stat); err != nil {
		return unix.Stat_t{}, trustedReleaseStoreError(
			"inspect trusted release "+policy.name+" executable",
			err,
		)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG {
		return unix.Stat_t{}, trustedReleaseStoreUntrusted(
			"trusted release "+policy.name+" executable is not a regular file",
			nil,
		)
	}
	if stat.Uid != directory.policy.expectedUID || stat.Gid != directory.policy.expectedGID ||
		stat.Mode&07777 != trustedReleaseStoreExecutableMode {
		return unix.Stat_t{}, trustedReleaseStoreUntrusted(
			"trusted release "+policy.name+" executable has unexpected owner or mode",
			nil,
		)
	}
	if stat.Nlink != 1 {
		return unix.Stat_t{}, trustedReleaseStoreUntrusted(
			"trusted release "+policy.name+" executable must have exactly one link",
			nil,
		)
	}
	if stat.Size < int64(trustedReleaseExecutableMinBytes) ||
		stat.Size > int64(trustedReleaseExecutableMaxBytes) || uint64(stat.Size) != policy.expected.sizeBytes {
		return unix.Stat_t{}, trustedReleaseStoreUntrusted(
			"trusted release "+policy.name+" executable size does not match its manifest",
			nil,
		)
	}
	return stat, nil
}

func hashTrustedReleaseExecutable(
	ctx context.Context,
	deps trustedReleaseStoreDeps,
	fd int,
	policy trustedReleaseExecutablePolicy,
) ([trustedReleaseELFHeaderBytes]byte, trustedReleaseExecutableDigest, error) {
	var header [trustedReleaseELFHeaderBytes]byte
	hasher := sha256.New()
	buffer := make([]byte, trustedReleaseExecutableReadBufferSize)
	var offset uint64
	for offset < policy.expected.sizeBytes {
		if err := ctx.Err(); err != nil {
			return header, trustedReleaseExecutableDigest{}, trustedReleaseStoreError(
				"read trusted release "+policy.name+" executable",
				err,
			)
		}
		remaining := policy.expected.sizeBytes - offset
		readSize := len(buffer)
		if remaining < uint64(readSize) {
			readSize = int(remaining)
		}
		count, readErr := deps.pread(ctx, fd, buffer[:readSize], int64(offset))
		if count < 0 || count > readSize {
			return header, trustedReleaseExecutableDigest{}, trustedReleaseStoreError(
				"read trusted release "+policy.name+" executable",
				fmt.Errorf("invalid positioned read count %d", count),
			)
		}
		if count > 0 {
			if _, err := hasher.Write(buffer[:count]); err != nil {
				return header, trustedReleaseExecutableDigest{}, trustedReleaseStoreError(
					"hash trusted release "+policy.name+" executable",
					err,
				)
			}
			if offset < trustedReleaseELFHeaderBytes {
				copy(header[offset:], buffer[:count])
			}
			offset += uint64(count)
		}
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return header, trustedReleaseExecutableDigest{}, trustedReleaseStoreError(
				"read trusted release "+policy.name+" executable",
				readErr,
			)
		}
		if offset < policy.expected.sizeBytes && (count == 0 || errors.Is(readErr, io.EOF)) {
			return header, trustedReleaseExecutableDigest{}, trustedReleaseStoreUntrusted(
				"trusted release "+policy.name+" executable is shorter than its manifest",
				nil,
			)
		}
	}

	if err := ctx.Err(); err != nil {
		return header, trustedReleaseExecutableDigest{}, trustedReleaseStoreError(
			"probe trusted release "+policy.name+" executable length",
			err,
		)
	}
	var probe [1]byte
	count, readErr := deps.pread(ctx, fd, probe[:], int64(policy.expected.sizeBytes))
	if count < 0 || count > len(probe) {
		return header, trustedReleaseExecutableDigest{}, trustedReleaseStoreError(
			"probe trusted release "+policy.name+" executable length",
			fmt.Errorf("invalid positioned read count %d", count),
		)
	}
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return header, trustedReleaseExecutableDigest{}, trustedReleaseStoreError(
			"probe trusted release "+policy.name+" executable length",
			readErr,
		)
	}
	if count != 0 {
		return header, trustedReleaseExecutableDigest{}, trustedReleaseStoreUntrusted(
			"trusted release "+policy.name+" executable is longer than its manifest",
			nil,
		)
	}

	var digest trustedReleaseExecutableDigest
	copy(digest[:], hasher.Sum(nil))
	return header, digest, nil
}

func sameTrustedReleaseExecutableStat(first, second unix.Stat_t) bool {
	return first.Dev == second.Dev &&
		first.Ino == second.Ino &&
		first.Mode == second.Mode &&
		first.Nlink == second.Nlink &&
		first.Uid == second.Uid &&
		first.Gid == second.Gid &&
		first.Size == second.Size &&
		first.Mtim == second.Mtim &&
		first.Ctim == second.Ctim
}

func classifyTrustedReleaseExecutableOpenError(name string, cause error) error {
	if errors.Is(cause, unix.ENOENT) || errors.Is(cause, unix.ELOOP) || errors.Is(cause, unix.ENOTDIR) ||
		errors.Is(cause, unix.ENXIO) {
		return trustedReleaseStoreUntrusted(
			"trusted release "+name+" executable is missing or unsafe",
			cause,
		)
	}
	return trustedReleaseStoreError("open trusted release "+name+" executable", cause)
}

func (executables *trustedReleaseExecutables) Release(ctx context.Context) error {
	if executables == nil || len(executables.retained) == 0 {
		return nil
	}
	err := closeTrustedReleaseStoreFDs(ctx, executables.deps, executables.retained)
	executables.retained = nil
	executables.firecrackerFD = -1
	executables.jailerFD = -1
	if err != nil {
		return trustedReleaseStoreError("release trusted release executable descriptors", err)
	}
	return nil
}
