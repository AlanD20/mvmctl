"""Kernel data models."""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import TYPE_CHECKING, Any

if TYPE_CHECKING:
    from mvmctl.db.models import Kernel as DBKernel


@dataclass
class KernelRecord:
    id: str
    name: str
    version: str
    arch: str
    path: str
    base_name: str | None = None
    type: str | None = None
    is_default: bool = False
    created_at: str | None = None
    updated_at: str | None = None

    @classmethod
    def from_db(cls, record: "DBKernel") -> "KernelRecord":
        return cls(
            id=record.id,
            name=record.name,
            version=record.version,
            arch=record.arch,
            path=record.path,
            base_name=record.base_name,
            type=record.type,
            is_default=record.is_default,
            created_at=record.created_at,
            updated_at=record.updated_at,
        )

    def to_dict(self) -> dict[str, Any]:
        return {
            "name": self.name,
            "filename": self.path,
            "version": self.version,
            "arch": self.arch,
            "base_name": self.base_name,
            "type": self.type,
            "is_default": 1 if self.is_default else 0,
            "created_at": self.created_at,
            "last_modified": self.updated_at,
        }


@dataclass
class KernelSpec:
    """Specification for building or fetching a kernel, loaded from kernels.yaml."""

    name: str
    kernel_type: str
    version: str
    source: str
    output_name: str
    build_dir: str
    list_url_template: str | None = None
    config_url_template: str | None = None
    sha256: str | None = None
    sha256_url: str | None = None
    config_fragments: list[str] = field(default_factory=list)
    parallel_jobs: int | None = None
    enabled_configs: list[str] = field(default_factory=list)
    disabled_configs: list[str] = field(default_factory=list)
    set_val_configs: list[tuple[str, str]] = field(default_factory=list)
    required_settings: list[str] = field(default_factory=list)
