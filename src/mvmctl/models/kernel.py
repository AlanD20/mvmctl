"""Kernel data models."""

from __future__ import annotations

from dataclasses import dataclass, field
from pathlib import Path

from mvmctl.utils.common import CommonUtils


@dataclass
class KernelItem:
    """
    Kernel record — maps to kernels table.

    The ``path`` field stores a *relative* filename (e.g.
    ``"vmlinux-firecracker-6.1.155-x86_64"``).  Use :attr:`resolved_path`
    when you need the absolute filesystem location.
    """

    id: str
    name: str
    base_name: str
    version: str
    arch: str
    type: str
    path: str
    is_default: bool
    is_present: bool
    created_at: str
    updated_at: str
    deleted_at: str | None = None

    def __post_init__(self) -> None:
        """Coerce bool fields loaded from SQLite."""
        CommonUtils.coerce_bool_fields(self, {"is_default", "is_present"})

    @property
    def resolved_path(self) -> Path:
        """Absolute filesystem path for this kernel.

        If ``self.path`` is already absolute, returns it directly
        (supporting kernels fetched to custom output directories).
        Otherwise, resolves the relative path against the default
        kernels cache directory for backward compatibility with
        existing database records.
        """
        path = Path(self.path)
        if path.is_absolute():
            return path
        from mvmctl.utils.common import CacheUtils

        return CacheUtils.get_kernels_dir() / self.path


@dataclass
class KernelPullResult:
    """
    Unified result from kernel pull/build operations.

    This dataclass provides a consistent return type for both Firecracker
    download and official kernel build paths, eliminating the need for
    the caller to parse filenames or handle nested result structures.
    """

    path: Path
    version: str
    arch: str
    kernel_type: str
    warnings: list[str] = field(default_factory=list)
    info_messages: list[str] = field(default_factory=list)

    @property
    def name(self) -> str:
        return self.path.name

    def exists(self) -> bool:
        return self.path.exists()


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
