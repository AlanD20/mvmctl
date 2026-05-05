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

        ssh_home = "/root" if user == "root" else f"/home/{user}"

        ops.append(
            FileOp(
                path=f"{ssh_home}/.ssh/authorized_keys",
                data=("\n".join(ssh_pubkeys) + "\n").encode("utf-8"),
                mode=0o600,
                uid=0,
                gid=0,
            )
        )
        ops.append(
            FileOp(
                path="/etc/ssh/sshd_config.d/mvm.conf",
                data=cls.sshd_config(user).encode("utf-8"),
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

        if user != "root":
            ops.append(ChrootOp(f"useradd -m {user}"))
            ops.append(
                ChrootOp(
                    f"echo '{user} ALL=(ALL) NOPASSWD: ALL' > "
                    f"/etc/sudoers.d/{user}"
                )
            )
            ops.append(ChrootOp(f"chmod 440 /etc/sudoers.d/{user}"))

        ops.append(ChrootOp("ssh-keygen -A"))
        ops.append(
            ChrootOp(
                "if [ -d /run/systemd/system ] || [ -d /usr/lib/systemd ]; then "
                "  systemctl enable sshd 2>/dev/null || "
                "systemctl enable ssh 2>/dev/null || true; "
                "fi"
            )
        )
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
