"""Cache operations - cross-domain orchestration for cache management."""

from __future__ import annotations

import logging
import shutil
import subprocess
from pathlib import Path

from mvmctl.constants import DEFAULT_NETWORK_NAME
from mvmctl.core._internal._db import Database
from mvmctl.core.binary._repository import BinaryRepository
from mvmctl.core.host._helper import HostPrivilegeHelper
from mvmctl.core.image._repository import ImageRepository
from mvmctl.core.kernel._repository import KernelRepository
from mvmctl.core.network._repository import LeaseRepository, NetworkRepository
from mvmctl.core.vm._repository import VMRepository
from mvmctl.models.vm import VMStatus
from mvmctl.utils.common import CacheUtils

logger = logging.getLogger(__name__)


class CacheOperation:
    """Cache management orchestration."""

    @staticmethod
    def init_all() -> dict[str, str | list[str] | None]:
        """Initialize all cache directories.

        Creates all necessary cache directories and optionally builds the
        libguestfs fixed appliance for faster image operations.

        Returns:
            Dictionary with cache_dir path, list of created directory paths,
            and guestfs_appliance path if built.
        """
        cache_dir = CacheUtils.get_cache_dir()
        created: list[str] = []

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

        # libguestfs fixed appliance
        appliance_path = CacheOperation._build_guestfs_appliance(cache_dir)

        return {
            "cache_dir": str(cache_dir),
            "directories": created,
            "guestfs_appliance": str(appliance_path)
            if appliance_path
            else None,
        }

    @staticmethod
    def _build_guestfs_appliance(cache_dir: Path) -> Path | None:
        """Build the libguestfs fixed appliance for faster image operations.

        Returns:
            Path to appliance directory if built, None if skipped or failed.
        """
        make_tool = shutil.which("libguestfs-make-fixed-appliance")
        if not make_tool:
            logger.debug(
                "libguestfs-make-fixed-appliance not found — skipping appliance build"
            )
            return None

        appliance_dir = cache_dir / "appliance"
        appliance_dir.mkdir(parents=True, exist_ok=True)

        try:
            subprocess.run(
                [make_tool, str(appliance_dir)],
                capture_output=True,
                text=True,
                check=True,
                timeout=150,
            )
            logger.info("libguestfs fixed appliance built at %s", appliance_dir)
            return appliance_dir
        except subprocess.TimeoutExpired:
            logger.warning("libguestfs appliance build timed out after 150s")
            return None
        except subprocess.CalledProcessError as e:
            logger.warning("libguestfs appliance build failed: %s", e.stderr)
            return None
        except FileNotFoundError:
            return None

    @staticmethod
    def prune_vms(
        dry_run: bool = False,
        include_all: bool = False,
    ) -> list[str]:
        """Prune VMs based on their status.

        By default, prunes all VMs EXCEPT those in RUNNING or STARTING state.
        Use ``include_all=True`` to prune ALL VMs regardless of state.

        Args:
            dry_run: If True, only report what would be removed.
            include_all: Prune ALL VMs including RUNNING and STARTING.

        Returns:
            List of VM names that were removed.
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

                    VMOperation.remove(VMInput(name=[vm.name]))
                    removed.append(vm.name)
                except Exception as e:
                    logger.warning("Failed to remove VM %s: %s", vm.name, e)
            else:
                removed.append(vm.name)

        return removed

    @staticmethod
    def prune_networks(
        dry_run: bool = False, include_all: bool = False
    ) -> list[str]:
        """Prune unused networks.

        Args:
            dry_run: If True, only report what would be removed.
            include_all: If True, remove ALL networks including default and referenced.

        Returns:
            List of network names that were removed.
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
                if network.name == DEFAULT_NETWORK_NAME:
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

                    NetworkOperation.remove(
                        NetworkInput(name=[network.name]),
                        force=include_all,
                    )
                    removed.append(network.name)
                except Exception as e:
                    logger.warning(
                        "Failed to remove network %s: %s", network.name, e
                    )
            else:
                removed.append(network.name)

        return removed

    @staticmethod
    def prune_images(
        dry_run: bool = False, include_all: bool = False
    ) -> list[str]:
        """Prune unused images.

        Args:
            dry_run: If True, only report what would be removed.
            include_all: If True, remove ALL images including default and referenced.

        Returns:
            List of image IDs that were removed.
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

        return removed

    @staticmethod
    def prune_kernels(
        dry_run: bool = False, include_all: bool = False
    ) -> list[str]:
        """Prune unused kernels.

        Args:
            dry_run: If True, only report what would be removed.
            include_all: If True, remove ALL kernels including default and referenced.

        Returns:
            List of kernel IDs that were removed.
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

        return removed

    @staticmethod
    def prune_binaries(
        dry_run: bool = False, include_all: bool = False
    ) -> list[str]:
        """Prune unused binaries.

        Args:
            dry_run: If True, only report what would be removed.
            include_all: If True, remove ALL binaries including default version.

        Returns:
            List of binary identifiers (name:version) that were removed.
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

        return removed

    @staticmethod
    def prune_misc(dry_run: bool = False) -> dict[str, bool]:
        """Prune miscellaneous cache: appliance and warm images.

        Always removes both appliance and warm images — no protection flags.

        Args:
            dry_run: If True, only report what would be removed.

        Returns:
            Dictionary with keys "appliance" and "warm_images" indicating
            whether each was removed.
        """
        appliance_pruned = CacheOperation._prune_appliance(dry_run)
        warm_images_pruned = CacheOperation._prune_warm_images(dry_run)
        return {
            "appliance": appliance_pruned,
            "warm_images": warm_images_pruned,
        }

    @staticmethod
    def prune_all(
        dry_run: bool = False,
        include_all: bool = False,
    ) -> dict[str, list[str] | bool]:
        """Prune all cache resources.

        Performs a complete prune operation across all resource types:
        VMs, networks, images, kernels, binaries, and misc (appliance + warm images).

        Args:
            dry_run: If True, only report what would be removed.
            include_all: If True, remove ALL resources including protected items.

        Returns:
            Dictionary with results per resource type:
            - "vms": list of removed VM names
            - "networks": list of removed network names
            - "images": list of removed image IDs
            - "kernels": list of kernel IDs that were removed.
            - "binaries": list of binary identifiers
            - "appliance": bool indicating if appliance was pruned
            - "warm_images": bool indicating if warm images were pruned
        """
        HostPrivilegeHelper.check_privileges(
            "/usr/sbin/ip", "prune all cache resources"
        )

        misc = CacheOperation.prune_misc(dry_run=dry_run)

        return {
            "vms": CacheOperation.prune_vms(
                dry_run=dry_run, include_all=include_all
            ),
            "networks": CacheOperation.prune_networks(
                dry_run=dry_run, include_all=include_all
            ),
            "images": CacheOperation.prune_images(
                dry_run=dry_run, include_all=include_all
            ),
            "kernels": CacheOperation.prune_kernels(
                dry_run=dry_run, include_all=include_all
            ),
            "binaries": CacheOperation.prune_binaries(
                dry_run=dry_run, include_all=include_all
            ),
            "appliance": misc["appliance"],
            "warm_images": misc["warm_images"],
        }

    @staticmethod
    def _prune_appliance(dry_run: bool = False) -> bool:
        """Remove the libguestfs appliance folder.

        Args:
            dry_run: If True, only report what would be removed.

        Returns:
            True if appliance folder was removed or would be removed.
        """
        appliance_dir = CacheUtils.get_cache_dir() / "appliance"
        if appliance_dir.exists():
            if not dry_run:
                shutil.rmtree(appliance_dir, ignore_errors=True)
            return True
        return False

    @staticmethod
    def _prune_warm_images(dry_run: bool = False) -> bool:
        """Remove warm images from the tmpfs ready pool.

        Warm images are decompressed VM images cached in RAM for fast cloning.

        Args:
            dry_run: If True, only report what would be removed.

        Returns:
            True if warm images were removed or would be removed.
        """
        warm_dir = CacheUtils.get_warm_image_dir()
        if not warm_dir.exists():
            return False

        has_content = any(warm_dir.iterdir())
        if not has_content:
            return False

        if not dry_run:
            for item in warm_dir.iterdir():
                try:
                    if item.is_dir():
                        shutil.rmtree(item)
                    else:
                        item.unlink()
                except OSError:
                    pass
        return True


__all__ = ["CacheOperation"]
