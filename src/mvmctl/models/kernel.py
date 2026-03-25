"""Kernel data models."""

from dataclasses import dataclass, field


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
