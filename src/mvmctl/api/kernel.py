"""Kernel API — unified orchestration for kernel fetch and build operations.

This module provides the API layer for kernel management, implementing
the unified fetch flow that eliminates triple resolve and inconsistent
return types.
"""

from __future__ import annotations

import logging
from datetime import datetime, timezone
from pathlib import Path

from mvmctl.api.metadata import find_kernels_by_id_prefix
from mvmctl.api.vms import get_vm_manager
from mvmctl.constants import (
    KERNEL_TYPE_FIRECRACKER,
    KERNEL_TYPE_OFFICIAL,
    KERNEL_TYPE_UNKNOWN,
)
from mvmctl.core.mvm_db import MVMDatabase
from mvmctl.exceptions import KernelError
from mvmctl.models.kernel import KernelFetchInput, KernelFetchResult, KernelItem, KernelSpec
from mvmctl.utils.fs import get_cache_dir, get_kernels_dir
from mvmctl.utils.full_hash import generate_full_hash_kernel
from mvmctl.utils.id_lookup import resolve_single_by_id_prefix

logger = logging.getLogger(__name__)

__all__ = [
    "fetch_kernel",
    "register_fetched_kernel",
    "resolve_kernel_spec",
    "list_kernels",
    "set_default_kernel",
    "remove_kernel",
    "get_default_kernel_path",
    "save_kernel_metadata",
    "resolve_kernel_path",
    "resolve_kernel_id_path",
    "download_firecracker_kernel",
]


def fetch_kernel(input: KernelFetchInput) -> KernelFetchResult:
    """Fetch or build a kernel.

    This is the unified orchestration function that:
    1. Resolves kernel spec ONCE
    2. Calls appropriate core function (download or build)
    3. Returns unified KernelFetchResult

    Args:
        input: KernelFetchInput containing all fetch parameters:
            - kernel_type: Type of kernel (firecracker, official)
            - version: Version string or None
            - arch: Architecture (x86_64, arm64)
            - output_dir: Directory to store kernel
            - output_name: Optional filename override
            - output_path: Optional explicit output path
            - jobs: Parallel build jobs (official only)
            - keep_build_dir: Keep build directory (official only)
            - clean_build: Skip cache (official only)
            - kernel_config: Custom config path (official only)

    Returns:
        KernelFetchResult with path, version, arch, type, warnings, info

    Raises:
        KernelError: If kernel type is unsupported or fetch fails
    """
    spec = _resolve_kernel_spec(input.kernel_type, input.version)

    if spec.kernel_type == KERNEL_TYPE_FIRECRACKER:
        return _fetch_firecracker_kernel(
            spec, input.arch, input.output_dir, input.output_name, input.output_path
        )
    if spec.kernel_type == KERNEL_TYPE_OFFICIAL:
        return _build_official_kernel(
            spec,
            input.arch,
            input.output_dir,
            input.output_name,
            input.output_path,
            input.jobs,
            input.keep_build_dir,
            input.clean_build,
            input.kernel_config,
        )

    raise KernelError(f"Unsupported kernel type: {spec.kernel_type}")


def _fetch_firecracker_kernel(
    spec: KernelSpec,
    arch: str,
    output_dir: Path,
    output_name: str | None,
    output_path: Path | None,
) -> KernelFetchResult:
    """Internal: Fetch Firecracker kernel.

    Args:
        spec: Resolved kernel specification
        arch: Target architecture
        output_dir: Directory to store kernel
        output_name: Optional filename override
        output_path: Optional explicit output path

    Returns:
        KernelFetchResult with fetch results
    """
    from mvmctl.core.kernel import download_firecracker_kernel as _core_download

    ci_version = _get_ci_version()

    result = _core_download(
        ci_version=ci_version,
        arch=arch,
        kernels_dir=output_dir,
        output_name=output_name,
        output_path=output_path,
        kernel_spec=spec,
    )

    return result


def download_firecracker_kernel(
    ci_version: str,
    arch: str,
    kernels_dir: Path | None = None,
    output_name: str | None = None,
    output_path: Path | None = None,
) -> "KernelFetchResult":
    """Download a Firecracker kernel from CI.

    Thin wrapper around core.kernel.download_firecracker_kernel.
    Used by vm_config.py for missing-asset prompts.
    """
    from mvmctl.core.kernel import download_firecracker_kernel as _core

    if kernels_dir is None:
        from mvmctl.utils.fs import get_kernels_dir

        kernels_dir = get_kernels_dir()
    kernels_dir.mkdir(parents=True, exist_ok=True)
    return _core(
        ci_version=ci_version,
        arch=arch,
        kernels_dir=kernels_dir,
        output_name=output_name,
        output_path=output_path,
        kernel_spec=None,  # resolved internally
    )


def _build_official_kernel(
    spec: KernelSpec,
    arch: str,
    output_dir: Path,
    output_name: str | None,
    output_path: Path | None,
    jobs: int | None,
    keep_build_dir: bool,
    clean_build: bool,
    kernel_config: Path | None,
) -> KernelFetchResult:
    """Internal: Build official kernel.

    Args:
        spec: Resolved kernel specification
        arch: Target architecture
        output_dir: Directory to store kernel
        output_name: Optional filename override
        output_path: Optional explicit output path
        jobs: Parallel build jobs
        keep_build_dir: Whether to retain build directory
        clean_build: Whether to skip build cache
        kernel_config: Optional custom config path

    Returns:
        KernelFetchResult with build results
    """
    from mvmctl.core.kernel import build_kernel_pipeline as _core_build

    effective_output_path = output_path or (
        output_dir / f"{output_name or spec.output_name}-{spec.version}-{arch}"
    )

    build_result = _core_build(
        version=spec.version,
        source_url=spec.source,
        output_path=effective_output_path,
        build_dir=None,
        sha256=spec.sha256,
        jobs=jobs,
        keep_build_dir=keep_build_dir,
        user_config_path=kernel_config,
        arch=arch,
        kernel_spec=spec,
        use_cache=not clean_build,
    )

    warnings: list[str] = []
    info_messages: list[str] = []

    if build_result.config_result:
        warnings.extend(build_result.config_result.warnings)
        info_messages.extend(build_result.config_result.info_messages)

    if build_result.build_result:
        warnings.extend(build_result.build_result.warnings)
        info_messages.extend(build_result.build_result.info_messages)

    info_messages.append(f"Kernel built: {effective_output_path}")

    return KernelFetchResult(
        path=effective_output_path,
        version=spec.version,
        arch=arch,
        kernel_type=KERNEL_TYPE_OFFICIAL,
        warnings=warnings,
        info_messages=info_messages,
    )


def register_fetched_kernel(
    result: KernelFetchResult,
    spec: KernelSpec,
    set_default: bool = False,
) -> str:
    """Register a fetched kernel in the database.

    Args:
        result: KernelFetchResult from fetch_kernel()
        spec: KernelSpec used for fetching
        set_default: Whether to set as default kernel

    Returns:
        Full hash ID of the registered kernel
    """
    from mvmctl.core.kernel import parse_kernel_filename
    from mvmctl.core.metadata import set_default_kernel_by_filename, update_kernel_entry

    kernel_path = result.path
    kernel_name = kernel_path.name

    last_modified = "-"
    if kernel_path.exists():
        mtime = kernel_path.stat().st_mtime
        last_modified = datetime.fromtimestamp(mtime, tz=timezone.utc).isoformat()

    full_id = generate_full_hash_kernel(
        kernel_path,
        result.version,
        result.arch,
    )

    parsed = parse_kernel_filename(kernel_name)
    cache_dir = get_cache_dir()
    update_kernel_entry(
        cache_dir,
        full_id,
        path=kernel_name,
        full_hash=full_id,
        name=kernel_name,
        base_name=parsed.base_name,
        version=result.version,
        arch=result.arch,
        type=result.kernel_type,
        last_modified=last_modified,
    )

    if set_default:
        set_default_kernel_by_filename(cache_dir, kernel_name)
        logger.info("Default kernel set to: %s", kernel_name)

    return full_id


def resolve_kernel_spec(kernel_type: str, version: str | None = None) -> KernelSpec:
    """Resolve kernel spec from kernels.yaml.

    Args:
        kernel_type: Type of kernel (firecracker, official)
        version: Optional version string

    Returns:
        Resolved KernelSpec

    Raises:
        KernelError: If spec cannot be resolved
    """
    from mvmctl.core.kernel import resolve_kernel_spec as _core_resolve

    return _core_resolve(kernel_type, version)


def _resolve_kernel_spec(kernel_type: str, version: str | None = None) -> KernelSpec:
    """Internal: Resolve kernel spec with error handling."""
    try:
        return resolve_kernel_spec(kernel_type, version)
    except KernelError:
        raise
    except Exception as exc:
        raise KernelError(f"Failed to resolve kernel spec: {exc}") from exc


def list_kernels(kernels_dir: Path) -> list[KernelItem]:
    """List all kernels with their metadata.

    Args:
        kernels_dir: Directory containing kernels

    Returns:
        List of KernelItem objects with kernel metadata
    """
    from mvmctl.core.kernel import parse_kernel_filename
    from mvmctl.core.metadata import list_kernel_entries

    kernels_dir.mkdir(parents=True, exist_ok=True)
    cache_dir = get_cache_dir()

    entries = list_kernel_entries(cache_dir, kernels_dir, include_missing=True)

    results: list[KernelItem] = []

    for entry_id, meta in sorted(entries.items()):
        path = str(meta.get("path", entry_id))

        last_modified = meta.get("last_modified")
        if not last_modified:
            last_modified = meta.get("built_at", "-")

        if meta.get("base_name"):
            base_name = str(meta["base_name"])
            version = str(meta.get("version", "-"))
            arch = str(meta.get("arch", "-"))
            kernel_type = str(meta.get("type", KERNEL_TYPE_UNKNOWN))
        else:
            parsed = parse_kernel_filename(path)
            base_name = parsed.base_name
            version = parsed.version
            arch = parsed.arch
            kernel_type = KERNEL_TYPE_UNKNOWN

        is_default_val = meta.get("is_default", 0)
        is_default_flag = str(is_default_val) in ("1", "true")

        results.append(
            KernelItem(
                id=entry_id,
                name=base_name,
                path=path,
                version=version,
                arch=arch,
                base_name=base_name,
                type=kernel_type,
                is_default=is_default_flag,
                created_at=str(last_modified) if last_modified else None,
                updated_at=str(last_modified) if last_modified else None,
            )
        )

    return results


def set_default_kernel(kernels_dir: Path, kernel_prefix: str) -> None:
    """Set a kernel as the default.

    Args:
        kernels_dir: Directory containing kernels
        kernel_prefix: Kernel ID prefix or filename to set as default

    Raises:
        KernelError: If the kernel file does not exist
    """
    from mvmctl.core.metadata import set_default_kernel_by_filename

    cache_dir = get_cache_dir()

    # Try direct filename first
    kernel_path = kernels_dir / kernel_prefix
    if kernel_path.exists():
        set_default_kernel_by_filename(cache_dir, kernel_prefix)
        logger.info("Default kernel set to: %s", kernel_prefix)
        return

    # Try resolving as ID prefix
    match = resolve_single_by_id_prefix(
        kernel_prefix, find_kernels_by_id_prefix, cache_dir, "kernel"
    )
    if match is None:
        raise KernelError(f"Kernel not found: {kernel_prefix}")

    _full_id, meta = match
    path = str(meta.get("path", ""))
    if not path:
        raise KernelError(f"Kernel path not found for: {kernel_prefix}")

    kernel_path = kernels_dir / path
    if not kernel_path.exists():
        raise KernelError(f"Kernel file not found: {kernel_path}")

    set_default_kernel_by_filename(cache_dir, path)
    logger.info("Default kernel set to: %s", path)


def get_default_kernel_path(kernels_dir: Path) -> Path | None:
    """Get the path to the default kernel.

    Args:
        kernels_dir: Directory containing kernels

    Returns:
        Path to default kernel or None if not set or not found
    """
    db = MVMDatabase()
    default_kernel = db.get_default_kernel()
    if default_kernel is None:
        return None
    path = default_kernel.path
    if not isinstance(path, str) or not path:
        return None
    kernel_path = kernels_dir / path
    return kernel_path if kernel_path.exists() else None


def save_kernel_metadata(
    kernels_dir: Path,
    kernel_name: str,
    version: str | None = None,
    kernel_type: str | None = None,
    arch: str | None = None,
) -> str:
    """Save kernel metadata to database (backward-compatible wrapper).

    This function provides backward compatibility for code that uses the old
    save_kernel_metadata API. It creates a KernelFetchResult and delegates
    to register_fetched_kernel.

    Args:
        kernels_dir: Directory containing kernels
        kernel_name: Name of the kernel file
        version: Kernel version string
        kernel_type: Type of kernel (firecracker, official, unknown)
        arch: Architecture (x86_64, arm64, etc.)

    Returns:
        The full hash ID of the kernel entry
    """
    from mvmctl.core.kernel import parse_kernel_filename

    kernel_path = kernels_dir / kernel_name
    parsed = parse_kernel_filename(kernel_name)

    effective_version = version or parsed.version or "-"
    effective_arch = arch or parsed.arch or "-"
    effective_type = kernel_type or KERNEL_TYPE_UNKNOWN

    result = KernelFetchResult(
        path=kernel_path,
        version=effective_version,
        arch=effective_arch,
        kernel_type=effective_type,
        warnings=[],
        info_messages=[],
    )

    # Create a minimal spec for registration
    spec = KernelSpec(
        name=kernel_name,
        kernel_type=effective_type,
        version=effective_version,
        source="",
        output_name=parsed.base_name,
        build_dir="",
    )

    return register_fetched_kernel(result, spec, set_default=False)


def remove_kernel(prefix: str, kernels_dir: Path, force: bool = False) -> None:
    """Remove a kernel by ID prefix.

    Args:
        prefix: Kernel ID prefix to remove
        kernels_dir: Directory containing kernels
        force: Remove even if referenced by VMs

    Raises:
        KernelError: If kernel not found or referenced by running VMs (and force=False)
    """
    from mvmctl.core.metadata import remove_kernel_entry

    cache_dir = get_cache_dir()

    match = resolve_single_by_id_prefix(prefix, find_kernels_by_id_prefix, cache_dir, "kernel")
    if match is None:
        raise KernelError(f"Kernel not found: {prefix}")

    full_id, meta = match
    path = str(meta.get("path", ""))
    kernel_path = kernels_dir / path if path else None

    # Check for VM references unless force is True
    if not force and kernel_path:
        vm_manager = get_vm_manager()
        vms = vm_manager.list_all()
        kernel_path_str = str(kernel_path)
        referencing = [
            vm.name
            for vm in vms
            if (vm.config and vm.config.kernel_path == kernel_path)
            or (vm.kernel_id and vm.kernel_id == kernel_path_str)
        ]
        if referencing:
            raise KernelError(
                f"Kernel '{prefix}' is referenced by active VMs: {', '.join(referencing)}"
            )

    if kernel_path and kernel_path.exists():
        kernel_path.unlink()

    remove_kernel_entry(cache_dir, full_id)
    logger.info("Removed kernel: %s", full_id[:6])


def resolve_kernel_path(kernel: str) -> Path:
    """Resolve a kernel identifier to a filesystem path.

    Tries multiple strategies:
    1. Direct file path in kernels directory
    2. Absolute path
    3. Database lookup by ID prefix

    Args:
        kernel: Kernel identifier (filename, path, or ID prefix)

    Returns:
        Resolved path to the kernel file

    Raises:
        KernelError: If kernel cannot be found
    """
    from mvmctl.core.metadata import list_kernel_entries

    kernels_dir = get_kernels_dir()
    candidate = kernels_dir / kernel
    if candidate.exists():
        return candidate

    direct = Path(kernel)
    if direct.is_absolute() and direct.exists():
        return direct

    # Try database lookup by ID prefix
    cache_dir = get_cache_dir()
    matches = [
        (k, m)
        for k, m in list_kernel_entries(cache_dir, kernels_dir).items()
        if k.startswith(kernel)
    ]
    if len(matches) == 1:
        full_key, meta = matches[0]
        path = str(meta.get("path", ""))
        if path:
            candidate = kernels_dir / path
            if candidate.exists():
                return candidate
        candidate = kernels_dir / full_key
        if candidate.exists():
            return candidate

    if direct.exists():
        return direct

    raise KernelError(f"Kernel not found: {kernel!r}")


def resolve_kernel_id_path(kernel: str) -> Path:
    """Resolve a kernel ID prefix to a filesystem path.

    Args:
        kernel: Kernel ID prefix

    Returns:
        Resolved path to the kernel file

    Raises:
        KernelError: If kernel ID is not found or ambiguous
    """
    from mvmctl.core.metadata import list_kernel_entries

    kernels_dir = get_kernels_dir()
    cache_dir = get_cache_dir()

    def _find(cache_dir: Path, prefix: str) -> list[tuple[str, dict[str, object]]]:
        return [
            (k, m)
            for k, m in list_kernel_entries(cache_dir, kernels_dir).items()
            if k.startswith(prefix)
        ]

    match = resolve_single_by_id_prefix(kernel, _find, cache_dir)
    if match is None:
        raise KernelError(f"Kernel ID not found or ambiguous: {kernel!r}")

    full_key, meta = match
    path = str(meta.get("path", ""))
    if path:
        candidate = kernels_dir / path
        if candidate.exists():
            return candidate
    candidate = kernels_dir / full_key
    if candidate.exists():
        return candidate

    raise KernelError(f"Kernel not found: {kernel!r}")


def _get_ci_version() -> str:
    """Get CI version from default binary.

    Returns:
        CI version string from default binary, or fallback
    """
    from mvmctl.api.metadata import get_default_binary_entry

    try:
        default_binary = get_default_binary_entry()
        if default_binary is not None:
            raw_ci_version = default_binary.ci_version
            if isinstance(raw_ci_version, str):
                return raw_ci_version
    except Exception:
        pass

    return "v1.11"  # Fallback CI version
