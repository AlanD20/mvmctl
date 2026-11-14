package jailer

import (
	"context"
	"errors"
	"fmt"

	"golang.org/x/sys/unix"

	"mvmctl/internal/infra"
)

type launchChildMountSource struct {
	name     string
	fd       int
	readOnly bool
}

func launchChildSourceDescriptorCount(plan launchChildPlan) int {
	count := childBaseDescriptors + len(plan.Volumes)
	if plan.CloudInit {
		count++
	}
	if plan.Metrics {
		count++
	}
	if plan.RestoreMemory {
		count += 2
	}
	if plan.Console {
		count++
	}
	return count
}

func launchChildMountDescriptorCount(plan launchChildPlan) int {
	count := 5 + len(plan.Volumes)
	if plan.CloudInit {
		count++
	}
	if plan.Metrics {
		count++
	}
	if plan.RestoreMemory {
		count += 2
	}
	return count
}

// The root parent clones mounts from its admitted file pins before the child enters
// CLONE_NEWNS. Raw file pins remain in the packet for independent launch attestation.
func prepareLaunchChildMountDescriptors(
	ctx context.Context,
	plan launchChildPlan,
	sources []int,
	outputDirectory int,
) (mounts []int, returnErr error) {
	if len(sources) != launchChildSourceDescriptorCount(plan) {
		return nil, instanceValidationError("launch child source descriptor count differs")
	}
	defer func() {
		if returnErr != nil {
			for _, fd := range mounts {
				returnErr = errors.Join(returnErr, unix.Close(fd))
			}
		}
	}()
	selection := []launchChildMountSource{
		{name: "rootfs", fd: sources[childRootfs]},
		{name: "kernel", fd: sources[childKernel], readOnly: true},
		{name: "config", fd: sources[childConfig], readOnly: true},
		{name: "runtime", fd: sources[childRuntime]},
		{name: "log", fd: sources[childLog]},
	}
	extra := childBaseDescriptors
	if plan.CloudInit {
		selection = append(selection, launchChildMountSource{name: "cloud-init", fd: sources[extra], readOnly: true})
		extra++
	}
	if plan.Metrics {
		selection = append(selection, launchChildMountSource{name: "metrics", fd: sources[extra]})
		extra++
	}
	for _, volume := range plan.Volumes {
		selection = append(selection, launchChildMountSource{
			name: "volume", fd: sources[extra], readOnly: volume.ReadOnly,
		})
		extra++
	}
	if plan.RestoreMemory {
		selection = append(selection,
			launchChildMountSource{name: "vmstate", fd: sources[extra], readOnly: true},
			launchChildMountSource{name: "memory", fd: sources[extra+1], readOnly: true},
		)
	}
	for _, source := range selection {
		if err := ctx.Err(); err != nil {
			return mounts, err
		}
		if source.name == "config" {
			mount, err := prepareLaunchChildConfigMount(ctx, source.fd, plan.Metrics)
			if err != nil {
				return mounts, fmt.Errorf("prepare detached launch config mount: %w", err)
			}
			mounts = append(mounts, mount)
			continue
		}
		sourceFD := source.fd
		linkedFD := -1
		var err error
		if source.name == "log" || source.name == "metrics" {
			name := infra.VMFirecrackerLogFilename
			if source.name == "metrics" {
				name = infra.VMFirecrackerMetricsFilename
			}
			linkedFD, err = reopenLaunchChildOutput(ctx, outputDirectory, name, source.fd)
			if err != nil {
				return mounts, fmt.Errorf("reopen published launch %s: %w", source.name, err)
			}
			sourceFD = linkedFD
		}
		mount, err := unix.OpenTree(
			sourceFD, "", uint(unix.OPEN_TREE_CLONE|unix.OPEN_TREE_CLOEXEC|unix.AT_EMPTY_PATH),
		)
		if linkedFD >= 0 {
			err = errors.Join(err, unix.Close(linkedFD))
		}
		if err != nil {
			if mount >= 0 {
				err = errors.Join(err, unix.Close(mount))
			}
			return mounts, fmt.Errorf("clone pinned launch resource %s: %w", source.name, err)
		}
		attributes := uint64(unix.MOUNT_ATTR_NOSUID | unix.MOUNT_ATTR_NODEV | unix.MOUNT_ATTR_NOEXEC)
		if source.readOnly {
			attributes |= unix.MOUNT_ATTR_RDONLY
		}
		if err := unix.MountSetattr(mount, "", unix.AT_EMPTY_PATH, &unix.MountAttr{Attr_set: attributes}); err != nil {
			return mounts, errors.Join(
				fmt.Errorf("set launch resource mount attributes for %s: %w", source.name, err), unix.Close(mount),
			)
		}
		mounts = append(mounts, mount)
	}
	return mounts, nil
}
