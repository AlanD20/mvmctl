from __future__ import annotations

from dataclasses import dataclass
from typing import TYPE_CHECKING, Any

if TYPE_CHECKING:
    from mvmctl.db.models import Binary as DBBinary


@dataclass
class BinaryItem:
    id: str
    name: str
    version: str
    path: str
    full_version: str | None = None
    ci_version: str | None = None
    is_default: bool = False
    created_at: str | None = None
    updated_at: str | None = None

    @classmethod
    def from_db(cls, record: "DBBinary") -> "BinaryItem":
        return cls(
            id=record.id,
            name=record.name,
            version=record.version,
            path=record.path,
            full_version=record.full_version,
            ci_version=record.ci_version,
            is_default=record.is_default,
            created_at=record.created_at,
            updated_at=record.updated_at,
        )

    def to_dict(self) -> dict[str, Any]:
        return {
            "binary_id": self.id,
            "binary_name": self.name,
            "package_version": self.version,
            "binary_path": self.path,
            "full_version": self.full_version,
            "ci_version": self.ci_version,
            "created_at": self.created_at,
            "updated_at": self.updated_at,
        }
