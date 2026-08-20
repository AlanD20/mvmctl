package privileged

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"golang.org/x/sys/unix"

	"mvmctl/internal/infra"
	"mvmctl/pkg/errs"
)

type executableDeps struct {
	executable func() (string, error)
	open       func(string, int, uint32) (int, error)
	openAt     func(int, string, int, uint32) (int, error)
	fstat      func(int, *unix.Stat_t) error
	close      func(int) error
}

type verifiedExecutable struct {
	deps executableDeps
	fds  []int
}

func (v *verifiedExecutable) close() error {
	var result error
	for index := len(v.fds) - 1; index >= 0; index-- {
		if err := v.deps.close(v.fds[index]); err != nil {
			result = errors.Join(result, err)
		}
	}
	v.fds = nil
	return result
}

func verifySystemExecutable(deps executableDeps) (*verifiedExecutable, error) {
	executable, err := deps.executable()
	if err != nil {
		return nil, errs.New(
			errs.CodePrivilegeRequired,
			"resolve running executable: "+err.Error(),
		)
	}
	if executable != infra.SystemBinaryPath {
		return nil, errs.New(
			errs.CodePrivilegeRequired,
			fmt.Sprintf("privileged protocol must run from %s", infra.SystemBinaryPath),
		)
	}

	pinned := &verifiedExecutable{deps: deps}
	verified := false
	defer func() {
		if !verified {
			if err := pinned.close(); err != nil {
				slog.Warn("close rejected privileged executable descriptors", "error", err)
			}
		}
	}()

	directoryFlags := unix.O_PATH | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW
	rootFD, err := deps.open("/", directoryFlags, 0)
	if err != nil {
		return nil, errs.New(errs.CodePrivilegeRequired, "open trusted executable root: "+err.Error())
	}
	pinned.fds = append(pinned.fds, rootFD)
	if err := verifyTrustedDirectoryDescriptor(deps, rootFD, "/"); err != nil {
		return nil, err
	}

	parts := strings.Split(strings.TrimPrefix(infra.SystemBinaryPath, "/"), "/")
	if len(parts) < 2 {
		return nil, errs.New(errs.CodeInternal, "canonical system executable path is invalid")
	}
	parentFD := rootFD
	for _, component := range parts[:len(parts)-1] {
		fd, err := deps.openAt(parentFD, component, directoryFlags, 0)
		if err != nil {
			return nil, errs.New(
				errs.CodePrivilegeRequired,
				fmt.Sprintf("open trusted executable path component %s: %v", component, err),
			)
		}
		pinned.fds = append(pinned.fds, fd)
		if err := verifyTrustedDirectoryDescriptor(deps, fd, component); err != nil {
			return nil, err
		}
		parentFD = fd
	}

	executableFlags := unix.O_PATH | unix.O_CLOEXEC | unix.O_NOFOLLOW
	canonicalFD, err := deps.openAt(parentFD, parts[len(parts)-1], executableFlags, 0)
	if err != nil {
		return nil, errs.New(
			errs.CodePrivilegeRequired,
			"open canonical system executable: "+err.Error(),
		)
	}
	pinned.fds = append(pinned.fds, canonicalFD)
	canonicalStat, err := verifyCanonicalExecutableDescriptor(deps, canonicalFD)
	if err != nil {
		return nil, err
	}

	runningFD, err := deps.open("/proc/self/exe", unix.O_PATH|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, errs.New(errs.CodePrivilegeRequired, "open running process image: "+err.Error())
	}
	pinned.fds = append(pinned.fds, runningFD)
	var runningStat unix.Stat_t
	if err := deps.fstat(runningFD, &runningStat); err != nil {
		return nil, errs.New(
			errs.CodePrivilegeRequired,
			"inspect running process image descriptor: "+err.Error(),
		)
	}
	if canonicalStat.Dev != runningStat.Dev || canonicalStat.Ino != runningStat.Ino {
		return nil, errs.New(
			errs.CodePrivilegeRequired,
			"canonical system executable does not match the running process image",
		)
	}

	verified = true
	return pinned, nil
}

func verifyTrustedDirectoryDescriptor(deps executableDeps, fd int, component string) error {
	var stat unix.Stat_t
	if err := deps.fstat(fd, &stat); err != nil {
		return errs.New(
			errs.CodePrivilegeRequired,
			fmt.Sprintf("inspect trusted executable descriptor %s: %v", component, err),
		)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		return errs.New(
			errs.CodePrivilegeRequired,
			fmt.Sprintf("trusted executable path %s must be a directory", component),
		)
	}
	if stat.Uid != 0 {
		return errs.New(
			errs.CodePrivilegeRequired,
			fmt.Sprintf("trusted executable path %s must be owned by root", component),
		)
	}
	if stat.Mode&0022 != 0 {
		return errs.New(
			errs.CodePrivilegeRequired,
			fmt.Sprintf("trusted executable path %s must not be group/world writable", component),
		)
	}
	return nil
}

func verifyCanonicalExecutableDescriptor(deps executableDeps, fd int) (unix.Stat_t, error) {
	var stat unix.Stat_t
	if err := deps.fstat(fd, &stat); err != nil {
		return unix.Stat_t{}, errs.New(
			errs.CodePrivilegeRequired,
			"inspect canonical system executable descriptor: "+err.Error(),
		)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG {
		return unix.Stat_t{}, errs.New(
			errs.CodePrivilegeRequired,
			fmt.Sprintf("system executable %s must be a regular file", infra.SystemBinaryPath),
		)
	}
	if stat.Uid != 0 || stat.Gid != 0 {
		return unix.Stat_t{}, errs.New(
			errs.CodePrivilegeRequired,
			fmt.Sprintf("system executable %s must be owned by root:root", infra.SystemBinaryPath),
		)
	}
	if stat.Mode&07777 != infra.ExecutablePerm {
		return unix.Stat_t{}, errs.New(
			errs.CodePrivilegeRequired,
			fmt.Sprintf("system executable %s must have mode 0755", infra.SystemBinaryPath),
		)
	}
	return stat, nil
}

func realExecutableDeps() executableDeps {
	return executableDeps{
		executable: os.Executable,
		open:       unix.Open,
		openAt:     unix.Openat,
		fstat:      unix.Fstat,
		close:      unix.Close,
	}
}
