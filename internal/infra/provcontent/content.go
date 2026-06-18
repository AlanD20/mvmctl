// Package provcontent holds shared provisioning content types and builders.
//
// This package is intentionally separate from "mvmctl/internal/lib/provisioner"
// to avoid circular imports: guestfs and loopmount packages import this for
// Operation types and ProvisionerContent, while provisioner/backend.go imports
// guestfs and loopmount directly.
package provcontent

import (
	"fmt"
	"os"
	"strings"
)

// --- Operation types -- pure data, no execution logic ---

// FileOpDefaultMode is the default file permission mode (0644).
// Go zero-initializes int fields to 0, so callers who create FileOp without
// setting Mode will get 0000 permissions instead of 0644. Use this constant
// or NewFileOp() to ensure correct defaults.
const FileOpDefaultMode = 0644

// FileOp represents writing a file inside the root filesystem.
//
// IMPORTANT: Go zero-initializes Mode to 0, NOT 0644.
// Always set Mode explicitly, use FileOpDefaultMode, or use NewFileOp().
type FileOp struct {
	Path string
	Data []byte
	Mode int
	UID  int
	GID  int
}

// ChrootOp represents running a shell command inside a chroot environment.
type ChrootOp struct {
	Command string
}

// CopyDirOp represents copying a directory tree into the root filesystem.
type CopyDirOp struct {
	Src string
	Dst string
}

// ResizeAction represents the resize action type.
type ResizeAction string

const (
	ResizeActionGrow   ResizeAction = "grow"
	ResizeActionShrink ResizeAction = "shrink"
)

// ResizeOp represents resizing the root filesystem (grow or shrink).
type ResizeOp struct {
	Action ResizeAction
	Bytes  int64
}

// Operation is a union type: FileOp | ChrootOp | CopyDirOp | ResizeOp.
type Operation interface {
	operationMarker()
}

func (FileOp) operationMarker()    {}
func (ChrootOp) operationMarker()  {}
func (CopyDirOp) operationMarker() {}
func (ResizeOp) operationMarker()  {}

// NewFileOp creates a FileOp with the default mode (0644) and uid=0, gid=0.
//
// This is the recommended way to create FileOp values — it ensures the mode
// default is set even if the caller forgets to set Mode.
func NewFileOp(path string, data []byte) FileOp {
	return FileOp{
		Path: path,
		Data: data,
		Mode: FileOpDefaultMode,
		UID:  0,
		GID:  0,
	}
}

// --- Builder -- shared provisioning content ---
//
// Raw content methods return plain strings/bytes (single source of truth).
// Builder methods wrap raw content into Operation types.

type Builder struct{}

// Provisioning content constants and shell scripts.
var (
	CloudInitDisableDatasource = []byte("datasource_list: [None]\n")
	CloudInitDisabledMarker    = []byte("disabled by mvmctl\n")
	SnapdOverride              = []byte("[Service]\nExecStart=\nExecStart=/bin/true\n")
	NetworkdWaitOverride       = []byte("[Unit]\nConditionPathExists=/nonexistent-disabled-by-mvm\n")

	maskServicesScript = `# Mask non-essential systemd services for faster microVM boot
if command -v systemctl >/dev/null 2>&1; then
  for svc in \
    systemd-timesyncd.service \
    systemd-time-wait-sync.service \
    systemd-firstboot.service \
    ldconfig.service \
    modprobe@drm.service \
    modprobe@efi_pstore.service \
    sys-kernel-debug.mount \
    pollinate.service \
    snapd.service \
    snapd.socket \
    systemd-udev-settle.service \
    unattended-upgrades.service \
    packagekit.service \
    man-db.timer \
    whoopsie.service \
    apport.service \
    udisks2.service \
    console-setup.service \
    keyboard-setup.service \
    motd-news.service \
    fstrim.timer \
    logrotate.timer \
    multipathd.service \
    accounts-daemon.service \
    systemd-userdbd.service \
    systemd-nsresourced.service \
    systemd-pcrphase.service \
    systemd-pcrphase-initrd.service \
    systemd-pcrphase-sysinit.service \
    systemd-boot-update.service; do
    ln -sf /dev/null "/etc/systemd/system/$svc" 2>/dev/null || true
  done
fi`

	enableSSHScript = `if command -v systemctl >/dev/null 2>&1; then
  systemctl enable sshd 2>/dev/null || systemctl enable ssh 2>/dev/null || true;
fi`

	maskTimerServicesScript = `# Mask unnecessary timer services for microVM
systemctl mask e2scrub_all.timer e2scrub_reap.service apt-daily.timer apt-daily-upgrade.timer 2>/dev/null || true`

	alpineSSHEnableScript = `# Pre-enable SSH daemon for OpenRC
rc-update add sshd default 2>/dev/null || rc-update add ssh default 2>/dev/null || true`

	alpineParallelStartupScript = `# Enable parallel service startup (microVM optimisation)
sed -i '/^rc_parallel=/d; /^#rc_parallel=/d' /etc/rc.conf 2>/dev/null; echo 'rc_parallel="YES"' >> /etc/rc.conf`

	firstBootInstallerScript = `#!/bin/bash
if ! command -v sshd >/dev/null 2>&1 && ! command -v ssh >/dev/null 2>&1; then
  if command -v pacman >/dev/null 2>&1; then pacman -Sy --noconfirm openssh 2>/dev/null || true;
  elif command -v apt-get >/dev/null 2>&1; then apt-get update && apt-get install -y openssh-server 2>/dev/null || true;
  elif command -v apk >/dev/null 2>&1; then apk add --no-cache openssh 2>/dev/null || true; fi;
fi
if command -v systemctl >/dev/null 2>&1; then
  systemctl enable --now sshd 2>/dev/null || systemctl enable --now ssh 2>/dev/null || true;
elif [ -f /sbin/openrc ]; then
  rc-update add sshd default 2>/dev/null || rc-update add ssh default 2>/dev/null || true;
  rc-service sshd start 2>/dev/null || rc-service ssh start 2>/dev/null || true;
fi
systemctl disable first-boot-ssh-installer.service 2>/dev/null || true`

	firstBootServiceScript = `[Unit]
Description=First-boot SSH installer
After=network.target
ConditionFirstBoot=yes

[Service]
Type=oneshot
ExecStart=/usr/local/bin/first-boot-ssh-installer.sh
RemainAfterExit=yes

[Install]
WantedBy=multi-user.target`

	hostsTemplate = `127.0.0.1	localhost
127.0.1.1	%s

::1	localhost ip6-localhost ip6-loopback
fe00::0	ip6-localnet
ff00::0	ip6-mcastprefix
ff02::1	ip6-allnodes
ff02::2	ip6-allrouters`

	alpineDhcpcdScript = `grep -qs '^denyinterfaces eth0' /etc/dhcpcd.conf 2>/dev/null || echo 'denyinterfaces eth0' >> /etc/dhcpcd.conf
sed -i 's/iface eth0 inet dhcp/iface eth0 inet manual/' /etc/network/interfaces`

	alpineCloudInitDisableScript = `# Disable cloud-init services for faster boot
rc-update del cloud-init default 2>/dev/null || true
rc-update del cloud-config default 2>/dev/null || true
rc-update del cloud-final default 2>/dev/null || true
rc-update del cloud-init-hotplugd default 2>/dev/null || true
rc-update del cloud-init ssh 2>/dev/null || true`

	alpineSerialGettyScript = `# Disable serial getty on ttyS0
sed -i '/ttyS0/s/^/#/' /etc/inittab 2>/dev/null || true`

	archPacmanKeyInitScript = `if [ ! -f /etc/pacman.d/gnupg/pubring.gpg ]; then pacman-key --init 2>/dev/null || true; fi`

	archPacmanKeyPopulateScript = `if [ -f /etc/pacman.d/gnupg/pubring.gpg ]; then pacman-key --populate archlinux 2>/dev/null || true; fi`

	archHostnameLocaleScript = `echo 'mvm' > /etc/hostname 2>/dev/null || true
echo 'LANG=en_US.UTF-8' > /etc/locale.conf 2>/dev/null || true
echo 'KEYMAP=us' > /etc/vconsole.conf 2>/dev/null || true`

	archBtrfsMkinitcpioScript = `# Remove btrfs mkinitcpio hook (not needed for single-device btrfs)
if [ -f /etc/mkinitcpio.conf ]; then
  sed -i 's/ btrfs / /g' /etc/mkinitcpio.conf 2>/dev/null || true
fi`

	archPacmanInitMaskScript = `ln -sf /dev/null /etc/systemd/system/pacman-init.service 2>/dev/null || true`

	archFirstBootMaskScript = `ln -sf /dev/null /etc/systemd/system/systemd-firstboot.service 2>/dev/null || true`

	archUdevSettleMaskScript = `ln -sf /dev/null /etc/systemd/system/systemd-udev-settle.service 2>/dev/null || true`

	archBtrfsBalanceScript = `# Consolidate btrfs metadata chunks
command -v btrfs >/dev/null 2>&1 && btrfs balance start -dusage=0 / 2>/dev/null || true`

	cloudInitMaskScript = `if command -v systemctl >/dev/null 2>&1; then
  for svc in \
    cloud-init.service \
    cloud-init-local.service \
    cloud-config.service \
    cloud-final.service; do
    ln -sf /dev/null "/etc/systemd/system/$svc" 2>/dev/null || true
  done
fi`

	fixFstabScript = `if [ -f /etc/fstab ]; then
  sed -i '/^PARTUUID=/s/^/#/' /etc/fstab
  sed -i '/^UUID=/s/^/#/' /etc/fstab
  sed -i '/^\/dev\/vda\s/ s/defaults/noatime,defaults/' /etc/fstab
  sed -i '/\/boot\/efi/s/^/#/' /etc/fstab
  sed -i '/ swap /s/^/#/' /etc/fstab
  sed -i '/\/dev\/vda[0-9]/s/^/#/' /etc/fstab
fi`
)

// SSHDConfig returns content for /etc/ssh/sshd_config.d/mvm.conf.
func (Builder) SSHDConfig(user string) string {
	lines := []string{
		"PubkeyAuthentication yes",
		"AuthorizedKeysFile .ssh/authorized_keys",
		"PasswordAuthentication no",
		"PermitEmptyPasswords no",
		"UsePAM yes",
		"UseDNS no",
		"GSSAPIAuthentication no",
	}
	if user != "root" {
		lines = append(lines, fmt.Sprintf("AllowUsers %s", user))
	} else {
		lines = append(lines, "PermitRootLogin prohibit-password")
	}
	return strings.Join(lines, "\n") + "\n"
}

// FirstBootInstaller returns content for /usr/local/bin/first-boot-ssh-installer.sh.
func (Builder) FirstBootInstaller() string {
	return firstBootInstallerScript + "\n"
}

// FirstBootService returns content for /etc/systemd/system/first-boot-ssh-installer.service.
func (Builder) FirstBootService() string {
	return firstBootServiceScript + "\n"
}

// Hosts returns content for /etc/hosts with a 127.0.1.1 entry.
func (Builder) Hosts(hostname string) string {
	return fmt.Sprintf(hostsTemplate+"\n", hostname)
}

// BuildHostnameOps generates operations for setting hostname and /etc/hosts.
func (pc Builder) BuildHostnameOps(hostname string) []Operation {
	return []Operation{
		FileOp{
			Path: "/etc/hostname",
			Data: []byte(hostname), // no trailing newline
			Mode: 0644,
			UID:  0,
			GID:  0,
		},
		FileOp{
			Path: "/etc/hosts",
			Data: []byte(pc.Hosts(hostname)),
			Mode: 0644,
			UID:  0,
			GID:  0,
		},
	}
}

// BuildDNSOps generates operation for injecting DNS resolver.
func (Builder) BuildDNSOps(dnsServer string) []Operation {
	return []Operation{
		FileOp{
			Path: "/etc/resolv.conf",
			Data: fmt.Appendf([]byte(nil), "nameserver %s\n", dnsServer),
			Mode: 0644,
			UID:  0,
			GID:  0,
		},
	}
}

// BuildSSHOps generates operations for SSH key injection and SSHD config.
func (pc Builder) BuildSSHOps(user string, sshPubkeys []string) []Operation {
	var ops []Operation
	if len(sshPubkeys) == 0 {
		return ops
	}

	keyData := []byte(strings.Join(sshPubkeys, "\n") + "\n")

	// ALWAYS inject into /root/.ssh/authorized_keys
	ops = append(ops, FileOp{
		Path: "/root/.ssh/authorized_keys",
		Data: keyData,
		Mode: 0600,
		UID:  0,
		GID:  0,
	})

	if user == "root" {
		// Fix /root ownership — some cloud images (e.g. Ubuntu cloud)
		// ship with /root owned by a non-root user. SSHD's StrictModes
		// checks home directory ownership and rejects publickey auth if
		// it's not owned by the target user.
		ops = append(ops, ChrootOp{Command: "chown root:root /root"})
	} else {
		userHome := "/home/" + user

		// ALSO inject into the non-root user's authorized_keys
		ops = append(ops, FileOp{
			Path: userHome + "/.ssh/authorized_keys",
			Data: keyData,
			Mode: 0600,
			UID:  0,
			GID:  0,
		})

		ops = append(ops, ChrootOp{Command: fmt.Sprintf("useradd -m %s", user)})
		// Fix ownership: useradd -m creates home owned by root:root in chroot
		ops = append(ops, ChrootOp{Command: fmt.Sprintf("chown %s:%s %s", user, user, userHome)})
		ops = append(ops, ChrootOp{Command: fmt.Sprintf("chown %s:%s %s/.ssh", user, user, userHome)})
		ops = append(ops, ChrootOp{Command: fmt.Sprintf("chown %s:%s %s/.ssh/authorized_keys", user, user, userHome)})
	}

	// --- Static SSH infrastructure (identical for every VM from this image) ---
	// These ops set up the SSH daemon config, first-boot installer, host keys,
	// and service enablement. Moved here from BuildDeblobOps so they are always
	// produced when SSH keys are injected, regardless of deblobbing.
	ops = append(ops, FileOp{
		Path: "/etc/ssh/sshd_config.d/mvm.conf",
		Data: []byte(pc.SSHDConfig("root")),
		Mode: 0644,
		UID:  0,
		GID:  0,
	})
	ops = append(ops, FileOp{
		Path: "/usr/local/bin/first-boot-ssh-installer.sh",
		Data: []byte(pc.FirstBootInstaller()),
		Mode: 0755,
		UID:  0,
		GID:  0,
	})
	ops = append(ops, FileOp{
		Path: "/etc/systemd/system/first-boot-ssh-installer.service",
		Data: []byte(pc.FirstBootService()),
		Mode: 0644,
		UID:  0,
		GID:  0,
	})
	ops = append(ops, ChrootOp{Command: "ssh-keygen -A"})
	ops = append(ops, ChrootOp{Command: enableSSHScript})

	return ops
}

// SetupSudo generates operations to ensure sudo works inside the guest.
// Fixes broken ownership in some cloud images (Ubuntu cloud ships with
// /etc/sudo.conf and /usr/bin/sudo owned by uid 1000 instead of root,
// which causes "sudo: /etc/sudo.conf is owned by uid 1000, should be 0").
// Also creates a sudoers drop-in granting passwordless sudo to the user.
func (Builder) SetupSudo(user string) []Operation {
	return []Operation{
		ChrootOp{Command: "mkdir -p /etc/sudoers.d"},
		ChrootOp{Command: fmt.Sprintf("echo '%s ALL=(ALL) NOPASSWD: ALL' > /etc/sudoers.d/%s", user, user)},
		ChrootOp{Command: fmt.Sprintf("chmod 440 /etc/sudoers.d/%s", user)},
		ChrootOp{Command: `chown root:root /etc/sudo.conf && \
chmod 0440 /etc/sudo.conf && \
chown root:root /etc/sudoers && \
chmod 0440 /etc/sudoers && \
chown root:root -R /etc/sudoers.d && \
chown root:root /usr/bin/sudo && \
chmod 4755 /usr/bin/sudo`},
	}
}

// BuildCloudInitDisableOps generates operations to disable cloud-init.
func (Builder) BuildCloudInitDisableOps() []Operation {
	ops := []Operation{
		FileOp{
			Path: "/etc/cloud/cloud.cfg.d/99-disable-datasources.cfg",
			Data: CloudInitDisableDatasource,
			Mode: 0644,
			UID:  0,
			GID:  0,
		},
		FileOp{
			Path: "/etc/cloud/cloud-init.disabled",
			Data: CloudInitDisabledMarker,
			Mode: 0644,
			UID:  0,
			GID:  0,
		},
		FileOp{
			Path: "/etc/systemd/system/snapd.seeded.service.d/override.conf",
			Data: SnapdOverride,
			Mode: 0644,
			UID:  0,
			GID:  0,
		},
		FileOp{
			Path: "/etc/systemd/system/systemd-networkd-wait-online.service.d/override.conf",
			Data: NetworkdWaitOverride,
			Mode: 0644,
			UID:  0,
			GID:  0,
		},
	}
	for _, svc := range []string{
		"cloud-init.service",
		"cloud-init-local.service",
		"cloud-config.service",
		"cloud-final.service",
	} {
		ops = append(ops, ChrootOp{
			Command: fmt.Sprintf("ln -sf /dev/null /etc/systemd/system/%s", svc),
		})
	}
	return ops
}

// BuildCloudInitInjectOps generates operations to inject cloud-init seed directory.
// Returns empty list if the directory does not exist.
func (Builder) BuildCloudInitInjectOps(cloudInitDir string) []Operation {
	if _, err := os.Stat(cloudInitDir); os.IsNotExist(err) {
		return nil
	}
	return []Operation{
		CopyDirOp{
			Src: cloudInitDir,
			Dst: "/var/lib/cloud/seed/nocloud-net",
		},
	}
}

// BuildResizeOps generates operation for filesystem resize (grow).
func (Builder) BuildResizeOps(targetSizeBytes int64) []Operation {
	return []Operation{
		ResizeOp{
			Action: ResizeActionGrow,
			Bytes:  targetSizeBytes,
		},
	}
}

// BuildShrinkOps generates operation for filesystem shrink to minimum size.
func (Builder) BuildShrinkOps(limitBytes int64) []Operation {
	return []Operation{
		ResizeOp{
			Action: ResizeActionShrink,
			Bytes:  limitBytes,
		},
	}
}

// BuildDeblobOps generates OS cache cleanup, SSH config, and cloud-init disable operations.
//
// These operations run once at image import time — they are identical
// for every VM from the same image.
func (pc Builder) BuildDeblobOps(osType string) []Operation {
	var ops []Operation

	// --- Common cleanup (all distros) ---
	ops = append(ops, ChrootOp{Command: "rm -rf /var/log/* /tmp/* /var/tmp/* 2>/dev/null || true"})
	ops = append(
		ops,
		ChrootOp{Command: "rm -rf /usr/share/doc/* /usr/share/man/* /usr/share/info/* 2>/dev/null || true"},
	)
	ops = append(ops, ChrootOp{Command: "find /var/log -type f -delete 2>/dev/null || true"})

	// --- MicroVM boot optimizations (systemd) ---
	ops = append(ops, ChrootOp{Command: maskServicesScript})

	// --- OS-specific cache cleanup ---
	osLower := strings.ToLower(osType)
	switch {
	case osLower == "ubuntu" || osLower == "debian":
		ops = append(ops, ChrootOp{Command: "apt-get clean 2>/dev/null || true"})
		ops = append(ops, ChrootOp{Command: "rm -rf /var/cache/apt/archives/*.deb 2>/dev/null || true"})
		ops = append(ops, ChrootOp{Command: "rm -rf /var/cache/debconf/* 2>/dev/null || true"})
		ops = append(ops, ChrootOp{Command: maskTimerServicesScript})

	case osLower == "alpine":
		ops = append(ops, ChrootOp{Command: "apk cache clean 2>/dev/null || true"})
		ops = append(ops, ChrootOp{Command: "rm -rf /var/cache/apk/* 2>/dev/null || true"})
		ops = append(
			ops,
			ChrootOp{Command: alpineDhcpcdScript},
		)
		ops = append(ops, ChrootOp{Command: alpineSSHEnableScript})
		ops = append(ops, ChrootOp{Command: alpineParallelStartupScript})
		ops = append(ops, ChrootOp{Command: alpineCloudInitDisableScript})
		ops = append(ops, ChrootOp{Command: "rc-update del chronyd default 2>/dev/null || true"})
		ops = append(ops, ChrootOp{Command: alpineSerialGettyScript})

	case osLower == "arch" || osLower == "archlinux" || osLower == "manjaro":
		ops = append(ops, ChrootOp{Command: "pacman -Sc --noconfirm 2>/dev/null || true"})
		ops = append(ops, ChrootOp{Command: "rm -rf /var/cache/pacman/pkg/* 2>/dev/null || true"})
		ops = append(
			ops,
			ChrootOp{Command: archPacmanKeyInitScript},
		)
		ops = append(
			ops,
			ChrootOp{Command: archPacmanKeyPopulateScript},
		)
		ops = append(
			ops,
			ChrootOp{Command: archHostnameLocaleScript},
		)
		ops = append(
			ops,
			ChrootOp{Command: archPacmanInitMaskScript},
		)
		ops = append(
			ops,
			ChrootOp{Command: archFirstBootMaskScript},
		)
		ops = append(ops, ChrootOp{Command: archBtrfsMkinitcpioScript})
		ops = append(ops, ChrootOp{Command: archBtrfsBalanceScript})
		ops = append(
			ops,
			ChrootOp{Command: archUdevSettleMaskScript},
		)

	case osLower == "fedora" || osLower == "centos" || osLower == "rhel" ||
		osLower == "rocky" || osLower == "almalinux":
		ops = append(ops, ChrootOp{
			Command: "dnf clean all 2>/dev/null || yum clean all 2>/dev/null || true",
		})
		ops = append(ops, ChrootOp{
			Command: "rm -rf /var/cache/dnf/* /var/cache/yum/* 2>/dev/null || true",
		})

	default:
		// Generic: clear all cache dirs
		ops = append(ops, ChrootOp{Command: "rm -rf /var/cache/* 2>/dev/null || true"})
	}

	// --- Cloud-init disable (all distros) ---
	ops = append(ops, FileOp{
		Path: "/etc/cloud/cloud.cfg.d/99-disable-datasources.cfg",
		Data: CloudInitDisableDatasource,
		Mode: 0644,
		UID:  0,
		GID:  0,
	})
	ops = append(ops, FileOp{
		Path: "/etc/cloud/cloud-init.disabled",
		Data: CloudInitDisabledMarker,
		Mode: 0644,
		UID:  0,
		GID:  0,
	})
	ops = append(ops, FileOp{
		Path: "/etc/systemd/system/snapd.seeded.service.d/override.conf",
		Data: SnapdOverride,
		Mode: 0644,
		UID:  0,
		GID:  0,
	})
	ops = append(ops, FileOp{
		Path: "/etc/systemd/system/systemd-networkd-wait-online.service.d/override.conf",
		Data: NetworkdWaitOverride,
		Mode: 0644,
		UID:  0,
		GID:  0,
	})
	ops = append(ops, ChrootOp{Command: cloudInitMaskScript})

	ops = append(ops, ChrootOp{Command: "rm -rf /var/lib/apt/lists/* 2>/dev/null || true"})

	return ops
}

// BuildVsockAgentOps generates operations for embedding the vsock guest agent
// into the root filesystem. Used by both loop-mount and guestfs provisioner
// backends at VM creation time.
//
// Produces:
// - FileOp: /usr/bin/mvm-vsock-agent (agent binary, mode 0755)
// - FileOp: /var/run/mvm-vsock-agent.token (auth token, mode 0644)
// - FileOp: /etc/systemd/system/mvm-vsock-agent.service (systemd unit, mode 0644)
// - FileOp: /etc/init.d/mvm-vsock-agent (OpenRC init script, mode 0755)
// - ChrootOp: detect init system and enable agent
func (Builder) BuildVsockAgentOps(agentBinary []byte, port int, token string) []Operation {
	return []Operation{
		FileOp{
			Path: "/usr/bin/mvm-vsock-agent",
			Data: agentBinary,
			Mode: 0755,
			UID:  0,
			GID:  0,
		},
		FileOp{
			Path: "/var/run/mvm-vsock-agent.token",
			Data: []byte(token),
			Mode: 0600,
			UID:  0,
			GID:  0,
		},
		FileOp{
			Path: "/etc/systemd/system/mvm-vsock-agent.service",
			Data: fmt.Appendf(nil, `[Unit]
Description=MVM VSock Agent
DefaultDependencies=no

[Service]
Type=simple
ExecStart=/usr/bin/mvm-vsock-agent -port %d
Restart=always
RestartSec=2

[Install]
WantedBy=sysinit.target
`, port),
			Mode: 0644,
			UID:  0,
			GID:  0,
		},
		FileOp{
			Path: "/etc/init.d/mvm-vsock-agent",
			Data: fmt.Appendf(nil, `#!/sbin/openrc-run

description="MVM VSock Agent"

command=/usr/bin/mvm-vsock-agent
command_args="-port %d"
pidfile=/var/run/mvm-vsock-agent.pid
command_background=true

depend() {
    need localmount
}
`, port),
			Mode: 0755,
			UID:  0,
			GID:  0,
		},
		ChrootOp{
			Command: `
	if command -v systemctl >/dev/null 2>&1; then
    mkdir -p /etc/systemd/system/multi-user.target.wants 2>/dev/null || true
    ln -sf /etc/systemd/system/mvm-vsock-agent.service /etc/systemd/system/multi-user.target.wants/mvm-vsock-agent.service 2>/dev/null || true
elif rc-update >/dev/null 2>&1; then
    rc-update add mvm-vsock-agent default
else
    echo "mvm: warning - unknown init system, mvm-vsock-agent not auto-enabled"
fi
`,
		},
	}
}

// BuildFixFstabOps generates operation to fix /etc/fstab for Firecracker.
func (Builder) BuildFixFstabOps() []Operation {
	return []Operation{
		ChrootOp{Command: fixFstabScript},
	}
}
