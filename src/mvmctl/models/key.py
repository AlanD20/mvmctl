"""SSH key data models."""

from __future__ import annotations

from dataclasses import dataclass
from pathlib import Path


@dataclass
class KeyCreateInput:
    """Input model for SSH key creation."""

    name: str
    output_dir: Path | None = None
    comment: str | None = None
    overwrite: bool = False
