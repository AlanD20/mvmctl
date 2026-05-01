"""Kernel input models for API boundary — existing resource actions."""

from __future__ import annotations

from dataclasses import dataclass, field

from mvmctl.core._shared import Database
from mvmctl.core.kernel._repository import KernelRepository
from mvmctl.core.kernel._resolver import KernelResolver
from mvmctl.exceptions import KernelNotFoundError
from mvmctl.models.kernel import KernelItem

__all__ = ["KernelInput", "KernelRequest", "ResolvedKernelInput"]


@dataclass
class KernelInput:
    """
    Input model for identifying existing kernels.

    Used for operations on existing kernels (remove, get, inspect, set-default).
    Provides identifiers (name or id) to resolve the kernel from DB.
    """

    id: list[str] = field(default_factory=list)
    name: list[str] = field(default_factory=list)
    force: bool | None = None


@dataclass(frozen=True)
class ResolvedKernelInput:
    """
    Immutable resolved kernel request — contains resolved KernelItem records.

    These records are guaranteed to exist in the DB, making them safe to operate on.
    """

    kernels: list[KernelItem]
    force: bool


class KernelRequest:
    """
    Resolve kernel identifiers to DB records and validate.

    Takes KernelInput (names/ids) and resolves them to KernelItem records
    using KernelResolver. Calls ensure_validate() after resolution.
    """

    _result: ResolvedKernelInput | None = None

    def __init__(
        self, *, inputs: KernelInput, db: Database | None = None
    ) -> None:
        """Initialize the resolver with database and sub-resolvers."""
        self._inputs = inputs
        self._db = db if db is not None else Database()
        self._resolver = KernelResolver(KernelRepository(self._db))

    @property
    def result(self) -> ResolvedKernelInput | None:
        return self._result

    def resolve(self) -> ResolvedKernelInput:
        """
        Resolve kernel identifiers to KernelItem records.

        Returns:
            ResolvedKernelInput with resolved kernel records.

        Raises:
            KernelNotFoundError: If any identifier cannot be resolved.

        """
        identifiers = self._inputs.id + self._inputs.name

        if not identifiers:
            raise KernelNotFoundError("No kernel identifiers provided")

        result = self._resolver.resolve_many(list(identifiers))

        if result.errors and not result.items:
            raise KernelNotFoundError(
                f"Could not resolve any kernels: {', '.join(result.errors)}"
            )

        self._result = ResolvedKernelInput(
            kernels=result.items,
            force=self._inputs.force if self._inputs.force else False,
        )

        # Validate
        self.ensure_validate()

        return self._result

    def ensure_validate(self) -> None:
        """Validate resolved kernel inputs."""
        if self._result is None:
            raise KernelNotFoundError(
                "Failed to resolve necessary dependencies to validate"
            )

        if not self._result.kernels:
            raise KernelNotFoundError("No kernels found matching identifiers")
