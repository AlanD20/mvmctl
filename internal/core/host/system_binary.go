package host

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"slices"
	"strings"

	"golang.org/x/sys/unix"

	"mvmctl/internal/infra"
	"mvmctl/pkg/errs"
)

const (
	directoryInstallFlags = unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW
	sourceInstallFlags    = unix.O_RDONLY | unix.O_CLOEXEC
	targetInspectFlags    = unix.O_PATH | unix.O_CLOEXEC | unix.O_NOFOLLOW
	tempInstallFlags      = unix.O_WRONLY | unix.O_CREAT | unix.O_EXCL | unix.O_CLOEXEC | unix.O_NOFOLLOW

	systemBinaryName      = "mvm"
	systemBinaryCopyChunk = 32 * 1024
	tempNameAttempts      = 8
)

type systemBinaryInstallDeps struct {
	effectiveUID func() int
	readSudoers  func(string) ([]byte, error)
	open         func(string, int, uint32) (int, error)
	openAt       func(int, string, int, uint32) (int, error)
	mkdirAt      func(int, string, uint32) error
	fstat        func(int, *unix.Stat_t) error
	fchown       func(int, int, int) error
	fchmod       func(int, uint32) error
	read         func(int, []byte) (int, error)
	write        func(int, []byte) (int, error)
	fsync        func(int) error
	close        func(int) error
	renameAt     func(int, string, int, string) error
	unlinkAt     func(int, string, int) error
	randomName   func() (string, error)
}

type systemBinaryInstallPolicy struct {
	rootPath    string
	expectedUID uint32
	expectedGID uint32
}

func productionSystemBinaryInstallPolicy() systemBinaryInstallPolicy {
	return systemBinaryInstallPolicy{rootPath: "/", expectedUID: 0, expectedGID: 0}
}

// InstallSystemBinary installs the running image at the canonical system path.
// CRITICAL: This administrator-only operation accepts no caller-selected path
// and retains no-follow directory descriptors through atomic replacement.
func InstallSystemBinary(ctx context.Context) (bool, error) {
	return installSystemBinaryWithPolicy(
		ctx,
		realSystemBinaryInstallDeps(),
		productionSystemBinaryInstallPolicy(),
	)
}

// InstallSystemBinary installs the running image through the host Service.
func (s *Service) InstallSystemBinary(ctx context.Context) (bool, error) {
	changed, err := InstallSystemBinary(ctx)
	if err != nil {
		slog.Error("Could not install canonical system binary", "path", infra.SystemBinaryPath, "error", err)
	}
	return changed, err
}

func installSystemBinary(
	ctx context.Context,
	deps systemBinaryInstallDeps,
) (changed bool, returnErr error) {
	return installSystemBinaryWithPolicy(ctx, deps, productionSystemBinaryInstallPolicy())
}

func installSystemBinaryWithPolicy(
	ctx context.Context,
	deps systemBinaryInstallDeps,
	policy systemBinaryInstallPolicy,
) (changed bool, returnErr error) {
	if deps.effectiveUID() != 0 {
		return false, errs.New(
			errs.CodePrivilegeRequired,
			fmt.Sprintf("installing %s requires root", infra.SystemBinaryPath),
			errs.WithClass(errs.ClassNeedsInteraction),
		)
	}
	if err := ctx.Err(); err != nil {
		return false, systemBinaryInstallError("install system binary", err)
	}
	if err := guardSystemBinaryInstallSudoers(deps); err != nil {
		return false, err
	}

	retainedFDs := make([]int, 0, 6)
	defer func() {
		if closeErr := closeSystemBinaryDescriptors(deps, retainedFDs); closeErr != nil {
			durabilityUncertain := false
			if domainErr := errs.AsDomainError(returnErr); domainErr != nil {
				durabilityUncertain, _ = domainErr.Details["durability_uncertain"].(bool)
			}
			returnErr = joinSystemBinaryInstallError(
				returnErr,
				"close system binary installer descriptor",
				closeErr,
			)
			if changed {
				returnErr = annotateSystemBinaryReplacement(returnErr, durabilityUncertain)
			}
		}
	}()

	rootFD, err := deps.open(policy.rootPath, directoryInstallFlags, 0)
	if err != nil {
		return false, systemBinaryInstallError("open filesystem root", err)
	}
	retainedFDs = append(retainedFDs, rootFD)
	if err := verifySystemBinaryInstallDirectory(deps, rootFD, "/", policy); err != nil {
		return false, err
	}

	sourceFD, err := deps.open("/proc/self/exe", sourceInstallFlags, 0)
	if err != nil {
		return false, systemBinaryInstallError("open running process image", err)
	}
	retainedFDs = append(retainedFDs, sourceFD)
	sourceStat, err := inspectSystemBinaryInstallFile(deps, sourceFD, "running process image")
	if err != nil {
		return false, err
	}

	usrFD, err := openSystemBinaryInstallDirectory(deps, rootFD, "usr", false, policy)
	if err != nil {
		return false, err
	}
	retainedFDs = append(retainedFDs, usrFD)
	localFD, err := openSystemBinaryInstallDirectory(deps, usrFD, "local", true, policy)
	if err != nil {
		return false, err
	}
	retainedFDs = append(retainedFDs, localFD)
	binFD, err := openSystemBinaryInstallDirectory(deps, localFD, "bin", true, policy)
	if err != nil {
		return false, err
	}
	retainedFDs = append(retainedFDs, binFD)

	targetFD, targetStat, targetExists, err := inspectExistingSystemBinary(deps, binFD)
	if err != nil {
		return false, err
	}
	if targetExists {
		retainedFDs = append(retainedFDs, targetFD)
		if sourceStat.Dev == targetStat.Dev && sourceStat.Ino == targetStat.Ino &&
			targetStat.Uid == policy.expectedUID && targetStat.Gid == policy.expectedGID &&
			targetStat.Mode&07777 == infra.ExecutablePerm {
			return false, nil
		}
	}

	tempName, tempFD, err := createSystemBinaryTemp(deps, binFD)
	if err != nil {
		return false, err
	}
	tempOpen := true
	abort := func(cause error) (bool, error) {
		return false, abortSystemBinaryInstall(deps, binFD, tempFD, tempName, tempOpen, cause)
	}

	if err := copySystemBinaryImage(ctx, deps, sourceFD, tempFD); err != nil {
		return abort(systemBinaryInstallError("copy running process image", err))
	}
	if err := deps.fchown(tempFD, int(policy.expectedUID), int(policy.expectedGID)); err != nil {
		return abort(systemBinaryInstallError("set system binary owner", err))
	}
	if err := deps.fchmod(tempFD, uint32(infra.ExecutablePerm)); err != nil {
		return abort(systemBinaryInstallError("set system binary mode", err))
	}
	tempStat, err := verifyInstalledSystemBinaryDescriptor(deps, tempFD, policy)
	if err != nil {
		return abort(err)
	}
	if tempStat.Size != sourceStat.Size {
		return abort(systemBinaryInstallError(
			"verify copied system binary size",
			fmt.Errorf("got %d bytes, expected %d", tempStat.Size, sourceStat.Size),
		))
	}
	if err := deps.fsync(tempFD); err != nil {
		return abort(systemBinaryInstallError("sync system binary temporary file", err))
	}
	if err := deps.close(tempFD); err != nil {
		tempOpen = false
		return abort(systemBinaryInstallError("close system binary temporary file", err))
	}
	tempOpen = false

	if err := ctx.Err(); err != nil {
		return abort(systemBinaryInstallError("install system binary before replacement", err))
	}
	if err := deps.renameAt(binFD, tempName, binFD, systemBinaryName); err != nil {
		return abort(systemBinaryInstallError("atomically replace system binary", err))
	}
	changed = true
	if err := deps.fsync(binFD); err != nil {
		return changed, annotateSystemBinaryReplacement(
			systemBinaryInstallError("sync system binary directory", err),
			true,
		)
	}
	return changed, nil
}

func openSystemBinaryInstallDirectory(
	deps systemBinaryInstallDeps,
	parentFD int,
	name string,
	allowCreate bool,
	policy systemBinaryInstallPolicy,
) (int, error) {
	fd, err := deps.openAt(parentFD, name, directoryInstallFlags, 0)
	if err == nil {
		if verifyErr := verifySystemBinaryInstallDirectory(deps, fd, name, policy); verifyErr != nil {
			return -1, closeRejectedSystemBinaryDescriptor(deps, fd, verifyErr)
		}
		return fd, nil
	}
	if !errors.Is(err, unix.ENOENT) || !allowCreate {
		return -1, systemBinaryInstallError("open trusted system binary directory "+name, err)
	}
	mkdirErr := deps.mkdirAt(parentFD, name, uint32(infra.DirPerm))
	created := mkdirErr == nil
	if mkdirErr != nil && !errors.Is(mkdirErr, unix.EEXIST) {
		return -1, systemBinaryInstallError("create trusted system binary directory "+name, mkdirErr)
	}
	fd, err = deps.openAt(parentFD, name, directoryInstallFlags, 0)
	if err != nil {
		return -1, systemBinaryInstallError("open created system binary directory "+name, err)
	}
	if !created {
		if verifyErr := verifySystemBinaryInstallDirectory(deps, fd, name, policy); verifyErr != nil {
			return -1, closeRejectedSystemBinaryDescriptor(deps, fd, verifyErr)
		}
		return fd, nil
	}
	if err := deps.fchown(fd, int(policy.expectedUID), int(policy.expectedGID)); err != nil {
		return -1, closeRejectedSystemBinaryDescriptor(
			deps,
			fd,
			systemBinaryInstallError("set created system binary directory owner "+name, err),
		)
	}
	if err := deps.fchmod(fd, uint32(infra.DirPerm)); err != nil {
		return -1, closeRejectedSystemBinaryDescriptor(
			deps,
			fd,
			systemBinaryInstallError("set created system binary directory mode "+name, err),
		)
	}
	if err := verifyCreatedSystemBinaryInstallDirectory(deps, fd, name, policy); err != nil {
		return -1, closeRejectedSystemBinaryDescriptor(deps, fd, err)
	}
	if err := deps.fsync(fd); err != nil {
		return -1, closeRejectedSystemBinaryDescriptor(
			deps,
			fd,
			systemBinaryInstallError("sync created system binary directory "+name, err),
		)
	}
	if err := deps.fsync(parentFD); err != nil {
		return -1, closeRejectedSystemBinaryDescriptor(
			deps,
			fd,
			systemBinaryInstallError("sync parent of created system binary directory "+name, err),
		)
	}
	return fd, nil
}

func verifySystemBinaryInstallDirectory(
	deps systemBinaryInstallDeps,
	fd int,
	name string,
	policy systemBinaryInstallPolicy,
) error {
	var stat unix.Stat_t
	if err := deps.fstat(fd, &stat); err != nil {
		return systemBinaryInstallError("inspect trusted system binary directory "+name, err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		return systemBinaryInstallError(
			"inspect trusted system binary directory "+name,
			fmt.Errorf("must be a directory"),
		)
	}
	if stat.Uid != policy.expectedUID {
		return systemBinaryInstallError(
			"inspect trusted system binary directory "+name,
			fmt.Errorf("must be owned by root"),
		)
	}
	if stat.Mode&0022 != 0 {
		return systemBinaryInstallError(
			"inspect trusted system binary directory "+name,
			fmt.Errorf("must not be group/world writable"),
		)
	}
	return nil
}

func verifyCreatedSystemBinaryInstallDirectory(
	deps systemBinaryInstallDeps,
	fd int,
	name string,
	policy systemBinaryInstallPolicy,
) error {
	if err := verifySystemBinaryInstallDirectory(deps, fd, name, policy); err != nil {
		return err
	}
	var stat unix.Stat_t
	if err := deps.fstat(fd, &stat); err != nil {
		return systemBinaryInstallError("inspect created system binary directory "+name, err)
	}
	if stat.Gid != policy.expectedGID || stat.Mode&07777 != infra.DirPerm {
		return systemBinaryInstallError(
			"inspect created system binary directory "+name,
			fmt.Errorf("must be root:root with mode 0755"),
		)
	}
	return nil
}

func inspectSystemBinaryInstallFile(
	deps systemBinaryInstallDeps,
	fd int,
	description string,
) (unix.Stat_t, error) {
	var stat unix.Stat_t
	if err := deps.fstat(fd, &stat); err != nil {
		return unix.Stat_t{}, systemBinaryInstallError("inspect "+description, err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG {
		return unix.Stat_t{}, systemBinaryInstallError("inspect "+description, fmt.Errorf("must be a regular file"))
	}
	return stat, nil
}

func inspectExistingSystemBinary(
	deps systemBinaryInstallDeps,
	binFD int,
) (int, unix.Stat_t, bool, error) {
	targetFD, err := deps.openAt(binFD, systemBinaryName, targetInspectFlags, 0)
	if errors.Is(err, unix.ENOENT) {
		return -1, unix.Stat_t{}, false, nil
	}
	if err != nil {
		return -1, unix.Stat_t{}, false, systemBinaryInstallError("open existing system binary", err)
	}
	targetStat, err := inspectSystemBinaryInstallFile(deps, targetFD, "existing system binary")
	if err != nil {
		return -1, unix.Stat_t{}, false, closeRejectedSystemBinaryDescriptor(deps, targetFD, err)
	}
	return targetFD, targetStat, true, nil
}

func createSystemBinaryTemp(deps systemBinaryInstallDeps, binFD int) (string, int, error) {
	for range tempNameAttempts {
		name, err := deps.randomName()
		if err != nil {
			return "", -1, systemBinaryInstallError("generate system binary temporary name", err)
		}
		fd, err := deps.openAt(binFD, name, tempInstallFlags, 0600)
		if errors.Is(err, unix.EEXIST) {
			continue
		}
		if err != nil {
			return "", -1, systemBinaryInstallError("create system binary temporary file", err)
		}
		return name, fd, nil
	}
	return "", -1, systemBinaryInstallError(
		"create system binary temporary file",
		fmt.Errorf("exclusive name attempts exhausted"),
	)
}

func copySystemBinaryImage(
	ctx context.Context,
	deps systemBinaryInstallDeps,
	sourceFD int,
	tempFD int,
) error {
	buffer := make([]byte, systemBinaryCopyChunk)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		readCount, readErr := deps.read(sourceFD, buffer)
		if readCount < 0 || readCount > len(buffer) {
			return fmt.Errorf("invalid read count %d", readCount)
		}
		written := 0
		for written < readCount {
			if err := ctx.Err(); err != nil {
				return err
			}
			writeCount, writeErr := deps.write(tempFD, buffer[written:readCount])
			if writeCount < 0 || writeCount > readCount-written {
				return fmt.Errorf("invalid write count %d", writeCount)
			}
			written += writeCount
			if writeErr != nil {
				return writeErr
			}
			if writeCount == 0 {
				return io.ErrShortWrite
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return nil
			}
			return readErr
		}
		if readCount == 0 {
			return nil
		}
	}
}

func verifyInstalledSystemBinaryDescriptor(
	deps systemBinaryInstallDeps,
	fd int,
	policy systemBinaryInstallPolicy,
) (unix.Stat_t, error) {
	stat, err := inspectSystemBinaryInstallFile(deps, fd, "system binary temporary file")
	if err != nil {
		return unix.Stat_t{}, err
	}
	if stat.Uid != policy.expectedUID || stat.Gid != policy.expectedGID ||
		stat.Mode&07777 != infra.ExecutablePerm {
		return unix.Stat_t{}, systemBinaryInstallError(
			"inspect system binary temporary file",
			fmt.Errorf("must be root:root with mode 0755"),
		)
	}
	return stat, nil
}

func abortSystemBinaryInstall(
	deps systemBinaryInstallDeps,
	binFD int,
	tempFD int,
	tempName string,
	tempOpen bool,
	cause error,
) error {
	result := cause
	if tempOpen {
		if err := deps.close(tempFD); err != nil {
			result = joinSystemBinaryInstallError(result, "close incomplete system binary temporary file", err)
		}
	}
	if err := deps.unlinkAt(binFD, tempName, 0); err != nil {
		result = joinSystemBinaryInstallError(result, "remove incomplete system binary temporary file", err)
	}
	return result
}

func closeRejectedSystemBinaryDescriptor(
	deps systemBinaryInstallDeps,
	fd int,
	cause error,
) error {
	if err := deps.close(fd); err != nil {
		return joinSystemBinaryInstallError(cause, "close rejected system binary descriptor", err)
	}
	return cause
}

func closeSystemBinaryDescriptors(deps systemBinaryInstallDeps, fds []int) error {
	var result error
	for index := len(fds) - 1; index >= 0; index-- {
		if err := deps.close(fds[index]); err != nil {
			result = errors.Join(result, fmt.Errorf("fd %d: %w", fds[index], err))
		}
	}
	return result
}

func guardSystemBinaryInstallSudoers(deps systemBinaryInstallDeps) error {
	path := infra.SudoersDropInPath()
	content, err := deps.readSudoers(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return systemBinaryInstallError("read managed sudoers policy", err)
	}
	return validateSystemBinaryInstallSudoers(string(content))
}

func validateSystemBinaryInstallSudoers(content string) error {
	prefix := fmt.Sprintf("%%%s ALL=(root) NOPASSWD:", infra.MVMUnixGroup)
	canonicalWildcard := infra.SystemBinaryPath + " *"
	markerWildcard := infra.SystemBinaryPath + " " + infra.PrivilegedProtocolMarker + " *"
	for index, rawLine := range strings.Split(content, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") &&
			!strings.HasPrefix(line, "#include") &&
			!strings.HasPrefix(line, "#includedir") {
			continue
		}
		if !strings.HasPrefix(line, prefix) {
			return unrecognizedSystemBinaryInstallSudoers(index+1, line)
		}
		commandList := strings.TrimSpace(strings.TrimPrefix(line, prefix))
		if commandList == "" {
			return unrecognizedSystemBinaryInstallSudoers(index+1, line)
		}
		for _, rawCommand := range strings.Split(commandList, ",") {
			command := strings.TrimSpace(rawCommand)
			if command == canonicalWildcard || command == markerWildcard ||
				slices.Contains(infra.PrivilegedBinariesOrdered[:], command) {
				continue
			}
			return unrecognizedSystemBinaryInstallSudoers(index+1, line)
		}
	}
	return nil
}

func unrecognizedSystemBinaryInstallSudoers(lineNumber int, line string) *errs.DomainError {
	// EUID 0 only identifies the resulting process identity. It cannot prove
	// that sudo required a password rather than honoring an imported or indirect rule.
	return errs.New(
		errs.CodePrivilegeSudoers,
		fmt.Sprintf(
			"refusing to install %s because %s contains unrecognized active syntax on line %d; "+
				"the process cannot prove whether sudo password authentication was used. "+
				"In an authenticated root session remove the legacy policy, then run "+
				"'sudo <trusted-mvm-binary> host install-system', followed by 'sudo %s host init'",
			infra.SystemBinaryPath,
			infra.SudoersDropInPath(),
			lineNumber,
			infra.SystemBinaryPath,
		),
		errs.WithClass(errs.ClassNeedsInteraction),
		errs.WithDetails(map[string]any{"sudoers_line": lineNumber, "sudoers_syntax": line}),
	)
}

func annotateSystemBinaryReplacement(err error, durabilityUncertain bool) *errs.DomainError {
	domainErr := errs.AsDomainError(err)
	if domainErr == nil {
		domainErr = systemBinaryInstallError("system binary replacement", err)
	}
	if domainErr.Details == nil {
		domainErr.Details = make(map[string]any)
	}
	domainErr.Details["system_binary_replaced"] = true
	if durabilityUncertain {
		domainErr.Details["durability_uncertain"] = true
	}
	return domainErr
}

func systemBinaryInstallError(message string, err error) *errs.DomainError {
	return errs.WrapMsg(
		errs.CodeHostInitFailed,
		message+": "+err.Error(),
		err,
		errs.WithClass(errs.ClassInternal),
	)
}

func joinSystemBinaryInstallError(primary error, message string, err error) *errs.DomainError {
	if primary == nil {
		return systemBinaryInstallError(message, err)
	}
	return errs.WrapMsg(
		errs.CodeHostInitFailed,
		primary.Error()+"; "+message+": "+err.Error(),
		errors.Join(primary, err),
		errs.WithClass(errs.ClassInternal),
	)
}

func randomSystemBinaryTempName() (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	return ".mvm-install-" + hex.EncodeToString(random[:]) + ".tmp", nil
}

func realSystemBinaryInstallDeps() systemBinaryInstallDeps {
	return systemBinaryInstallDeps{
		effectiveUID: os.Geteuid,
		readSudoers:  os.ReadFile,
		open:         unix.Open,
		openAt:       unix.Openat,
		mkdirAt:      unix.Mkdirat,
		fstat:        unix.Fstat,
		fchown:       unix.Fchown,
		fchmod:       unix.Fchmod,
		read:         unix.Read,
		write:        unix.Write,
		fsync:        unix.Fsync,
		close:        unix.Close,
		renameAt:     unix.Renameat,
		unlinkAt:     unix.Unlinkat,
		randomName:   randomSystemBinaryTempName,
	}
}
