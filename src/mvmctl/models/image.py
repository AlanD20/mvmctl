"""Image data models."""

from __future__ import annotations

import platform
from dataclasses import dataclass, field


@dataclass
class ImageItem:
    """Image record — maps to images table."""

    id: str
    os_slug: str
    os_name: str
    arch: str
    path: str
    fs_type: str
    minimum_rootfs_size_mib: int
    original_size: int
    is_default: bool
    pulled_at: str
    created_at: str
    updated_at: str

    fs_uuid: str | None = None
    compressed_size: int | None = None
    compression_ratio: float | None = None
    compressed_format: str | None = None


@dataclass
class ImageSpec:
    id: str
    image_type: str
    version: str
    name: str
    source: str
    format: str  # noqa: N816
    convert_to: str
    arch: str = field(default_factory=platform.machine)
    sha256: str | None = None
    sha256_url: str | None = None
    list_url_template: str | None = None
    source_base: str | None = None
