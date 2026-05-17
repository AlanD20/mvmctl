"""Kernel data models."""

from __future__ import annotations

from dataclasses import dataclass, field
from pathlib import Path
from typing import TYPE_CHECKING

from mvmctl.utils.common import CommonUtils

if TYPE_CHECKING:
    from mvmctl.models.vm import VMInstanceItem


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

    vms: list[VMInstanceItem] | None = None

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
class KernelFeature:
    """A named feature group of kernel config options, loaded from kernels.yaml."""

    desc: str
    configs: list[str]
    requires: list[str] = field(default_factory=list)


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
    resolver: str | None = None
    versions_url: str | None = None
    options: dict[str, object] | None = None
    file_pattern: str | None = None
    file_suffix: str | None = None
    features: dict[str, KernelFeature] = field(default_factory=dict)

    def with_enabled_features(self, feature_names: list[str]) -> KernelSpec:
        """Return a shallow copy with feature configs merged into enabled_configs/required_settings.

        Unknown feature names are silently skipped.  Duplicates are removed
        while preserving the original insertion order.
        """
        extra_configs: list[str] = []
        extra_requires: list[str] = []
        for name in feature_names:
            feature = self.features.get(name)
            if feature is None:
                continue
            extra_configs.extend(feature.configs)
            extra_requires.extend(feature.requires)

        seen_configs = set(self.enabled_configs)
        merged_configs = list(self.enabled_configs)
        for c in extra_configs:
            if c not in seen_configs:
                seen_configs.add(c)
                merged_configs.append(c)

        seen_requires = set(self.required_settings)
        merged_requires = list(self.required_settings)
        for r in extra_requires:
            if r not in seen_requires:
                seen_requires.add(r)
                merged_requires.append(r)

        return KernelSpec(
            name=self.name,
            kernel_type=self.kernel_type,
            version=self.version,
            source=self.source,
            output_name=self.output_name,
            build_dir=self.build_dir,
            list_url_template=self.list_url_template,
            config_url_template=self.config_url_template,
            sha256=self.sha256,
            sha256_url=self.sha256_url,
            config_fragments=self.config_fragments,
            parallel_jobs=self.parallel_jobs,
            enabled_configs=merged_configs,
            disabled_configs=self.disabled_configs,
            set_val_configs=self.set_val_configs,
            required_settings=merged_requires,
            resolver=self.resolver,
            versions_url=self.versions_url,
            options=self.options,
            file_pattern=self.file_pattern,
            file_suffix=self.file_suffix,
            features=self.features,
        )
