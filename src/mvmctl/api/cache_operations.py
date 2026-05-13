"""Cache operations - cross-domain orchestration for cache management."""

from __future__ import annotations

import logging
import shutil
from collections.abc import Callable
from pathlib import Path

from mvmctl.core._shared import Database
from mvmctl.core._shared._guestfs import GuestfsService
from mvmctl.core.binary._repository import BinaryRepository
from mvmctl.core.binary._service import BinaryService
from mvmctl.core.cache import CacheService
from mvmctl.core.host._helper import HostPrivilegeHelper
from mvmctl.core.vm._repository import VMRepository
from mvmctl.models import CleanResult, PruneAllResult, VMStatus
from mvmctl.models.result import OperationResult, ProgressEvent
from mvmctl.utils.common import CacheUtils

logger = logging.getLogger(__name__)


class CacheOperation:
    """Cache management orchestration."""

    @staticmethod
    def init_all(
        *,
        on_progress: Callable[[ProgressEvent], None] | None = None,
    ) -> OperationResult[dict[str, str | list[str] | None]]:
        """Initialize all cache directories.

        Creates all necessary cache directories and optionally builds the
        libguestfs fixed appliance for faster image operations (only when
        ``guestfs_enabled`` is set in user settings).

        Args:
            on_progress: Optional callback for progress events during
                long-running operations (e.g., appliance build).

        Returns:
            OperationResult with item dict containing cache_dir path,
            list of created directory paths, and guestfs_appliance path if built.
        """
        from mvmctl.core.config._service import SettingsService

        cache_dir = CacheUtils.get_cache_dir()
        created: list[str] = []
        guestfs_enabled: bool = False

        # Ensure DB schema exists before any DB writes.
        Database().migrate()

        # Core directories
        dirs = [
            CacheUtils.get_vms_dir(),
            CacheUtils.get_images_dir(),
            CacheUtils.get_kernels_dir(),
            CacheUtils.get_bin_dir(),
            CacheUtils.get_logs_dir(),
            CacheUtils.get_keys_dir(),
        ]
        for path in dirs:
            created.append(str(path))

        # Extract embedded service binaries and create service symlinks
        # In compiled mode: copies the binary from embedded data
        # In dev mode: creates symlinks only (binary already exists from build)
        try:
            from mvmctl.core._shared import Database as _ExtractDb

            BinaryService(
                BinaryRepository(_ExtractDb())
            ).extract_service_binaries()
        except Exception:
            logger.exception("Failed to extract embedded service binaries")

        # Check whether guestfs was enabled by the user
        db = Database()
        try:
            guestfs_enabled = bool(
                SettingsService.resolve(db, "settings", "guestfs_enabled")
            )
        except Exception:
            pass

        # libguestfs fixed appliance (heavy operation) — only when enabled
        appliance_path: Path | None = None
        if guestfs_enabled:
            if on_progress is not None:
                on_progress(
                    ProgressEvent(
                        phase="appliance",
                        status="running",
                        message="Building libguestfs appliance...",
                    )
                )
            appliance_path = CacheOperation._build_guestfs_appliance(cache_dir)

        # Detected guestfs kernel
        from mvmctl.core._shared._guestfs import KernelDetector

        kernel_info = KernelDetector.find_best_kernel()

        return OperationResult(
            status="success",
            code="cache.initialized",
            message="Cache initialized successfully",
            item={
                "cache_dir": str(cache_dir),
                "directories": created,
                "guestfs_appliance": str(appliance_path)
                if appliance_path
                else None,
                "guestfs_kernel": str(kernel_info[0]) if kernel_info else None,
            },
        )

    @staticmethod
    def _build_guestfs_appliance(cache_dir: Path) -> Path | None:
        """Build the libguestfs fixed appliance for faster image operations.

        Returns:
            Path to appliance directory if built, None if skipped or failed.
        """
        return GuestfsService.build_appliance(cache_dir)

    @staticmethod
    def prune_vms(
        dry_run: bool = False,
        include_all: bool = False,
    ) -> OperationResult[list[str]]:
        """Prune VMs via :meth:`VMOperation.prune`."""
        from mvmctl.api.vm_operations import VMOperation

        return VMOperation.prune(dry_run=dry_run, include_all=include_all)

    @staticmethod
    def prune_networks(
        dry_run: bool = False, include_all: bool = False
    ) -> OperationResult[list[str]]:
        """Prune unused networks via :meth:`NetworkOperation.prune`."""
        from mvmctl.api.network_operations import NetworkOperation

        return NetworkOperation.prune(dry_run=dry_run, include_all=include_all)

    @staticmethod
    def prune_images(
        dry_run: bool = False, include_all: bool = False
    ) -> OperationResult[list[str]]:
        """Prune unused images via :meth:`ImageOperation.prune`."""
        from mvmctl.api.image_operations import ImageOperation

        return ImageOperation.prune(dry_run=dry_run, include_all=include_all)

    @staticmethod
    def prune_kernels(
        dry_run: bool = False, include_all: bool = False
    ) -> OperationResult[list[str]]:
        """Prune unused kernels via :meth:`KernelOperation.prune`."""
        from mvmctl.api.kernel_operations import KernelOperation

        return KernelOperation.prune(dry_run=dry_run, include_all=include_all)

    @staticmethod
    def prune_binaries(
        dry_run: bool = False, include_all: bool = False
    ) -> OperationResult[list[str]]:
        """Prune unused binaries via :meth:`BinaryOperation.prune`."""
        from mvmctl.api.binary_operations import BinaryOperation

        return BinaryOperation.prune(dry_run=dry_run, include_all=include_all)

    @staticmethod
    def prune_misc(
        dry_run: bool = False,
    ) -> OperationResult[dict[str, bool]]:
        """Prune miscellaneous cache: appliance, warm images, stale guestfs state,
        and stale provision mount directories.

        Always removes appliance, warm images, stale libguestfs locks/sockets,
        and stale ``mvm-provision-*`` mount points in ``/tmp/`` — no protection flags.

        Args:
            dry_run: If True, only report what would be removed.

        Returns:
            OperationResult with item dict with keys ``"appliance"``,
            ``"warm_images"``, ``"guestfs_state"``, and
            ``"stale_provision_mounts"`` indicating whether each was removed.
        """
        result = {
            "appliance": GuestfsService.prune_appliance(dry_run),
            "warm_images": CacheService.prune_warm_images(dry_run),
            "guestfs_state": GuestfsService.clean_stale_guestfs_state(),
            "stale_provision_mounts": CacheService.clean_stale_provision_mounts(
                dry_run
            ),
        }
        return OperationResult(
            status="success",
            code="cache.pruned",
            message="Misc cache pruned",
            item=result,
        )

    @staticmethod
    def prune_all(
        dry_run: bool = False,
        include_all: bool = False,
    ) -> OperationResult[PruneAllResult]:
        """Prune all cache resources.

        Performs a complete prune operation across all resource types:
        VMs, networks, images, kernels, binaries, and misc (appliance + warm images).

        Args:
            dry_run: If True, only report what would be removed.
            include_all: If True, remove ALL resources including protected items.

        Returns:
            OperationResult with item PruneAllResult containing aggregated
            pruned IDs, failed IDs, and whether running VMs were present.
        """
        HostPrivilegeHelper.check_privileges(
            "/usr/sbin/ip", "prune all cache resources"
        )

        db = Database()
        vms = VMRepository(db).list_all()
        had_running_vms = any(
            vm.status in (VMStatus.RUNNING, VMStatus.STARTING) for vm in vms
        )

        pruned_ids: list[str] = []
        failed_ids: list[str] = []

        for op_result in [
            CacheOperation.prune_vms(dry_run=dry_run, include_all=include_all),
            CacheOperation.prune_networks(
                dry_run=dry_run, include_all=include_all
            ),
            CacheOperation.prune_images(
                dry_run=dry_run, include_all=include_all
            ),
            CacheOperation.prune_kernels(
                dry_run=dry_run, include_all=include_all
            ),
            CacheOperation.prune_binaries(
                dry_run=dry_run, include_all=include_all
            ),
        ]:
            if op_result.is_ok and op_result.item:
                pruned_ids.extend(op_result.item)

        misc_result = CacheOperation.prune_misc(dry_run=dry_run)
        misc = misc_result.item or {}
        if misc.get("appliance"):
            pruned_ids.append("appliance")
        if misc.get("warm_images"):
            pruned_ids.append("warm_images")
        if misc.get("guestfs_state"):
            pruned_ids.append("guestfs_state")
        if misc.get("stale_provision_mounts"):
            pruned_ids.append("stale_provision_mounts")

        result = PruneAllResult(
            pruned_ids=pruned_ids,
            failed_ids=failed_ids,
            had_running_vms=had_running_vms,
        )
        return OperationResult(
            status="success",
            code="cache.pruned",
            message=f"Pruned {len(pruned_ids)} item(s)",
            item=result,
        )

    @staticmethod
    def clean(dry_run: bool = False) -> OperationResult[CleanResult]:
        """Completely clean all cache — host, prune everything, remove cache dir.

        This is the "nuclear option" for cache cleanup. It:
        1. Cleans host networking (TAPs, bridges, iptables chains)
        2. Prunes all resources (VMs, networks, images, kernels, binaries, misc)
        3. Removes the entire cache directory at ~/.cache/mvmctl

        Args:
            dry_run: If True, only report what would be removed.

        Returns:
            OperationResult with item CleanResult containing prune details
            and cache dir removal status.
        """
        from mvmctl.api.host_operations import HostOperation

        # Step 1: Prune all cached resources
        prune_op_result = CacheOperation.prune_all(
            dry_run=dry_run, include_all=True
        )
        prune_result = prune_op_result.item

        # Step 2: Clean host networking (while DB still exists in cache dir)
        if not dry_run:
            HostOperation.clean(CacheUtils.get_cache_dir())

        # Step 3: Remove the cache directory itself
        cache_dir = CacheUtils.get_cache_dir()
        cache_dir_removed = False
        if cache_dir.exists():
            if not dry_run:
                shutil.rmtree(cache_dir, ignore_errors=True)
            cache_dir_removed = True

        result = CleanResult(
            prune_result=prune_result
            if prune_result
            else PruneAllResult(
                pruned_ids=[], failed_ids=[], had_running_vms=False
            ),
            cache_dir_removed=cache_dir_removed,
            cache_dir=str(cache_dir),
        )
        return OperationResult(
            status="success",
            code="cache.cleaned",
            message="Cache cleaned successfully",
            item=result,
        )


__all__ = ["CacheOperation"]
