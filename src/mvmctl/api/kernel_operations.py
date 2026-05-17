"""Kernel operations - cross-domain orchestration for kernel management."""

from __future__ import annotations

import logging
from collections.abc import Callable
from dataclasses import replace
from datetime import UTC, datetime
from typing import Any

from mvmctl.api.inputs._kernel_import_input import (
    KernelImportInput,
    KernelImportRequest,
)
from mvmctl.api.inputs._kernel_input import KernelInput
from mvmctl.api.inputs._kernel_pull_input import (
    KernelPullInput,
    KernelPullRequest,
)
from mvmctl.constants import DEFAULT_FIRECRACKER_CI_VERSION
from mvmctl.core._shared import Database
from mvmctl.core._shared._http_dir_version_resolver import VersionInfo
from mvmctl.core.binary._repository import BinaryRepository
from mvmctl.core.binary._service import BinaryService
from mvmctl.core.config._service import SettingsService
from mvmctl.core.kernel._controller import KernelController
from mvmctl.core.kernel._repository import KernelRepository
from mvmctl.core.kernel._service import KernelService
from mvmctl.exceptions import KernelError
from mvmctl.models import KernelItem, KernelPullResult
from mvmctl.models.result import (
    BatchResult,
    NeedsInteraction,
    OperationResult,
    ProgressEvent,
)
from mvmctl.utils.auditlog import AuditLog
from mvmctl.utils.crypto import HashGenerator
from mvmctl.utils.operation_utils import OperationUtils

logger = logging.getLogger(__name__)

__all__ = ["KernelOperation"]


class KernelOperation:
    """
    Orchestration layer for kernel operations.

    All methods are @staticmethod — they take Input classes as arguments,
    create Request/Resolved internally, and orchestrate across core modules.
    """

    @staticmethod
    def prune(
        dry_run: bool = False,
        include_all: bool = False,
    ) -> OperationResult[list[str]]:
        """Prune unused kernels.

        Args:
            dry_run: If True, only report what would be removed.
            include_all: If True, remove ALL kernels including default and referenced.

        Returns:
            OperationResult with item list of kernel IDs that were removed.
        """
        from mvmctl.core.vm._repository import VMRepository

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
    def pull(
        inputs: KernelPullInput,
        *,
        on_progress: Callable[[ProgressEvent], None] | None = None,
    ) -> OperationResult[KernelItem] | NeedsInteraction:
        """
        Pull or build a kernel based on type.

        Args:
            inputs: KernelPullInput with kernel_type, version, arch, etc.
            on_progress: Optional callback for progress events.

        Returns:
            OperationResult with the created KernelItem.

        """
        try:
            db = Database()
            repo = KernelRepository(db)

            # Resolve "latest" version to concrete version using
            # cross-domain orchestration (API layer responsibility).
            if inputs.version == "latest":
                ci_version: str | None = None
                if inputs.kernel_type == "firecracker":
                    try:
                        binary_service = BinaryService(BinaryRepository(db))
                        default_fc = binary_service.get_default_firecracker()
                        if default_fc and default_fc.ci_version:
                            ci_version = default_fc.ci_version
                    except Exception:
                        ci_version = DEFAULT_FIRECRACKER_CI_VERSION
                resolved_version = KernelService.resolve_latest_version(
                    inputs.kernel_type, ci_version=ci_version
                )
                inputs = replace(inputs, version=resolved_version)

            # Resolve and validate inputs
            request = KernelPullRequest(inputs=inputs, db=db)
            resolved = request.resolve()

            # Check for existing kernel
            existing = None
            if resolved.kernel_type == "firecracker":
                existing = repo.get_by_type(resolved.kernel_type)
            elif resolved.kernel_type == "official" and resolved.version:
                existing = repo.get_by_version_and_type(
                    resolved.version, resolved.kernel_type
                )

            if existing is not None:
                existing_path = existing.resolved_path
                if existing_path.exists():
                    logger.info("Kernel already exists: %s", existing.path)
                    if resolved.set_default:
                        repo.set_default(existing.id)
                    return OperationResult(
                        status="skipped",
                        code="kernel.already_present",
                        message=f"Kernel already exists: {existing.path}",
                        item=existing,
                    )

            # Resolve spec via KernelService
            specs = KernelService.get_specs_for(
                kernel_type=resolved.kernel_type, version=resolved.version
            )
            if len(specs) != 1:
                raise KernelError(
                    f"Expected exactly one kernel spec for type='{resolved.kernel_type}' "
                    f"version='{resolved.version}', got {len(specs)}"
                )
            spec = specs[0]

            kernel_service = KernelService(repo)
            fetch_result: KernelPullResult

            if resolved.kernel_type == "firecracker":
                binary_service = BinaryService(BinaryRepository(db))
                default_firecracker = binary_service.get_default_firecracker()

                # Get CI version for template resolution
                ci_version = DEFAULT_FIRECRACKER_CI_VERSION
                if default_firecracker and default_firecracker.ci_version:
                    ci_version = default_firecracker.ci_version

                if on_progress is not None:
                    on_progress(
                        ProgressEvent(
                            phase="download",
                            status="running",
                            message="Downloading Firecracker kernel...",
                        )
                    )

                fetch_result = kernel_service.fetch_firecracker_kernel(
                    spec=spec,
                    ci_version=ci_version,
                    arch=resolved.arch,
                    output_dir=resolved.output_dir,
                    progress_callback=OperationUtils.download_progress_bridge(
                        on_progress
                    ),
                )
                if on_progress is not None:
                    on_progress(
                        ProgressEvent(
                            phase="download",
                            status="complete",
                            message="Firecracker kernel download complete.",
                        )
                    )
            elif resolved.kernel_type == "official":
                if on_progress is not None:
                    on_progress(
                        ProgressEvent(
                            phase="build",
                            status="running",
                            message="Building kernel (this may take a while)...",
                        )
                    )
                fetch_result = kernel_service.build_official_kernel(
                    spec=spec,
                    arch=resolved.arch,
                    output_dir=resolved.output_dir,
                    jobs=resolved.jobs,
                    keep_build_dir=resolved.keep_build_dir,
                    clean_build=resolved.clean_build,
                    kernel_config=resolved.kernel_config,
                    progress_callback=OperationUtils.download_progress_bridge(
                        on_progress
                    ),
                    on_status=lambda msg: (
                        on_progress(
                            ProgressEvent(
                                phase="build", status="running", message=msg
                            )
                        )
                        if on_progress
                        else None
                    ),
                )
                if on_progress is not None:
                    on_progress(
                        ProgressEvent(
                            phase="build",
                            status="complete",
                            message="Kernel build complete.",
                        )
                    )
            else:
                raise KernelError(
                    f"Unsupported kernel type: {resolved.kernel_type}"
                )

            # Generate hash from the fetched/built kernel file
            timestamp = datetime.now(tz=UTC).isoformat()
            kernel_id = HashGenerator.kernel(
                fetch_result.path,
                fetch_result.version,
                resolved.arch,
                timestamp,
            )

            # Parse filename for base_name
            parsed = KernelService.parse_filename(fetch_result.path.name)

            # Store the resolved absolute path so resolved_path
            # points to the correct filesystem location even when
            # a custom output directory was used.
            kernel_item = KernelItem(
                id=kernel_id,
                name=fetch_result.path.name,
                base_name=parsed.base_name,
                version=fetch_result.version,
                arch=resolved.arch,
                type=resolved.kernel_type,
                path=str(fetch_result.path.resolve()),
                is_default=False,
                is_present=True,
                created_at=timestamp,
                updated_at=timestamp,
            )

            repo.upsert(kernel_item)

            if resolved.set_default:
                repo.set_default(kernel_item.id)

            # Clean up old kernel file if ID changed AND the path is different
            # from the newly downloaded file (avoids deleting the file we just
            # downloaded when the file is re-downloaded to the same path).
            if existing is not None and existing.id != kernel_item.id:
                old_path = existing.resolved_path
                new_path = fetch_result.path.resolve()
                if old_path.resolve() != new_path and old_path.exists():
                    old_path.unlink()
                    logger.info(
                        "Cleaned up old kernel file for %s-%s",
                        resolved.kernel_type,
                        resolved.version,
                    )

            AuditLog.log(
                "kernel.pull",
                changes={
                    "id": kernel_item.id,
                    "type": kernel_item.type,
                    "version": kernel_item.version,
                    "arch": kernel_item.arch,
                },
            )
            return OperationResult(
                status="success",
                code="kernel.pulled",
                message=f"Kernel '{kernel_item.name}' pulled successfully",
                item=kernel_item,
            )
        except KernelError as e:
            return OperationResult(
                status="error",
                code="kernel.pull_failed",
                message=str(e),
                exception=e,
            )

    @staticmethod
    def remove(
        inputs: KernelInput, force: bool = False
    ) -> BatchResult[KernelItem]:
        """
        Remove kernel by ID prefix or name.

        Args:
            inputs: KernelInput with id/name identifiers.
            force: If True, remove even if referenced by VMs.

        Returns:
            BatchResult with per-kernel results.

        """
        from mvmctl.api.inputs._kernel_input import KernelRequest
        from mvmctl.core.kernel._resolver import KernelResolver

        db = Database()
        repo = KernelRepository(db)

        # Resolve identifiers
        request = KernelRequest(inputs=inputs, db=db)
        resolved = request.resolve()

        service = KernelService(repo)

        # Batch-enrich with VM references for VM reference check
        enriched = KernelResolver(repo, include=["vm"]).enrich(resolved.kernels)

        items: list[OperationResult[KernelItem]] = []

        for kernel in enriched:
            try:
                service.remove(kernel, force=force or resolved.force)
                items.append(
                    OperationResult(
                        status="success",
                        code="kernel.removed",
                        message=f"Removed kernel {kernel.name}",
                        item=kernel,
                    )
                )
                AuditLog.log(
                    "kernel.remove",
                    changes={
                        "id": kernel.id,
                        "name": kernel.name,
                        "type": kernel.type,
                    },
                )
            except KernelError as e:
                items.append(
                    OperationResult(
                        status="error",
                        code="kernel.remove_failed",
                        message=str(e),
                        item=kernel,
                        exception=e,
                    )
                )

        return BatchResult(items=items)

    @staticmethod
    def list_all(
        remote: bool = False,
        *,
        no_cache: bool = False,
    ) -> list[KernelItem] | list[VersionInfo]:
        """
        List kernels.

        Args:
            remote: If True, return available remote kernel versions.
                    If False (default), return locally cached kernels.
            no_cache: If True, bypass cached version listings and fetch
                      live from upstream. Only relevant when ``remote=True``.

        Returns:
            List of KernelItem (local) or VersionInfo (remote).

        """
        if remote:
            return KernelOperation._list_remote(no_cache=no_cache)

        db = Database()
        repo = KernelRepository(db)
        return KernelService(repo).list_all()

    @staticmethod
    def _list_remote(
        *,
        no_cache: bool = False,
    ) -> list[VersionInfo]:
        """
        List available remote kernel versions.

        Fetches version listings from upstream providers (kernel.org for
        official kernels, Firecracker S3 for firecracker kernels) using
        the shared version resolver.

        Args:
            no_cache: If True, bypass cached version listings and fetch
                live from upstream.

        Returns:
            List of ``VersionInfo`` objects describing available versions.

        """
        configs = KernelService.load_kernel_types_config()

        arch = "x86_64"  # Default arch for kernel listing
        db = Database()
        cache_ttl: int | None = (
            None
            if no_cache
            else int(
                SettingsService.resolve(
                    db, "defaults.kernel", "remote_list_cache_ttl"
                )
            )
        )

        # Resolve ci_version from default firecracker binary
        resolved_ci_version: str | None = None
        try:
            binary_repo = BinaryRepository(db)
            binary_service = BinaryService(binary_repo)
            default_fc = binary_service.get_default_firecracker()
            if default_fc and default_fc.ci_version:
                resolved_ci_version = default_fc.ci_version
        except Exception:
            pass  # Fall back to resolver's default constant

        # Resolve remote_list_limit from settings
        remote_list_limit: int = int(
            SettingsService.resolve(db, "defaults.kernel", "remote_list_limit")
        )

        from mvmctl.core._shared._http_dir_version_resolver import (
            HttpDirVersionResolver,
        )

        version_map = HttpDirVersionResolver.resolve(
            configs,
            arch=arch,
            cache_ttl_seconds=cache_ttl,
            ci_version=resolved_ci_version,
            limit=remote_list_limit,
        )

        flattened: list[VersionInfo] = []
        for versions in version_map.values():
            flattened.extend(versions)
        return flattened

    @staticmethod
    def get(inputs: KernelInput) -> KernelItem:
        """
        Get a single kernel by ID prefix or name.

        Args:
            inputs: KernelInput with id/name identifiers.

        Returns:
            The resolved KernelItem.

        Raises:
            KernelError: If kernel not found or ambiguous.

        """
        from mvmctl.api.inputs._kernel_input import KernelRequest

        db = Database()

        request = KernelRequest(inputs=inputs, db=db)
        resolved = request.resolve()

        if len(resolved.kernels) != 1:
            raise KernelError(
                f"Expected exactly one kernel, got {len(resolved.kernels)}"
            )

        return resolved.kernels[0]

    @staticmethod
    def _kernel_to_dict(kernel: KernelItem) -> dict[str, Any]:
        """
        Convert KernelItem to dictionary for JSON output.

        Includes every field from the model.
        """
        return {
            "id": kernel.id,
            "name": kernel.name,
            "base_name": kernel.base_name,
            "version": kernel.version,
            "arch": kernel.arch,
            "type": kernel.type,
            "path": kernel.path,
            "is_default": kernel.is_default,
            "is_present": kernel.is_present,
            "created_at": kernel.created_at,
            "updated_at": kernel.updated_at,
        }

    @staticmethod
    def inspect(inputs: KernelInput) -> dict[str, Any]:
        """
        Inspect a kernel with full details.

        Args:
            inputs: KernelInput with id/name identifiers.

        Returns:
            Grouped dict representation of the kernel.

        """
        kernel_item = KernelOperation.get(inputs)
        return {
            "kernel": {
                "id": kernel_item.id,
                "name": kernel_item.name,
                "base_name": kernel_item.base_name,
                "version": kernel_item.version,
                "arch": kernel_item.arch,
                "type": kernel_item.type,
                "is_default": kernel_item.is_default,
                "is_present": kernel_item.is_present,
            },
            "storage": {
                "path": kernel_item.path,
            },
            "timestamps": {
                "created_at": kernel_item.created_at,
                "updated_at": kernel_item.updated_at,
            },
        }

    @staticmethod
    def set_default(inputs: KernelInput) -> OperationResult[KernelItem]:
        """
        Set a kernel as the default.

        Args:
            inputs: KernelInput with id/name identifiers.

        Returns:
            OperationResult with the kernel that was set as default.

        """
        from mvmctl.api.inputs._kernel_input import KernelRequest

        try:
            db = Database()
            repo = KernelRepository(db)

            request = KernelRequest(inputs=inputs, db=db)
            resolved = request.resolve()

            if len(resolved.kernels) != 1:
                raise KernelError(
                    f"Expected exactly one kernel, got {len(resolved.kernels)}"
                )

            controller = KernelController(resolved.kernels[0], repo)
            controller.set_default()

            kernel = resolved.kernels[0]
            AuditLog.log("kernel.set_default", changes={"name": kernel.name})
            return OperationResult(
                status="success",
                code="kernel.default_set",
                message=f"Default kernel set to {kernel.name}",
                item=kernel,
            )
        except KernelError as e:
            return OperationResult(
                status="error",
                code="kernel.default_set_failed",
                message=str(e),
                exception=e,
            )

    @staticmethod
    def import_(
        inputs: KernelImportInput,
    ) -> OperationResult[KernelItem]:
        """
        Import a local vmlinux file as a kernel in the database.

        Auto-detects version and architecture from the filename when not
        explicitly provided, copies the file to the kernels cache directory,
        generates a content-addressed ID, creates a ``KernelItem`` with
        type ``"custom"``, and persists it to the database.

        Args:
            inputs: KernelImportInput with name, path, version, arch, etc.

        Returns:
            OperationResult with the imported KernelItem on success.

        """
        db = Database()
        repo = KernelRepository(db)

        try:
            # Resolve and validate inputs via the standard Request pattern
            request = KernelImportRequest(inputs=inputs, db=db)
            resolved = request.resolve()

            service = KernelService(repo)
            kernel_item = service.import_kernel(
                name=resolved.name,
                source_path=resolved.path,
                version=resolved.version,
                arch=resolved.arch,
                set_default=resolved.set_default,
            )

            AuditLog.log(
                "kernel.import",
                changes={
                    "name": kernel_item.name,
                    "version": kernel_item.version,
                    "arch": kernel_item.arch,
                },
            )

            return OperationResult(
                status="success",
                code="kernel.imported",
                message=f"Kernel imported: {kernel_item.name}",
                item=kernel_item,
            )
        except KernelError as e:
            return OperationResult(
                status="error",
                code="kernel.import_failed",
                message=str(e),
                exception=e,
            )
