"""Image data models."""

from __future__ import annotations

import platform
from dataclasses import dataclass, field
from pathlib import Path
from typing import TYPE_CHECKING, Any

if TYPE_CHECKING:
    from mvmctl.db.models import Image as DBImage


@dataclass
class ImageItem:
    id: str
    os_slug: str
    path: str
    os_name: str | None
    fs_type: str | None
    fs_uuid: str | None
    compressed_size: int | None
    original_size: int | None
    compression_ratio: float | None
    compressed_format: str | None
    pulled_at: str | None
    arch: str | None = None
    is_default: bool = False
    created_at: str | None = None
    updated_at: str | None = None

    @classmethod
    def from_db(cls, record: "DBImage") -> "ImageItem":
        return cls(
            id=record.id,
            os_slug=record.os_slug,
            path=record.path,
            os_name=record.os_name,
            fs_type=record.fs_type,
            fs_uuid=record.fs_uuid,
            compressed_size=record.compressed_size,
            original_size=record.original_size,
            compression_ratio=record.compression_ratio,
            compressed_format=record.compressed_format,
            pulled_at=record.pulled_at,
            arch=record.arch,
            is_default=record.is_default,
            created_at=record.created_at,
            updated_at=record.updated_at,
        )

    def to_dict(self) -> dict[str, Any]:
        return {
            "os_slug": self.os_slug,
            "path": self.path,
            "os_name": self.os_name,
            "arch": self.arch,
            "fs_type": self.fs_type,
            "fs_uuid": self.fs_uuid,
            "compressed_size": self.compressed_size,
            "original_size": self.original_size,
            "compression_ratio": self.compression_ratio,
            "compressed_format": self.compressed_format,
            "pulled_at": self.pulled_at,
            "is_default": 1 if self.is_default else 0,
            "created_at": self.created_at,
            "updated_at": self.updated_at,
        }


@dataclass
class ImageSpec:
    id: str
    image_type: str
    version: str
    name: str
    source: str
    format: str  # noqa: N816
    convert_to: str
    minimum_rootfs_size: int
    arch: str = field(default_factory=platform.machine)
    sha256: str | None = None
    sha256_url: str | None = None
    list_url_template: str | None = None
    source_base: str | None = None


@dataclass
class ImageImportInput:
    """Specification for importing a local image file."""

    id: str
    name: str
    source_path: Path
    output_dir: Path
    format: str  # noqa: N816  # "qcow2", "raw", "tar-rootfs"
    convert_to: str = "ext4"
    minimum_rootfs_size: int = field(default=2048)
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
