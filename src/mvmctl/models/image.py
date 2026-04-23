"""Image data models."""

from __future__ import annotations

import platform
from dataclasses import dataclass, field
from typing import TYPE_CHECKING

if TYPE_CHECKING:
    from mvmctl.models.vm import VMInstanceItem


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
    is_present: bool
    pulled_at: str
    created_at: str
    updated_at: str

    fs_uuid: str | None = None
    compressed_size: int | None = None
    compression_ratio: float | None = None
    compressed_format: str | None = None
    deleted_at: str | None = None
    vms: list[VMInstanceItem] | None = None


@dataclass
class ImageSpec:
    id: str
    image_type: str
    version: str
    name: str
    source: str
    format: str  # noqa: N816
    arch: str = field(default_factory=platform.machine)
    sha256: str | None = None
    sha256_url: str | None = None
    list_url_template: str | None = None
    size: int | None = None
