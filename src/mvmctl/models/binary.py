"""Binary data models."""

from __future__ import annotations

from dataclasses import dataclass


@dataclass
class BinaryItem:
    """Binary record — maps to binaries table."""

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
