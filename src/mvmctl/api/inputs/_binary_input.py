"""Binary request resolver for binary operations (get, remove, set-default, etc.)."""

from __future__ import annotations

from dataclasses import dataclass, field

from mvmctl.core._shared import Database
from mvmctl.core.binary._repository import BinaryRepository
from mvmctl.core.binary._resolver import BinaryResolver
from mvmctl.exceptions import BinaryError, BinaryNotFoundError
from mvmctl.models.binary import BinaryItem

__all__ = ["BinaryInput", "BinaryRequest", "ResolvedBinaryInput"]


@dataclass
class BinaryInput:
    """Raw identifiers for binary operations."""

    id: list[str] = field(default_factory=list)
    names: list[str] = field(default_factory=list)
    version: str = field(default_factory=str)


@dataclass(frozen=True)
class ResolvedBinaryInput:
    """Immutable resolved binary request."""

    binaries: list[BinaryItem]


@dataclass
class BinaryRequest:
    """Resolve binary identifiers to DB records."""

    _result: ResolvedBinaryInput | None = None

    def __init__(
        self, *, inputs: BinaryInput, db: Database | None = None
    ) -> None:
        self._inputs = inputs
        self._db = db if db is not None else Database()
        self._resolver = BinaryResolver(BinaryRepository(self._db))

    @property
    def result(self) -> ResolvedBinaryInput | None:
        return self._result

    def resolve(self) -> ResolvedBinaryInput:
        """Resolve identifiers to BinaryItem list.

        - If id provided: use BinaryResolver.by_id() for each
        - If name+version provided: use BinaryResolver.by_name_version() for each pair
        - If only name provided: resolve default via BinaryResolver.get_default()

        Returns:
            ResolvedBinaryInput with the resolved BinaryItem list.

        Raises:
            BinaryNotFoundError: If binary cannot be found.
        """
        identifiers: list[str | list[str]] = self._inputs.id + [[]]

        # Resolve by name+version
        if self._inputs.names and self._inputs.version:
            for bin in self._inputs.names:
                identifiers.append([bin, self._inputs.version])

        self.binaries = self._resolver.resolve_many(identifiers).items

        if not self.binaries:
            raise BinaryNotFoundError(
                "No binary identifiers provided or could be resolved"
            )

        self._result = ResolvedBinaryInput(binaries=self.binaries)

        # Validate
        self.ensure_validate()

        return self._result

    def ensure_validate(self) -> None:
        """Validate resolved inputs."""
        if self._result is None:
            raise BinaryError("No resolved binaries to validate")

        for binary in self._result.binaries:
            if not binary.id:
                raise BinaryError(f"Binary '{binary.name}' has no ID")
            if not binary.path:
                raise BinaryError(f"Binary '{binary.name}' has no path")
