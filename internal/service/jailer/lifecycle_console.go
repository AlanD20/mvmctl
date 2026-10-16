package jailer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"

	"golang.org/x/sys/unix"

	"mvmctl/internal/lib/system"
	"mvmctl/internal/service/console"
)

const launchConsoleMarker = "__mvm_launch_console_v1"

const (
	launchConsolePTYFD             = 5
	launchConsolePacketLimit       = 1024
	launchConsoleRuntimeDescriptor = 0
	launchConsoleLogDescriptor     = 1
)

type launchConsolePlan struct {
	Schema uint32 `json:"schema"`
	VMID   string `json:"vm_id"`
	UID    uint32 `json:"uid"`
	GID    uint32 `json:"gid"`
}

// IsLaunchConsoleInvocation reserves the inherited-descriptor relay route before app initialization.
// Extra arguments still select this route so malformed invocations cannot fall through to the CLI.
func IsLaunchConsoleInvocation(args []string) bool {
	return len(args) != 0 && args[0] == launchConsoleMarker
}

// RunLaunchConsole runs only the dropped-caller relay created by the authenticated root receiver.
// CRITICAL: The fixed inherited channels must prove their root parent before any effects. No CLI
// argument selects an executable, descriptor, PID, cache path, socket, or log destination.
func RunLaunchConsole(ctx context.Context, args []string) (returnErr error) {
	if ctx == nil || len(args) != 1 || args[0] != launchConsoleMarker ||
		os.Getuid() == 0 || os.Geteuid() != os.Getuid() || os.Getgid() == 0 || os.Getegid() != os.Getgid() {
		return processAuthorityError("", "invalid dropped-caller console invocation", nil)
	}
	groups, err := os.Getgroups()
	if err != nil || len(groups) != 0 {
		return processAuthorityError("", "console supplementary groups must be empty", err)
	}
	setupCtx, cancel := context.WithTimeout(ctx, launchChildSetupBudget)
	defer cancel()
	// Console deliberately uses the same fixed gate/setup slots as the launch child; this
	// authentication admits only inherited Unix sockets created by this process's root parent.
	if err := authenticateLaunchChildParent(setupCtx); err != nil {
		return err
	}
	gate := os.NewFile(launchChildGateFD, "console-child-gate")
	setup := os.NewFile(launchChildSetupFD, "console-child-setup")
	ptyFile := os.NewFile(launchConsolePTYFD, "console-child-pty")
	defer func() {
		returnErr = errors.Join(returnErr, gate.Close(), setup.Close(), ptyFile.Close())
	}()
	if _, err := unix.FcntlInt(launchConsolePTYFD, unix.F_SETFD, unix.FD_CLOEXEC); err != nil {
		return err
	}
	if _, err := unix.IoctlGetInt(launchConsolePTYFD, unix.TIOCGPTN); err != nil {
		return processAuthorityError("", "console inherited descriptor is not a PTY master", err)
	}
	executable, err := system.PinSystemExecutable(setupCtx)
	if err != nil {
		return err
	}
	if err := executable.Release(setupCtx); err != nil {
		return err
	}
	// Registration needs a live challenge acknowledgement before durable launching exists.
	// Setup then transfers pinned outputs, still behind the external-effect release gate.
	challenge := make([]byte, blockedLaunchChallengeBytes)
	if err := readBlockedLaunchGateFull(setupCtx, launchChildGateFD, challenge); err != nil {
		return processAuthorityError("", "read console launch challenge", err)
	}
	if err := writeBlockedLaunchGateFull(setupCtx, launchChildGateFD, challenge); err != nil {
		return processAuthorityError("", "acknowledge console launch challenge", err)
	}
	plan, fds, err := receiveLaunchConsolePlan(setupCtx)
	if err != nil {
		return err
	}
	runtimeDirectory := os.NewFile(uintptr(fds[launchConsoleRuntimeDescriptor]), "console-runtime-directory")
	logFile := os.NewFile(uintptr(fds[launchConsoleLogDescriptor]), "console-persistent-log")
	defer func() {
		returnErr = errors.Join(returnErr, runtimeDirectory.Close(), logFile.Close())
	}()
	if err := writeBlockedLaunchGateFull(setupCtx, launchChildSetupFD, []byte{1}); err != nil {
		return processAuthorityError(plan.VMID, "acknowledge console descriptor setup", err)
	}
	release := []byte{0}
	if err := readBlockedLaunchGateFull(setupCtx, launchChildGateFD, release); err != nil {
		return processAuthorityError(plan.VMID, "console release gate closed before handoff", err)
	}
	if release[0] != blockedLaunchReleaseByte {
		return processAuthorityError(plan.VMID, "invalid console release byte", nil)
	}
	unix.Umask(0077)
	return console.RunDescriptors(ctx, ptyFile, logFile, runtimeDirectory, setup)
}

func receiveLaunchConsolePlan(ctx context.Context) (_ launchConsolePlan, descriptors []int, returnErr error) {
	packet := make([]byte, launchConsolePacketLimit+1)
	control := make([]byte, unix.CmsgSpace(2*4))
	defer func() {
		if returnErr != nil {
			for _, fd := range descriptors {
				returnErr = errors.Join(returnErr, unix.Close(fd))
			}
		}
	}()
	for {
		if err := ctx.Err(); err != nil {
			return launchConsolePlan{}, descriptors, err
		}
		n, controlN, flags, _, err := unix.Recvmsg(launchChildSetupFD, packet, control,
			unix.MSG_CMSG_CLOEXEC|unix.MSG_DONTWAIT)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EWOULDBLOCK) {
			if err := waitBlockedLaunchGate(ctx, launchChildSetupFD, unix.POLLIN); err != nil {
				return launchConsolePlan{}, descriptors, err
			}
			continue
		}
		if err != nil {
			return launchConsolePlan{}, descriptors, err
		}
		messages, parseErr := unix.ParseSocketControlMessage(control[:controlN])
		for _, message := range messages {
			if message.Header.Level != unix.SOL_SOCKET || message.Header.Type != unix.SCM_RIGHTS {
				parseErr = errors.Join(parseErr, instanceValidationError("unexpected console ancillary message"))
				continue
			}
			fds, fdErr := unix.ParseUnixRights(&message)
			descriptors = append(descriptors, fds...)
			parseErr = errors.Join(parseErr, fdErr)
		}
		if parseErr != nil || len(messages) != 1 || len(descriptors) != 2 ||
			flags&(unix.MSG_TRUNC|unix.MSG_CTRUNC) != 0 || n == 0 || n > launchConsolePacketLimit {
			return launchConsolePlan{}, descriptors, processAuthorityError("", "invalid console setup packet", parseErr)
		}
		var plan launchConsolePlan
		decoder := json.NewDecoder(bytes.NewReader(packet[:n]))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&plan); err != nil {
			return launchConsolePlan{}, descriptors, processAuthorityError("", "decode console setup", err)
		}
		if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
			return launchConsolePlan{}, descriptors, processAuthorityError("", "console setup has trailing data", err)
		}
		if plan.Schema != 1 || !vmIDPattern.MatchString(plan.VMID) ||
			plan.UID != uint32(os.Getuid()) || plan.GID != uint32(os.Getgid()) {
			return launchConsolePlan{}, descriptors, instanceValidationError(
				"console setup identity does not match caller",
			)
		}
		if err := admitLaunchConsoleDescriptors(ctx, plan, descriptors); err != nil {
			return launchConsolePlan{}, descriptors, err
		}
		return plan, descriptors, nil
	}
}

func admitLaunchConsoleDescriptors(ctx context.Context, plan launchConsolePlan, fds []int) error {
	for index, fd := range fds {
		if err := ctx.Err(); err != nil {
			return err
		}
		var stat unix.Stat_t
		if err := unix.Fstat(fd, &stat); err != nil {
			return err
		}
		wantMode := uint32(unix.S_IFDIR | 0700)
		if index == launchConsoleLogDescriptor {
			wantMode = unix.S_IFREG | 0600
		}
		if stat.Uid != plan.UID || stat.Gid != plan.GID || stat.Mode != wantMode || stat.Nlink == 0 {
			return processAuthorityError(plan.VMID, "console descriptor identity is unsafe", nil)
		}
		if index == launchConsoleLogDescriptor {
			flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
			if err != nil {
				return err
			}
			if flags&unix.O_ACCMODE != unix.O_RDWR || stat.Nlink != 1 {
				return processAuthorityError(plan.VMID, "console log descriptor is not a pinned writable file", nil)
			}
			if _, err := unix.FcntlInt(uintptr(fd), unix.F_SETFL, flags|unix.O_APPEND); err != nil {
				return err
			}
		}
	}
	return nil
}
