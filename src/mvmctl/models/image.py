"""Image data models."""

from __future__ import annotations

import platform
from dataclasses import dataclass, field
from typing import TYPE_CHECKING

from mvmctl.utils.common import CommonUtils

if TYPE_CHECKING:
    from mvmctl.models.vm import VMInstanceItem


@dataclass
class ImageItem:
    """Image record — maps to images table."""

    id: str
    type: str
    name: str
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

    distro: str | None = None
    fs_uuid: str | None = None
    compressed_size: int | None = None
    compression_ratio: float | None = None
    compressed_format: str | None = None
    deleted_at: str | None = None
    vms: list[VMInstanceItem] | None = None

    def __post_init__(self) -> None:
        """Coerce bool fields loaded from SQLite."""
        CommonUtils.coerce_bool_fields(self, {"is_default", "is_present"})


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


@dataclass
class ImageVersion:
    """A published version of an image type from an upstream provider.

    NOT frozen — mutable for convenience. Returned by version resolvers
    to describe an available download for a given image type.
    """

    version: str
    codename: str | None
    type: str
    download_url: str
    sha256_url: str | None
    format: str
    display_name: str = ""
    type_name: str = ""
