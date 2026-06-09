"""
VMProvisioner — VM rootfs provisioning via backends.

Selects the backend automatically based on ``ProvisionerType``::

    from mvmctl.core.vm._provisioner import VMProvisioner

    p = VMProvisioner(
        rootfs_path=...,
        provisioner_type=ProvisionerType.LOOP_MOUNT,
        fs_type="ext4",
    )
    p.resize(8_589_934_592)
    p.set_hostname("my-vm")
    p.inject_dns(dns_server="10.0.0.1")
    p.setup_ssh("myuser", ["ssh-ed25519 AAA..."])
    p.disable_cloud_init()
    p.run()
"""

from __future__ import annotations

from pathlib import Path

from mvmctl.core._shared._provisioner._backend import (
    ProvisionerBackend,
    _GuestfsBackend,
    _LoopMountBackend,
)
from mvmctl.models.provisioner import ProvisionerType


class VMProvisioner:
    """Unified VM provisioner — selected by ``provisioner_type``.

    All builder methods queue operations.  Call ``.run()`` to execute
    everything in a single session.
    """

    def __init__(
        self,
        rootfs_path: Path,
        *,
        provisioner_type: ProvisionerType,
        fs_type: str,
        root_uid: int = 0,
        root_gid: int = 0,
        user_uid: int = 1000,
        user_gid: int = 1000,
    ) -> None:
        self._backend: _LoopMountBackend | _GuestfsBackend = (
            ProvisionerBackend.get_vm(
                rootfs_path,
                provisioner_type=provisioner_type,
                fs_type=fs_type,
                root_uid=root_uid,
                root_gid=root_gid,
                user_uid=user_uid,
                user_gid=user_gid,
            )
        )

    # -- builder methods --------------------------------------------------

    def detect_os(self) -> str:
        """Detect OS type from the rootfs."""
        return self._backend.detect_os()

    def resize(self, target_size_bytes: int) -> None:
        """Queue a rootfs resize operation."""
        self._backend.resize(target_size_bytes)

    def set_hostname(self, hostname: str) -> None:
        """Queue hostname + /etc/hosts setup."""
        self._backend.set_hostname(hostname)

    def inject_dns(self, *, dns_server: str) -> None:
        """Queue DNS resolver injection."""
        self._backend.inject_dns(dns_server=dns_server)

    def setup_ssh(self, user: str, ssh_pubkeys: list[str]) -> None:
        """Queue SSH key, config, and host-key generation."""
        self._backend.setup_ssh(user, ssh_pubkeys)

    def disable_cloud_init(self) -> None:
        """Queue cloud-init datasource blocking + service masking."""
        self._backend.disable_cloud_init()

    def inject_cloud_init(self, cloud_init_dir: Path) -> None:
        """Queue cloud-init seed directory injection."""
        self._backend.inject_cloud_init(cloud_init_dir)

    def fix_fstab(self) -> None:
        """Queue fstab fix for Firecracker (PARTUUID → /dev/vda)."""
        self._backend.fix_fstab()

    def deblob(self, os_type: str | None = None) -> None:
        """Queue debloat operations (OS cache cleanup).

        Args:
            os_type: Pre-detected OS type string. If ``None``, the backend
                will detect the OS from the rootfs (incurring an extra
                loop-mount cycle). Pass the value from the image's
                ``distro`` field to eliminate the redundant detection.

        """
        self._backend.deblob(os_type=os_type)

    # -- execution ---------------------------------------------------------

    def run(self) -> None:
        """Execute all queued operations with the selected backend."""
        self._backend.run()
