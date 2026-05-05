"""Kernel operations - cross-domain orchestration for kernel management."""

from __future__ import annotations

import logging
from collections.abc import Callable
from datetime import UTC, datetime
from pathlib import Path
from typing import Any

from mvmctl.api.inputs._kernel_input import KernelInput
from mvmctl.api.inputs._kernel_pull_input import (
    KernelPullInput,
    KernelPullRequest,
)
from mvmctl.constants import DEFAULT_FIRECRACKER_CI_VERSION
from mvmctl.core._shared import Database
from mvmctl.core.binary._repository import BinaryRepository
from mvmctl.core.binary._service import BinaryService
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
                existing_path = Path(existing.path)
                if existing_path.exists():
                    logger.info("Kernel already exists: %s", existing.path)
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

            # Build KernelItem (path is relative — filename only)
            kernel_item = KernelItem(
                id=kernel_id,
                name=fetch_result.path.name,
                base_name=parsed.base_name,
                version=fetch_result.version,
                arch=resolved.arch,
                type=resolved.kernel_type,
                path=fetch_result.path.name,
                is_default=resolved.set_default,
                is_present=True,
                created_at=timestamp,
                updated_at=timestamp,
            )

            repo.upsert(kernel_item)

            # Clean up old kernel file if ID changed
            if existing is not None and existing.id != kernel_item.id:
                old_path = existing.resolved_path
                if old_path.exists():
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

        db = Database()
        repo = KernelRepository(db)

        # Resolve identifiers
        request = KernelRequest(inputs=inputs, db=db)
        resolved = request.resolve()

        service = KernelService(repo)
        items: list[OperationResult[KernelItem]] = []

        for kernel in resolved.kernels:
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
    def list_all() -> list[KernelItem]:
        """
        List all kernels, syncing is_present with filesystem.

        Returns:
            List of all KernelItem records.

        """
        db = Database()
        repo = KernelRepository(db)
        return KernelService(repo).list_all()

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
    def inspect(
        inputs: KernelInput, is_json: bool = False
    ) -> KernelItem | dict[str, Any]:
        """
        Inspect a kernel with full details.

        Args:
            inputs: KernelInput with id/name identifiers.
            is_json: If True, return a dict suitable for JSON serialization.

        Returns:
            KernelItem or dict representation depending on is_json.

        """
        kernel_item = KernelOperation.get(inputs)
        if is_json:
            return KernelOperation._kernel_to_dict(kernel_item)
        return kernel_item

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
