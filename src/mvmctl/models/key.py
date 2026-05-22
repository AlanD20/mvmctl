"""SSH key data models."""

from __future__ import annotations

from dataclasses import dataclass
from typing import Any

from mvmctl.utils.common import CommonUtils


@dataclass
class SSHKeyItem:
    """SSH key record — maps to ssh_keys table."""

    id: str
    name: str
    fingerprint: str
    algorithm: str
    comment: str
    public_key_path: str
    is_default: bool
    is_present: bool
    created_at: str
    updated_at: str

    private_key_path: str | None = None

    def to_dict(self) -> dict[str, Any]:
        """Convert SSHKeyItem to a dictionary for JSON output."""
        return {
            "id": self.id,
            "name": self.name,
            "fingerprint": self.fingerprint,
            "algorithm": self.algorithm,
            "comment": self.comment,
            "public_key_path": self.public_key_path,
            "private_key_path": self.private_key_path,
            "is_default": self.is_default,
            "is_present": self.is_present,
            "created_at": self.created_at,
            "updated_at": self.updated_at,
        }

    def __post_init__(self) -> None:
        """Coerce bool fields loaded from SQLite."""
        CommonUtils.coerce_bool_fields(self, {"is_default", "is_present"})
