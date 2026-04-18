"""Image input models for API boundary."""

from __future__ import annotations

from dataclasses import dataclass, field
from pathlib import Path

from mvmctl.models.image import ImageSpec


@dataclass
class ImageImportInput:
    """Specification for importing a local image file."""

    id: str
    name: str
    source_path: Path
    output_dir: Path
    format: str  # noqa: N816  # "qcow2", "raw", "tar-rootfs"
    convert_to: str = "ext4"
    disabled_detectors: list[str] = field(default_factory=list)
    force: bool = False
    partition: int | None = None


@dataclass
class ImageFetchInput:
    """Input model for image fetch and registration operations."""

    spec: ImageSpec
    output_dir: Path
    force: bool = False
    partition: int | None = None
    skip_optimization: bool = False
