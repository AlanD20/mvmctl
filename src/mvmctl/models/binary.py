"""Binary data models."""

from __future__ import annotations

from dataclasses import dataclass
from pathlib import Path
from typing import TYPE_CHECKING

from mvmctl.utils.common import CommonUtils

if TYPE_CHECKING:
    from mvmctl.models.vm import VMInstanceItem


@dataclass
class BinaryItem:
    """
    Binary record — maps to binaries table.

    The ``path`` field stores a *relative* filename (e.g.
    ``"firecracker-v1.15.1"``).  Use :attr:`resolved_path` when you need
    the absolute filesystem location.
    """

    id: str
    name: str
    version: str
    full_version: str
    ci_version: str | None
    path: str
    is_default: bool
    is_present: bool
    created_at: str
    updated_at: str
    deleted_at: str | None = None

    vms: list[VMInstanceItem] | None = None

    def __post_init__(self) -> None:
        """Coerce bool fields loaded from SQLite."""
        CommonUtils.coerce_bool_fields(self, {"is_default", "is_present"})

    @property
    def resolved_path(self) -> Path:
        """Absolute path resolved against the binaries cache directory."""
        from mvmctl.utils.common import CacheUtils

        return CacheUtils.get_bin_dir() / self.path
