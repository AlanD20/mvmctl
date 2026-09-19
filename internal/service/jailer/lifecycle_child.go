package jailer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"mvmctl/internal/infra"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/lib/system"
)

const launchChildMarker = "__mvm_jailer_child_v1"

const (
	launchChildGateFD      = 3
	launchChildSetupFD     = 4
	launchChildPacketLimit = 32 * 1024
	launchChildSetupBudget = 30 * time.Second
)

// IsLaunchChildInvocation reserves the root-parent-only child route before ordinary initialization.
func IsLaunchChildInvocation(args []string) bool {
	return len(args) != 0 && args[0] == launchChildMarker
}

type launchChildPlan struct {
	Schema    uint32               `json:"schema"`
	VMID      string               `json:"vm_id"`
	UID       uint32               `json:"uid"`
	GID       uint32               `json:"gid"`
	Release   ReleaseSlot          `json:"release"`
	Limits    model.VMCgroupLimits `json:"limits"`
	CloudInit bool                 `json:"cloud_init"`
	Metrics   bool                 `json:"metrics"`
	Console   bool                 `json:"console"`
	Volumes   []VMLaunchVolume     `json:"volumes,omitempty"`
}

// The descriptor ordering is fixed by the root-built launch plan; none is selected by caller argv.
const (
	childJailParent = iota
	childRootfs
	childKernel
	childConfig
	childRuntime
	childLog
	childConsoleLog
	childBaseDescriptors
)

// RunLaunchChild executes only an authenticated receiver's inherited launch capability.
// CRITICAL: This route has no sudo action and accepts no CLI-selected path, descriptor, or argv.
func RunLaunchChild(ctx context.Context, args []string) (returnErr error) {
	if len(args) != 1 || args[0] != launchChildMarker || os.Geteuid() != 0 || os.Getegid() != 0 {
		return processAuthorityError("", "invalid root launch child invocation", nil)
	}
	if ctx == nil {
		return processAuthorityError("", "root launch child context is required", nil)
	}
	// This dedicated process exits after this function returns. Never return a thread that has
	// entered the VM network namespace to Go's scheduler.
	runtime.LockOSThread()
	setupCtx, cancel := context.WithTimeout(ctx, launchChildSetupBudget)
	defer cancel()
	if err := authenticateLaunchChildParent(setupCtx); err != nil {
		return err
	}
	executable, err := system.PinSystemExecutable(setupCtx)
	if err != nil {
		return err
	}
	if err := executable.Release(setupCtx); err != nil {
		return err
	}
	defer func() {
		returnErr = errors.Join(returnErr, unix.Close(launchChildGateFD), unix.Close(launchChildSetupFD))
	}()
	released, err := awaitBlockedLaunchRelease(setupCtx, realBlockedLaunchGateDeps(), launchChildGateFD)
	if err != nil {
		return processAuthorityError("", "await authenticated launch release", err)
	}
	if !released {
		return processAuthorityError("", "root launch parent closed the release gate", nil)
	}
	plan, descriptors, err := receiveLaunchChildPlan(setupCtx)
	if err != nil {
		return err
	}
	defer func() {
		for _, fd := range descriptors {
			returnErr = errors.Join(returnErr, unix.Close(fd))
		}
	}()
	namespaceFD, err := receiveLaunchChildNamespace(setupCtx)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, unix.Close(namespaceFD)) }()
	if err := prepareLaunchChildMounts(setupCtx, plan, descriptors); err != nil {
		return err
	}
	return execLaunchChildJailer(setupCtx, plan, descriptors, namespaceFD)
}

func authenticateLaunchChildParent(ctx context.Context) error {
	parent := os.Getppid()
	if parent <= 1 {
		return processAuthorityError("", "root launch parent is absent", nil)
	}
	for _, channel := range []struct{ fd, kind int }{
		{launchChildGateFD, unix.SOCK_STREAM}, {launchChildSetupFD, unix.SOCK_SEQPACKET},
	} {
		if err := ctx.Err(); err != nil {
			return err
		}
		kind, err := unix.GetsockoptInt(channel.fd, unix.SOL_SOCKET, unix.SO_TYPE)
		if err != nil || kind != channel.kind {
			return processAuthorityError("", "launch child inherited channel has invalid type", err)
		}
		local, err := unix.Getsockname(channel.fd)
		if err != nil {
			return processAuthorityError("", "inspect launch child inherited channel", err)
		}
		if _, ok := local.(*unix.SockaddrUnix); !ok {
			return processAuthorityError("", "launch child inherited channel is not Unix", nil)
		}
		peer, err := unix.GetsockoptUcred(channel.fd, unix.SOL_SOCKET, unix.SO_PEERCRED)
		if err != nil || peer == nil || peer.Uid != 0 || peer.Gid != 0 || int(peer.Pid) != parent {
			return processAuthorityError("", "launch child channel is not owned by its root parent", err)
		}
		if _, err := unix.FcntlInt(uintptr(channel.fd), unix.F_SETFD, unix.FD_CLOEXEC); err != nil {
			return processAuthorityError("", "seal launch child inherited channel", err)
		}
	}
	return nil
}

func receiveLaunchChildPlan(ctx context.Context) (_ launchChildPlan, descriptors []int, returnErr error) {
	packet := make([]byte, launchChildPacketLimit+1)
	control := make([]byte, unix.CmsgSpace((childBaseDescriptors+3+managedMMIOMaxInterruptSlots)*4))
	for {
		if err := ctx.Err(); err != nil {
			return launchChildPlan{}, nil, err
		}
		n, controlN, flags, _, err := unix.Recvmsg(launchChildSetupFD, packet, control,
			unix.MSG_CMSG_CLOEXEC|unix.MSG_DONTWAIT)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EWOULDBLOCK) {
			if err := waitBlockedLaunchGate(ctx, launchChildSetupFD, unix.POLLIN); err != nil {
				return launchChildPlan{}, nil, err
			}
			continue
		}
		if err != nil {
			return launchChildPlan{}, nil, err
		}
		messages, parseErr := unix.ParseSocketControlMessage(control[:controlN])
		for _, message := range messages {
			if message.Header.Level != unix.SOL_SOCKET || message.Header.Type != unix.SCM_RIGHTS {
				parseErr = errors.Join(parseErr, fmt.Errorf("unexpected launch child ancillary message"))
				continue
			}
			fds, fdErr := unix.ParseUnixRights(&message)
			descriptors = append(descriptors, fds...)
			parseErr = errors.Join(parseErr, fdErr)
		}
		defer func() {
			if returnErr != nil {
				for _, fd := range descriptors {
					returnErr = errors.Join(returnErr, unix.Close(fd))
				}
			}
		}()
		if parseErr != nil || len(messages) != 1 || flags&(unix.MSG_TRUNC|unix.MSG_CTRUNC) != 0 ||
			n == 0 || n > launchChildPacketLimit {
			return launchChildPlan{}, descriptors, processAuthorityError(
				"",
				"invalid launch child setup packet",
				parseErr,
			)
		}
		var plan launchChildPlan
		decoder := json.NewDecoder(bytes.NewReader(packet[:n]))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&plan); err != nil {
			return launchChildPlan{}, descriptors, processAuthorityError("", "decode launch child setup", err)
		}
		if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
			return launchChildPlan{}, descriptors, processAuthorityError(
				"",
				"launch child setup has trailing data",
				err,
			)
		}
		if err := validateLaunchChildPlan(plan, descriptors); err != nil {
			return launchChildPlan{}, descriptors, err
		}
		return plan, descriptors, nil
	}
}

func validateLaunchChildPlan(plan launchChildPlan, descriptors []int) error {
	if len(plan.Volumes) > managedMMIOMaxInterruptSlots {
		return instanceValidationError("launch child volume descriptor selection exceeds the MMIO pool")
	}
	if len(plan.Volumes) != 0 && plan.Release.Version != "1.16.1" {
		return instanceValidationError("launch child volume device release is unsupported")
	}
	volumes := make([]vmVolumeResourceSpec, 0, len(plan.Volumes))
	for _, volume := range plan.Volumes {
		if err := volume.Validate(); err != nil {
			return err
		}
		volumes = append(volumes, volume.private())
	}
	if err := validateVMVolumeResourceSelection(volumes); err != nil {
		return err
	}
	if plan.Schema != 1 || !vmIDPattern.MatchString(plan.VMID) {
		return instanceValidationError("launch child setup identity is invalid")
	}
	if err := validateInstanceCaller(instanceCaller{uid: plan.UID, gid: plan.GID}); err != nil {
		return err
	}
	if err := validateReleaseSlotValue(
		releaseSlot{version: plan.Release.Version, architecture: plan.Release.Architecture},
	); err != nil {
		return instanceValidationError(err.Error())
	}
	if err := validateManagedVMCgroupLimits(plan.Limits); err != nil {
		return err
	}
	want := childBaseDescriptors + len(plan.Volumes)
	if plan.CloudInit {
		want++
	}
	if plan.Metrics {
		want++
	}
	if plan.Console {
		want++
	}
	if len(descriptors) != want {
		return instanceValidationError("launch child setup has unexpected descriptor count")
	}
	return nil
}

func prepareLaunchChildMounts(ctx context.Context, plan launchChildPlan, fds []int) (returnErr error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	// This process was clone-created with CLONE_NEWNS. Propagation is severed before any mount
	// effect so its fixed destination attachments cannot propagate back into the host namespace.
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		return processAuthorityError(plan.VMID, "make launch mount propagation private", err)
	}
	unix.Umask(0)
	var parent unix.Stat_t
	if err := unix.Fstat(fds[childJailParent], &parent); err != nil {
		return err
	}
	if parent.Mode != unix.S_IFDIR|0700 || parent.Uid != 0 || parent.Gid != 0 {
		return processAuthorityError(plan.VMID, "invalid retained jail parent", nil)
	}
	// CRITICAL: The root child alone creates the fresh VM tree after the parent's final
	// absence check. EEXIST is never adopted, even when an existing tree appears correctly owned.
	if err := unix.Mkdirat(fds[childJailParent], plan.VMID, 0700); err != nil {
		return processAuthorityError(plan.VMID, "create exclusive jail VM directory", err)
	}
	vmFD, err := unix.Openat(
		fds[childJailParent],
		plan.VMID,
		unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW,
		0,
	)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, unix.Close(vmFD)) }()
	if err := unix.Mkdirat(vmFD, "root", 0755); err != nil {
		return err
	}
	rootFD, err := unix.Openat(vmFD, "root", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, unix.Close(rootFD)) }()
	if err := unix.Mkdirat(rootFD, "run", 0755); err != nil {
		return err
	}
	runFD, err := unix.Openat(rootFD, "run", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, unix.Close(runFD)) }()
	if err := unix.Mkdirat(runFD, "mvm", 0755); err != nil {
		return err
	}
	mvmFD, err := unix.Openat(runFD, "mvm", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, unix.Close(mvmFD)) }()
	mounts := []struct {
		source, parent      int
		name                string
		readOnly, directory bool
	}{
		{fds[childRootfs], rootFD, "rootfs", false, false},
		{fds[childKernel], rootFD, "kernel", true, false},
		{fds[childConfig], mvmFD, infra.VMFirecrackerConfigFilename, true, false},
		{fds[childRuntime], mvmFD, "runtime", false, true},
		{fds[childLog], mvmFD, infra.VMFirecrackerLogFilename, false, false},
	}
	extra := childBaseDescriptors
	if plan.CloudInit {
		mounts = append(mounts, struct {
			source, parent      int
			name                string
			readOnly, directory bool
		}{
			fds[extra], rootFD, infra.VMCloudInitISOFilename, true, false})
		extra++
	}
	if plan.Metrics {
		mounts = append(mounts, struct {
			source, parent      int
			name                string
			readOnly, directory bool
		}{
			fds[extra], mvmFD, infra.VMFirecrackerMetricsFilename, false, false})
		extra++
	}
	for _, mount := range mounts {
		if err := mountLaunchChildResource(
			ctx,
			mount.source,
			mount.parent,
			mount.name,
			mount.readOnly,
			mount.directory,
		); err != nil {
			return processAuthorityError(plan.VMID, "mount fixed jail resource "+mount.name, err)
		}
	}
	if err := mountLaunchChildVolumes(ctx, plan, rootFD, fds[extra:extra+len(plan.Volumes)]); err != nil {
		return err
	}
	return nil
}

func mountLaunchChildResource(
	ctx context.Context,
	source, parent int,
	name string,
	readOnly, directory bool,
) (returnErr error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	var target int
	var err error
	if directory {
		if err := unix.Mkdirat(parent, name, 0700); err != nil {
			return err
		}
		target, err = unix.Openat(parent, name, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	} else {
		target, err = unix.Openat(
			parent,
			name,
			unix.O_RDONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW,
			0600,
		)
	}
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, unix.Close(target)) }()
	mountFD, err := unix.OpenTree(source, "", uint(unix.OPEN_TREE_CLONE|unix.OPEN_TREE_CLOEXEC|unix.AT_EMPTY_PATH))
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, unix.Close(mountFD)) }()
	attributes := uint64(unix.MOUNT_ATTR_NOSUID | unix.MOUNT_ATTR_NODEV | unix.MOUNT_ATTR_NOEXEC)
	if readOnly {
		attributes |= unix.MOUNT_ATTR_RDONLY
	}
	if err := unix.MountSetattr(mountFD, "", unix.AT_EMPTY_PATH, &unix.MountAttr{Attr_set: attributes}); err != nil {
		return err
	}
	return unix.MoveMount(mountFD, "", target, "", unix.MOVE_MOUNT_F_EMPTY_PATH|unix.MOVE_MOUNT_T_EMPTY_PATH)
}

func execLaunchChildJailer(ctx context.Context, plan launchChildPlan, fds []int, namespaceFD int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// Jailer's early descriptor sweep closes inherited FDs. Pin the namespace in this child's
	// setup first; the /proc/self/ns/net locator then names only that already-selected namespace.
	if err := unix.Setns(namespaceFD, unix.CLONE_NEWNET); err != nil {
		return err
	}
	output := fds[childConsoleLog]
	if plan.Console {
		output = fds[len(fds)-1]
	}
	for _, target := range []int{1, 2} {
		if err := unix.Dup3(output, target, 0); err != nil {
			return err
		}
	}
	if plan.Console {
		if err := unix.Dup3(output, 0, 0); err != nil {
			return err
		}
	}
	unix.Umask(0077)
	base := filepath.Join(infra.TrustedBinaryRoot, plan.Release.Architecture, plan.Release.Version)
	jailerPath := filepath.Join(base, "jailer")
	limits := plan.Limits
	args := []string{jailerPath, "--id", plan.VMID, "--exec-file", filepath.Join(base, "firecracker"),
		"--uid", strconv.FormatUint(uint64(plan.UID), 10), "--gid", strconv.FormatUint(uint64(plan.GID), 10),
		"--chroot-base-dir", infra.JailerChrootBase, "--netns", "/proc/self/ns/net",
		"--cgroup-version", "2", "--parent-cgroup", infra.JailerCgroupParent,
		"--cgroup", fmt.Sprintf("cpu.max=%d %d", limits.CPUQuotaMicros, limits.CPUPeriodMicros),
		"--cgroup", fmt.Sprintf("cpu.weight=%d", limits.CPUWeight),
		"--cgroup", fmt.Sprintf("memory.high=%d", limits.MemoryHighBytes),
		"--cgroup", "memory.max=max",
		"--cgroup", fmt.Sprintf("memory.swap.max=%d", limits.SwapMaxBytes),
		"--cgroup", fmt.Sprintf("pids.max=%d", limits.PIDsMax), "--",
		"--api-sock", managedVMRuntimeMountPath + "/" + infra.VMFirecrackerAPISocketFilename,
		"--config-file", managedVMPersistentMountPath + "/" + infra.VMFirecrackerConfigFilename}
	return syscall.Exec(jailerPath, args, []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C", "LC_ALL=C"})
}

func receiveLaunchChildNamespace(ctx context.Context) (_ int, returnErr error) {
	for {
		if err := ctx.Err(); err != nil {
			return -1, err
		}
		n, err := unix.SendmsgN(launchChildSetupFD, []byte(system.JailerChildResourcesReady), nil, nil,
			unix.MSG_DONTWAIT|unix.MSG_NOSIGNAL)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EWOULDBLOCK) {
			if err := waitBlockedLaunchGate(ctx, launchChildSetupFD, unix.POLLOUT); err != nil {
				return -1, err
			}
			continue
		}
		if err != nil {
			return -1, err
		}
		if n != len(system.JailerChildResourcesReady) {
			return -1, fmt.Errorf("incomplete child resource acknowledgement")
		}
		break
	}
	packet := make([]byte, len(system.JailerChildNamespacePacket)+1)
	control := make([]byte, unix.CmsgSpace(4))
	for {
		if err := ctx.Err(); err != nil {
			return -1, err
		}
		n, controlN, flags, _, err := unix.Recvmsg(launchChildSetupFD, packet, control,
			unix.MSG_CMSG_CLOEXEC|unix.MSG_DONTWAIT)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EWOULDBLOCK) {
			if err := waitBlockedLaunchGate(ctx, launchChildSetupFD, unix.POLLIN); err != nil {
				return -1, err
			}
			continue
		}
		if err != nil {
			return -1, err
		}
		messages, parseErr := unix.ParseSocketControlMessage(control[:controlN])
		var received []int
		for _, message := range messages {
			if message.Header.Level != unix.SOL_SOCKET || message.Header.Type != unix.SCM_RIGHTS {
				parseErr = errors.Join(parseErr, fmt.Errorf("unexpected child namespace ancillary message"))
				continue
			}
			rights, err := unix.ParseUnixRights(&message)
			received = append(received, rights...)
			parseErr = errors.Join(parseErr, err)
		}
		defer func() {
			if returnErr != nil {
				for _, fd := range received {
					returnErr = errors.Join(returnErr, unix.Close(fd))
				}
			}
		}()
		if parseErr != nil || flags&(unix.MSG_CTRUNC|unix.MSG_TRUNC) != 0 || len(messages) != 1 || len(received) != 1 ||
			n != len(system.JailerChildNamespacePacket) || string(packet[:n]) != system.JailerChildNamespacePacket {
			return -1, fmt.Errorf("invalid child namespace packet")
		}
		kind, err := unix.IoctlRetInt(received[0], unix.NS_GET_NSTYPE)
		if err != nil || kind != unix.CLONE_NEWNET {
			return -1, fmt.Errorf("child descriptor is not a network namespace")
		}
		return received[0], nil
	}
}
