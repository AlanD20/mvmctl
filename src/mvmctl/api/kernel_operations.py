"""Kernel operations - cross-domain orchestration for kernel management."""

from __future__ import annotations

import logging
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

from mvmctl.api.inputs._kernel_fetch_input import (
    KernelFetchInput,
    KernelFetchRequest,
)
from mvmctl.api.inputs._kernel_input import KernelInput
from mvmctl.constants import DEFAULT_FIRECRACKER_CI_VERSION
from mvmctl.core._internal._db import Database
from mvmctl.core.binary._repository import BinaryRepository
from mvmctl.core.binary._service import BinaryService
from mvmctl.core.kernel._controller import KernelController
from mvmctl.core.kernel._repository import KernelRepository
from mvmctl.core.kernel._service import KernelService
from mvmctl.exceptions import KernelError
from mvmctl.models.kernel import KernelFetchResult, KernelItem
from mvmctl.utils.auditlog import AuditLog
from mvmctl.utils.full_hash import HashGenerator

logger = logging.getLogger(__name__)

__all__ = ["KernelOperation"]


class KernelOperation:
    """Orchestration layer for kernel operations.

    All methods are @staticmethod — they take Input classes as arguments,
    create Request/Resolved internally, and orchestrate across core modules.
    """

    @staticmethod
    def fetch(inputs: KernelFetchInput) -> KernelItem:
        """Fetch or build a kernel based on type.

        Args:
            inputs: KernelFetchInput with kernel_type, version, arch, etc.

        Returns:
            The created KernelItem.

        Raises:
            KernelError: If fetch/build fails.
        """
        db = Database()
        repo = KernelRepository(db)

        # Resolve and validate inputs
        request = KernelFetchRequest(inputs=inputs, db=db)
        resolved = request.resolve()

        # Check for existing kernel
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
                return existing

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
        fetch_result: KernelFetchResult

        if resolved.kernel_type == "firecracker":
            binary_service = BinaryService(BinaryRepository(db))
            default_firecracker = binary_service.get_default_firecracker()

            # Get CI version for template resolution
            ci_version = DEFAULT_FIRECRACKER_CI_VERSION
            if default_firecracker and default_firecracker.ci_version:
                ci_version = default_firecracker.ci_version

            fetch_result = kernel_service.fetch_firecracker_kernel(
                spec=spec,
                ci_version=ci_version,
                arch=resolved.arch,
                output_dir=resolved.output_dir,
            )
        elif resolved.kernel_type == "official":
            fetch_result = kernel_service.build_official_kernel(
                spec=spec,
                arch=resolved.arch,
                output_dir=resolved.output_dir,
                jobs=resolved.jobs,
                keep_build_dir=resolved.keep_build_dir,
                clean_build=resolved.clean_build,
                kernel_config=resolved.kernel_config,
            )
        else:
            raise KernelError(
                f"Unsupported kernel type: {resolved.kernel_type}"
            )

        # Generate hash from the fetched/built kernel file
        timestamp = datetime.now(tz=timezone.utc).isoformat()
        kernel_id = HashGenerator.kernel(
            fetch_result.path, fetch_result.version, resolved.arch, timestamp
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
            "kernel.fetch",
            changes={
                "id": kernel_item.id,
                "type": kernel_item.type,
                "version": kernel_item.version,
                "arch": kernel_item.arch,
            },
        )
        return kernel_item

    @staticmethod
    def remove(inputs: KernelInput, force: bool = False) -> None:
        """Remove kernel by ID prefix or name.

        Args:
            inputs: KernelInput with id/name identifiers.
            force: If True, remove even if referenced by VMs.

        Raises:
            KernelError: If kernel not found or referenced by VMs.
        """
        from mvmctl.api.inputs._kernel_input import KernelRequest

        db = Database()
        repo = KernelRepository(db)

        # Resolve identifiers
        request = KernelRequest(inputs=inputs, db=db)
        resolved = request.resolve()

        service = KernelService(repo)
        service.remove_many(resolved.kernels, force=force or resolved.force)

        for kernel in resolved.kernels:
            AuditLog.log(
                "kernel.remove",
                changes={
                    "id": kernel.id,
                    "name": kernel.name,
                    "type": kernel.type,
                },
            )

    @staticmethod
    def list_all() -> list[KernelItem]:
        """List all kernels, syncing is_present with filesystem.

        Returns:
            List of all KernelItem records.
        """
        db = Database()
        repo = KernelRepository(db)
        return KernelService(repo).list_all()

    @staticmethod
    def get(inputs: KernelInput) -> KernelItem:
        """Get a single kernel by ID prefix or name.

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
        """Convert KernelItem to dictionary for JSON output.

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
            "created_at": kernel.created_at,
            "updated_at": kernel.updated_at,
        }

    @staticmethod
    def inspect(
        inputs: KernelInput, is_json: bool = False
    ) -> KernelItem | dict[str, Any]:
        """Inspect a kernel with full details.

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
    def set_default(inputs: KernelInput) -> None:
        """Set a kernel as the default.

        Args:
            inputs: KernelInput with id/name identifiers.
        """
        from mvmctl.api.inputs._kernel_input import KernelRequest

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

        AuditLog.log(
            "kernel.set_default", changes={"name": resolved.kernels[0].name}
        )

    @staticmethod
    def ensure_default() -> KernelItem:
        """Ensure the default kernel exists and is available.

        Returns:
            The default KernelItem.

        Raises:
            KernelError: If no default kernel exists.
        """
        db = Database()
        repo = KernelRepository(db)

        default_kernel = repo.get_default()
        if default_kernel is None:
            raise KernelError("No default kernel found in database")

        # Verify file exists on disk
        kernel_path = default_kernel.resolved_path
        if not kernel_path.exists():
            raise KernelError(
                f"Default kernel file not found: {kernel_path}"
            )

        return default_kernel
