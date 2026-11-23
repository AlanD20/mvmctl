package loopmount

import (
	"context"
	"errors"

	"golang.org/x/sys/unix"
)

const (
	rootfsSSHBootstrapPath = "/usr/local/bin/first-boot-ssh-installer.sh"
	rootfsSSHBootstrapUnit = "/etc/systemd/system/first-boot-ssh-installer.service"
	rootfsSSHBootstrapLink = "/etc/systemd/system/multi-user.target.wants/first-boot-ssh-installer.service"
	rootfsSSHOpenRCPath    = "/etc/init.d/mvm-ssh-setup"
	rootfsSSHOpenRCLink    = "/etc/runlevels/default/mvm-ssh-setup"
)

// Guest package installation and key generation execute only after the VM boots.
const rootfsSSHBootstrap = `#!/bin/sh
set -eu
if ! command -v sshd >/dev/null 2>&1; then
  if command -v apt-get >/dev/null 2>&1; then apt-get update && apt-get install -y openssh-server;
  elif command -v apk >/dev/null 2>&1; then apk add --no-cache openssh;
  elif command -v pacman >/dev/null 2>&1; then pacman -Sy --noconfirm openssh;
  elif command -v dnf >/dev/null 2>&1; then dnf install -y openssh-server;
  elif command -v yum >/dev/null 2>&1; then yum install -y openssh-server;
  else exit 1; fi
fi
ssh-keygen -A
if command -v systemctl >/dev/null 2>&1; then
  systemctl enable --now sshd.service || systemctl enable --now ssh.service
elif command -v rc-update >/dev/null 2>&1; then
  rc-update add sshd default || rc-update add ssh default
  rc-service sshd start || rc-service ssh start
fi
mkdir -p /var/lib/mvmctl
touch /var/lib/mvmctl/ssh-ready
`

func configureRootfsSSHBoot(ctx context.Context, tree rootfsGuestTree) (uint32, error) {
	for _, path := range []string{"/usr", "/usr/local", "/usr/local/bin"} {
		if _, err := tree.ensureDirectory(ctx, path, 0755, 0, 0); err != nil {
			return 0, err
		}
	}
	changed, err := tree.replaceFile(ctx, rootfsProvisionFile{
		path: rootfsSSHBootstrapPath, content: []byte(rootfsSSHBootstrap), mode: 0755,
	})
	var written uint32
	if changed {
		written++
	}
	if err != nil {
		return written, err
	}
	systemd, err := tree.fixedPathHasType(ctx, "/etc/systemd", unix.S_IFDIR)
	if err != nil {
		return written, err
	}
	if systemd {
		for _, path := range []string{"/etc/systemd/system", "/etc/systemd/system/multi-user.target.wants"} {
			if _, err := tree.ensureDirectory(ctx, path, 0755, 0, 0); err != nil {
				return written, err
			}
		}
		changed, err := tree.replaceFile(ctx, rootfsProvisionFile{
			path: rootfsSSHBootstrapUnit, mode: 0644,
			content: []byte(
				"[Unit]\nDescription=MVM guest SSH setup\nAfter=network-online.target\nWants=network-online.target\nConditionPathExists=!/var/lib/mvmctl/ssh-ready\n\n[Service]\nType=oneshot\nExecStart=" + rootfsSSHBootstrapPath + "\n\n[Install]\nWantedBy=multi-user.target\n",
			),
		})
		if changed {
			written++
		}
		if err != nil {
			return written, err
		}
		changed, err = tree.replaceFixedSymlink(ctx, rootfsSSHBootstrapLink, "../first-boot-ssh-installer.service")
		if changed {
			written++
		}
		return written, err
	}
	openrc, err := tree.fixedPathHasType(ctx, "/etc/runlevels", unix.S_IFDIR)
	if err != nil || !openrc {
		return written, err
	}
	for _, path := range []string{"/etc/init.d", "/etc/runlevels/default"} {
		if _, err := tree.ensureDirectory(ctx, path, 0755, 0, 0); err != nil {
			return written, err
		}
	}
	changed, err = tree.replaceFile(ctx, rootfsProvisionFile{
		path: rootfsSSHOpenRCPath, mode: 0755,
		content: []byte(
			"#!/sbin/openrc-run\ndescription=\"MVM guest SSH setup\"\ndepend() { need net; }\nstart() { [ -e /var/lib/mvmctl/ssh-ready ] || " + rootfsSSHBootstrapPath + "; }\n",
		),
	})
	if changed {
		written++
	}
	if err != nil {
		return written, err
	}
	changed, err = tree.replaceFixedSymlink(ctx, rootfsSSHOpenRCLink, "/etc/init.d/mvm-ssh-setup")
	if changed {
		written++
	}
	return written, err
}

func validRootfsSSHEnablementLink(path, target string) bool {
	return path == rootfsSSHBootstrapLink && target == "../first-boot-ssh-installer.service" ||
		path == rootfsSSHOpenRCLink && target == "/etc/init.d/mvm-ssh-setup"
}

func repairRootfsSudoOwnership(ctx context.Context, tree rootfsGuestTree) error {
	for _, file := range []struct {
		path string
		mode uint32
	}{
		{"/etc/sudo.conf", 0440},
		{"/etc/sudoers", 0440},
		{"/usr/bin/sudo", 04755},
		{"/usr/libexec/sudo/sudoers.so", 0755},
	} {
		if err := repairRootfsSudoFile(ctx, tree, file.path, file.mode); err != nil {
			return err
		}
	}
	return nil
}

func repairRootfsSudoFile(ctx context.Context, tree rootfsGuestTree, path string, mode uint32) (returnErr error) {
	parent, leaf, err := tree.openParent(ctx, path)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() {
		returnErr = appendRootfsCleanupError(
			returnErr,
			"close fixed sudo parent",
			tree.deps.close(context.WithoutCancel(ctx), parent),
		)
	}()
	fd, err := tree.deps.openAt2(ctx, parent, leaf, &unix.OpenHow{
		Flags: rootfsGuestReadFlags, Resolve: rootfsGuestResolveFlags,
	})
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return rootfsAtomicError("open fixed guest sudo file", err)
	}
	defer func() {
		returnErr = appendRootfsCleanupError(
			returnErr,
			"close fixed sudo file",
			tree.deps.close(context.WithoutCancel(ctx), fd),
		)
	}()
	var stat unix.Stat_t
	if err := tree.deps.fstat(ctx, fd, &stat); err != nil {
		return rootfsAtomicError("inspect fixed guest sudo file", err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 {
		return rootfsAtomicError("fixed guest sudo file has unsafe type or link count", nil)
	}
	if stat.Uid != 0 || stat.Gid != 0 {
		if err := tree.deps.fchown(ctx, fd, 0, 0); err != nil {
			return rootfsAtomicError("repair fixed guest sudo owner", err)
		}
		tree.tracker.markModified()
	}
	// chown can clear set-ID bits, so always restore the exact compiled mode afterward.
	if err := tree.deps.fchmod(ctx, fd, mode); err != nil {
		return rootfsAtomicError("repair fixed guest sudo mode", err)
	}
	tree.tracker.markModified()
	if err := tree.deps.fsync(ctx, fd); err != nil {
		return rootfsCleanupError("sync fixed guest sudo metadata", err)
	}
	return nil
}
