"""
ImageProvisioner — image optimization via backends.

Debloats (OS cache cleanup + fstab fix) and optionally shrinks a root
filesystem image, then executes them via the selected backend (loop-mount
or guestfs).  Debloat and shrink are run as **separate backend sessions**
so a shrink failure (e.g. filesystem already at minimum size) never
prevents the debloat step from being applied::

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

from mvmctl.core._shared._provisioner._backend import ProvisionerBackend
from mvmctl.models.provisioner import ProvisionerType

logger = logging.getLogger(__name__)


class ImageProvisioner:
    """Optimize a root filesystem image — shrink, deblob, fix fstab.

    ``deblob()`` and ``shrink()`` are **declarative** — they only set
    flags.  ``run()`` creates a fresh backend for each phase so a
    failure in one phase never leaks into the next.
    """

    def __init__(
        self,
        image_path: Path,
        *,
        provisioner_type: ProvisionerType,
        fs_type: str,
    ) -> None:
        self._image_path = image_path
        self._provisioner_type = provisioner_type
        self._fs_type = fs_type
        self._deblob = False
        self._shrink = False

    # -- builder methods (declarative) ------------------------------------

    def detect_os(self) -> str:
        """Detect the OS type from the image using a fresh backend session.

        Returns:
            OS identifier string (e.g. ``"ubuntu"``, ``"debian"``, ``"alpine"``).

        """
        backend = ProvisionerBackend.get_image(
            self._image_path,
            provisioner_type=self._provisioner_type,
            fs_type=self._fs_type,
        )
        return backend.detect_os()

    def deblob(self) -> None:
        """Mark that deblob + fstab fix should run."""
        self._deblob = True

    def shrink(self) -> None:
        """Mark that filesystem shrink should run."""
        self._shrink = True

    # -- execution ---------------------------------------------------------

    def run(self) -> bool:
        """Execute queued operations with the selected backend.

        Runs deblob and shrink as **separate backend sessions** so that a
        shrink failure (e.g. filesystem already at minimum size) does not
        prevent the deblob step from being applied.

        Returns:
            True if at least one phase ran successfully, False if all
            phases were skipped or failed.

        """
        from mvmctl.exceptions import LoopMountError

        deblob_ok = False
        shrink_ok = False

        # Phase 1: deblob + fstab fix (fresh backend — no state leakage)
        if self._deblob:
            backend = ProvisionerBackend.get_image(
                self._image_path,
                provisioner_type=self._provisioner_type,
                fs_type=self._fs_type,
            )
            backend.deblob()
            try:
                backend.run()
                deblob_ok = True
            except (LoopMountError, OSError, RuntimeError) as exc:
                logger.warning(
                    "Debloating skipped: %s. "
                    "Build the provisioner binary with 'python scripts/build_services.py' "
                    "or enable libguestfs to enable boot optimization.",
                    exc,
                )

        # Phase 2: shrink (fresh backend — deblob state completely isolated)
        if self._shrink:
            backend = ProvisionerBackend.get_image(
                self._image_path,
                provisioner_type=self._provisioner_type,
                fs_type=self._fs_type,
            )
            backend.shrink()
            try:
                backend.run()
                shrink_ok = True
            except (LoopMountError, OSError, RuntimeError) as exc:
                logger.warning(
                    "Shrink skipped (image may already be minimal): %s", exc
                )

        return deblob_ok or shrink_ok
