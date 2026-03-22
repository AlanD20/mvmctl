"""Image data models."""

from dataclasses import dataclass


@dataclass
class ImageSpec:
    """Image specification for download and conversion."""

    id: str
    name: str
    source: str
    format: str
    convert_to: str
    size_mib: int
    sha256: str | None = None
