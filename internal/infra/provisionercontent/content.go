// Package provisionercontent holds shared provisioning content types and builders.
//
// This package is intentionally separate from "mvmctl/internal/infra/provisioner"
// to avoid circular imports: guestfs and loopmount packages import this for
// Operation types and ProvisionerContent, while provisioner/backend.go imports
// guestfs and loopmount directly.
//
// Mirrors Python's src/mvmctl/core/_shared/_provisioner/_content.py exactly.
package provisionercontent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// =========================================================================
// Operation types — pure data, no execution logic
// Matches Python's dataclasses in _content.py exactly.
// =========================================================================

// FileOpDefaultMode is the default file permission mode matching Python's
// FileOp(mode=0o644) default. Go zero-initializes int fields to 0, so
// callers who create FileOp without setting Mode will get 0000 permissions
// instead of 0644. Use this constant or NewFileOp() to ensure correct defaults.
const FileOpDefaultMode = 0644

// FileOp represents writing a file inside the root filesystem.
// Matches Python @dataclass FileOp.
//
// IMPORTANT: Go zero-initializes Mode to 0, NOT 0644 like Python's default.
// Always set Mode explicitly, use FileOpDefaultMode, or use NewFileOp().
type FileOp struct {
	Path string
	Data []byte
	Mode int
	UID  int
	GID  int
}

// ChrootOp represents running a shell command inside a chroot environment.
// Matches Python @dataclass ChrootOp.
type ChrootOp struct {
	Command string
}

// CopyDirOp represents copying a directory tree into the root filesystem.
// Matches Python @dataclass CopyDirOp.
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
// Matches Python @dataclass ResizeOp.
type ResizeOp struct {
	Action ResizeAction
	Bytes  int64
}

// Operation is a union type matching Python's "Operation = FileOp | ChrootOp | CopyDirOp | ResizeOp".
type Operation interface {
	operationMarker()
}

func (FileOp) operationMarker()    {}
func (ChrootOp) operationMarker()  {}
func (CopyDirOp) operationMarker() {}
func (ResizeOp) operationMarker()  {}

// NewFileOp creates a FileOp with the default mode (0644) matching Python's
// FileOp(mode=0o644) default, and uid=0, gid=0.
//
// This is the recommended way to create FileOp values — it ensures the mode
// default matches Python behavior even if the caller forgets to set Mode.
func NewFileOp(path string, data []byte) FileOp {
	return FileOp{
		Path: path,
		Data: data,
		Mode: FileOpDefaultMode,
		UID:  0,
		GID:  0,
	}
}

// =========================================================================
// ProvisionerContent — shared provisioning content
// Matches Python's ProvisionerContent class exactly.
//
// - Raw content methods return plain strings/bytes (single source of truth).
// - Builder methods wrap raw content into Operation types.
// =========================================================================

type ProvisionerContent struct{}

// ---------------------------------------------------------------------------
// Raw content methods
// ---------------------------------------------------------------------------

// SSHDConfig returns content for /etc/ssh/sshd_config.d/mvm.conf.
// Matches Python's sshd_config() static method.
func (ProvisionerContent) SSHDConfig(user string) string {
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
// Matches Python's first_boot_installer() static method.
func (ProvisionerContent) FirstBootInstaller() string {
	return "#!/bin/bash\n" +
		"if ! command -v sshd >/dev/null 2>&1 && " +
		"! command -v ssh >/dev/null 2>&1; then\n" +
		"  if command -v pacman >/dev/null 2>&1; then " +
		"pacman -Sy --noconfirm openssh 2>/dev/null || true;\n" +
		"  elif command -v apt-get >/dev/null 2>&1; then " +
		"apt-get update && apt-get install -y openssh-server " +
		"2>/dev/null || true;\n" +
		"  elif command -v apk >/dev/null 2>&1; then " +
		"apk add --no-cache openssh 2>/dev/null || true; fi;\n" +
		"fi\n" +
		"if command -v systemctl >/dev/null 2>&1; then\n" +
		"  systemctl enable --now sshd 2>/dev/null || " +
		"systemctl enable --now ssh 2>/dev/null || true;\n" +
		"elif [ -f /sbin/openrc ]; then\n" +
		"  rc-update add sshd default 2>/dev/null || " +
		"rc-update add ssh default 2>/dev/null || true;\n" +
		"  rc-service sshd start 2>/dev/null || " +
		"rc-service ssh start 2>/dev/null || true;\n" +
		"fi\n" +
		"systemctl disable first-boot-ssh-installer.service " +
		"2>/dev/null || true\n"
}

// FirstBootService returns content for /etc/systemd/system/first-boot-ssh-installer.service.
// Matches Python's first_boot_service() static method.
func (ProvisionerContent) FirstBootService() string {
	return "[Unit]\n" +
		"Description=First-boot SSH installer\n" +
		"After=network.target\n" +
		"ConditionFirstBoot=yes\n\n" +
		"[Service]\n" +
		"Type=oneshot\n" +
		"ExecStart=/usr/local/bin/first-boot-ssh-installer.sh\n" +
		"RemainAfterExit=yes\n\n" +
		"[Install]\n" +
		"WantedBy=multi-user.target\n"
}

// Hosts returns content for /etc/hosts with a 127.0.1.1 entry.
// Matches Python's hosts() static method.
func (ProvisionerContent) Hosts(hostname string) string {
	return fmt.Sprintf("127.0.0.1\tlocalhost\n"+
		"127.0.1.1\t%s\n"+
		"\n"+
		"::1\tlocalhost ip6-localhost ip6-loopback\n"+
		"fe00::0\tip6-localnet\n"+
		"ff00::0\tip6-mcastprefix\n"+
		"ff02::1\tip6-allnodes\n"+
		"ff02::2\tip6-allrouters\n", hostname)
}

// Cloud-init disable content constants.
// Matches Python's class-level constants.
var (
	CloudInitDisableDatasource = []byte("datasource_list: [None]\n")
	CloudInitDisabledMarker    = []byte("disabled by mvmctl\n")
	SnapdOverride              = []byte("[Service]\nExecStart=\nExecStart=/bin/true\n")
	NetworkdWaitOverride       = []byte("[Unit]\nConditionPathExists=/nonexistent-disabled-by-mvm\n")
)

// ---------------------------------------------------------------------------
// Builder methods — wrap raw content into Operation objects
// ---------------------------------------------------------------------------

// BuildHostnameOps generates operations for setting hostname and /etc/hosts.
// Matches Python's build_hostname_ops() classmethod.
func (pc ProvisionerContent) BuildHostnameOps(hostname string) []Operation {
	return []Operation{
		FileOp{
			Path: "/etc/hostname",
			Data: []byte(hostname), // Python: no trailing newline
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
// Matches Python's build_dns_ops() classmethod.
func (ProvisionerContent) BuildDNSOps(dnsServer string) []Operation {
	return []Operation{
		FileOp{
			Path: "/etc/resolv.conf",
			Data: []byte(fmt.Sprintf("nameserver %s\n", dnsServer)),
			Mode: 0644,
			UID:  0,
			GID:  0,
		},
	}
}

// BuildSSHOps generates operations for SSH key injection and SSHD config.
// Matches Python's build_ssh_ops() classmethod.
func (pc ProvisionerContent) BuildSSHOps(user string, sshPubkeys []string) []Operation {
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

	if user != "root" {
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
		ops = append(ops, ChrootOp{Command: "mkdir -p /etc/sudoers.d"})
		ops = append(ops, ChrootOp{Command: fmt.Sprintf("echo '%s ALL=(ALL) NOPASSWD: ALL' > /etc/sudoers.d/%s", user, user)})
		ops = append(ops, ChrootOp{Command: fmt.Sprintf("chmod 440 /etc/sudoers.d/%s", user)})
	}

	return ops
}

// BuildCloudInitDisableOps generates operations to disable cloud-init.
// Matches Python's build_cloud_init_disable_ops() classmethod.
func (ProvisionerContent) BuildCloudInitDisableOps() []Operation {
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
// Matches Python's build_cloud_init_inject_ops() classmethod — returns empty
// list if the directory does not exist.
func (ProvisionerContent) BuildCloudInitInjectOps(cloudInitDir string) []Operation {
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
// Matches Python's build_resize_ops() classmethod.
func (ProvisionerContent) BuildResizeOps(targetSizeBytes int64) []Operation {
	return []Operation{
		ResizeOp{
			Action: ResizeActionGrow,
			Bytes:  targetSizeBytes,
		},
	}
}

// BuildShrinkOps generates operation for filesystem shrink to minimum size.
// Matches Python's build_shrink_ops() classmethod.
func (ProvisionerContent) BuildShrinkOps(limitBytes int64) []Operation {
	return []Operation{
		ResizeOp{
			Action: ResizeActionShrink,
			Bytes:  limitBytes,
		},
	}
}

// BuildDeblobOps generates OS cache cleanup, SSH config, and cloud-init disable operations.
// Matches Python's build_deblob_ops() classmethod (~320 lines).
//
// These operations run once at image import time — they are identical
// for every VM from the same image.
func (pc ProvisionerContent) BuildDeblobOps(osType string) []Operation {
	var ops []Operation

	// ── Common cleanup (all distros) ──────────────────────────────
	ops = append(ops, ChrootOp{Command: "rm -rf /var/log/* /tmp/* /var/tmp/* 2>/dev/null || true"})
	ops = append(ops, ChrootOp{Command: "rm -rf /usr/share/doc/* /usr/share/man/* /usr/share/info/* 2>/dev/null || true"})
	ops = append(ops, ChrootOp{Command: "find /var/log -type f -delete 2>/dev/null || true"})

	// ── MicroVM boot optimizations (systemd) ──────────────────────────
	ops = append(ops, ChrootOp{
		Command: "# Mask non-essential systemd services for faster microVM boot\n" +
			"if command -v systemctl >/dev/null 2>&1; then\n" +
			"  for svc in \\\n" +
			"    systemd-timesyncd.service \\\n" +
			"    systemd-time-wait-sync.service \\\n" +
			"    systemd-firstboot.service \\\n" +
			"    ldconfig.service \\\n" +
			"    modprobe@drm.service \\\n" +
			"    modprobe@efi_pstore.service \\\n" +
			"    sys-kernel-debug.mount \\\n" +
			"    pollinate.service \\\n" +
			"    snapd.service \\\n" +
			"    snapd.socket \\\n" +
			"    systemd-udev-settle.service \\\n" +
			"    unattended-upgrades.service \\\n" +
			"    packagekit.service \\\n" +
			"    man-db.timer \\\n" +
			"    whoopsie.service \\\n" +
			"    apport.service \\\n" +
			"    udisks2.service \\\n" +
			"    console-setup.service \\\n" +
			"    keyboard-setup.service \\\n" +
			"    motd-news.service \\\n" +
			"    fstrim.timer \\\n" +
			"    logrotate.timer \\\n" +
			"    multipathd.service \\\n" +
			"    accounts-daemon.service \\\n" +
			"    systemd-userdbd.service \\\n" +
			"    systemd-nsresourced.service \\\n" +
			"    systemd-pcrphase.service \\\n" +
			"    systemd-pcrphase-initrd.service \\\n" +
			"    systemd-pcrphase-sysinit.service \\\n" +
			"    systemd-boot-update.service; do\n" +
			`    ln -sf /dev/null "/etc/systemd/system/$svc" 2>/dev/null || true` + "\n" +
			"  done\n" +
			"fi",
	})

	// ── SSH daemon configuration (identical for every VM from this image) ──
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
	ops = append(ops, ChrootOp{
		Command: "if command -v systemctl >/dev/null 2>&1; then\n" +
			"  systemctl enable sshd 2>/dev/null || " +
			"systemctl enable ssh 2>/dev/null || true;\n" +
			"fi",
	})

	// ── OS-specific cache cleanup ─────────────────────────────────
	osLower := strings.ToLower(osType)
	switch {
	case osLower == "ubuntu" || osLower == "debian":
		ops = append(ops, ChrootOp{Command: "apt-get clean 2>/dev/null || true"})
		ops = append(ops, ChrootOp{Command: "rm -rf /var/cache/apt/archives/*.deb 2>/dev/null || true"})
		ops = append(ops, ChrootOp{Command: "rm -rf /var/cache/debconf/* 2>/dev/null || true"})
		ops = append(ops, ChrootOp{
			Command: "# Mask unnecessary timer services for microVM\n" +
				"systemctl mask e2scrub_all.timer " +
				"e2scrub_reap.service " +
				"apt-daily.timer " +
				"apt-daily-upgrade.timer " +
				"2>/dev/null || true",
		})

	case osLower == "alpine":
		ops = append(ops, ChrootOp{Command: "apk cache clean 2>/dev/null || true"})
		ops = append(ops, ChrootOp{Command: "rm -rf /var/cache/apk/* 2>/dev/null || true"})
		ops = append(ops, ChrootOp{
			Command: "grep -qs '^denyinterfaces eth0' /etc/dhcpcd.conf " +
				"2>/dev/null || echo 'denyinterfaces eth0' >> /etc/dhcpcd.conf; " +
				"sed -i 's/iface eth0 inet dhcp/iface eth0 inet manual/' " +
				"/etc/network/interfaces",
		})
		ops = append(ops, ChrootOp{
			Command: "# Pre-enable SSH daemon for OpenRC\n" +
				"rc-update add sshd default 2>/dev/null || " +
				"rc-update add ssh default 2>/dev/null || true",
		})
		ops = append(ops, ChrootOp{
			Command: "# Enable parallel service startup (microVM optimisation)\n" +
				"sed -i '/^rc_parallel=/d; /^#rc_parallel=/d' /etc/rc.conf 2>/dev/null; " +
				`echo 'rc_parallel="YES"' >> /etc/rc.conf`,
		})
		ops = append(ops, ChrootOp{
			Command: "# Disable cloud-init services for faster boot\n" +
				"rc-update del cloud-init default 2>/dev/null || true; " +
				"rc-update del cloud-config default 2>/dev/null || true; " +
				"rc-update del cloud-final default 2>/dev/null || true; " +
				"rc-update del cloud-init-hotplugd default 2>/dev/null || true; " +
				"rc-update del cloud-init ssh 2>/dev/null || true",
		})
		ops = append(ops, ChrootOp{Command: "rc-update del chronyd default 2>/dev/null || true"})
		ops = append(ops, ChrootOp{
			Command: "# Disable serial getty on ttyS0\n" +
				"sed -i '/ttyS0/s/^/#/' /etc/inittab 2>/dev/null || true",
		})

	case osLower == "arch" || osLower == "archlinux" || osLower == "manjaro":
		ops = append(ops, ChrootOp{Command: "pacman -Sc --noconfirm 2>/dev/null || true"})
		ops = append(ops, ChrootOp{Command: "rm -rf /var/cache/pacman/pkg/* 2>/dev/null || true"})
		ops = append(ops, ChrootOp{
			Command: "if [ ! -f /etc/pacman.d/gnupg/pubring.gpg ]; then " +
				"pacman-key --init 2>/dev/null || true; fi",
		})
		ops = append(ops, ChrootOp{
			Command: "if [ -f /etc/pacman.d/gnupg/pubring.gpg ]; then " +
				"pacman-key --populate archlinux 2>/dev/null || true; fi",
		})
		ops = append(ops, ChrootOp{
			Command: "echo 'mvm' > /etc/hostname 2>/dev/null || true; " +
				"echo 'LANG=en_US.UTF-8' > /etc/locale.conf 2>/dev/null || true; " +
				"echo 'KEYMAP=us' > /etc/vconsole.conf 2>/dev/null || true",
		})
		ops = append(ops, ChrootOp{
			Command: "ln -sf /dev/null " +
				"/etc/systemd/system/pacman-init.service 2>/dev/null || true",
		})
		ops = append(ops, ChrootOp{
			Command: "ln -sf /dev/null " +
				"/etc/systemd/system/systemd-firstboot.service 2>/dev/null || true",
		})
		ops = append(ops, ChrootOp{
			Command: "# Remove btrfs mkinitcpio hook (not needed for single-device btrfs)\n" +
				"if [ -f /etc/mkinitcpio.conf ]; then\n" +
				"  sed -i 's/ btrfs / /g' /etc/mkinitcpio.conf 2>/dev/null || true\n" +
				"fi",
		})
		ops = append(ops, ChrootOp{
			Command: "# Consolidate btrfs metadata chunks\n" +
				"command -v btrfs >/dev/null 2>&1 && " +
				"btrfs balance start -dusage=0 / " +
				"2>/dev/null || true",
		})
		ops = append(ops, ChrootOp{
			Command: "ln -sf /dev/null " +
				"/etc/systemd/system/systemd-udev-settle.service " +
				"2>/dev/null || true",
		})

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

	// ── Cloud-init disable (all distros) ──────────────────────────────
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
	ops = append(ops, ChrootOp{
		Command: "if command -v systemctl >/dev/null 2>&1; then\n" +
			"  for svc in \\\n" +
			"    cloud-init.service \\\n" +
			"    cloud-init-local.service \\\n" +
			"    cloud-config.service \\\n" +
			"    cloud-final.service; do\n" +
			`    ln -sf /dev/null "/etc/systemd/system/$svc" ` +
			"2>/dev/null || true\n" +
			"  done\n" +
			"fi",
	})

	ops = append(ops, ChrootOp{Command: "rm -rf /var/lib/apt/lists/* 2>/dev/null || true"})

	return ops
}

// BuildFixFstabOps generates operation to fix /etc/fstab for Firecracker.
// Matches Python's build_fix_fstab_ops() classmethod.
func (ProvisionerContent) BuildFixFstabOps() []Operation {
	return []Operation{
		ChrootOp{
			Command: "if [ -f /etc/fstab ]; then " +
				"sed -i '/^PARTUUID=/s/^/#/' /etc/fstab; " +
				"sed -i '/^UUID=/s/^/#/' /etc/fstab; " +
				"sed -i '/^\\/dev\\/vda\\s/ s/defaults/noatime,defaults/' /etc/fstab; " +
				"sed -i '/^PARTUUID=/s/^/#/' /etc/fstab; " +
				"sed -i '/\\/boot\\/efi/s/^/#/' /etc/fstab; " +
				"sed -i '/ swap /s/^/#/' /etc/fstab; " +
				"sed -i '/\\/dev\\/vda[0-9]/s/^/#/' /etc/fstab; " +
				"fi",
		},
	}
}

// =========================================================================
// Helpers
// =========================================================================

// JoinLines joins lines for a shell command execution.
func JoinLines(lines ...string) string {
	return strings.Join(lines, "\n")
}

// MaskServicePath returns the path for masking a systemd service.
func MaskServicePath(serviceName string) string {
	return filepath.Join("/etc/systemd/system", serviceName)
}
