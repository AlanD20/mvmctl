"""Binary fetch resolver for download operations."""

from __future__ import annotations

import re
from dataclasses import dataclass
from pathlib import Path

from mvmctl.core._shared import Database
from mvmctl.exceptions import BinaryError
from mvmctl.utils.common import CacheUtils

__all__ = [
    "BinaryFetchInput",
    "BinaryFetchRequest",
    "ResolvedBinaryFetchInput",
]


@dataclass
class BinaryFetchInput:
    """Raw input for binary fetch operation."""

    version: str
    set_as_default: bool = False
    download_override: bool = True


@dataclass(frozen=True)
class ResolvedBinaryFetchInput:
    """Immutable resolved binary fetch request."""

    version: str
    set_as_default: bool
    bin_dir: Path
    download_override: bool


@dataclass
class BinaryFetchRequest:
    """Resolve binary fetch inputs."""

    _result: ResolvedBinaryFetchInput | None = None

    def __init__(
        self, *, inputs: BinaryFetchInput, db: Database | None = None
    ) -> None:
        self._inputs = inputs
        self._db = db if db is not None else Database()

    @property
    def result(self) -> ResolvedBinaryFetchInput | None:
        return self._result

    def resolve(self) -> ResolvedBinaryFetchInput:
        """Resolve and validate fetch inputs.

        Returns:
            ResolvedBinaryFetchInput with resolved values.

        Raises:
            BinaryError: If version format is invalid.
        """
        # Normalize version (strip 'v' prefix)
        version = self._inputs.version.removeprefix("v")

        # Validate version format (semver-like: x.y.z)
        if not re.match(r"^\d+\.\d+(\.\d+)?$", version):
            raise BinaryError(
                f"Invalid version format: '{self._inputs.version}'. "
                "Expected format: x.y.z (e.g., 1.15.0)"
            )

        # Resolve bin_dir
        bin_dir = CacheUtils.get_bin_dir()

        self._result = ResolvedBinaryFetchInput(
            version=version,
            set_as_default=self._inputs.set_as_default,
            bin_dir=bin_dir,
            download_override=self._inputs.download_override,
        )

        # Validate
        self.ensure_validate()

        return self._result

    def ensure_validate(self) -> None:
        """Validate version is valid semver-like string."""
        if self._result is None:
            raise BinaryError("No resolved fetch input to validate")

        if not self._result.version:
            raise BinaryError("Version cannot be empty")
