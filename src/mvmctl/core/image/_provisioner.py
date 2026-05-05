"""
ImageProvisioner — image optimization via backends.

Queues shrink, deblob, and fstab-fix operations on a root filesystem image,
then executes them via the selected backend (loop-mount or guestfs)::

    from mvmctl.core.image._provisioner import ImageProvisioner

    p = ImageProvisioner(
        image_path=...,
        provisioner_type=ProvisionerType.LOOP_MOUNT,
        fs_type="ext4",
    )
    p.deblob()
    p.shrink()
    p.run()
"""

from __future__ import annotations

import logging
from pathlib import Path

from mvmctl.core._shared._provisioner._backend import (
    ProvisionerBackend,
    _GuestfsBackend,
    _LoopMountBackend,
)
from mvmctl.models.provisioner import ProvisionerType

logger = logging.getLogger(__name__)


class ImageProvisioner:
    """Optimize a root filesystem image — shrink, deblob, fix fstab.

    All builder methods queue operations.  Call ``.run()`` to execute
    everything in a single session.
    """

    def __init__(
        self,
        image_path: Path,
        *,
        provisioner_type: ProvisionerType,
        fs_type: str,
    ) -> None:
        self._backend: _LoopMountBackend | _GuestfsBackend = (
            ProvisionerBackend.get_image(
                image_path,
                provisioner_type=provisioner_type,
                fs_type=fs_type,
            )
        )

    # -- builder methods --------------------------------------------------

    def deblob(self) -> None:
        """Detect OS and queue cache cleanup + fstab fix."""
        self._backend.deblob()

    def shrink(self) -> None:
        """Queue filesystem shrink to minimum size."""
        self._backend.shrink()

    # -- execution ---------------------------------------------------------

    def run(self) -> None:
        """Execute all queued operations with the selected backend."""
        from mvmctl.exceptions import LoopMountError

        try:
            self._backend.run()
        except (LoopMountError, OSError, RuntimeError) as exc:
            logger.debug(
                "Image optimization (deblob/shrink) backend failed: %s: %s",
                type(exc).__name__,
                exc,
            )
