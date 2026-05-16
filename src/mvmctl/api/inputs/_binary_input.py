"""Binary request resolver for binary operations (get, remove, default, etc.)."""

from __future__ import annotations

from dataclasses import dataclass, field

from mvmctl.core._shared import Database, VersionResolver
from mvmctl.core.binary._repository import BinaryRepository
from mvmctl.core.binary._resolver import BinaryResolver
from mvmctl.exceptions import BinaryError, BinaryNotFoundError
from mvmctl.models import BinaryItem

__all__ = ["BinaryInput", "BinaryRequest", "ResolvedBinaryInput"]


@dataclass
class BinaryInput:
    """Raw identifiers for binary operations.

    Each entry in *identifiers* is resolved by ID prefix first, then by
    name.  When *version* is set, each identifier is treated as a name
    and resolved together with that version.
    """

    identifiers: list[str] = field(default_factory=list)
    version: str | None = None


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
        """
        Resolve identifiers to BinaryItem list.

        Each identifier is processed through ``VersionResolver.parse_selector``
        to support both plain names/IDs and ``name:version`` inline format:

        * ``BinaryInput(identifiers=["firecracker:1.15.0"])`` — inline format
        * ``BinaryInput(identifiers=["firecracker"], version="1.15.0")`` — separate

        If no inline version is found and *version* is set, each identifier
        is paired with *version* for ``by_name_version()`` resolution.

        Returns:
            ResolvedBinaryInput with the resolved BinaryItem list.

        Raises:
            BinaryNotFoundError: If binary cannot be found.

        """
        candidates: list[str | list[str]] = []

        for ident in self._inputs.identifiers:
            prefix, value = VersionResolver.parse_selector(ident)
            if prefix is not None:
                # name:version inline format
                candidates.append([prefix, value])
            elif self._inputs.version:
                # Pair bare name with the shared version
                candidates.append([ident, self._inputs.version])
            else:
                # Resolve by ID prefix or name
                candidates.append(ident)

        self.binaries = self._resolver.resolve_many(candidates).items

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
