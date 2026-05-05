"""
Provisioner abstraction — unified interface for VM rootfs provisioning.

Two backends are available:

- ``_LoopMountBackend``: Uses the compiled ``mvm-provision`` binary
  (via ``ProvisionerManager``) to loop-mount the image, write files,
  run chroot commands, and resize filesystems.  ~200ms per VM.

- ``_GuestfsBackend``: Uses ``libguestfs`` (via ``GuestfsProvisioner``)
  to mount the image in a QEMU appliance.  ~2600ms per VM.  Used only
  as fallback when the loop-mount binary is unavailable.

The ``Provisioner`` class selects the backend automatically based on a
``ProvisionerType`` value, and exposes the same builder-method interface
regardless of backend::

    from mvmctl.core._shared._provisioner import Provisioner

    p = Provisioner(
        rootfs_path=...,
        provisioner_type=ProvisionerType.LOOP_MOUNT,
        fs_type="ext4",
        root_uid=0, root_gid=0,
        user_uid=1000, user_gid=1000,
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
from typing import Any

from mvmctl.models.provisioner import ProvisionerType

# =========================================================================
# Backend: loop-mount (mvm-provision binary)
# =========================================================================


class _LoopMountBackend:
    """Delegates all operations to ``LoopMountProvisioner``."""

    def __init__(self, rootfs_path: Path, fs_type: str) -> None:
        from mvmctl.core._shared._loopmount import LoopMountProvisioner

        self._lp: Any = LoopMountProvisioner(rootfs_path, fs_type)

    def resize(self, target_size_bytes: int) -> None:
        self._lp.resize(target_size_bytes)

    def set_hostname(self, hostname: str) -> None:
        self._lp.set_hostname(hostname)

    def inject_dns(self, *, dns_server: str) -> None:
        self._lp.inject_dns(dns_server=dns_server)

    def setup_ssh(self, user: str, ssh_pubkeys: list[str]) -> None:
        self._lp.setup_ssh(user, ssh_pubkeys)

    def disable_cloud_init(self) -> None:
        self._lp.disable_cloud_init()

    def inject_cloud_init(self, cloud_init_dir: Path) -> None:
        self._lp.inject_cloud_init(cloud_init_dir)

    def run(self) -> None:
        self._lp.run()


# =========================================================================
# Backend: guestfs (libguestfs appliance)
# =========================================================================


class _GuestfsBackend:
    """Delegates all operations to ``GuestfsProvisioner``."""

    def __init__(
        self,
        rootfs_path: Path,
        root_uid: int = 0,
        root_gid: int = 0,
        user_uid: int = 1000,
        user_gid: int = 1000,
    ) -> None:
        from mvmctl.core._shared._guestfs import GuestfsProvisioner

        self._gp: Any = GuestfsProvisioner(
            rootfs_path,
            readonly=False,
            root_uid=root_uid,
            root_gid=root_gid,
            user_uid=user_uid,
            user_gid=user_gid,
        )

    def resize(self, target_size_bytes: int) -> None:
        self._gp.resize(target_size_bytes)

    def set_hostname(self, hostname: str) -> None:
        self._gp.set_hostname(hostname)

    def inject_dns(self, *, dns_server: str) -> None:
        self._gp.inject_dns(dns_server=dns_server)

    def setup_ssh(self, user: str, ssh_pubkeys: list[str]) -> None:
        self._gp.setup_ssh(user, ssh_pubkeys)

    def disable_cloud_init(self) -> None:
        self._gp.disable_cloud_init()

    def inject_cloud_init(self, cloud_init_dir: Path) -> None:
        self._gp.inject_cloud_init(cloud_init_dir)

    def run(self) -> None:
        self._gp.run()


# =========================================================================
# Public interface
# =========================================================================


class Provisioner:
    """Unified provisioner — selected by ``provisioner_type``.

    All builder methods queue operations.  Call ``.run()`` to execute
    everything in a single session.
    """

    def __init__(
        self,
        rootfs_path: Path,
        *,
        provisioner_type: ProvisionerType,
        fs_type: str = "ext4",
        root_uid: int = 0,
        root_gid: int = 0,
        user_uid: int = 1000,
        user_gid: int = 1000,
    ) -> None:
        if provisioner_type == ProvisionerType.LOOP_MOUNT:
            self._backend: _LoopMountBackend | _GuestfsBackend = (
                _LoopMountBackend(rootfs_path, fs_type)
            )
        elif provisioner_type == ProvisionerType.GUESTFS:
            self._backend = _GuestfsBackend(
                rootfs_path,
                root_uid=root_uid,
                root_gid=root_gid,
                user_uid=user_uid,
                user_gid=user_gid,
            )
        else:
            raise ValueError(f"Unknown provisioner type: {provisioner_type!r}")

    # -- builder methods --------------------------------------------------

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

    # -- execution ---------------------------------------------------------

    def run(self) -> None:
        """Execute all queued operations with the selected backend."""
        self._backend.run()
