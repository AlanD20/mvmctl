"""Cache operations - cross-domain orchestration for cache management."""

from __future__ import annotations

import logging
import shutil
import subprocess
from pathlib import Path

from mvmctl.core._shared import Database
from mvmctl.core.binary._repository import BinaryRepository
from mvmctl.core.config._service import SettingsService
from mvmctl.core.host._helper import HostPrivilegeHelper
from mvmctl.core.image._repository import ImageRepository
from mvmctl.core.kernel._repository import KernelRepository
from mvmctl.core.network._repository import LeaseRepository, NetworkRepository
from mvmctl.core.vm._repository import VMRepository
from mvmctl.models import CleanResult, PruneAllResult, VMStatus
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

        # If a complete appliance already exists, skip the (slow) rebuild.
        required_files = {"kernel", "initrd", "root"}
        if required_files.issubset({p.name for p in appliance_dir.iterdir()}):
            logger.debug(
                "libguestfs appliance already present at %s", appliance_dir
            )
            return appliance_dir

        # Libguestfs leaves daemon state in system temp dirs that can cause
        # subsequent builds to hang if a previous run was interrupted. Clean
        # stale locks and sockets before every build.
        CacheOperation._clean_stale_guestfs_state()

        try:
            # Discard stdout to avoid pipe-buffer deadlock — this tool is
            # extremely verbose.  Keep stderr so we can report errors.
            subprocess.run(
                [make_tool, str(appliance_dir)],
                stdout=subprocess.DEVNULL,
                stderr=subprocess.PIPE,
                text=True,
                check=True,
                timeout=150,
            )
        except subprocess.TimeoutExpired:
            logger.warning("libguestfs appliance build timed out after 150s")
            return None
        except subprocess.CalledProcessError as e:
            logger.warning("libguestfs appliance build failed: %s", e.stderr)
            return None
        except FileNotFoundError:
            return None
        else:
            logger.info("libguestfs fixed appliance built at %s", appliance_dir)
            return appliance_dir

    @staticmethod
    def _clean_stale_guestfs_state() -> bool:
        """Remove stale libguestfs locks and sockets that cause hangs.

        Returns:
            True if any stale state was removed.
        """
        import os

        uid = os.getuid()
        cleaned = False

        # 1. Remove the global lock file — if a previous run died, this
        #    prevents new libguestfs instances from waiting indefinitely.
        lock_file = Path(f"/var/tmp/.guestfs-{uid}/lock")
        if lock_file.exists():
            try:
                lock_file.unlink()
                cleaned = True
                logger.debug("Removed stale libguestfs lock: %s", lock_file)
            except OSError:
                pass

        # 2. Remove stale daemon sockets — libguestfs may try to connect to
        #    a dead daemon and hang.
        for sock_dir in Path(f"/run/user/{uid}").glob("libguestfs*"):
            for sock in sock_dir.glob("guestfsd.sock"):
                try:
                    sock.unlink()
                    cleaned = True
                    logger.debug("Removed stale libguestfs socket: %s", sock)
                except OSError:
                    pass

        # 3. Remove cached appliance directories in /var/tmp — these are
        #    rebuildable and can confuse libguestfs about appliance freshness.
        guestfs_tmp = Path(f"/var/tmp/.guestfs-{uid}")
        if guestfs_tmp.exists():
            for entry in guestfs_tmp.glob("appliance.d*"):
                if entry.is_dir():
                    try:
                        shutil.rmtree(entry)
                        cleaned = True
                        logger.debug(
                            "Removed stale libguestfs cache: %s", entry
                        )
                    except OSError:
                        pass

        return cleaned

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

                    VMOperation.remove(VMInput(identifiers=[vm.name]))
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
        """Prune miscellaneous cache: appliance, warm images, and stale guestfs state.

        Always removes appliance, warm images, and stale libguestfs locks/sockets
        — no protection flags.

        Args:
            dry_run: If True, only report what would be removed.

        Returns:
            Dictionary with keys "appliance", "warm_images", and
            "guestfs_state" indicating whether each was removed.
        """
        appliance_pruned = CacheOperation._prune_appliance(dry_run)
        warm_images_pruned = CacheOperation._prune_warm_images(dry_run)
        guestfs_state_pruned = CacheOperation._clean_stale_guestfs_state()
        return {
            "appliance": appliance_pruned,
            "warm_images": warm_images_pruned,
            "guestfs_state": guestfs_state_pruned,
        }

    @staticmethod
    def prune_all(
        dry_run: bool = False,
        include_all: bool = False,
    ) -> PruneAllResult:
        """Prune all cache resources.

        Performs a complete prune operation across all resource types:
        VMs, networks, images, kernels, binaries, and misc (appliance + warm images).

        Args:
            dry_run: If True, only report what would be removed.
            include_all: If True, remove ALL resources including protected items.

        Returns:
            PruneAllResult with aggregated pruned IDs, failed IDs, and
            whether running VMs were present during the operation.
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

        pruned_ids.extend(
            CacheOperation.prune_vms(dry_run=dry_run, include_all=include_all)
        )
        pruned_ids.extend(
            CacheOperation.prune_networks(
                dry_run=dry_run, include_all=include_all
            )
        )
        pruned_ids.extend(
            CacheOperation.prune_images(
                dry_run=dry_run, include_all=include_all
            )
        )
        pruned_ids.extend(
            CacheOperation.prune_kernels(
                dry_run=dry_run, include_all=include_all
            )
        )
        pruned_ids.extend(
            CacheOperation.prune_binaries(
                dry_run=dry_run, include_all=include_all
            )
        )

        misc = CacheOperation.prune_misc(dry_run=dry_run)
        if misc.get("appliance"):
            pruned_ids.append("appliance")
        if misc.get("warm_images"):
            pruned_ids.append("warm_images")
        if misc.get("guestfs_state"):
            pruned_ids.append("guestfs_state")

        return PruneAllResult(
            pruned_ids=pruned_ids,
            failed_ids=failed_ids,
            had_running_vms=had_running_vms,
        )

    @staticmethod
    def clean(dry_run: bool = False) -> CleanResult:
        """Completely clean all cache — host, prune everything, remove cache dir.

        This is the "nuclear option" for cache cleanup. It:
        1. Cleans host networking (TAPs, bridges, iptables chains)
        2. Prunes all resources (VMs, networks, images, kernels, binaries, misc)
        3. Removes the entire cache directory at ~/.cache/mvmctl

        Args:
            dry_run: If True, only report what would be removed.

        Returns:
            CleanResult with prune details and cache dir removal status.
        """
        from mvmctl.api.host_operations import HostOperation

        # Step 1: Prune all cached resources
        prune_result = CacheOperation.prune_all(
            dry_run=dry_run, include_all=True
        )

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

        return CleanResult(
            prune_result=prune_result,
            cache_dir_removed=cache_dir_removed,
            cache_dir=str(cache_dir),
        )

    @staticmethod
    def _prune_appliance(dry_run: bool = False) -> bool:
        """Remove the libguestfs appliance folder and stale system state.

        Also cleans up stale locks and sockets in /var/tmp and /run/user
        that can cause subsequent appliance builds to hang.

        Args:
            dry_run: If True, only report what would be removed.

        Returns:
            True if appliance folder or stale state was removed.
        """
        appliance_dir = CacheUtils.get_cache_dir() / "appliance"
        removed = False
        if appliance_dir.exists():
            if not dry_run:
                shutil.rmtree(appliance_dir, ignore_errors=True)
            removed = True

        if not dry_run:
            state_cleaned = CacheOperation._clean_stale_guestfs_state()
            removed = removed or state_cleaned

        return removed

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
