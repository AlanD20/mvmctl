"""
Binary operations orchestration.

This module provides the orchestration layer for binary management operations.
It combines download, removal, listing, and default setting into a single
operation class.
"""

from __future__ import annotations

import logging

from mvmctl.api.inputs._binary_input import (
    BinaryInput,
    BinaryRequest,
)
from mvmctl.api.inputs._binary_pull_input import (
    BinaryPullInput,
    BinaryPullRequest,
)
from mvmctl.core._shared import Database
from mvmctl.core.binary._controller import BinaryController
from mvmctl.core.binary._repository import BinaryRepository
from mvmctl.core.binary._resolver import BinaryResolver
from mvmctl.core.binary._service import BinaryService
from mvmctl.core.config._service import SettingsService
from mvmctl.exceptions import (
    BinaryError,
    BinaryNotFoundError,
)
from mvmctl.models import BinaryItem
from mvmctl.models.result import (
    BatchResult,
    NeedsInteraction,
    OperationResult,
)
from mvmctl.utils.auditlog import AuditLog

logger = logging.getLogger(__name__)


class BinaryOperation:
    """Binary management orchestration."""

    @staticmethod
    def pull(
        inputs: BinaryPullInput,
    ) -> OperationResult[list[BinaryItem]] | NeedsInteraction:
        """
        Download a binary version.

        Flow:
        1. Resolve inputs via BinaryPullRequest
        2. Check if version already exists in DB
        3. If exists and no override, return existing binaries
        4. Download via BinaryService.download()
        5. Upsert to DB via BinaryRepository
        6. If set_as_default, mark as default
        7. Return OperationResult

        Args:
            inputs: BinaryPullInput with version and set_as_default flag.

        Returns:
            OperationResult with firecracker and jailer entries.

        Raises:
            BinaryError: If download or resolution fails.

        """
        try:
            db = Database()
            repo = BinaryRepository(db)

            # Resolve inputs
            request = BinaryPullRequest(inputs=inputs, db=db)
            resolved = request.resolve()

            # Check if version already exists
            normalized = resolved.version.removeprefix("v")
            fc_exists = repo.get_by_name_and_version("firecracker", normalized)
            jl_exists = repo.get_by_name_and_version("jailer", normalized)
            version_exists = fc_exists is not None and jl_exists is not None

            if version_exists and not resolved.download_override:
                # Early exit: return existing binaries without downloading
                assert fc_exists is not None
                assert jl_exists is not None
                if resolved.set_as_default:
                    for b in (fc_exists, jl_exists):
                        repo.set_default(
                            name=b.name,
                            version=b.version,
                            path=b.path,
                        )
                return OperationResult(
                    status="skipped",
                    code="binary.already_present",
                    message=f"Binary v{normalized} already present",
                    item=[fc_exists, jl_exists],
                )

            # Download (override or first-time)
            no_default = repo.get_default("firecracker") is None
            should_set_default = resolved.set_as_default or no_default

            binaries = BinaryService.download_firecracker(
                version=resolved.version,
                bin_dir=resolved.bin_dir,
            )

            # Persist to DB
            for binary in binaries:
                repo.upsert(binary)
                if should_set_default:
                    repo.set_default(
                        name=binary.name,
                        version=binary.version,
                        path=binary.path,
                    )

            AuditLog.log("binary.pull", changes={"version": resolved.version})

            return OperationResult(
                status="success",
                code="binary.downloaded",
                message=f"Downloaded Firecracker v{normalized}",
                item=binaries,
            )
        except BinaryError as e:
            return OperationResult(
                status="error",
                code="binary.pull_failed",
                message=str(e),
                exception=e,
            )

    @staticmethod
    def remove(
        inputs: BinaryInput, force: bool = False
    ) -> BatchResult[BinaryItem]:
        """
        Remove binaries by ID (canonical method).

        Flow:
        1. Resolve inputs via BinaryRequest
        2. For each binary, delegate to BinaryService.remove() which handles
           VM reference checks and soft/hard delete.
        3. Return BatchResult with per-binary OperationResult items.

        Args:
            inputs: BinaryInput with identifiers to remove.
            force: If True, remove even if referenced by VMs.

        Returns:
            BatchResult with per-binary results.

        """
        db = Database()
        repo = BinaryRepository(db)

        # Resolve inputs
        request = BinaryRequest(inputs=inputs, db=db)
        resolved = request.resolve()

        service = BinaryService(repo=repo)
        items: list[OperationResult[BinaryItem]] = []

        for binary in resolved.binaries:
            try:
                service.remove(binary, force=force)
                items.append(
                    OperationResult(
                        status="success",
                        code="binary.removed",
                        message=f"Removed {binary.name} v{binary.full_version}",
                        item=binary,
                    )
                )
                AuditLog.log(
                    "binary.remove",
                    changes={
                        "id": binary.id,
                        "name": binary.name,
                        "version": binary.full_version,
                    },
                )
            except (BinaryError, BinaryNotFoundError) as e:
                items.append(
                    OperationResult(
                        status="error",
                        code="binary.remove_failed",
                        message=str(e),
                        item=binary,
                        exception=e,
                    )
                )

        return BatchResult(items=items)

    @staticmethod
    def remove_by_version(
        version: str, force: bool = False
    ) -> OperationResult[None]:
        """
        Remove both firecracker and jailer for a version (convenience).

        Flow:
        1. Resolve to firecracker and jailer BinaryItems for version
        2. Delegate to BinaryService.remove_many()
        3. Return OperationResult

        Args:
            version: Version string to remove (e.g., "1.15.0").
            force: If True, remove even if referenced by VMs.

        Returns:
            OperationResult with operation status.

        """
        try:
            db = Database()
            repo = BinaryRepository(db)
            resolver = BinaryResolver(repo)

            normalized = version.removeprefix("v")

            binaries_to_remove: list[BinaryItem] = []
            for name in ("firecracker", "jailer"):
                try:
                    binary = resolver.by_name_version(name, normalized)
                    binaries_to_remove.append(binary)
                except BinaryNotFoundError:
                    logger.debug(
                        "Binary %s v%s not found in DB, skipping",
                        name,
                        normalized,
                    )

            if not binaries_to_remove:
                return OperationResult(
                    status="error",
                    code="binary.not_found",
                    message=f"No binaries found for version {normalized}",
                )

            if binaries_to_remove:
                service = BinaryService(repo=repo)
                service.remove_many(binaries_to_remove, force=force)

                for binary in binaries_to_remove:
                    AuditLog.log(
                        "binary.remove",
                        changes={
                            "id": binary.id,
                            "name": binary.name,
                            "version": normalized,
                        },
                    )

            return OperationResult(
                status="success",
                code="binary.removed",
                message=f"Removed binaries for v{normalized}",
            )
        except (BinaryError, BinaryNotFoundError) as e:
            return OperationResult(
                status="error",
                code="binary.remove_failed",
                message=str(e),
                exception=e,
            )

    @staticmethod
    def get(inputs: BinaryInput) -> list[BinaryItem]:
        """
        Get binaries by identifier.

        Args:
            inputs: BinaryInput with identifiers to resolve.

        Returns:
            list[BinaryItem] matching the identifiers.

        """
        db = Database()
        request = BinaryRequest(inputs=inputs, db=db)
        resolved = request.resolve()
        return resolved.binaries

    @staticmethod
    def list_local() -> list[BinaryItem]:
        """
        List all locally installed binaries.

        Returns:
            list[BinaryItem] from database query with filesystem sync.

        """
        db = Database()
        repo = BinaryRepository(db)
        service = BinaryService(repo)
        return service.list_local()

    @staticmethod
    def list_remote(limit: int | None = None) -> list[str]:
        """
        List available remote versions.

        Args:
            limit: Maximum number of versions to return.

        Returns:
            list[str] of version strings.

        """
        if limit is None:
            limit = int(
                SettingsService.resolve(
                    Database(), "defaults.binary", "remote_version_limit"
                )
            )
        return BinaryService.list_remote(limit=limit)

    @staticmethod
    def set_default(inputs: BinaryInput) -> OperationResult[BinaryItem]:
        """
        Set binary as default.

        Args:
            inputs: BinaryInput with identifier of binary to set as default.

        Returns:
            OperationResult with the binary that was set as default.

        """
        try:
            db = Database()
            repo = BinaryRepository(db)

            request = BinaryRequest(inputs=inputs, db=db)
            resolved = request.resolve()

            if len(resolved.binaries) > 1:
                raise BinaryError("Ambiguous ID to set to default")

            binary = resolved.binaries[0]
            controller = BinaryController(entity=binary, repo=repo)
            controller.set_default()
            AuditLog.log(
                "binary.set_default",
                changes={
                    "id": binary.id,
                    "name": binary.name,
                    "version": binary.version,
                },
            )
            return OperationResult(
                status="success",
                code="binary.default_set",
                message=f"Default binary set to {binary.name} v{binary.version}",
                item=binary,
            )
        except BinaryError as e:
            return OperationResult(
                status="error",
                code="binary.default_set_failed",
                message=str(e),
                exception=e,
            )

    @staticmethod
    def ensure_default() -> OperationResult[BinaryItem]:
        """
        Ensure a default Firecracker binary exists.

        If local Firecracker binaries exist but none is marked default, sets
        the latest Firecracker binary as default.

        Returns:
            OperationResult with the default binary (or None if no binaries exist).

        """
        try:
            from packaging.version import Version

            db = Database()
            repo = BinaryRepository(db)
            service = BinaryService(repo)

            local = service.list_local()
            if not local:
                return OperationResult(
                    status="success",
                    code="binary.default_unchanged",
                    message="No local binaries found",
                )

            default = service.get_default_firecracker()
            if default is not None:
                return OperationResult(
                    status="skipped",
                    code="binary.default_unchanged",
                    message="Default already set",
                    item=default,
                )

            firecracker_bins = [b for b in local if b.name == "firecracker"]
            if not firecracker_bins:
                return OperationResult(
                    status="success",
                    code="binary.default_unchanged",
                    message="No firecracker binaries found",
                )

            latest_fc = sorted(
                firecracker_bins,
                key=lambda b: Version(b.version),
                reverse=True,
            )[0]
            controller = BinaryController(entity=latest_fc, repo=repo)
            controller.set_default()
            AuditLog.log(
                "binary.ensure_default",
                changes={
                    "id": latest_fc.id,
                    "name": latest_fc.name,
                    "version": latest_fc.version,
                },
            )
            return OperationResult(
                status="success",
                code="binary.default_repaired",
                message=f"Default set to {latest_fc.name} v{latest_fc.version}",
                item=latest_fc,
            )
        except BinaryError as e:
            return OperationResult(
                status="error",
                code="binary.ensure_default_failed",
                message=str(e),
                exception=e,
            )


__all__ = ["BinaryOperation"]
