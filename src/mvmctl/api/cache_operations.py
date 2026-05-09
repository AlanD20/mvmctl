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
from mvmctl.core.config._service import SettingsService
from mvmctl.core.host._helper import HostPrivilegeHelper
from mvmctl.core.image._repository import ImageRepository
from mvmctl.core.kernel._repository import KernelRepository
from mvmctl.core.network._repository import LeaseRepository, NetworkRepository
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

        # Extract embedded service binaries (compiled mode only)
        from mvmctl.constants import is_compiled_mode

        if is_compiled_mode():
            try:
                from mvmctl.core._shared import Database as _ExtractDb

                BinaryService(
                    BinaryRepository(_ExtractDb())
                ).extract_service_binaries()
            except Exception:
                logger.exception("Failed to extract embedded service binaries")

        # Check whether guestfs was enabled by the user
        from mvmctl.core._shared import Database

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
        """Prune VMs based on their status.

        By default, prunes all VMs EXCEPT those in RUNNING or STARTING state.
        Use ``include_all=True`` to prune ALL VMs regardless of state.

        Args:
            dry_run: If True, only report what would be removed.
            include_all: Prune ALL VMs including RUNNING and STARTING.

        Returns:
            OperationResult with item list of VM names that were removed.
        """
        HostPrivilegeHelper.check_privileges("/usr/sbin/ip", "prune VMs")
        db = Database()
        vms = VMRepository(db).list_all()

        removed: list[str] = []
        for vm in vms:
            if vm.status in (VMStatus.RUNNING, VMStatus.STARTING):
                if not include_all:
                    continue

            if not dry_run:
                try:
                    from mvmctl.api.inputs._vm_input import VMInput
                    from mvmctl.api.vm_operations import VMOperation

                    VMOperation.remove(
                        VMInput(identifiers=[vm.name], force=True)
                    )
                    removed.append(vm.name)
                except Exception as e:
                    logger.warning("Failed to remove VM %s: %s", vm.name, e)
            else:
                removed.append(vm.name)

        return OperationResult(
            status="success",
            code="cache.pruned",
            message=f"Pruned {len(removed)} VM(s)",
            item=removed,
        )

    @staticmethod
    def prune_networks(
        dry_run: bool = False, include_all: bool = False
    ) -> OperationResult[list[str]]:
        """Prune unused networks.

        Args:
            dry_run: If True, only report what would be removed.
            include_all: If True, remove ALL networks including default and referenced.

        Returns:
            OperationResult with item list of network names that were removed.
        """
        HostPrivilegeHelper.check_privileges("/usr/sbin/ip", "prune networks")
        db = Database()
        repo = NetworkRepository(db)
        all_networks = repo.list_all()

        # Get referenced network IDs from VMs
        vm_repo = VMRepository(db)
        vms = vm_repo.list_all()
        referenced_network_ids: set[str] = set()
        for vm in vms:
            if vm.network_id:
                referenced_network_ids.add(vm.network_id)

        lease_repo = LeaseRepository(db)
        removed: list[str] = []

        for network in all_networks:
            if not include_all:
                if network.name == str(
                    SettingsService.resolve(db, "defaults.network", "name")
                ):
                    continue
                if network.id in referenced_network_ids:
                    continue
                leases = lease_repo.list_all(network.id)
                if leases:
                    continue

            if not dry_run:
                try:
                    from mvmctl.api.inputs._network_input import NetworkInput
                    from mvmctl.api.network_operations import NetworkOperation

                    remove_result = NetworkOperation.remove(
                        NetworkInput(name=[network.name]),
                        force=include_all,
                    )
                    if remove_result.is_error:
                        logger.warning(
                            "Failed to remove network %s: %s",
                            network.name,
                            remove_result.message,
                        )
                    else:
                        removed.append(network.name)
                except Exception as e:
                    logger.warning(
                        "Failed to remove network %s: %s", network.name, e
                    )
            else:
                removed.append(network.name)

        return OperationResult(
            status="success",
            code="cache.pruned",
            message=f"Pruned {len(removed)} network(s)",
            item=removed,
        )

    @staticmethod
    def prune_images(
        dry_run: bool = False, include_all: bool = False
    ) -> OperationResult[list[str]]:
        """Prune unused images.

        Args:
            dry_run: If True, only report what would be removed.
            include_all: If True, remove ALL images including default and referenced.

        Returns:
            OperationResult with item list of image IDs that were removed.
        """
        db = Database()
        repo = ImageRepository(db)

        # Get referenced image IDs from VMs
        vm_repo = VMRepository(db)
        vms = vm_repo.list_all()
        referenced_image_ids: set[str] = set()
        for vm in vms:
            if vm.image_id:
                referenced_image_ids.add(vm.image_id)

        default_item = repo.get_default()
        default_id = default_item.id if default_item else None

        all_images = repo.list_all()
        removed: list[str] = []

        for image in all_images:
            if not include_all:
                if image.id == default_id:
                    continue
                if image.id in referenced_image_ids:
                    continue

            if not dry_run:
                try:
                    from mvmctl.api.image_operations import ImageOperation
                    from mvmctl.api.inputs._image_input import ImageInput

                    ImageOperation.remove(
                        ImageInput(id=[image.id]),
                        force=include_all,
                    )
                    removed.append(image.id)
                except Exception as e:
                    logger.warning("Failed to remove image %s: %s", image.id, e)
            else:
                removed.append(image.id)

        return OperationResult(
            status="success",
            code="cache.pruned",
            message=f"Pruned {len(removed)} image(s)",
            item=removed,
        )

    @staticmethod
    def prune_kernels(
        dry_run: bool = False, include_all: bool = False
    ) -> OperationResult[list[str]]:
        """Prune unused kernels.

        Args:
            dry_run: If True, only report what would be removed.
            include_all: If True, remove ALL kernels including default and referenced.

        Returns:
            OperationResult with item list of kernel IDs that were removed.
        """
        db = Database()
        repo = KernelRepository(db)

        # Get referenced kernel IDs from VMs
        vm_repo = VMRepository(db)
        vms = vm_repo.list_all()
        referenced_kernel_ids: set[str] = set()
        for vm in vms:
            if vm.kernel_id:
                referenced_kernel_ids.add(vm.kernel_id)

        default_item = repo.get_default()
        default_id = default_item.id if default_item else None

        all_kernels = repo.list_all()
        removed: list[str] = []

        for kernel in all_kernels:
            if not include_all:
                if kernel.id == default_id:
                    continue
                if kernel.id in referenced_kernel_ids:
                    continue

            if not dry_run:
                try:
                    from mvmctl.api.inputs._kernel_input import KernelInput
                    from mvmctl.api.kernel_operations import KernelOperation

                    KernelOperation.remove(
                        KernelInput(id=[kernel.id]),
                        force=include_all,
                    )
                    removed.append(kernel.id)
                except Exception as e:
                    logger.warning(
                        "Failed to remove kernel %s: %s", kernel.id, e
                    )
            else:
                removed.append(kernel.id)

        return OperationResult(
            status="success",
            code="cache.pruned",
            message=f"Pruned {len(removed)} kernel(s)",
            item=removed,
        )

    @staticmethod
    def prune_binaries(
        dry_run: bool = False, include_all: bool = False
    ) -> OperationResult[list[str]]:
        """Prune unused binaries.

        Args:
            dry_run: If True, only report what would be removed.
            include_all: If True, remove ALL binaries including default version.

        Returns:
            OperationResult with item list of binary identifiers (name:version)
            that were removed.
        """
        db = Database()
        repo = BinaryRepository(db)
        all_binaries = repo.list_all()

        default_binary = repo.get_default("firecracker")
        default_version = default_binary.version if default_binary else None

        removed: list[str] = []
        for binary in all_binaries:
            if not include_all:
                if binary.version == default_version:
                    continue

            if not dry_run:
                try:
                    from mvmctl.api.binary_operations import BinaryOperation
                    from mvmctl.api.inputs._binary_input import BinaryInput

                    BinaryOperation.remove(
                        BinaryInput(id=[binary.id]),
                        force=include_all,
                    )
                    removed.append(f"{binary.name}:{binary.version}")
                except Exception as e:
                    logger.warning(
                        "Failed to remove binary %s:%s: %s",
                        binary.name,
                        binary.version,
                        e,
                    )
            else:
                removed.append(f"{binary.name}:{binary.version}")

        return OperationResult(
            status="success",
            code="cache.pruned",
            message=f"Pruned {len(removed)} binary(ies)",
            item=removed,
        )

    @staticmethod
    def prune_misc(
        dry_run: bool = False,
    ) -> OperationResult[dict[str, bool]]:
        """Prune miscellaneous cache: appliance, warm images, and stale guestfs state.

        Always removes appliance, warm images, and stale libguestfs locks/sockets
        — no protection flags.

        Args:
            dry_run: If True, only report what would be removed.

        Returns:
            OperationResult with item dict with keys "appliance",
            "warm_images", and "guestfs_state" indicating whether each was removed.
        """
        result = {
            "appliance": GuestfsService.prune_appliance(dry_run),
            "warm_images": CacheService.prune_warm_images(dry_run),
            "guestfs_state": GuestfsService.clean_stale_guestfs_state(),
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
