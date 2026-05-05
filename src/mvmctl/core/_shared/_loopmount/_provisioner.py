"""
Loop-mount provisioner — accumulates provisioning operations and executes
them via the mvm-provision binary.

Content generation is delegated to ``_provisioner/_content.py`` (shared with
all backends).  This class only handles serialization and binary execution::

    lp = LoopMountProvisioner("/path/to/rootfs.ext4", "ext4")
    lp.resize(8_589_934_592)
    lp.set_hostname("my-vm")
    lp.inject_dns(dns_server="10.0.0.1")
    lp.setup_ssh("myuser", pubkeys)
    lp.disable_cloud_init()
    lp.run()   # → build ops → serialize JSON → subprocess → binary
"""

from __future__ import annotations

import base64
import logging
from pathlib import Path

from mvmctl.core._shared._loopmount._manager import LoopMountManager
from mvmctl.core._shared._provisioner._content import (
    ChrootOp,
    CopyDirOp,
    FileOp,
    Operation,
    ProvisionerContent,
    ResizeOp,
)

logger = logging.getLogger(__name__)


class LoopMountProvisioner:
    """
    Accumulates provisioning operations and executes via the mvm-provision binary.

    All content generation is delegated to the shared ``_content.py`` builders.
    This class only stores the pre-built ops and serializes them to the JSON
    protocol expected by the loop-mount binary.
    """

    def __init__(self, rootfs_path: Path, fs_type: str) -> None:
        self._rootfs_path = rootfs_path
        self._fs_type = fs_type
        self._ops: list[Operation] = []

    # -- builder methods --------------------------------------------------

    def resize(self, target_size_bytes: int) -> None:
        """Queue a rootfs resize operation."""
        self._ops.extend(ProvisionerContent.build_resize_ops(target_size_bytes))

    def set_hostname(self, hostname: str) -> None:
        """Queue hostname + /etc/hosts setup."""
        self._ops.extend(ProvisionerContent.build_hostname_ops(hostname))

    def inject_dns(self, *, dns_server: str) -> None:
        """Queue DNS resolver injection."""
        self._ops.extend(ProvisionerContent.build_dns_ops(dns_server))

    def setup_ssh(self, user: str, ssh_pubkeys: list[str]) -> None:
        """Queue SSH key, config, and host-key generation."""
        self._ops.extend(ProvisionerContent.build_ssh_ops(user, ssh_pubkeys))

    def disable_cloud_init(self) -> None:
        """Queue cloud-init datasource blocking + service masking."""
        self._ops.extend(ProvisionerContent.build_cloud_init_disable_ops())

    def inject_cloud_init(self, cloud_init_dir: Path) -> None:
        """Queue cloud-init seed directory injection."""
        self._ops.extend(
            ProvisionerContent.build_cloud_init_inject_ops(cloud_init_dir)
        )

    # -- execution ---------------------------------------------------------

    def run(self) -> None:
        """Execute all queued operations by spawning the mvm-provision binary."""
        files: list[dict[str, object]] = []
        commands: list[str] = []
        copy_dirs: list[dict[str, object]] = []
        resize: dict[str, object] | None = None

        for op in self._ops:
            match op:
                case FileOp():
                    files.append(
                        {
                            "path": op.path,
                            "data": base64.b64encode(op.data).decode("ascii"),
                            "mode": op.mode,
                            "uid": op.uid,
                            "gid": op.gid,
                        }
                    )
                case ChrootOp():
                    commands.append(op.command)
                case CopyDirOp():
                    copy_dirs.append({"src": op.src, "dst": op.dst})
                case ResizeOp():
                    # Only one resize operation expected — take the last one
                    resize = {"action": op.action, "bytes": op.bytes}

        LoopMountManager.execute(
            image_path=str(self._rootfs_path),
            fs_type=self._fs_type,
            files=files if files else None,
            commands=commands if commands else None,
            copy_dirs=copy_dirs if copy_dirs else None,
            resize=resize,
        )
        logger.info("Loop-mount provisioning succeeded")
