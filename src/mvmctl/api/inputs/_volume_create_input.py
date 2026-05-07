"""Volume creation input models for API boundary."""

from __future__ import annotations

from dataclasses import dataclass
from pathlib import Path

from mvmctl.core._shared import Database
from mvmctl.exceptions import VolumeCreateError
from mvmctl.utils._disk import parse_disk_size
from mvmctl.utils._validators import VMValidator
from mvmctl.utils.common import CacheUtils

__all__ = [
    "VolumeCreateInput",
    "VolumeCreateRequest",
    "ResolvedVolumeCreateInput",
]


@dataclass
class VolumeCreateInput:
    """Specification for creating a new volume."""

    name: str
    size: str
    format: str | None = None  # 'raw' or 'qcow2', default resolved in request


@dataclass(frozen=True)
class ResolvedVolumeCreateInput:
    """Resolved input model for volume creation."""

    name: str
    size_bytes: int
    format: str
    path: Path


class VolumeCreateRequest:
    """Resolve volume creation inputs to explicit values."""

    _result: ResolvedVolumeCreateInput | None = None

    def __init__(
        self, *, inputs: VolumeCreateInput, db: Database | None = None
    ) -> None:
        """Initialize the resolver with database."""
        self._inputs = inputs
        self._db = db if db is not None else Database()

    @property
    def result(self) -> ResolvedVolumeCreateInput | None:
        return self._result

    def resolve(self) -> ResolvedVolumeCreateInput:
        """Resolve creation inputs to explicit values."""
        VMValidator.validate_name(self._inputs.name)
        size_bytes = parse_disk_size(self._inputs.size)
        fmt = self._inputs.format if self._inputs.format is not None else "raw"
        if fmt not in ("raw", "qcow2"):
            raise VolumeCreateError(
                f"Unsupported format: {fmt}. Use 'raw' or 'qcow2'."
            )
        path = CacheUtils.get_volumes_dir() / f"{self._inputs.name}.{fmt}"
        self._result = ResolvedVolumeCreateInput(
            name=self._inputs.name,
            size_bytes=size_bytes,
            format=fmt,
            path=path,
        )
        return self._result

    def ensure_validate(self) -> None:
        """Validate resolved creation inputs."""
        if self._result is None:
            raise VolumeCreateError(
                "Failed to resolve necessary dependencies to validate"
            )
