"""Binary pull resolver for download operations."""

from __future__ import annotations

import re
from dataclasses import dataclass
from pathlib import Path

from mvmctl.core._shared import Database
from mvmctl.exceptions import BinaryError
from mvmctl.utils.common import CacheUtils

__all__ = [
    "BinaryPullInput",
    "BinaryPullRequest",
    "ResolvedBinaryPullInput",
]


@dataclass
class BinaryPullInput:
    """Raw input for binary pull operation."""

    version: str
    name: str = "firecracker"
    git_ref: str | None = None
    set_default: bool = False
    download_override: bool = True


@dataclass(frozen=True)
class ResolvedBinaryPullInput:
    """Immutable resolved binary pull request."""

    version: str
    name: str
    git_ref: str | None
    set_default: bool
    bin_dir: Path
    download_override: bool


@dataclass
class BinaryPullRequest:
    """Resolve binary pull inputs."""

    _result: ResolvedBinaryPullInput | None = None

    def __init__(
        self, *, inputs: BinaryPullInput, db: Database | None = None
    ) -> None:
        self._inputs = inputs
        self._db = db if db is not None else Database()

    @property
    def result(self) -> ResolvedBinaryPullInput | None:
        return self._result

    def resolve(self) -> ResolvedBinaryPullInput:
        """
        Resolve and validate pull inputs.

        Returns:
            ResolvedBinaryPullInput with resolved values.

        Raises:
            BinaryError: If version format is invalid.

        """
        # Normalize version (strip 'v' prefix)
        version = self._inputs.version.removeprefix("v")

        # When git_ref is provided, skip semver version validation —
        # the version will be determined after building from source.
        if not self._inputs.git_ref:
            # Validate version format (semver-like: x.y.z)
            if not re.match(r"^\d+\.\d+(\.\d+)?$", version):
                raise BinaryError(
                    f"Invalid version format: '{self._inputs.version}'. "
                    "Expected format: x.y.z (e.g., 1.15.0)"
                )

        # Resolve bin_dir
        bin_dir = CacheUtils.get_bin_dir()

        self._result = ResolvedBinaryPullInput(
            version=version,
            name=self._inputs.name,
            git_ref=self._inputs.git_ref,
            set_default=self._inputs.set_default,
            bin_dir=bin_dir,
            download_override=self._inputs.download_override,
        )

        # Validate
        self.ensure_validate()

        return self._result

    def ensure_validate(self) -> None:
        """Validate resolved binary pull inputs."""
        if self._result is None:
            raise BinaryError("No resolved pull input to validate")

        # Validate binary name — only firecracker is supported for pull/build
        if self._result.name.lower() != "firecracker":
            raise BinaryError(
                f"Unsupported binary: '{self._result.name}'. "
                "Only 'firecracker' is supported for download or build."
            )

        # Skip version check for git builds — version is determined after build
        if self._result.git_ref:
            return

        if not self._result.version:
            raise BinaryError("Version cannot be empty")
