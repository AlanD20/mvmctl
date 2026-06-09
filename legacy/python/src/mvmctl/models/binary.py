"""Binary data models."""

from __future__ import annotations

from dataclasses import dataclass
from typing import TYPE_CHECKING

from mvmctl.utils.common import CommonUtils

if TYPE_CHECKING:
    from mvmctl.models.vm import VMInstanceItem


@dataclass
class BinaryItem:
    """
    Binary record — maps to binaries table.

    The ``path`` field stores the absolute filesystem path to the binary
    (e.g. ``"/home/user/.cache/mvmctl/bin/firecracker-v1.15.1"``).
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
