"""Kernel import input model for API boundary."""

from __future__ import annotations

import platform
from dataclasses import dataclass
from pathlib import Path

from mvmctl.core._shared import Database
from mvmctl.core.config._service import SettingsService
from mvmctl.exceptions import KernelError

__all__ = [
    "KernelImportInput",
    "KernelImportRequest",
    "ResolvedKernelImportInput",
]

FIRECRACKER_SUPPORTED_ARCH = ["x86_64", "amd64", "aarch64", "arm64"]


@dataclass
class KernelImportInput:
    """Specification for importing a local kernel file.

    Attributes:
        name: User-assigned name for this kernel entry.
        path: Path to the vmlinux file on disk.
        version: Override auto-detected version. If None, detected from filename.
        arch: Override auto-detected architecture. If None, resolved from settings
            or platform default.
        set_default: Whether to set as the default kernel after import.

    """

    name: str
    path: Path
    version: str | None = None
    arch: str | None = None
    set_default: bool = False


@dataclass(frozen=True)
class ResolvedKernelImportInput:
    """Fully resolved kernel import input with all values concretized.

    All optional fields from ``KernelImportInput`` are resolved to concrete
    values. The ``path`` is validated to exist and be non-empty.
    """

    name: str
    path: Path
    version: str
    arch: str
    set_default: bool = False


class KernelImportRequest:
    """Resolve and validate kernel import inputs.

    Follows the standard Request pattern: constructs a ``ResolvedKernelImportInput``
    from raw user input by resolving defaults, detecting values from the filename,
    and validating constraints.
    """

    _result: ResolvedKernelImportInput | None = None

    def __init__(
        self,
        *,
        inputs: KernelImportInput,
        db: Database | None = None,
    ) -> None:
        """Initialize the request with raw inputs and optional database.

        Args:
            inputs: Raw user-provided ``KernelImportInput``.
            db: Database instance for resolving settings overrides.
                Creates a fresh ``Database()`` if not provided.
        """
        self._inputs = inputs
        self._db = db if db is not None else Database()

    @property
    def result(self) -> ResolvedKernelImportInput | None:
        """The resolved input, or ``None`` if ``resolve()`` has not been called."""
        return self._result

    def resolve(self) -> ResolvedKernelImportInput:
        """Resolve all input fields to concrete values and validate.

        Resolution order:
        1. **Path** — expanded and resolved to an absolute path.
        2. **Arch** — user-provided ``arch`` → ``SettingsService`` default
           (``defaults.kernel.arch``) → ``platform.machine()``.
        3. **Version** — user-provided ``version`` → auto-detected from filename
           via ``KernelService.parse_filename()`` → ``"unknown"``.

        All validation (path exists, arch, name, etc.) is delegated to
        ``ensure_validate()`` after the resolved input is constructed.

        Returns:
            ``ResolvedKernelImportInput`` with all fields concretized.

        Raises:
            KernelError: If validation fails.
        """
        source_path = self._inputs.path.expanduser().resolve()

        from mvmctl.core.kernel._service import KernelService

        parsed = KernelService.parse_filename(source_path.name)

        if self._inputs.arch is not None:
            arch = self._inputs.arch
        elif parsed.arch != "-":
            arch = parsed.arch
        else:
            arch = str(
                SettingsService.resolve(self._db, "defaults.kernel", "arch")
            )
            if not arch:
                arch = platform.machine()

        if self._inputs.version is not None:
            version = self._inputs.version
        else:
            version = parsed.version if parsed.version != "-" else "unknown"

        self._result = ResolvedKernelImportInput(
            name=self._inputs.name,
            path=source_path,
            version=version,
            arch=arch,
            set_default=self._inputs.set_default,
        )

        self.ensure_validate()
        return self._result

    def ensure_validate(self) -> None:
        """Validate the resolved input against all business rules.

        Checks:
        - ``path`` exists and is non-empty.
        - ``arch`` is a supported Firecracker architecture.
        - ``name`` is non-empty.

        Raises:
            KernelError: If any validation check fails.
        """
        if self._result is None:
            raise KernelError(
                "Failed to resolve necessary dependencies to validate"
            )

        if not self._result.path.exists():
            raise KernelError(f"Kernel file not found: {self._result.path}")
        if self._result.path.stat().st_size == 0:
            raise KernelError(f"Kernel file is empty: {self._result.path}")
        if self._result.arch not in FIRECRACKER_SUPPORTED_ARCH:
            raise KernelError(
                f"Unknown arch: {self._result.arch}. "
                f"Valid: {', '.join(FIRECRACKER_SUPPORTED_ARCH)}"
            )
        if not self._result.name:
            raise KernelError("Kernel name cannot be empty")
