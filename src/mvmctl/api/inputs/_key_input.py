"""SSH key input models for API boundary — existing resource actions."""

from __future__ import annotations

from dataclasses import dataclass, field

from mvmctl.core._shared import Database
from mvmctl.core.key._resolver import KeyResolver
from mvmctl.exceptions import KeyNotFoundError
from mvmctl.models import SSHKeyItem

__all__ = [
    "KeyInput",
    "KeyRequest",
    "ResolvedKeyInput",
]


@dataclass
class KeyInput:
    """Input model for identifying existing SSH keys."""

    name: list[str] = field(default_factory=list)
    id: list[str] = field(default_factory=list)


@dataclass(frozen=True)
class ResolvedKeyInput:
    """Immutable resolved key request."""

    keys: list[SSHKeyItem]


class KeyRequest:
    """Resolve key identifiers to DB records."""

    _result: ResolvedKeyInput | None = None

    def __init__(self, *, inputs: KeyInput, db: Database | None = None) -> None:
        self._inputs = inputs
        from mvmctl.core.key._repository import KeyRepository

        self._resolver = KeyResolver(KeyRepository(db))

    @property
    def result(self) -> ResolvedKeyInput | None:
        return self._result

    def resolve(self) -> ResolvedKeyInput:
        identifiers = self._inputs.name + self._inputs.id
        if not identifiers:
            raise KeyNotFoundError("No key identifiers provided")

        result = self._resolver.resolve_many(identifiers)
        if result.errors and not result.items:
            raise KeyNotFoundError(
                f"Could not resolve any keys: {', '.join(result.errors)}"
            )

        self._result = ResolvedKeyInput(keys=result.items)
        return self._result
