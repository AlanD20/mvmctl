"""Kernel fetch resolver — resolves and validates kernel fetch/build inputs."""

from __future__ import annotations

import os
from dataclasses import dataclass
from pathlib import Path

from mvmctl.constants import (
    DEFAULT_IMAGE_ARCH,
    DEFAULT_KERNEL_BUILD_JOBS,
    DEFAULT_KERNEL_VERSION,
)
from mvmctl.core._shared import Database
from mvmctl.exceptions import KernelError
from mvmctl.utils.common import CacheUtils

__all__ = [
    "KernelFetchInput",
    "KernelFetchRequest",
    "ResolvedKernelFetchRequest",
]


@dataclass
class KernelFetchInput:
    """Input model for kernel fetch and build operations.

    Optional fields are None when not provided by the user.
    DB-backed defaults are resolved by KernelFetchRequest.
    """

    kernel_type: str
    version: str | None = None
    arch: str | None = None
    output_dir: Path | None = None
    output_name: str | None = None
    output_path: Path | None = None
    jobs: int | None = None
    keep_build_dir: bool = False
    clean_build: bool = False
    kernel_config: Path | None = None
    set_default: bool = False


@dataclass(frozen=True)
class ResolvedKernelFetchRequest:
    """Immutable resolved inputs for kernel fetch/build — all values explicit.

    Output of KernelFetchRequest.resolve(). No None values for required fields.
    """

    kernel_type: str
    arch: str
    output_dir: Path
    jobs: int
    keep_build_dir: bool
    clean_build: bool
    set_default: bool
    kernel_config: Path | None
    version: str | None = None


class KernelFetchRequest:
    """Resolve and validate kernel fetch/build inputs.

    Takes KernelFetchInput and resolves DB-backed defaults,
    computes output paths, and produces a ResolvedKernelFetchRequest
    suitable for kernel fetch/build operations.
    """

    _result: ResolvedKernelFetchRequest | None = None

    def __init__(
        self, *, inputs: KernelFetchInput, db: Database | None = None
    ) -> None:
        """Initialize the resolver with database and sub-resolvers."""
        self._inputs = inputs
        self._db = db if db is not None else Database()

    @property
    def result(self) -> ResolvedKernelFetchRequest | None:
        return self._result

    def resolve(self) -> ResolvedKernelFetchRequest:
        """Resolve all inputs to explicit values.

        This method resolves DB-backed defaults and computes derived values
        (arch, output_path, jobs). It does NOT validate —
        validation happens in ensure_validate().
        """
        version = self._inputs.version or DEFAULT_KERNEL_VERSION

        if self._inputs.kernel_type == "firecracker":
            version = None
        elif version:
            version = version.removeprefix("v")

        self._result = ResolvedKernelFetchRequest(
            kernel_type=self._inputs.kernel_type,
            version=version,
            arch=self._inputs.arch or DEFAULT_IMAGE_ARCH,
            output_dir=self._inputs.output_dir or CacheUtils.get_kernels_dir(),
            jobs=self._inputs.jobs
            or os.cpu_count()
            or DEFAULT_KERNEL_BUILD_JOBS,
            keep_build_dir=self._inputs.keep_build_dir,
            clean_build=self._inputs.clean_build,
            kernel_config=self._inputs.kernel_config,
            set_default=self._inputs.set_default,
        )

        # Validate
        self.ensure_validate()

        return self._result

    def ensure_validate(self) -> None:
        """Validate resolved kernel fetch inputs."""
        import re

        if self._result is None:
            raise KernelError(
                "Failed to resolve necessary dependencies to validate"
            )

        # 1. Validate kernel type
        valid_types = ("firecracker", "official")
        if self._result.kernel_type not in valid_types:
            raise KernelError(
                f"Unsupported kernel type: {self._result.kernel_type}. "
                f"Valid types: {', '.join(valid_types)}"
            )

        # 2. Validate version (semver-like: 5.10, 6.1.0, v6.1)
        version = self._result.version
        if version:
            stripped = version.removeprefix("v")
            if not re.fullmatch(r"\d+(\.\d+)*", stripped):
                raise KernelError(
                    f"Invalid kernel version: '{version}'. "
                    f"Expected format like '5.10', '6.1.0', or 'v6.1'"
                )

        # 3. Validate architecture
        valid_archs = ("x86_64", "amd64", "arm64", "aarch64")
        if self._result.arch not in valid_archs:
            raise KernelError(
                f"Unsupported architecture: {self._result.arch}. "
                f"Valid architectures: {', '.join(valid_archs)}"
            )

        # 4. Validate output directory (must exist or be creatable)
        output_dir = self._result.output_dir
        if output_dir.exists() and not output_dir.is_dir():
            raise KernelError(
                f"Output path exists but is not a directory: {output_dir}"
            )

        # 5. Validate build jobs (positive integer)
        jobs = self._result.jobs
        if jobs <= 0:
            raise KernelError(
                f"Invalid build jobs: {jobs}. Must be a positive integer."
            )
