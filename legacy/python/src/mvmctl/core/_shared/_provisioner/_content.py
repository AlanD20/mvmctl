"""
Shared provisioning content — single source of truth for file/command operations.

Usage::

    from mvmctl.core._shared._provisioner._content import ProvisionerContent

    # Guestfs backend: call raw content methods directly
    handle.write("/etc/ssh/sshd_config.d/mvm.conf",
                 ProvisionerContent.sshd_config("myuser"))

    # Loop-mount backend: use builder methods
    ops = ProvisionerContent.build_ssh_ops("myuser", pubkeys)
    for op in ops:
        match op:
            case FileOp(): ...
"""

from __future__ import annotations

from dataclasses import dataclass
from pathlib import Path
from typing import Literal

# =========================================================================
# Operation types — pure data, no execution logic
# =========================================================================


@dataclass
class FileOp:
    """Write a file inside the root filesystem."""

    path: str
    data: bytes
    mode: int = 0o644
    uid: int = 0
    gid: int = 0


@dataclass
class ChrootOp:
    """Run a shell command inside a chroot environment."""

    command: str


@dataclass
class CopyDirOp:
    """Copy a directory tree into the root filesystem."""

    src: str
    dst: str


@dataclass
class ResizeOp:
    """Resize the root filesystem (grow or shrink)."""

    action: Literal["grow", "shrink"]
    bytes: int = 0


Operation = FileOp | ChrootOp | CopyDirOp | ResizeOp


# =========================================================================
# Single source of truth for provisioning content
# =========================================================================


class ProvisionerContent:
    """Shared provisioning content — every method is a single source of truth.

    - **Raw content methods** (``sshd_config()``, etc.) return plain ``str``
      or ``bytes``.  ALL backends call these — this is the single source of
      truth for *what* to write.
    - **Builder methods** (``build_ssh_ops()``, etc.) wrap raw content into
      ``Operation`` dataclasses for backends that consume ops (loop-mount).

    Changing file content? Edit the raw method — once.  Every backend
    automatically gets the update.
    """

    # ═══════════════════════════════════════════════════════════════════
    # Raw content methods
    # ═══════════════════════════════════════════════════════════════════

    @staticmethod
    def sshd_config(user: str) -> str:
        """Content for ``/etc/ssh/sshd_config.d/mvm.conf``."""
        lines: list[str] = [
            "PubkeyAuthentication yes",
            "AuthorizedKeysFile .ssh/authorized_keys",
            "PasswordAuthentication no",
            "PermitEmptyPasswords no",
            "UsePAM yes",
            "UseDNS no",
            "GSSAPIAuthentication no",
        ]
        if user != "root":
            lines.append(f"AllowUsers {user}")
        else:
            lines.append("PermitRootLogin prohibit-password")
        return "\n".join(lines) + "\n"

    @staticmethod
    def first_boot_installer() -> str:
        """Content for ``/usr/local/bin/first-boot-ssh-installer.sh``."""
        return (
            "#!/bin/bash\n"
            "if ! command -v sshd >/dev/null 2>&1 && "
            "! command -v ssh >/dev/null 2>&1; then\n"
            "  if command -v pacman >/dev/null 2>&1; then "
            "pacman -Sy --noconfirm openssh 2>/dev/null || true;\n"
            "  elif command -v apt-get >/dev/null 2>&1; then "
            "apt-get update && apt-get install -y openssh-server "
            "2>/dev/null || true;\n"
            "  elif command -v apk >/dev/null 2>&1; then "
            "apk add --no-cache openssh 2>/dev/null || true; fi;\n"
            "fi\n"
            "if command -v systemctl >/dev/null 2>&1; then\n"
            "  systemctl enable --now sshd 2>/dev/null || "
            "systemctl enable --now ssh 2>/dev/null || true;\n"
            "elif [ -f /sbin/openrc ]; then\n"
            "  rc-update add sshd default 2>/dev/null || "
            "rc-update add ssh default 2>/dev/null || true;\n"
            "  rc-service sshd start 2>/dev/null || "
            "rc-service ssh start 2>/dev/null || true;\n"
            "fi\n"
            "systemctl disable first-boot-ssh-installer.service "
            "2>/dev/null || true\n"
        )

    @staticmethod
    def first_boot_service() -> str:
        """Content for ``/etc/systemd/system/first-boot-ssh-installer.service``."""
        return (
            "[Unit]\n"
            "Description=First-boot SSH installer\n"
            "After=network.target\n"
            "ConditionFirstBoot=yes\n\n"
            "[Service]\n"
            "Type=oneshot\n"
            "ExecStart=/usr/local/bin/first-boot-ssh-installer.sh\n"
            "RemainAfterExit=yes\n\n"
            "[Install]\n"
            "WantedBy=multi-user.target\n"
        )

    @staticmethod
    def hosts(hostname: str) -> str:
        """Content for ``/etc/hosts`` with a ``127.0.1.1`` entry."""
        return (
            "127.0.0.1\tlocalhost\n"
            f"127.0.1.1\t{hostname}\n"
            "\n"
            "::1\tlocalhost ip6-localhost ip6-loopback\n"
            "fe00::0\tip6-localnet\n"
            "ff00::0\tip6-mcastprefix\n"
            "ff02::1\tip6-allnodes\n"
            "ff02::2\tip6-allrouters\n"
        )

    # Cloud-init disable content constants
    CLOUD_INIT_DISABLE_DATASOURCE: bytes = b"datasource_list: [None]\n"
    CLOUD_INIT_DISABLED_MARKER: bytes = b"disabled by mvmctl\n"
    SNAPD_OVERRIDE: bytes = b"[Service]\nExecStart=\nExecStart=/bin/true\n"
    NETWORKD_WAIT_OVERRIDE: bytes = (
        b"[Unit]\nConditionPathExists=/nonexistent-disabled-by-mvm\n"
    )

    # ═══════════════════════════════════════════════════════════════════
    # Builder methods — wrap raw content into Operation objects
    # ═══════════════════════════════════════════════════════════════════

    @classmethod
    def build_hostname_ops(cls, hostname: str) -> list[Operation]:
        """Generate operations for setting hostname and /etc/hosts."""
        return [
            FileOp(
                path="/etc/hostname",
                data=hostname.encode("utf-8"),
                mode=0o644,
                uid=0,
                gid=0,
            ),
            FileOp(
                path="/etc/hosts",
                data=cls.hosts(hostname).encode("utf-8"),
                mode=0o644,
                uid=0,
                gid=0,
            ),
        ]

    @classmethod
    def build_dns_ops(cls, dns_server: str) -> list[Operation]:
        """Generate operation for injecting DNS resolver."""
        return [
            FileOp(
                path="/etc/resolv.conf",
                data=f"nameserver {dns_server}\n".encode("utf-8"),
                mode=0o644,
                uid=0,
                gid=0,
            ),
        ]

    @classmethod
    def build_ssh_ops(
        cls, user: str, ssh_pubkeys: list[str]
    ) -> list[Operation]:
        """Generate operations for SSH key injection and SSHD config."""
        ops: list[Operation] = []
        if not ssh_pubkeys:
            return ops

        key_data = ("\n".join(ssh_pubkeys) + "\n").encode("utf-8")

        # ALWAYS inject into /root/.ssh/authorized_keys
        ops.append(
            FileOp(
                path="/root/.ssh/authorized_keys",
                data=key_data,
                mode=0o600,
                uid=0,
                gid=0,
            )
        )

        if user != "root":
            user_home = f"/home/{user}"

            # ALSO inject into the non-root user's authorized_keys
            ops.append(
                FileOp(
                    path=f"{user_home}/.ssh/authorized_keys",
                    data=key_data,
                    mode=0o600,
                    uid=0,
                    gid=0,
                )
            )

            ops.append(ChrootOp(f"useradd -m {user}"))
            # Fix ownership: ``useradd -m`` creates the home directory
            # owned by root:root when running in chroot (the user's UID
            # is not resolvable at chroot time).  We must chown the home
            # directory, .ssh subdirectory, and authorized_keys so that:
            # 1. sshd accepts the authorized_keys (ownership check)
            # 2. The user can write files to their own home (tar pipe
            #    during ``mvm cp``, ssh key writes, etc.)
            #
            # Using individual chowns (not -R) to avoid following
            # symlinks to system files outside the chroot boundary.
            ops.append(ChrootOp(f"chown {user}:{user} {user_home}"))
            ops.append(ChrootOp(f"chown {user}:{user} {user_home}/.ssh"))
            ops.append(
                ChrootOp(
                    f"chown {user}:{user} {user_home}/.ssh/authorized_keys"
                )
            )
            ops.append(ChrootOp("mkdir -p /etc/sudoers.d"))
            ops.append(
                ChrootOp(
                    f"echo '{user} ALL=(ALL) NOPASSWD: ALL' > "
                    f"/etc/sudoers.d/{user}"
                )
            )
            ops.append(ChrootOp(f"chmod 440 /etc/sudoers.d/{user}"))

        return ops

    @classmethod
    def build_cloud_init_disable_ops(cls) -> list[Operation]:
        """Generate operations to disable cloud-init."""
        return [
            FileOp(
                path="/etc/cloud/cloud.cfg.d/99-disable-datasources.cfg",
                data=cls.CLOUD_INIT_DISABLE_DATASOURCE,
                mode=0o644,
                uid=0,
                gid=0,
            ),
            FileOp(
                path="/etc/cloud/cloud-init.disabled",
                data=cls.CLOUD_INIT_DISABLED_MARKER,
                mode=0o644,
                uid=0,
                gid=0,
            ),
            FileOp(
                path="/etc/systemd/system/snapd.seeded.service.d/override.conf",
                data=cls.SNAPD_OVERRIDE,
                mode=0o644,
                uid=0,
                gid=0,
            ),
            FileOp(
                path="/etc/systemd/system/"
                "systemd-networkd-wait-online.service.d/override.conf",
                data=cls.NETWORKD_WAIT_OVERRIDE,
                mode=0o644,
                uid=0,
                gid=0,
            ),
            *[
                ChrootOp(f"ln -sf /dev/null /etc/systemd/system/{svc}")
                for svc in (
                    "cloud-init.service",
                    "cloud-init-local.service",
                    "cloud-config.service",
                    "cloud-final.service",
                )
            ],
        ]

    @classmethod
    def build_cloud_init_inject_ops(
        cls, cloud_init_dir: Path
    ) -> list[Operation]:
        """Generate operations to inject cloud-init seed directory."""
        if not cloud_init_dir.exists():
            return []
        return [
            CopyDirOp(
                src=str(cloud_init_dir),
                dst="/var/lib/cloud/seed/nocloud-net",
            ),
        ]

    @classmethod
    def build_resize_ops(cls, target_size_bytes: int) -> list[Operation]:
        """Generate operation for filesystem resize (grow)."""
        return [
            ResizeOp(action="grow", bytes=target_size_bytes),
        ]

    @classmethod
    def build_shrink_ops(cls, limit_bytes: int = 0) -> list[Operation]:
        """Generate operation for filesystem shrink to minimum size."""
        return [
            ResizeOp(action="shrink", bytes=limit_bytes),
        ]

    @classmethod
    def build_deblob_ops(cls, os_type: str) -> list[Operation]:
        """Generate OS cache cleanup, SSH config, and cloud-init disable operations.

        These operations run once at image import time — they are identical
        for every VM from the same image.

        Args:
            os_type: Detected OS identifier (e.g. ``"ubuntu"``, ``"alpine"``,
                ``"arch"``, ``"debian"``, ``"fedora"``).

        Returns:
            List of FileOp/ChrootOp operations for OS cleanup.
        """
        ops: list[Operation] = []

        # ── Common cleanup (all distros) ──────────────────────────────
        ops.append(
            ChrootOp("rm -rf /var/log/* /tmp/* /var/tmp/* 2>/dev/null || true")
        )
        ops.append(
            ChrootOp(
                "rm -rf /usr/share/doc/* /usr/share/man/* /usr/share/info/* "
                "2>/dev/null || true"
            )
        )
        ops.append(
            ChrootOp("find /var/log -type f -delete 2>/dev/null || true")
        )

        # ── MicroVM boot optimizations (systemd) ──────────────────────────
        ops.append(
            ChrootOp(
                "# Mask non-essential systemd services for faster microVM boot\n"
                "if command -v systemctl >/dev/null 2>&1; then\n"
                "  for svc in \\\n"
                "    systemd-timesyncd.service \\\n"
                "    systemd-time-wait-sync.service \\\n"
                "    systemd-firstboot.service \\\n"
                "    ldconfig.service \\\n"
                "    modprobe@drm.service \\\n"
                "    modprobe@efi_pstore.service \\\n"
                "    sys-kernel-debug.mount \\\n"
                "    pollinate.service \\\n"
                "    snapd.service \\\n"
                "    snapd.socket \\\n"
                "    systemd-udev-settle.service \\\n"
                "    unattended-upgrades.service \\\n"
                "    packagekit.service \\\n"
                "    man-db.timer \\\n"
                "    whoopsie.service \\\n"
                "    apport.service \\\n"
                "    udisks2.service \\\n"
                "    console-setup.service \\\n"
                "    keyboard-setup.service \\\n"
                "    motd-news.service \\\n"
                "    fstrim.timer \\\n"
                "    logrotate.timer \\\n"
                "    multipathd.service \\\n"
                "    accounts-daemon.service \\\n"
                "    systemd-userdbd.service \\\n"
                "    systemd-nsresourced.service \\\n"
                "    systemd-pcrphase.service \\\n"
                "    systemd-pcrphase-initrd.service \\\n"
                "    systemd-pcrphase-sysinit.service \\\n"
                "    systemd-boot-update.service; do\n"
                '    ln -sf /dev/null "/etc/systemd/system/$svc" 2>/dev/null || true\n'
                "  done\n"
                "fi"
            )
        )

        # ── SSH daemon configuration (identical for every VM from this image) ──
        ops.append(
            FileOp(
                path="/etc/ssh/sshd_config.d/mvm.conf",
                data=cls.sshd_config("root").encode("utf-8"),
                mode=0o644,
                uid=0,
                gid=0,
            )
        )
        ops.append(
            FileOp(
                path="/usr/local/bin/first-boot-ssh-installer.sh",
                data=cls.first_boot_installer().encode("utf-8"),
                mode=0o755,
                uid=0,
                gid=0,
            )
        )
        ops.append(
            FileOp(
                path="/etc/systemd/system/first-boot-ssh-installer.service",
                data=cls.first_boot_service().encode("utf-8"),
                mode=0o644,
                uid=0,
                gid=0,
            )
        )
        ops.append(ChrootOp("ssh-keygen -A"))
        ops.append(
            ChrootOp(
                "if command -v systemctl >/dev/null 2>&1; then\n"
                "  systemctl enable sshd 2>/dev/null || "
                "systemctl enable ssh 2>/dev/null || true;\n"
                "fi"
            )
        )

        # ── OS-specific cache cleanup ─────────────────────────────────
        os_lower = os_type.lower()
        if os_lower in ("ubuntu", "debian"):
            ops.append(ChrootOp("apt-get clean 2>/dev/null || true"))
            ops.append(
                ChrootOp(
                    "rm -rf /var/cache/apt/archives/*.deb 2>/dev/null || true"
                )
            )
            ops.append(
                ChrootOp("rm -rf /var/cache/debconf/* 2>/dev/null || true")
            )
            # MicroVM boot optimizations
            ops.append(
                ChrootOp(
                    "# Mask unnecessary timer services for microVM\n"
                    "systemctl mask e2scrub_all.timer "
                    "e2scrub_reap.service "
                    "apt-daily.timer "
                    "apt-daily-upgrade.timer "
                    "2>/dev/null || true"
                )
            )
        elif os_lower in ("alpine",):
            ops.append(ChrootOp("apk cache clean 2>/dev/null || true"))
            ops.append(ChrootOp("rm -rf /var/cache/apk/* 2>/dev/null || true"))
            ops.append(
                ChrootOp(
                    # Prevent dhcpcd from managing eth0 (defensive — the
                    # iface change below is the primary fix, but denyinterfaces
                    # ensures dhcpcd won't touch it even if started manually).
                    "grep -qs '^denyinterfaces eth0' /etc/dhcpcd.conf "
                    "2>/dev/null || echo 'denyinterfaces eth0' >> /etc/dhcpcd.conf; "
                    # MicroVM has no DHCP server — the kernel ip= parameter
                    # already provides a static IP. Setting eth0 to manual
                    # prevents dhcpcd from starting and timing out, which
                    # adds ~15-20s to every Alpine boot.
                    "sed -i 's/iface eth0 inet dhcp/iface eth0 inet manual/' "
                    "/etc/network/interfaces"
                )
            )
            # MicroVM boot optimizations: pre-enable SSH
            ops.append(
                ChrootOp(
                    "# Pre-enable SSH daemon for OpenRC\n"
                    "rc-update add sshd default 2>/dev/null || "
                    "rc-update add ssh default 2>/dev/null || true"
                )
            )
            # Enable parallel OpenRC service startup (handle both commented and uncommented)
            ops.append(
                ChrootOp(
                    "# Enable parallel service startup (microVM optimisation)\n"
                    # Remove any existing rc_parallel line (commented or not), then append new
                    "sed -i '/^rc_parallel=/d; /^#rc_parallel=/d' /etc/rc.conf 2>/dev/null; "
                    "echo 'rc_parallel=\"YES\"' >> /etc/rc.conf"
                )
            )
            # Disable cloud-init services (not needed in microVM)
            ops.append(
                ChrootOp(
                    "# Disable cloud-init services for faster boot\n"
                    "rc-update del cloud-init default 2>/dev/null || true; "
                    "rc-update del cloud-config default 2>/dev/null || true; "
                    "rc-update del cloud-final default 2>/dev/null || true; "
                    "rc-update del cloud-init-hotplugd default 2>/dev/null || true; "
                    "rc-update del cloud-init ssh 2>/dev/null || true"
                )
            )
            # Disable chronyd (time sync not needed, kvm-clock handles it)
            ops.append(
                ChrootOp("rc-update del chronyd default 2>/dev/null || true")
            )
            # Disable serial getty (ttyS0) to save boot time
            ops.append(
                ChrootOp(
                    "# Disable serial getty on ttyS0\n"
                    "sed -i '/ttyS0/s/^/#/' /etc/inittab 2>/dev/null || true"
                )
            )
        elif os_lower in ("arch", "archlinux", "manjaro"):
            ops.append(ChrootOp("pacman -Sc --noconfirm 2>/dev/null || true"))
            ops.append(
                ChrootOp("rm -rf /var/cache/pacman/pkg/* 2>/dev/null || true")
            )
            # MicroVM boot optimizations: pre-initialize pacman keyring
            # (saves ~3.7s on every boot)
            # Only run if keyring not already populated (first time during
            # image optimization bakes it into the cached image — subsequent
            # runs during VM creation skip it, saving ~10s of entropy wait).
            ops.append(
                ChrootOp(
                    "if [ ! -f /etc/pacman.d/gnupg/pubring.gpg ]; then "
                    "pacman-key --init 2>/dev/null || true; fi"
                )
            )
            ops.append(
                ChrootOp(
                    "if [ -f /etc/pacman.d/gnupg/pubring.gpg ]; then "
                    "pacman-key --populate archlinux 2>/dev/null || true; fi"
                )
            )
            # Pre-create systemd-firstboot configs to skip firstboot prompts
            # (saves ~700ms on first boot)
            ops.append(
                ChrootOp(
                    "echo 'mvm' > /etc/hostname 2>/dev/null || true; "
                    "echo 'LANG=en_US.UTF-8' > /etc/locale.conf 2>/dev/null || true; "
                    "echo 'KEYMAP=us' > /etc/vconsole.conf 2>/dev/null || true"
                )
            )
            # Mask pacman-init.service (still runs on every boot even with pre-initialized keyring)
            # (saves ~2s on every boot)
            ops.append(
                ChrootOp(
                    "ln -sf /dev/null "
                    "/etc/systemd/system/pacman-init.service 2>/dev/null || true"
                )
            )
            # Mask systemd-firstboot.service (still runs on first boot even with config files)
            # (saves ~700ms on first boot)
            ops.append(
                ChrootOp(
                    "ln -sf /dev/null "
                    "/etc/systemd/system/systemd-firstboot.service 2>/dev/null || true"
                )
            )
            # Btrfs boot optimizations for single-device microVM
            ops.append(
                ChrootOp(
                    "# Remove btrfs mkinitcpio hook (not needed for single-device btrfs)\n"
                    "if [ -f /etc/mkinitcpio.conf ]; then\n"
                    "  sed -i 's/ btrfs / /g' /etc/mkinitcpio.conf 2>/dev/null || true\n"
                    "fi"
                )
            )
            ops.append(
                ChrootOp(
                    "# Consolidate btrfs metadata chunks\n"
                    "command -v btrfs >/dev/null 2>&1 && "
                    "btrfs balance start -dusage=0 / "
                    "2>/dev/null || true"
                )
            )
            # Mask systemd-udev-settle (not needed with known hardware)
            ops.append(
                ChrootOp(
                    "ln -sf /dev/null "
                    "/etc/systemd/system/systemd-udev-settle.service "
                    "2>/dev/null || true"
                )
            )
        elif os_lower in ("fedora", "centos", "rhel", "rocky", "almalinux"):
            ops.append(
                ChrootOp(
                    "dnf clean all 2>/dev/null || yum clean all 2>/dev/null || true"
                )
            )
            ops.append(
                ChrootOp(
                    "rm -rf /var/cache/dnf/* /var/cache/yum/* 2>/dev/null || true"
                )
            )
        else:
            # Generic: clear all cache dirs
            ops.append(ChrootOp("rm -rf /var/cache/* 2>/dev/null || true"))

        # ── Cloud-init disable (all distros) ──────────────────────────────
        ops.append(
            FileOp(
                path="/etc/cloud/cloud.cfg.d/99-disable-datasources.cfg",
                data=cls.CLOUD_INIT_DISABLE_DATASOURCE,
                mode=0o644,
                uid=0,
                gid=0,
            )
        )
        ops.append(
            FileOp(
                path="/etc/cloud/cloud-init.disabled",
                data=cls.CLOUD_INIT_DISABLED_MARKER,
                mode=0o644,
                uid=0,
                gid=0,
            )
        )
        ops.append(
            FileOp(
                path=(
                    "/etc/systemd/system/snapd.seeded.service.d/override.conf"
                ),
                data=cls.SNAPD_OVERRIDE,
                mode=0o644,
                uid=0,
                gid=0,
            )
        )
        ops.append(
            FileOp(
                path=(
                    "/etc/systemd/system/"
                    "systemd-networkd-wait-online.service.d/override.conf"
                ),
                data=cls.NETWORKD_WAIT_OVERRIDE,
                mode=0o644,
                uid=0,
                gid=0,
            )
        )
        ops.append(
            ChrootOp(
                "if command -v systemctl >/dev/null 2>&1; then\n"
                "  for svc in \\\n"
                "    cloud-init.service \\\n"
                "    cloud-init-local.service \\\n"
                "    cloud-config.service \\\n"
                "    cloud-final.service; do\n"
                '    ln -sf /dev/null "/etc/systemd/system/$svc" '
                "2>/dev/null || true\n"
                "  done\n"
                "fi"
            )
        )

        ops.append(ChrootOp("rm -rf /var/lib/apt/lists/* 2>/dev/null || true"))

        return ops

    @classmethod
    def build_fix_fstab_ops(cls) -> list[Operation]:
        """Generate operation to fix /etc/fstab for Firecracker.

        For superfloppy images (raw ext4/btrfs without partition table),
        systemd must not wait for non-existent partitions like /boot/efi
        or /dev/vda1 — that causes a 90-second timeout and emergency mode.

        PARTUUID and UUID entries are commented out (not replaced) because
        device names change under PCI transport. The kernel finds the root
        filesystem via root=UUID= or root=PARTUUID= in boot args instead.
        """
        return [
            ChrootOp(
                "if [ -f /etc/fstab ]; then "
                # Comment out PARTUUID entries — can't rely on them under PCI
                # transport where device names change. Kernel finds root via
                # root=UUID= or root=PARTUUID= in boot args instead.
                "sed -i '/^PARTUUID=/s/^/#/' /etc/fstab; "
                # Comment out UUID entries — same reasoning as PARTUUID
                "sed -i '/^UUID=/s/^/#/' /etc/fstab; "
                # Add noatime to root mount options for reduced metadata writes
                "sed -i '/^\\/dev\\/vda\\s/ s/defaults/noatime,defaults/' /etc/fstab; "
                # Comment out any remaining PARTUUID lines (non-root partitions)
                "sed -i '/^PARTUUID=/s/^/#/' /etc/fstab; "
                # Comment out /boot/efi mount (EFI partition doesn't exist)
                "sed -i '/\\/boot\\/efi/s/^/#/' /etc/fstab; "
                # Comment out swap entries (swap partition doesn't exist)
                "sed -i '/ swap /s/^/#/' /etc/fstab; "
                # Comment out any /dev/vda1..N references (partitions don't exist)
                "sed -i '/\\/dev\\/vda[0-9]/s/^/#/' /etc/fstab; "
                "fi"
            ),
        ]
