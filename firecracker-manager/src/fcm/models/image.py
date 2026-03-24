"""Image data models."""

from dataclasses import dataclass, field
from pathlib import Path


@dataclass
class ImageSpec:
    """Image specification for download and conversion."""

    id: str
    name: str
    source: str
    format: str  # noqa: N816
    convert_to: str
    size_mib: int
    sha256: str | None = None


@dataclass
class ImageImportSpec:
    """Specification for importing a local image file."""

    id: str
    name: str
    source_path: Path
    format: str  # noqa: N816  # "qcow2", "raw", "tar-rootfs"
    convert_to: str = "ext4"
    size_mib: int = field(default=2048)
