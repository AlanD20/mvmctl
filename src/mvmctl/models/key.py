"""SSH key data models."""

from __future__ import annotations

from dataclasses import dataclass


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
    created_at: str
    updated_at: str

    private_key_path: str | None = None
