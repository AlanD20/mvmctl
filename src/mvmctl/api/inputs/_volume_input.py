"""Volume input models for API boundary."""

from __future__ import annotations

from dataclasses import dataclass, field

from mvmctl.core._shared import Database
from mvmctl.core.volume._repository import VolumeRepository
from mvmctl.core.volume._resolver import VolumeResolver
from mvmctl.exceptions import VolumeNotFoundError
from mvmctl.models import VolumeItem

__all__ = [
    "VolumeInput",
    "VolumeRequest",
    "ResolvedVolumeInput",
]


@dataclass
class VolumeInput:
    """Identifiers for existing volume actions (ls, rm, inspect, get)."""

    id: list[str] = field(default_factory=list)
    name: list[str] = field(default_factory=list)


@dataclass(frozen=True)
class ResolvedVolumeInput:
    """Resolved volume identifiers."""

    volumes: list[VolumeItem]


class VolumeRequest:
    """Request that resolves VolumeInput to VolumeItem via DB."""

    _result: ResolvedVolumeInput | None = None

    def __init__(
        self, *, inputs: VolumeInput, db: Database | None = None
    ) -> None:
        """Initialize the resolver with database and sub-resolvers."""
        self._inputs = inputs
        self._db = db if db is not None else Database()
        self._volume_resolver = VolumeResolver(VolumeRepository(self._db))

    @property
    def result(self) -> ResolvedVolumeInput | None:
        return self._result

    def resolve(self) -> ResolvedVolumeInput:
        """
        Resolve identifiers to VolumeItem records from DB.

        Returns:
            ResolvedVolumeInput with resolved volume records.

        Raises:
            VolumeNotFoundError: If any identifier cannot be resolved.

        """
        identifiers = self._inputs.id + self._inputs.name

        if not identifiers:
            raise VolumeNotFoundError("No volume identifiers provided")

        result = self._volume_resolver.resolve_many(identifiers)

        if result.errors and not result.items:
            raise VolumeNotFoundError(
                f"Could not resolve any volumes: {', '.join(result.errors)}"
            )

        self._result = ResolvedVolumeInput(volumes=result.items)

        self.ensure_validate()

        return self._result

    def ensure_validate(self) -> None:
        """Validate resolved volume inputs."""
        if self._result is None:
            raise VolumeNotFoundError(
                "Failed to resolve necessary dependencies to validate"
            )

        if not self._result.volumes:
            raise VolumeNotFoundError("No volumes found matching identifiers")
