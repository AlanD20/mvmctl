package loopmount

import (
	"context"
	"errors"
	"strings"

	"golang.org/x/sys/unix"

	"mvmctl/internal/infra/provcontent"
)

const (
	maxRootfsDeblobEntries = 131072
	maxRootfsDeblobDepth   = 64
	rootfsDeblobDirentSize = 32768
)

type rootfsDeblob struct {
	tree            rootfsGuestTree
	entriesLeft     int
	direntBytesLeft int
	filesWritten    uint32
}

// CRITICAL: Deblob paths and unit names originate exclusively in this closed policy. Guest
// programs are never executed by the privileged receiver, including package-manager hooks.
func applyRootfsDeblob(ctx context.Context, tree rootfsGuestTree, osType string) (uint32, error) {
	d := rootfsDeblob{
		tree: tree, entriesLeft: maxRootfsDeblobEntries,
		direntBytesLeft: maxRootfsDeblobEntries * 280,
	}
	if tree.deps.readDirent == nil || tree.deps.fstatfs == nil {
		return 0, rootfsAtomicError("deblob directory reader is unavailable", nil)
	}
	for _, path := range []string{
		"/var/log", "/tmp", "/var/tmp", "/usr/share/doc", "/usr/share/man", "/usr/share/info",
	} {
		if err := d.clearDirectory(ctx, path); err != nil {
			return d.filesWritten, err
		}
	}
	systemd, err := tree.fixedPathHasType(ctx, "/etc/systemd/system", unix.S_IFDIR)
	if err != nil {
		return d.filesWritten, err
	}
	if systemd {
		for _, unit := range rootfsDeblobCommonMasks() {
			if err := d.mask(ctx, unit); err != nil {
				return d.filesWritten, err
			}
		}
	}
	var caches []string
	switch strings.ToLower(osType) {
	case "ubuntu", "debian":
		caches = []string{"/var/cache/apt/archives", "/var/cache/debconf"}
		if systemd {
			for _, unit := range []string{
				"e2scrub_all.timer", "e2scrub_reap.service", "apt-daily.timer", "apt-daily-upgrade.timer",
			} {
				if err := d.mask(ctx, unit); err != nil {
					return d.filesWritten, err
				}
			}
		}
	case "alpine":
		caches = []string{"/var/cache/apk"}
		if err := d.configureAlpine(ctx); err != nil {
			return d.filesWritten, err
		}
	case "arch", "archlinux", "manjaro":
		caches = []string{"/var/cache/pacman/pkg"}
		if err := d.configureArch(ctx, systemd); err != nil {
			return d.filesWritten, err
		}
	case "fedora", "centos", "rhel", "rocky", "almalinux":
		caches = []string{"/var/cache/dnf", "/var/cache/yum"}
	default:
		caches = []string{"/var/cache"}
	}
	for _, path := range append(caches, "/var/lib/apt/lists") {
		if err := d.clearDirectory(ctx, path); err != nil {
			return d.filesWritten, err
		}
	}
	for _, directory := range []string{"/etc/cloud", "/etc/cloud/cloud.cfg.d"} {
		if _, err := tree.ensureDirectory(ctx, directory, 0755, 0, 0); err != nil {
			return d.filesWritten, err
		}
	}
	for _, file := range []rootfsProvisionFile{
		{path: "/etc/cloud/cloud.cfg.d/99-disable-datasources.cfg",
			content: provcontent.CloudInitDisableDatasource, mode: 0644},
		{path: "/etc/cloud/cloud-init.disabled", content: provcontent.CloudInitDisabledMarker, mode: 0644},
	} {
		if err := d.write(ctx, file); err != nil {
			return d.filesWritten, err
		}
	}
	if systemd {
		for _, item := range []struct {
			unit    string
			content []byte
		}{
			{"snapd.seeded.service", provcontent.SnapdOverride},
			{"systemd-networkd-wait-online.service", provcontent.NetworkdWaitOverride},
		} {
			directory := "/etc/systemd/system/" + item.unit + ".d"
			if _, err := tree.ensureDirectory(ctx, directory, 0755, 0, 0); err != nil {
				return d.filesWritten, err
			}
			if err := d.write(ctx, rootfsProvisionFile{
				path: directory + "/override.conf", content: item.content, mode: 0644,
			}); err != nil {
				return d.filesWritten, err
			}
		}
	}
	return d.filesWritten, nil
}

func rootfsDeblobCommonMasks() []string {
	return []string{
		"systemd-timesyncd.service", "systemd-time-wait-sync.service", "systemd-firstboot.service",
		"ldconfig.service", "modprobe@drm.service", "modprobe@efi_pstore.service", "sys-kernel-debug.mount",
		"pollinate.service", "snapd.service", "snapd.socket", "systemd-udev-settle.service",
		"unattended-upgrades.service", "man-db.timer", "whoopsie.service", "apport.service", "udisks2.service",
		"console-setup.service", "keyboard-setup.service", "motd-news.service", "fstrim.timer", "logrotate.timer",
		"multipathd.service", "accounts-daemon.service", "systemd-userdbd.service", "systemd-nsresourced.service",
		"systemd-pcrphase.service", "systemd-pcrphase-initrd.service", "systemd-pcrphase-sysinit.service",
		"systemd-boot-update.service", "cloud-init.service", "cloud-init-local.service", "cloud-config.service",
		"cloud-final.service",
	}
}

func (d *rootfsDeblob) write(ctx context.Context, file rootfsProvisionFile) error {
	if err := ctx.Err(); err != nil {
		return rootfsAtomicError("write deblob guest configuration", err)
	}
	changed, err := d.tree.replaceFile(ctx, file)
	if changed {
		d.filesWritten++
	}
	return err
}

func (d *rootfsDeblob) mask(ctx context.Context, unit string) error {
	return d.replaceLink(ctx, "/etc/systemd/system/"+unit, "/dev/null")
}

func validRootfsDeblobLink(path, target string) bool {
	if target == "/dev/null" {
		for _, unit := range append(rootfsDeblobCommonMasks(),
			"e2scrub_all.timer", "e2scrub_reap.service", "apt-daily.timer", "apt-daily-upgrade.timer",
			"pacman-init.service") {
			if path == "/etc/systemd/system/"+unit {
				return true
			}
		}
	}
	switch path {
	case "/etc/runlevels/default/sshd":
		return target == "/etc/init.d/sshd"
	case "/etc/runlevels/default/ssh":
		return target == "/etc/init.d/ssh"
	case "/etc/systemd/system/multi-user.target.wants/mvm-deblob-pacman.service":
		return target == "../mvm-deblob-pacman.service"
	case "/etc/systemd/system/multi-user.target.wants/mvm-deblob-btrfs.service":
		return target == "../mvm-deblob-btrfs.service"
	default:
		return false
	}
}

func (d *rootfsDeblob) replaceLink(ctx context.Context, path, target string) (returnErr error) {
	if err := ctx.Err(); err != nil {
		return rootfsAtomicError("replace deblob service link", err)
	}
	if !validRootfsDeblobLink(path, target) {
		return rootfsAtomicError("deblob link is outside closed policy", nil)
	}
	parentFD, leaf, err := d.tree.openParent(ctx, path)
	if err != nil {
		return err
	}
	defer func() {
		returnErr = appendRootfsProvisionCleanupError(returnErr, "close deblob link parent",
			d.tree.deps.close(context.WithoutCancel(ctx), parentFD))
	}()
	var stat unix.Stat_t
	if err := d.tree.deps.fstatAt(ctx, parentFD, leaf, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		if !errors.Is(err, unix.ENOENT) {
			return rootfsAtomicError("inspect deblob link destination", err)
		}
	} else {
		kind := stat.Mode & unix.S_IFMT
		if (kind != unix.S_IFLNK && kind != unix.S_IFREG) || stat.Nlink != 1 {
			return rootfsAtomicError("deblob link destination has unsafe type", nil)
		}
		if kind == unix.S_IFLNK {
			equal, err := rootfsGuestSymlinkAlreadyEqual(ctx, d.tree.deps, parentFD, leaf, target)
			if err != nil || equal {
				return err
			}
		}
	}
	temp, err := d.tree.deps.tempName(ctx)
	if err != nil || !validRootfsGuestTempName(temp) {
		return rootfsAtomicError("derive deblob temporary link", err)
	}
	if err := d.tree.deps.symlinkAt(ctx, target, parentFD, temp); err != nil {
		return rootfsAtomicError("create deblob temporary link", err)
	}
	d.tree.tracker.markModified()
	present := true
	defer func() {
		if present {
			returnErr = appendRootfsProvisionCleanupError(returnErr, "remove deblob temporary link",
				d.tree.deps.unlinkAt(context.WithoutCancel(ctx), parentFD, temp, 0))
		}
	}()
	if err := d.tree.deps.renameAt(ctx, parentFD, temp, parentFD, leaf); err != nil {
		return rootfsAtomicError("publish deblob link", err)
	}
	present = false
	d.filesWritten++
	if err := d.tree.deps.fsync(ctx, parentFD); err != nil {
		return rootfsCleanupError("sync deblob link parent", err)
	}
	equal, err := rootfsGuestSymlinkAlreadyEqual(ctx, d.tree.deps, parentFD, leaf, target)
	if err != nil {
		return err
	}
	if !equal {
		return rootfsAtomicError("published deblob link changed", nil)
	}
	return nil
}

func (d *rootfsDeblob) clearDirectory(ctx context.Context, path string) (returnErr error) {
	if err := ctx.Err(); err != nil {
		return rootfsAtomicError("clear deblob directory", err)
	}
	components, err := rootfsGuestPathComponents(path)
	if err != nil {
		return err
	}
	fd, err := d.tree.deps.openAt2(ctx, d.tree.rootFD, strings.Join(components, "/"), &unix.OpenHow{
		Flags: rootfsGuestDirectoryFlags, Resolve: rootfsGuestResolveFlags,
	})
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil
		}
		return rootfsAtomicError("open fixed deblob directory", err)
	}
	defer func() {
		returnErr = appendRootfsProvisionCleanupError(returnErr, "close fixed deblob directory",
			d.tree.deps.close(context.WithoutCancel(ctx), fd))
	}()
	return d.clearChildren(ctx, fd, 0)
}

func (d *rootfsDeblob) clearChildren(ctx context.Context, fd, depth int) error {
	if depth > maxRootfsDeblobDepth {
		return rootfsAtomicError("deblob directory depth exceeds policy", nil)
	}
	// Read before unlinking so directory-offset changes cannot skip cache entries.
	names, err := d.readNames(ctx, fd)
	if err != nil {
		return err
	}
	for _, name := range names {
		if err := d.removeChild(ctx, fd, name, depth); err != nil {
			return err
		}
	}
	return nil
}

func (d *rootfsDeblob) readNames(ctx context.Context, fd int) ([]string, error) {
	var names []string
	buffer := make([]byte, rootfsDeblobDirentSize)
	for {
		if err := ctx.Err(); err != nil {
			return nil, rootfsAtomicError("enumerate deblob directory", err)
		}
		n, err := d.tree.deps.readDirent(ctx, fd, buffer)
		if err != nil || n < 0 || n > len(buffer) {
			return nil, rootfsAtomicError("read deblob directory entries", err)
		}
		if n == 0 {
			return names, nil
		}
		consumed, count, batch := unix.ParseDirent(buffer[:n], -1, nil)
		if consumed != n || count > d.entriesLeft || n > d.direntBytesLeft {
			return nil, rootfsAtomicError("deblob directory entry budget or encoding is invalid", nil)
		}
		d.entriesLeft -= count
		d.direntBytesLeft -= n
		for _, name := range batch {
			if name == "" || name == "." || name == ".." || len(name) > 255 || strings.ContainsAny(name, "/\x00") {
				return nil, rootfsAtomicError("deblob directory entry is invalid", nil)
			}
		}
		names = append(names, batch...)
	}
}

func (d *rootfsDeblob) removeChild(ctx context.Context, parentFD int, name string, depth int) (returnErr error) {
	if err := ctx.Err(); err != nil {
		return rootfsAtomicError("remove deblob cache entry", err)
	}
	// O_PATH pins a symlink itself. NO_XDEV rejects mounted leaves as well as mounted directories.
	fd, err := d.tree.deps.openAt2(ctx, parentFD, name, &unix.OpenHow{
		Flags: rootfsGuestInspectFlags, Resolve: rootfsGuestResolveFlags,
	})
	if err != nil {
		return rootfsAtomicError("pin deblob cache entry", err)
	}
	defer func() {
		returnErr = appendRootfsProvisionCleanupError(returnErr, "close deblob cache entry",
			d.tree.deps.close(context.WithoutCancel(ctx), fd))
	}()
	var before unix.Stat_t
	if err := d.tree.deps.fstat(ctx, fd, &before); err != nil {
		return rootfsAtomicError("inspect deblob cache entry", err)
	}
	flags := 0
	if before.Mode&unix.S_IFMT == unix.S_IFDIR {
		flags = unix.AT_REMOVEDIR
		if err := d.clearPinnedDirectory(ctx, fd, depth+1); err != nil {
			return err
		}
	}
	var current unix.Stat_t
	if err := d.tree.deps.fstatAt(ctx, parentFD, name, &current, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return rootfsAtomicError("reinspect deblob cache entry", err)
	}
	if current.Dev != before.Dev || current.Ino != before.Ino || current.Mode&unix.S_IFMT != before.Mode&unix.S_IFMT {
		return rootfsAtomicError("deblob cache entry identity changed", nil)
	}
	if err := d.tree.deps.unlinkAt(ctx, parentFD, name, flags); err != nil {
		return rootfsAtomicError("unlink deblob cache entry", err)
	}
	d.tree.tracker.markModified()
	if err := d.tree.deps.fsync(ctx, parentFD); err != nil {
		return rootfsCleanupError("sync deblob cache directory", err)
	}
	return nil
}

func (d *rootfsDeblob) clearPinnedDirectory(ctx context.Context, fd, depth int) (returnErr error) {
	directoryFD, err := d.tree.deps.openAt2(ctx, fd, ".", &unix.OpenHow{
		Flags: rootfsGuestDirectoryFlags, Resolve: rootfsGuestResolveFlags,
	})
	if err != nil {
		return rootfsAtomicError("open pinned deblob cache directory", err)
	}
	defer func() {
		returnErr = appendRootfsProvisionCleanupError(returnErr, "close deblob cache directory reader",
			d.tree.deps.close(context.WithoutCancel(ctx), directoryFD))
	}()
	return d.clearChildren(ctx, directoryFD, depth)
}
