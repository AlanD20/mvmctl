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
        self._convert_to: str | None = None

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

    def convert_to(self, target_fs: str) -> None:
        """Mark that filesystem conversion should run as Phase 0.

        Useful for converting btrfs images to ext4 before deblob/shrink.
        The conversion runs **first** (Phase 0) so that all subsequent
        operations see the converted filesystem.
        """
        self._convert_to = target_fs

    # -- execution ---------------------------------------------------------

    def run(self) -> bool:
        """Execute queued operations with the selected backend.

        Phases run in order — conversion (Phase 0), deblob (Phase 1),
        shrink (Phase 2).  Each phase uses a **fresh backend session** so a
        failure in one phase never leaks into the next.

        * Phase 0: filesystem conversion (e.g. btrfs → ext4).  Runs first
          so that deblob and shrink see the converted filesystem.
        * Phase 1: deblob + fstab fix.
        * Phase 2: filesystem shrink (ext-family only).

        Returns:
            True if at least one phase ran successfully, False if all
            phases were skipped or failed.

        """
        from mvmctl.exceptions import LoopMountError

        deblob_ok = False
        shrink_ok = False
        convert_ok = False

        # Phase 0: filesystem conversion (e.g. btrfs → ext4)
        # Runs first so that deblob and shrink operate on the converted fs.
        if self._convert_to is not None:
            backend = ProvisionerBackend.get_image(
                self._image_path,
                provisioner_type=self._provisioner_type,
                fs_type=self._fs_type,
            )
            try:
                backend.convert_to(self._convert_to)
                self._fs_type = self._convert_to
                convert_ok = True
                logger.info(
                    "Filesystem converted: %s → %s",
                    self._fs_type,
                    self._convert_to,
                )
            except (LoopMountError, OSError, RuntimeError) as exc:
                logger.warning(
                    "Filesystem conversion skipped: %s. "
                    "Build the provisioner binary with 'python scripts/build_services.py' "
                    "or enable libguestfs to enable fs conversion.",
                    exc,
                )

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
                logger.debug(
                    "Shrink skipped (image may already be minimal): %s", exc
                )

        return convert_ok or deblob_ok or shrink_ok
