"""SSH key data models."""

from __future__ import annotations

from dataclasses import dataclass

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

    def __post_init__(self) -> None:
        """Coerce bool fields loaded from SQLite."""
        CommonUtils.coerce_bool_fields(self, {"is_default", "is_present"})
