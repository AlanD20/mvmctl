package loopmount

import (
	"context"
	"errors"
	"strings"

	"golang.org/x/sys/unix"
)

func (d *rootfsDeblob) configureAlpine(ctx context.Context) error {
	for _, path := range []string{
		"/etc/dhcpcd.conf", "/etc/network/interfaces", "/etc/rc.conf", "/etc/inittab",
	} {
		content, absent, err := d.tree.readOptional(ctx, path, rootfsProvisionReadBufferLimit)
		if err != nil {
			if errors.Is(err, unix.ENOENT) && path == "/etc/network/interfaces" {
				continue
			}
			return err
		}
		if absent && (path == "/etc/network/interfaces" || path == "/etc/inittab") {
			continue
		}
		text := string(content)
		switch path {
		case "/etc/dhcpcd.conf":
			present := false
			for line := range strings.SplitSeq(text, "\n") {
				present = present || strings.HasPrefix(line, "denyinterfaces eth0")
			}
			if !present {
				text = strings.TrimRight(text, "\n") + "\ndenyinterfaces eth0\n"
			}
		case "/etc/network/interfaces":
			text = strings.ReplaceAll(text, "iface eth0 inet dhcp", "iface eth0 inet manual")
		case "/etc/rc.conf":
			var lines []string
			for line := range strings.SplitSeq(strings.TrimRight(text, "\n"), "\n") {
				if !strings.HasPrefix(line, "rc_parallel=") && !strings.HasPrefix(line, "#rc_parallel=") {
					lines = append(lines, line)
				}
			}
			text = strings.Join(append(lines, "rc_parallel=\"YES\""), "\n") + "\n"
		case "/etc/inittab":
			lines := strings.Split(text, "\n")
			for index, line := range lines {
				if strings.Contains(line, "ttyS0") && !strings.HasPrefix(line, "#") {
					lines[index] = "#" + line
				}
			}
			text = strings.Join(lines, "\n")
		}
		if err := d.write(ctx, rootfsProvisionFile{path: path, content: []byte(text), mode: 0644}); err != nil {
			return err
		}
	}
	for _, path := range []string{
		"/etc/runlevels/default/cloud-init", "/etc/runlevels/default/cloud-config",
		"/etc/runlevels/default/cloud-final", "/etc/runlevels/default/cloud-init-hotplugd",
		"/etc/runlevels/ssh/cloud-init", "/etc/runlevels/default/chronyd",
	} {
		if err := d.removeRunlevelLink(ctx, path); err != nil {
			return err
		}
	}
	for _, service := range []string{"sshd", "ssh"} {
		present, err := d.tree.fixedPathHasType(ctx, "/etc/init.d/"+service, unix.S_IFREG)
		if err != nil {
			return err
		}
		if !present {
			continue
		}
		for _, directory := range []string{"/etc/runlevels", "/etc/runlevels/default"} {
			if _, err := d.tree.ensureDirectory(ctx, directory, 0755, 0, 0); err != nil {
				return err
			}
		}
		return d.replaceLink(ctx, "/etc/runlevels/default/"+service, "/etc/init.d/"+service)
	}
	return nil
}

func (d *rootfsDeblob) removeRunlevelLink(ctx context.Context, path string) (returnErr error) {
	parentFD, leaf, err := d.tree.openParent(ctx, path)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil
		}
		return err
	}
	defer func() {
		returnErr = appendRootfsProvisionCleanupError(returnErr, "close deblob runlevel directory",
			d.tree.deps.close(context.WithoutCancel(ctx), parentFD))
	}()
	var stat unix.Stat_t
	if err := d.tree.deps.fstatAt(ctx, parentFD, leaf, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil
		}
		return rootfsAtomicError("inspect deblob runlevel link", err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFLNK || stat.Nlink != 1 {
		return rootfsAtomicError("deblob runlevel entry is not a single link", nil)
	}
	if err := d.tree.deps.unlinkAt(ctx, parentFD, leaf, 0); err != nil {
		return rootfsAtomicError("remove deblob runlevel link", err)
	}
	d.tree.tracker.markModified()
	if err := d.tree.deps.fsync(ctx, parentFD); err != nil {
		return rootfsCleanupError("sync deblob runlevel directory", err)
	}
	return nil
}

func (d *rootfsDeblob) configureArch(ctx context.Context, systemd bool) error {
	for _, file := range []rootfsProvisionFile{
		{path: "/etc/hostname", content: []byte("mvm\n"), mode: 0644},
		{path: "/etc/locale.conf", content: []byte("LANG=en_US.UTF-8\n"), mode: 0644},
		{path: "/etc/vconsole.conf", content: []byte("KEYMAP=us\n"), mode: 0644},
	} {
		if err := d.write(ctx, file); err != nil {
			return err
		}
	}
	content, absent, err := d.tree.readOptional(ctx, "/etc/mkinitcpio.conf", rootfsProvisionReadBufferLimit)
	if err != nil {
		return err
	}
	if !absent {
		if err := d.write(ctx, rootfsProvisionFile{
			path: "/etc/mkinitcpio.conf", mode: 0644,
			content: []byte(strings.ReplaceAll(string(content), " btrfs ", " ")),
		}); err != nil {
			return err
		}
	}
	if !systemd {
		return rootfsAtomicError("Arch deblob requires systemd for deferred keyring initialization", nil)
	}
	if err := d.mask(ctx, "pacman-init.service"); err != nil {
		return err
	}
	for _, directory := range []string{
		"/var/lib/mvmctl", "/etc/systemd/system/multi-user.target.wants",
	} {
		if _, err := d.tree.ensureDirectory(ctx, directory, 0755, 0, 0); err != nil {
			return err
		}
	}
	// CRITICAL: These fixed units execute only inside the booted guest. Completion markers are
	// written after successful commands, so failed or interrupted guest boots retry the work.
	for _, file := range []rootfsProvisionFile{
		{path: "/etc/systemd/system/mvm-deblob-pacman-init.service", mode: 0644,
			content: []byte(rootfsDeblobPacmanInitUnit)},
		{path: "/etc/systemd/system/mvm-deblob-pacman.service", mode: 0644,
			content: []byte(rootfsDeblobPacmanUnit)},
	} {
		if err := d.write(ctx, file); err != nil {
			return err
		}
	}
	if err := d.replaceLink(ctx,
		"/etc/systemd/system/multi-user.target.wants/mvm-deblob-pacman.service",
		"../mvm-deblob-pacman.service"); err != nil {
		return err
	}
	var filesystem unix.Statfs_t
	if err := d.tree.deps.fstatfs(ctx, d.tree.rootFD, &filesystem); err != nil {
		return rootfsAtomicError("inspect deblob root filesystem", err)
	}
	if filesystem.Type != unix.BTRFS_SUPER_MAGIC {
		return nil
	}
	if err := d.write(ctx, rootfsProvisionFile{
		path: "/etc/systemd/system/mvm-deblob-btrfs.service", mode: 0644,
		content: []byte(rootfsDeblobBtrfsUnit),
	}); err != nil {
		return err
	}
	return d.replaceLink(ctx,
		"/etc/systemd/system/multi-user.target.wants/mvm-deblob-btrfs.service",
		"../mvm-deblob-btrfs.service")
}

const rootfsDeblobPacmanInitUnit = `[Unit]
Description=Initialize the microVM package keyring
ConditionPathExists=/usr/bin/pacman-key
ConditionPathExists=!/etc/pacman.d/gnupg/pubring.gpg
Before=mvm-deblob-pacman.service

[Service]
Type=oneshot
ExecStart=/usr/bin/pacman-key --init
RemainAfterExit=yes
`

const rootfsDeblobPacmanUnit = `[Unit]
Description=Populate the microVM package keyring
Requires=mvm-deblob-pacman-init.service
After=mvm-deblob-pacman-init.service
ConditionPathExists=/usr/bin/pacman-key
ConditionPathExists=!/var/lib/mvmctl/deblob-pacman-ready

[Service]
Type=oneshot
ExecStart=/usr/bin/pacman-key --populate archlinux
ExecStartPost=/usr/bin/touch /var/lib/mvmctl/deblob-pacman-ready
RemainAfterExit=yes

[Install]
WantedBy=multi-user.target
`

const rootfsDeblobBtrfsUnit = `[Unit]
Description=Reclaim empty microVM Btrfs data chunks
ConditionPathExists=/usr/bin/btrfs
ConditionPathExists=!/var/lib/mvmctl/deblob-btrfs-ready

[Service]
Type=oneshot
ExecStart=/usr/bin/btrfs balance start -dusage=0 /
ExecStartPost=/usr/bin/touch /var/lib/mvmctl/deblob-btrfs-ready
RemainAfterExit=yes

[Install]
WantedBy=multi-user.target
`
