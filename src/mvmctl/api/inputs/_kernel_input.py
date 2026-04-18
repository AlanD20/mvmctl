"""Kernel input models for API boundary."""

from __future__ import annotations

from dataclasses import dataclass
from pathlib import Path


@dataclass
class KernelFetchInput:
    """Input model for kernel fetch and build operations."""

    kernel_type: str
    version: str | None
    arch: str
    output_dir: Path
    output_name: str | None = None
    output_path: Path | None = None
    jobs: int | None = None
    keep_build_dir: bool = False
    clean_build: bool = False
    kernel_config: Path | None = None
