package agent

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"mvmctl/internal/lib/model"
	"mvmctl/pkg/errs"
)

// pciVolumeMu serializes agent removal requests; it cannot exclude a privileged guest rescan.
var pciVolumeMu sync.Mutex

type pciVolumeFS struct{ sys, dev, proc string }

type pciVolumeDisk struct {
	name, path, pci, serial string
	bytes                   uint64
	device                  uint64
}

func productionPCIVolumeFS() pciVolumeFS { return pciVolumeFS{sys: "/sys", dev: "/dev", proc: "/proc"} }

func pciVolumeError(message string, cause error) error {
	return errs.WrapMsg(errs.CodeVMStateInvalid, "guest volume release: "+message, cause)
}

func readPCIVolumeLeaf(ctx context.Context, path string) (_ string, returnErr error) {
	if err := ctx.Err(); err != nil {
		return "", pciVolumeError("cancelled", err)
	}
	file, err := os.Open(path)
	if err != nil {
		return "", pciVolumeError("incomplete device discovery", err)
	}
	defer func() { returnErr = errors.Join(returnErr, file.Close()) }()
	raw, err := io.ReadAll(io.LimitReader(file, 65537))
	if err != nil || len(raw) > 65536 {
		return "", pciVolumeError("device evidence exceeds its bound", err)
	}
	return strings.TrimSpace(string(raw)), nil
}

func pciVolumeEntries(ctx context.Context, path string) ([]os.DirEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, pciVolumeError("cancelled", err)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, pciVolumeError("incomplete device enumeration", err)
	}
	entries, readErr := file.ReadDir(129)
	closeErr := file.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) || closeErr != nil || len(entries) > 128 {
		return nil, pciVolumeError(
			"device enumeration exceeds its bound or is incomplete",
			errors.Join(readErr, closeErr),
		)
	}
	return entries, nil
}

func pciVolumeAncestor(fs pciVolumeFS, path string) (string, error) {
	devices := filepath.Join(fs.sys, "devices")
	if !strings.HasPrefix(path, devices+"/") {
		return "", pciVolumeError("device is outside kernel inventory", nil)
	}
	for parent := path; parent != devices; parent = filepath.Dir(parent) {
		subsystem, err := filepath.EvalSymlinks(filepath.Join(parent, "subsystem"))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", pciVolumeError("incomplete PCI ancestry", err)
		}
		if subsystem != filepath.Join(fs.sys, "bus/pci") {
			continue
		}
		canonical, err := filepath.EvalSymlinks(filepath.Join(fs.sys, "bus/pci/devices", filepath.Base(parent)))
		if err != nil || canonical != parent {
			return "", pciVolumeError("PCI ancestor is ambiguous", err)
		}
		return parent, nil
	}
	return "", pciVolumeError("selected virtio disk has no PCI ancestor", nil)
}

func discoverPCIVolumes(ctx context.Context, fs pciVolumeFS) ([]pciVolumeDisk, error) {
	entries, err := pciVolumeEntries(ctx, filepath.Join(fs.sys, "block"))
	if err != nil {
		return nil, err
	}
	var disks []pciVolumeDisk
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "vd") {
			continue
		}
		path, err := filepath.EvalSymlinks(filepath.Join(fs.sys, "block", entry.Name()))
		if err != nil {
			return nil, pciVolumeError("incomplete virtio inventory", err)
		}
		serial, err := readPCIVolumeLeaf(ctx, filepath.Join(path, "serial"))
		if err != nil {
			return nil, err
		}
		sectors, err := readPCIVolumeLeaf(ctx, filepath.Join(path, "size"))
		if err != nil {
			return nil, err
		}
		count, err := strconv.ParseUint(sectors, 10, 64)
		if err != nil || count == 0 || count > (1<<63-1)/512 {
			return nil, pciVolumeError("virtio capacity is invalid", err)
		}
		dev, err := readPCIVolumeLeaf(ctx, filepath.Join(path, "dev"))
		if err != nil {
			return nil, err
		}
		majorText, minorText, valid := strings.Cut(dev, ":")
		major, majorErr := strconv.ParseUint(majorText, 10, 32)
		minor, minorErr := strconv.ParseUint(minorText, 10, 32)
		if !valid || majorErr != nil || minorErr != nil {
			return nil, pciVolumeError("virtio block identity is incomplete", errors.Join(majorErr, minorErr))
		}
		pci, err := pciVolumeAncestor(fs, path)
		if err != nil {
			return nil, err
		}
		disks = append(
			disks,
			pciVolumeDisk{
				name:   entry.Name(),
				path:   path,
				pci:    pci,
				serial: serial,
				bytes:  count * 512,
				device: unix.Mkdev(uint32(major), uint32(minor)),
			},
		)
	}
	// Bracket membership to avoid declaring absence from an interrupted scan.
	after, err := pciVolumeEntries(ctx, filepath.Join(fs.sys, "block"))
	if err != nil {
		return nil, err
	}
	if len(after) != len(entries) {
		return nil, pciVolumeError("block inventory changed during discovery", nil)
	}
	names := make(map[string]bool, len(entries))
	for _, entry := range entries {
		names[entry.Name()] = true
	}
	for _, entry := range after {
		if !names[entry.Name()] {
			return nil, pciVolumeError("block inventory changed during discovery", nil)
		}
	}
	return disks, nil
}

func selectPCIVolume(disks []pciVolumeDisk, identity model.PCIVolumeIdentity) (*pciVolumeDisk, error) {
	var selected *pciVolumeDisk
	for index := range disks {
		if disks[index].serial != identity.Serial {
			continue
		}
		if selected != nil || disks[index].bytes != identity.ExpectedBytes {
			return nil, pciVolumeError("serial or capacity is ambiguous", nil)
		}
		selected = &disks[index]
	}
	if selected != nil {
		for _, disk := range disks {
			if disk.name != selected.name && disk.pci == selected.pci {
				return nil, pciVolumeError("PCI function contains another disk", nil)
			}
		}
	}
	return selected, nil
}

func admitIdlePCIVolume(ctx context.Context, fs pciVolumeFS, disk pciVolumeDisk) error {
	children, err := pciVolumeEntries(ctx, disk.path)
	if err != nil {
		return err
	}
	for _, child := range children {
		if !strings.HasPrefix(child.Name(), disk.name) || child.Name() == disk.name {
			continue
		}
		if _, err := os.Stat(filepath.Join(disk.path, child.Name(), "partition")); err == nil {
			return pciVolumeError("selected disk has partitions; stop the VM before detaching", nil)
		} else if !errors.Is(err, os.ErrNotExist) {
			return pciVolumeError("partition discovery is incomplete", err)
		}
	}
	holders, err := pciVolumeEntries(ctx, filepath.Join(disk.path, "holders"))
	if err != nil {
		return err
	}
	if len(holders) != 0 {
		return pciVolumeError("selected disk has active holders", nil)
	}
	mounts, err := readPCIVolumeLeaf(ctx, filepath.Join(fs.proc, "self/mountinfo"))
	if err != nil {
		return err
	}
	rootFound := false
	for _, line := range strings.Split(mounts, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 10 {
			return pciVolumeError("mount inventory is incomplete", nil)
		}
		device := strconv.FormatUint(
			uint64(unix.Major(disk.device)),
			10,
		) + ":" + strconv.FormatUint(
			uint64(unix.Minor(disk.device)),
			10,
		)
		if fields[2] == device {
			return pciVolumeError("selected disk is mounted or owns guest root", nil)
		}
		if fields[4] == "/" {
			rootFound = true
			root, err := filepath.EvalSymlinks(filepath.Join(fs.sys, "dev/block", fields[2]))
			if err != nil {
				return pciVolumeError("guest root ancestry is incomplete", err)
			}
			if root == disk.path || strings.HasPrefix(root, disk.path+"/") {
				return pciVolumeError("selected disk owns guest root", nil)
			}
			rootPCI, err := pciVolumeAncestor(fs, root)
			if err != nil || rootPCI == disk.pci {
				return pciVolumeError("guest root PCI ancestry is ambiguous or selected", err)
			}
		}
	}
	if !rootFound {
		return pciVolumeError("guest root mount is unknown", nil)
	}
	swaps, err := readPCIVolumeLeaf(ctx, filepath.Join(fs.proc, "swaps"))
	if err != nil {
		return err
	}
	lines := strings.Split(swaps, "\n")
	if len(lines) == 0 || !strings.HasPrefix(lines[0], "Filename") {
		return pciVolumeError("swap inventory is incomplete", nil)
	}
	for _, line := range lines[1:] {
		fields := strings.Fields(line)
		if len(fields) != 5 {
			return pciVolumeError("swap inventory is incomplete", nil)
		}
		var stat unix.Stat_t
		if err := unix.Stat(fields[0], &stat); err != nil {
			return pciVolumeError("swap identity is incomplete", err)
		}
		if stat.Rdev == disk.device || stat.Dev == disk.device {
			return pciVolumeError("selected disk contains active swap", nil)
		}
	}
	return nil
}

func removePCIVolume(ctx context.Context, identity model.PCIVolumeIdentity, fs pciVolumeFS) (returnErr error) {
	if err := identity.Validate(); err != nil {
		return err
	}
	pciVolumeMu.Lock()
	defer pciVolumeMu.Unlock()
	disks, err := discoverPCIVolumes(ctx, fs)
	if err != nil {
		return err
	}
	selected, err := selectPCIVolume(disks, identity)
	if err != nil {
		return err
	}
	// Complete enumeration with zero serial matches proves an already released retry.
	if selected == nil {
		return nil
	}
	fd, err := unix.Open(
		filepath.Join(fs.dev, selected.name),
		unix.O_RDONLY|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK,
		0,
	)
	if err != nil {
		return pciVolumeError("exclusive block admission failed; unmount or stop the VM", err)
	}
	defer func() { returnErr = errors.Join(returnErr, unix.Close(fd)) }()
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return pciVolumeError("block admission failed", err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFBLK || stat.Rdev != selected.device {
		return pciVolumeError("block identity changed", nil)
	}
	if err := admitIdlePCIVolume(ctx, fs, *selected); err != nil {
		return err
	}
	fresh, err := discoverPCIVolumes(ctx, fs)
	if err != nil {
		return err
	}
	current, err := selectPCIVolume(fresh, identity)
	if err != nil {
		return err
	}
	if current == nil || *current != *selected {
		return pciVolumeError("selected block or PCI identity changed", nil)
	}
	if err := admitIdlePCIVolume(ctx, fs, *current); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return pciVolumeError("cancelled before removal", err)
	}
	pci, err := unix.Open(current.pci, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return pciVolumeError("pin selected PCI ancestor", err)
	}
	defer func() { returnErr = errors.Join(returnErr, unix.Close(pci)) }()
	var retainedPCI, entryPCI unix.Stat_t
	if err := unix.Fstat(pci, &retainedPCI); err != nil {
		return pciVolumeError("inspect selected PCI ancestor", err)
	}
	ancestor, err := pciVolumeAncestor(fs, current.path)
	if err != nil || ancestor != current.pci {
		return pciVolumeError("selected PCI ancestry changed", err)
	}
	if err := unix.Fstatat(
		unix.AT_FDCWD,
		current.pci,
		&entryPCI,
		unix.AT_SYMLINK_NOFOLLOW,
	); err != nil || retainedPCI.Dev != entryPCI.Dev ||
		retainedPCI.Ino != entryPCI.Ino {
		return pciVolumeError("selected PCI ancestor was replaced", err)
	}
	// CRITICAL: Only the freshly rediscovered PCI ancestor supplies the fixed remove leaf.
	// O_EXCL blocks ordinary block claims; it does not exclude a privileged guest writer.
	remove, err := unix.Openat(pci, "remove", unix.O_WRONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return pciVolumeError("open PCI removal leaf", err)
	}
	n, writeErr := unix.Write(remove, []byte("1\n"))
	closeErr := unix.Close(remove)
	if writeErr != nil || closeErr != nil || n != 2 {
		return pciVolumeError("PCI removal outcome is unknown; retry", errors.Join(writeErr, closeErr))
	}
	for {
		fresh, err := discoverPCIVolumes(ctx, fs)
		if err != nil {
			return err
		}
		current, err := selectPCIVolume(fresh, identity)
		if err != nil {
			return err
		}
		_, pciErr := os.Stat(selected.pci)
		_, blockErr := os.Lstat(filepath.Join(fs.sys, "block", selected.name))
		if current == nil && errors.Is(pciErr, os.ErrNotExist) && errors.Is(blockErr, os.ErrNotExist) {
			return nil
		}
		if pciErr != nil && !errors.Is(pciErr, os.ErrNotExist) ||
			blockErr != nil && !errors.Is(blockErr, os.ErrNotExist) {
			return pciVolumeError("device removal readback is incomplete", errors.Join(pciErr, blockErr))
		}
		select {
		case <-ctx.Done():
			return pciVolumeError("PCI removal outcome is unknown; retry", ctx.Err())
		case <-time.After(25 * time.Millisecond):
		}
	}
}
