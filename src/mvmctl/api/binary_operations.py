"""Binary operations orchestration.

This module provides the orchestration layer for binary management operations.
It combines download, removal, listing, and default setting into a single
operation class.
"""

from __future__ import annotations

import logging
from dataclasses import dataclass

from mvmctl.api.inputs._binary_fetch_input import (
    BinaryFetchInput,
    BinaryFetchRequest,
)
from mvmctl.api.inputs._binary_input import (
    BinaryInput,
    BinaryRequest,
)
from mvmctl.core._internal._db import Database
from mvmctl.core.binary._controller import BinaryController
from mvmctl.core.binary._repository import BinaryRepository
from mvmctl.core.binary._resolver import BinaryResolver
from mvmctl.core.binary._service import BinaryService
from mvmctl.exceptions import (
    BinaryError,
    BinaryNotFoundError,
)
from mvmctl.models.binary import BinaryItem
from mvmctl.utils.auditlog import AuditLog

logger = logging.getLogger(__name__)


@dataclass
class BinaryFetchResult:
    """Result of binary fetch operation — contains firecracker and jailer binaries."""

    result: list[BinaryItem]


class BinaryOperation:
    """Binary management orchestration."""

    @staticmethod
    def fetch(inputs: BinaryFetchInput) -> BinaryFetchResult:
        """Download a binary version.

        Flow:
        1. Resolve inputs via BinaryFetchRequest
        2. Check if version already exists in DB
        3. If exists and no override, return existing binaries
        4. Download via BinaryService.download()
        5. Upsert to DB via BinaryRepository
        6. If set_as_default, mark as default
        7. Return BinaryFetchResult

        Args:
            inputs: BinaryFetchInput with version and set_as_default flag.

        Returns:
            BinaryFetchResult with firecracker and jailer entries.
        """
        db = Database()
        repo = BinaryRepository(db)

        # Resolve inputs
        request = BinaryFetchRequest(inputs=inputs, db=db)
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
            return BinaryFetchResult(result=[fc_exists, jl_exists])

        # Download (override or first-time)
        no_default = repo.get_default("firecracker") is None
        should_set_default = resolved.set_as_default or no_default

        binaries = BinaryService.download_firecracker(
            version=resolved.version,
            bin_dir=resolved.bin_dir,
        )

        # Persist to DB
        for binary in binaries:
            binary.is_default = should_set_default
            repo.upsert(binary)

        AuditLog.log("binary.fetch", changes={"version": resolved.version})

        return BinaryFetchResult(result=binaries)

    @staticmethod
    def remove(inputs: BinaryInput, force: bool = False) -> None:
        """Remove binaries by ID (canonical method).

        Flow:
        1. Resolve inputs via BinaryRequest
        2. Delegate to BinaryService.remove_many() which handles
           VM reference checks and soft/hard delete per binary.

        Args:
            inputs: BinaryInput with identifiers to remove.
            force: If True, remove even if referenced by VMs.
        """
        db = Database()
        repo = BinaryRepository(db)

        # Resolve inputs
        request = BinaryRequest(inputs=inputs, db=db)
        resolved = request.resolve()

        service = BinaryService(repo=repo)
        service.remove_many(resolved.binaries, force=force)

        for binary in resolved.binaries:
            AuditLog.log(
                "binary.remove",
                changes={
                    "id": binary.id,
                    "name": binary.name,
                    "version": binary.full_version,
                },
            )

    @staticmethod
    def remove_by_version(version: str, force: bool = False) -> None:
        """Remove both firecracker and jailer for a version (convenience).

        Flow:
        1. Resolve to firecracker and jailer BinaryItems for version
        2. Delegate to BinaryService.remove_many()

        Args:
            version: Version string to remove (e.g., "1.15.0").
            force: If True, remove even if referenced by VMs.
        """
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
                    "Binary %s v%s not found in DB, skipping", name, normalized
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

    @staticmethod
    def get(inputs: BinaryInput) -> list[BinaryItem]:
        """Get binaries by identifier.

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
        """List all locally installed binaries.

        Returns:
            list[BinaryItem] from database query with filesystem sync.
        """
        db = Database()
        repo = BinaryRepository(db)
        service = BinaryService(repo)
        return service.list_local()

    @staticmethod
    def list_remote(limit: int | None = None) -> list[str]:
        """List available remote versions.

        Args:
            limit: Maximum number of versions to return.

        Returns:
            list[str] of version strings.
        """
        return BinaryService.list_remote(limit=limit)

    @staticmethod
    def set_default(inputs: BinaryInput) -> None:
        """Set binary as default.

        Args:
            inputs: BinaryInput with identifier of binary to set as default.
        """
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


__all__ = ["BinaryOperation", "BinaryFetchResult"]
