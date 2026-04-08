"""Asset management commands — unified interface for kernels, images, and binaries."""

import json
import shutil
from pathlib import Path
from typing import Any, List, Optional

import typer

from mvmctl.api.assets import (
    BinaryVersion,
    ImageImportSpec,
    build_kernel_pipeline,
    download_firecracker_kernel,
    fetch_binary,
    fetch_image,
    import_image,
    list_kernels,
    list_local_versions,
    list_remote_versions,
    load_images_config,
    remove_version,
    resolve_kernel_spec,
    set_active_version,
    set_default_kernel,
)
from mvmctl.api.metadata import (
    find_images_by_id_prefix,
    find_kernels_by_id_prefix,
    get_image_entry,
    list_image_entries,
    remove_image_entry,
    remove_kernel_entry,
    set_default_image_by_os_slug,
    set_default_image_entry,
    update_image_entry,
)
from mvmctl.api.vms import get_vm_manager
from mvmctl.constants import (
    COMPRESSION_EXTENSION_MAP,
    DEFAULT_IMAGE_ARCH,
    DEFAULT_IMAGE_IMPORT_FORMAT,
    IMAGE_IMPORT_FORMAT_MAP,
    KERNEL_TYPE_FIRECRACKER,
    KERNEL_TYPE_OFFICIAL,
    SUPPORTED_IMAGE_EXTENSIONS,
)
from mvmctl.exceptions import (
    AssetNotFoundError,
    BinaryError,
    ImageError,
    KernelError,
    RootPartitionDetectionError,
    TieDetectedError,
)
from mvmctl.utils.console import (
    format_timestamp,
    get_combined_marker,
    print_error,
    print_info,
    print_inspect_header,
    print_key_value,
    print_section_header,
    print_success,
    print_table,
    print_warning,
)
from mvmctl.utils.disk_size import format_bytes_human_readable, format_sectors_human_readable
from mvmctl.utils.fs import (
    get_assets_dir,
    get_cache_dir,
    get_file_size,
    get_images_dir,
    get_kernels_dir,
    is_file_missing,
)
from mvmctl.utils.full_hash import generate_full_hash_image, shorten_hash
from mvmctl.utils.id_lookup import resolve_single_by_id_prefix
from mvmctl.utils.time import human_readable_time


def _find_image_by_os_slug(
    all_meta: dict[str, dict[str, object]], os_slug: str
) -> tuple[str, dict[str, object]] | None:
    for key, meta in all_meta.items():
        if str(meta.get("os_slug", "")) == os_slug:
            return key, meta
    return None


def _find_local_image_path(images_dir: Path, image_id: str) -> Path | None:
    for ext in SUPPORTED_IMAGE_EXTENSIONS:
        candidate = images_dir / f"{image_id}{ext}"
        if candidate.exists():
            return candidate
    return None


def _resolve_image_file(images_dir: Path, image_id: str, meta: dict[str, object]) -> Path | None:
    filename = str(meta.get("path", ""))
    if filename:
        return images_dir / filename
    return _find_local_image_path(images_dir, image_id)


def _compute_official_output_path(
    kernels_dir: Path,
    out: Path | None,
    name: str | None,
    spec_version: str,
    spec_output_name: str,
    arch: str,
) -> Path:
    if out is not None:
        return out
    base_name = name if name is not None else spec_output_name
    return kernels_dir / f"{base_name}-{spec_version}-{arch}"


def _print_image_details(info: dict[str, Any], found_path: Path | None) -> None:
    os_slug = info.get("os_slug", "-")
    missing_marker = " (missing)" if info.get("missing") else ""

    print_inspect_header(f"Image: {os_slug}{missing_marker}")

    print_section_header("BASIC INFO")
    print_key_value("ID", info.get("id", "-"))
    print_key_value("Name", info.get("name", "-"))
    print_key_value("OS Slug", os_slug)
    print_key_value("Pulled", format_timestamp(info.get("pulled_at")))

    print_section_header("STORAGE")
    print_key_value("Filename", info.get("filename", "-"))
    print_key_value("FS Type", info.get("fs_type", "-"))
    print_key_value("FS UUID", info.get("fs_uuid", "-"))
    print_key_value("File Size", info.get("file_size", "-"))

    print_section_header("COMPRESSION")
    print_key_value("Format", info.get("compressed_format", "-"))
    print_key_value("Original", info.get("original_size", "-"))
    print_key_value("Compressed", info.get("compressed_size", "-"))
    print_key_value("Ratio", info.get("compression_ratio", "-"))


def _print_image_details_tree(info: dict[str, Any], found_path: Path | None) -> None:
    os_slug = info.get("os_slug", "-")
    missing_marker = " (missing)" if info.get("missing") else ""

    print(f"{os_slug}{missing_marker}")

    tree_lines = [
        f"├── ID:          {info.get('id', '-')}",
        f"├── Name:        {info.get('name', '-')}",
        f"├── OS Slug: {os_slug}",
        f"├── Pulled:      {info.get('pulled_at', '-')}",
    ]

    tree_lines.append("├── Storage")
    tree_lines.append(f"│   ├── Filename:  {info.get('filename', '-')}")
    tree_lines.append(f"│   ├── FS Type:   {info.get('fs_type', '-')}")
    tree_lines.append(f"│   ├── FS UUID:   {info.get('fs_uuid', '-')}")
    tree_lines.append(f"│   └── File Size: {info.get('file_size', '-')}")

    tree_lines.append("└── Compression")
    tree_lines.append(f"    ├── Format:    {info.get('compressed_format', '-')}")
    tree_lines.append(f"    ├── Original:  {info.get('original_size', '-')}")
    tree_lines.append(f"    ├── Compressed: {info.get('compressed_size', '-')}")
    tree_lines.append(f"    └── Ratio:     {info.get('compression_ratio', '-')}")

    for line in tree_lines:
        print(line)


def _print_pipeline_results(pipeline_result: Any) -> None:
    if pipeline_result.config_result:
        for warning in pipeline_result.config_result.warnings:
            print_warning(warning)
        for info in pipeline_result.config_result.info_messages:
            print_info(info)

    if pipeline_result.build_result:
        for warning in pipeline_result.build_result.warnings:
            print_warning(warning)
        for info in pipeline_result.build_result.info_messages:
            print_info(info)


def _prompt_for_partition_selection(
    partitions: list[dict[str, object]],
    tied_partitions: list[str] | None = None,
    disabled_detectors: list[str] | None = None,
) -> int:
    """Display partition information and prompt user to select the root partition.

    Args:
        partitions: List of partition dictionaries with 'size', 'type', 'name', 'label', 'fstype'.
        tied_partitions: Optional list of tied partition numbers (as strings) for display.
        disabled_detectors: List of detector names that were disabled during detection.

    Returns:
        The selected partition number (1-indexed).

    Raises:
        typer.Exit: If user cancels or invalid input after retries.
    """
    if not partitions:
        print_error("No partitions available to select")
        raise typer.Exit(code=1)

    print_warning("Could not automatically detect root partition")
    if tied_partitions:
        print_info(f"Tie detected between partitions: {', '.join(tied_partitions)}")
    print_info("")
    print_info("Available partitions:")

    rows: list[list[str]] = []
    for i, partition in enumerate(partitions, 1):
        size_sectors = partition.get("size", 0)
        size_str = (
            format_sectors_human_readable(int(size_sectors))
            if isinstance(size_sectors, (int, float))
            else "Unknown"
        )
        part_type = str(partition.get("type", "-"))[:20]
        label = str(partition.get("name", partition.get("label", "-")))[:15]
        fstype = str(partition.get("fstype", "-"))[:10]
        rows.append([str(i), size_str, part_type, label, fstype])

    print_table(
        columns=["#", "Size", "Type", "Label", "FS Type"],
        rows=rows,
    )

    num_partitions = len(partitions)
    while True:
        try:
            user_input = input(f"\nSelect root partition (1-{num_partitions}): ")
            selection = int(user_input.strip())
            if 1 <= selection <= num_partitions:
                return selection
            print_error(f"Invalid selection. Please enter a number between 1 and {num_partitions}")
        except ValueError:
            print_error(f"Invalid input. Please enter a number between 1 and {num_partitions}")


def _get_vms_using_kernel(kernel_path: Path) -> list[str]:
    """Get list of VM names that reference the given kernel path.

    Checks both vm.config.kernel_path (legacy) and vm.kernel_id (new field)
    to ensure VMs without persisted config are still protected.
    """
    vm_manager = get_vm_manager()
    vms = vm_manager.list_all()
    kernel_path_str = str(kernel_path)
    result = []
    for vm in vms:
        # Check config.kernel_path (legacy VMs with full config)
        if vm.config and vm.config.kernel_path == kernel_path:
            result.append(vm.name)
        # Check kernel_id field (new field for VMs without full config)
        elif vm.kernel_id and vm.kernel_id == kernel_path_str:
            result.append(vm.name)
    return result


def _get_vms_using_image(image_path: Path) -> list[str]:
    """Get list of VM names that reference the given image path.

    Checks both vm.config.rootfs_path (legacy) and vm.image_id (new field)
    to ensure VMs without persisted config are still protected.
    """
    vm_manager = get_vm_manager()
    vms = vm_manager.list_all()
    image_path_str = str(image_path)
    result = []
    for vm in vms:
        # Check config.rootfs_path (legacy VMs with full config)
        if vm.config and vm.config.rootfs_path == image_path:
            result.append(vm.name)
        # Check image_id field (new field for VMs without full config)
        elif vm.image_id and vm.image_id == image_path_str:
            result.append(vm.name)
    return result


def _resolve_image_spec(
    images: list[Any],
    effective_selector: str,
    version: str | None,
) -> Any:
    """Resolve an ImageSpec from the images list using selector and optional version.

    Tries exact ID match first, then falls back to image_type matching with optional
    version disambiguation.

    Args:
        images: List of ImageSpec objects loaded from images.yaml.
        effective_selector: The image ID or image_type to resolve.
        version: Optional version string to disambiguate multiple type matches.

    Returns:
        The matching ImageSpec.

    Raises:
        typer.Exit: With code 1 if no match or ambiguous match is found.
    """
    spec = next((img for img in images if img.id == effective_selector), None)
    if spec is not None:
        return spec

    type_matches = [img for img in images if img.image_type == effective_selector]
    if not type_matches:
        available = ", ".join(img.id for img in images)
        print_error(f"Image '{effective_selector}' not found. Available: {available}")
        raise typer.Exit(code=1)

    if version is not None:
        version_matches = [img for img in type_matches if img.version == version]
        if len(version_matches) == 1:
            return version_matches[0]
        if len(version_matches) > 1:
            ids = ", ".join(img.id for img in version_matches)
            print_error(
                f"Multiple '{effective_selector}' images with version '{version}' found: {ids}"
            )
            raise typer.Exit(code=1)
        versions = ", ".join(sorted({img.version for img in type_matches}))
        print_error(
            f"No '{effective_selector}' image with version '{version}'. Available: {versions}"
        )
        raise typer.Exit(code=1)

    if len(type_matches) == 1:
        return type_matches[0]

    versions = ", ".join(sorted({img.version for img in type_matches}))
    print_error(
        f"Multiple '{effective_selector}' images found. Provide --version. Available: {versions}"
    )
    raise typer.Exit(code=1)


def _handle_partition_detection_retry(
    func: Any,
    *args: Any,
    no_prompt: bool,
    disabled_detectors: list[str] | None = None,
    **kwargs: Any,
) -> Any:
    """Run an image fetch/import function and handle partition detection errors.

    If the function raises RootPartitionDetectionError or TieDetectedError, prompts the user
    to select a partition (unless no_prompt is True) then retries with partition= set.

    Args:
        func: Callable — fetch_image or import_image.
        *args: Positional arguments forwarded to func.
        no_prompt: If True, exit with error instead of prompting.
        disabled_detectors: Detector names to display in the partition prompt.
        **kwargs: Keyword arguments forwarded to func.

    Returns:
        The result returned by func.

    Raises:
        typer.Exit: With code 1 if no_prompt and a detection error occurs.
    """
    try:
        return func(*args, **kwargs)
    except (RootPartitionDetectionError, TieDetectedError) as exc:
        if no_prompt:
            print_error(str(exc))
            raise typer.Exit(code=1)
        tied = exc.tied_partitions if isinstance(exc, TieDetectedError) else None
        selected = _prompt_for_partition_selection(
            exc.partitions,
            tied_partitions=tied,
            disabled_detectors=disabled_detectors,
        )
        print_info(f"Using user-selected partition: {selected}")
        return func(*args, partition=selected, **kwargs)


kernel_app = typer.Typer(
    help="Kernel management",
    no_args_is_help=False,
    rich_markup_mode=None,
    add_completion=False,
)
image_app = typer.Typer(
    help="Image management",
    no_args_is_help=False,
    rich_markup_mode=None,
    add_completion=False,
)
bin_app = typer.Typer(
    help="Binary management",
    no_args_is_help=False,
    rich_markup_mode=None,
    add_completion=False,
)


@kernel_app.callback(invoke_without_command=True)
def kernel_callback(ctx: typer.Context) -> None:
    if ctx.invoked_subcommand is None:
        typer.echo(ctx.get_help())
        raise typer.Exit()


@image_app.callback(invoke_without_command=True)
def image_callback(ctx: typer.Context) -> None:
    if ctx.invoked_subcommand is None:
        typer.echo(ctx.get_help())
        raise typer.Exit()


@bin_app.callback(invoke_without_command=True)
def bin_callback(ctx: typer.Context) -> None:
    if ctx.invoked_subcommand is None:
        typer.echo(ctx.get_help())
        raise typer.Exit()


@kernel_app.command(name="ls")
def kernel_ls(
    json_output: bool = typer.Option(False, "--json", help="Output as JSON"),
    kernels_dir: Optional[Path] = typer.Option(None, "--kernels-dir", help="Kernels directory"),
    firecracker_only: bool = typer.Option(
        False, "--firecracker", help="Show only firecracker kernels"
    ),
    official_only: bool = typer.Option(
        False, "--official", help="Show only official/upstream kernels"
    ),
) -> None:
    """List cached kernels (both Firecracker CI and official upstream)."""
    kernels_dir = kernels_dir if kernels_dir is not None else get_kernels_dir()
    kernels_dir.mkdir(parents=True, exist_ok=True)
    kernels = list_kernels(kernels_dir)

    if firecracker_only:
        kernels = [k for k in kernels if k.get("type") == KERNEL_TYPE_FIRECRACKER]
    elif official_only:
        kernels = [k for k in kernels if k.get("type") == KERNEL_TYPE_OFFICIAL]

    if json_output:
        typer.echo(json.dumps(kernels, indent=2))
        return

    if not kernels:
        from mvmctl.utils.console import print_info

        print_info("No kernels found. Use 'mvm kernel fetch --type firecracker' to download one.")

    rows: list[list[str]] = []
    for k in kernels:
        is_default = k.get("is_default") == "true"
        last_modified_display = human_readable_time(k.get("last_modified", "-"))
        path_str = k.get("path", "")
        path = kernels_dir / path_str if path_str else None
        is_missing = is_file_missing(path)
        display_id = get_combined_marker(is_default, is_missing) + shorten_hash(k.get("id", ""), 12)
        size = path.stat().st_size if path and path.exists() else 0
        size_str = format_bytes_human_readable(size) if size > 0 else "-"
        rows.append(
            [
                display_id,
                k.get("name", "-"),
                k.get("version", ""),
                k.get("arch", "-"),
                k.get("type", ""),
                last_modified_display,
                size_str,
            ]
        )
    print_table(
        columns=["ID", "Name", "Version", "Arch", "Type", "Last Modified", "Size"],
        rows=rows,
    )


def _get_ci_version() -> str:
    from mvmctl.api.config import get_firecracker_config

    config = get_firecracker_config()
    ci_version = config.get("ci_version", "")
    if not ci_version:
        from mvmctl.exceptions import AssetNotFoundError

        raise AssetNotFoundError(
            "No CI version found for firecracker. "
            "Fetch a binary first with: mvm bin fetch <version>"
        )
    return ci_version


def _fetch_firecracker_kernel(
    spec: Any,
    kernels_dir: Path,
    arch: str | None,
    name: str | None,
    out: Path | None,
) -> Path:
    if arch is None:
        print_error(
            f"No architecture specified for kernel spec '{spec.name}'. Provide --arch explicitly."
        )
        raise typer.Exit(code=1)

    ci_version = _get_ci_version()
    try:
        return download_firecracker_kernel(
            ci_version=ci_version,
            arch=arch,
            kernels_dir=kernels_dir,
            output_name=name,
            output_path=out,
            kernel_spec=spec,
        )
    except KernelError as exc:
        print_error(f"Kernel fetch failed: {exc}")
        raise typer.Exit(code=1) from exc


def _build_official_kernel(
    spec: Any,
    kernels_dir: Path,
    out: Path | None,
    name: str | None,
    arch: str | None,
    jobs: int | None,
    keep_build_dir: bool,
    clean_build: bool,
    kernel_config: Path | None,
) -> Path:
    """Build an official upstream kernel.

    Args:
        spec: The resolved KernelSpec for the official kernel.
        kernels_dir: The kernels cache directory.
        out: Explicit --out path.
        name: --name override for the filename base.
        arch: Architecture string (required for official kernels).
        jobs: Parallel build jobs.
        keep_build_dir: Whether to retain the build directory.
        clean_build: Whether to skip the build cache.
        kernel_config: Optional path to a custom .config file.

    Returns:
        Path to the built kernel file.

    Raises:
        typer.Exit: With code 1 on build failure or missing required inputs.
    """
    # Validate required inputs — no silent fallbacks
    if not spec.version:
        print_error(
            f"No version available for kernel spec '{spec.name}'. Provide --version explicitly."
        )
        raise typer.Exit(code=1)

    if arch is None:
        print_error(
            f"No architecture specified for kernel spec '{spec.name}'. Provide --arch explicitly."
        )
        raise typer.Exit(code=1)

    if kernel_config and not kernel_config.exists():
        print_error(f"Kernel config file not found: {kernel_config}")
        raise typer.Exit(code=1)

    output_path = _compute_official_output_path(
        kernels_dir=kernels_dir,
        out=out,
        name=name,
        spec_version=spec.version,
        spec_output_name=spec.output_name,
        arch=arch,
    )

    try:
        pipeline_result = build_kernel_pipeline(
            version=spec.version,
            source_url=spec.source,
            output_path=output_path,
            build_dir=None,
            jobs=jobs,
            keep_build_dir=keep_build_dir,
            user_config_path=kernel_config,
            arch=arch,
            kernel_spec=spec,
            use_cache=not clean_build,
        )
    except KernelError as exc:
        print_error(f"Kernel build failed: {exc}")
        raise typer.Exit(code=1) from exc

    _print_pipeline_results(pipeline_result)

    if keep_build_dir:
        print_info(f"Build directory kept at: {pipeline_result.build_dir}")

    return output_path


@kernel_app.command(name="fetch")
def kernel_fetch(
    kernel_type: Optional[str] = typer.Option(
        None, "--type", help="Kernel type from kernels.yaml (e.g. firecracker, official)"
    ),
    firecracker: bool = typer.Option(
        False, "--firecracker", help="Shortcut for --type firecracker"
    ),
    official: bool = typer.Option(False, "--official", help="Shortcut for --type official"),
    version: Optional[str] = typer.Option(
        None,
        "--version",
        help="Kernel spec version from kernels.yaml (required if multiple specs share the same type)",
    ),
    arch: Optional[str] = typer.Option(None, "--arch", help="Architecture"),
    out: Optional[Path] = typer.Option(None, "--out", help="Output path/name"),
    name: Optional[str] = typer.Option(
        None,
        "--name",
        help="Override output filename only (placed in kernels directory unless --out is used)",
    ),
    jobs: Optional[int] = typer.Option(
        None, "--jobs", "-j", help="Parallel build jobs (official only)"
    ),
    keep_build_dir: bool = typer.Option(
        False, "--keep-build-dir", help="Keep build directory after build"
    ),
    clean_build: bool = typer.Option(
        False,
        "--clean-build",
        help="Skip kernel build cache and force a clean build",
    ),
    kernel_config: Optional[Path] = typer.Option(
        None, "--kernel-config", help="Path to custom kernel .config file"
    ),
    set_default: bool = typer.Option(False, "--set-default", help="Set this kernel as default"),
) -> None:
    # ── SETUP ──────────────────────────────────────────────────────────
    kernels_dir = get_kernels_dir()
    kernels_dir.mkdir(parents=True, exist_ok=True)

    # ── VALIDATE ───────────────────────────────────────────────────────
    if name is not None and out is not None:
        print_error("--name cannot be combined with --out")
        raise typer.Exit(code=1)

    if firecracker and official:
        print_error("--firecracker cannot be combined with --official")
        raise typer.Exit(code=1)

    if firecracker:
        if kernel_type is not None and kernel_type != KERNEL_TYPE_FIRECRACKER:
            print_error("--firecracker cannot be combined with a different --type value")
            raise typer.Exit(code=1)
        resolved_type = KERNEL_TYPE_FIRECRACKER
    elif official:
        if kernel_type is not None and kernel_type != KERNEL_TYPE_OFFICIAL:
            print_error("--official cannot be combined with a different --type value")
            raise typer.Exit(code=1)
        resolved_type = KERNEL_TYPE_OFFICIAL
    elif kernel_type is None:
        print_error("Provide --type <kernel-type> or use --firecracker/--official")
        raise typer.Exit(code=1)
    else:
        resolved_type = kernel_type

    try:
        spec = resolve_kernel_spec(kernel_type=resolved_type, version=version)
    except KernelError as exc:
        print_error(str(exc))
        raise typer.Exit(code=1) from exc

    if arch is None:
        arch = DEFAULT_IMAGE_ARCH

    # ── EXECUTE ────────────────────────────────────────────────────────
    if spec.kernel_type == KERNEL_TYPE_FIRECRACKER:
        result = _fetch_firecracker_kernel(spec, kernels_dir, arch, name, out)
        print_success(f"Firecracker kernel ready: {result}")

    elif spec.kernel_type == KERNEL_TYPE_OFFICIAL:
        result = _build_official_kernel(
            spec=spec,
            kernels_dir=kernels_dir,
            out=out,
            name=name,
            arch=arch,
            jobs=jobs,
            keep_build_dir=keep_build_dir,
            clean_build=clean_build,
            kernel_config=kernel_config,
        )
        print_success(f"Kernel built: {result}")

    else:
        print_error(f"Unsupported kernel type in spec '{spec.name}': {spec.kernel_type!r}")
        raise typer.Exit(code=1)

    # ── FINALIZE ───────────────────────────────────────────────────────
    if set_default:
        set_default_kernel(kernels_dir, result.name)
        print_success(f"Default kernel set to: {result.name}")

    raise typer.Exit(code=0)


@kernel_app.command(name="set-default")
def kernel_set_default(
    prefix: str = typer.Argument(..., help="Kernel ID prefix to set as default"),
    kernels_dir: Optional[Path] = typer.Option(None, "--kernels-dir", help="Kernels directory"),
) -> None:
    """Set a kernel as the default for VM creation."""
    kernels_dir = kernels_dir if kernels_dir is not None else get_kernels_dir()
    cache_dir = get_cache_dir()

    match = resolve_single_by_id_prefix(prefix, find_kernels_by_id_prefix, cache_dir, "kernel")
    if match is None:
        raise typer.Exit(code=1)

    _, meta = match
    path_str = str(meta.get("path", ""))
    if not path_str or not (kernels_dir / path_str).exists():
        print_error(f"Kernel file not found for ID '{prefix}'")
        raise typer.Exit(code=1)

    try:
        set_default_kernel(kernels_dir, path_str)
    except KernelError as exc:
        print_error(str(exc))
        raise typer.Exit(code=1) from exc
    print_success(f"Default kernel set to: {path_str}")


@kernel_app.command(name="rm")
def kernel_rm(
    prefixes: Optional[List[str]] = typer.Argument(None, help="Kernel ID prefixes to remove"),
    kernels_dir: Optional[Path] = typer.Option(None, "--kernels-dir", help="Kernels directory"),
    force: bool = typer.Option(False, "--force", "-f", help="Remove even if referenced by VMs"),
) -> None:
    """Remove cached kernels by ID prefix.

    Examples:
        mvm kernel rm abc123
        mvm kernel rm abc123 def456
    """
    kernels_dir = kernels_dir if kernels_dir is not None else get_kernels_dir()
    effective_ids: list[str] = list(prefixes) if prefixes else []
    if not effective_ids:
        print_error("Provide at least one kernel ID prefix")
        raise typer.Exit(code=1)

    cache_dir = get_cache_dir()
    exit_code = 0

    for prefix in effective_ids:
        match = resolve_single_by_id_prefix(prefix, find_kernels_by_id_prefix, cache_dir, "kernel")
        if match is None:
            if not find_kernels_by_id_prefix(cache_dir, prefix):
                print_error(f"No kernel found with ID prefix '{prefix}'")
            else:
                print_error(
                    f"Ambiguous ID prefix '{prefix}' matches {len(find_kernels_by_id_prefix(cache_dir, prefix))} kernels — use more characters"
                )
            exit_code = 1
            continue

        full_id, meta = match
        path_str = str(meta.get("path", ""))

        path: Path | None = kernels_dir / path_str if path_str else None
        if path is None or not path.exists():
            print_error(
                f"Kernel file not found for ID '{prefix}' (metadata exists but file missing)"
            )
            remove_kernel_entry(cache_dir, full_id)
            exit_code = 1
            continue

        # Check if kernel is referenced by any VMs
        referencing_vms = _get_vms_using_kernel(path)
        if referencing_vms and not force:
            print_warning(
                f"Kernel '{prefix}' is referenced by active VMs: {', '.join(referencing_vms)}"
            )
            print_info("Use --force to remove anyway (this may break those VMs)")
            exit_code = 1
            continue

        path.unlink()

        version = str(meta.get("version", ""))
        if version and version != "-":
            for stale in cache_dir.glob(f"kernel-cache-{version}-*.vmlinux"):
                stale.unlink(missing_ok=True)
            for stale in cache_dir.glob(f"kernel-cache-{version}-*.marker"):
                stale.unlink(missing_ok=True)

        remove_kernel_entry(cache_dir, full_id)
        print_success(f"Removed: {path_str}")

    raise typer.Exit(code=exit_code)


def _load_image_meta(image_id: str) -> dict[str, str]:
    cache_dir = get_cache_dir()
    meta = get_image_entry(cache_dir, image_id)
    return {str(k): str(v) for k, v in meta.items()}


def _save_image_meta(
    image_id: str,
    image_path: Path,
    meta: dict[str, str],
    fs_type: str | None = None,
    fs_uuid: str | None = None,
    compressed_size: int | None = None,
    original_size: int | None = None,
    compression_ratio: float | None = None,
    arch: str | None = None,
) -> None:
    from datetime import datetime, timezone

    cache_dir = get_cache_dir()
    fields: dict[str, object] = {
        "pulled_at": datetime.now(tz=timezone.utc).isoformat(),
        "fs_type": fs_type
        if fs_type
        else (image_path.suffix.lstrip(".") if image_path.suffix else "unknown"),
        "compressed_format": "zst",
        **meta,
    }
    if fs_uuid:
        fields.setdefault("fs_uuid", fs_uuid)
    if compressed_size is not None:
        fields.setdefault("compressed_size", compressed_size)
    if original_size is not None:
        fields.setdefault("original_size", original_size)
    if compression_ratio is not None:
        fields.setdefault("compression_ratio", compression_ratio)
    if arch is not None:
        fields["arch"] = arch
    update_image_entry(cache_dir, image_id, **fields)


def _output_remote_images(images: list[Any], images_dir: Path, json_output: bool) -> None:
    if json_output:
        typer.echo(
            json.dumps(
                [
                    {
                        "id": img.id,
                        "name": img.name,
                        "format": img.format,
                        "convert_to": img.convert_to,
                    }
                    for img in images
                ],
                indent=2,
            )
        )
        return

    all_meta = list_image_entries(get_cache_dir(), images_dir, include_missing=True)
    rows: list[list[str]] = []
    for img in images:
        entry = _find_image_by_os_slug(all_meta, img.id)
        if entry:
            meta_key, meta = entry
            found_path = _resolve_image_file(images_dir, meta_key, meta)
            compression = (
                str(meta.get("compressed_format", "-"))
                if found_path and found_path.exists()
                else "-"
            )
        else:
            found_path = None
            compression = "-"
        is_missing = is_file_missing(found_path)
        display_id = get_combined_marker(False, is_missing) + img.id
        size = get_file_size(found_path)
        rows.append(
            [
                display_id,
                img.name,
                compression,
                img.convert_to,
                format_bytes_human_readable(size) if size > 0 else "-",
            ]
        )

    print_table(
        columns=["Image ID", "Name", "Compression", "FS Type", "Size"],
        rows=rows,
    )


def _output_local_images(images: list[Any], images_dir: Path, json_output: bool) -> None:
    cache_dir = get_cache_dir()
    all_meta = list_image_entries(cache_dir, images_dir, include_missing=True)
    os_slugs = {img.id for img in images}

    if json_output:
        result: list[dict[str, str]] = []
        for img in images:
            entry = _find_image_by_os_slug(all_meta, img.id)
            if entry is None:
                continue
            meta_key, meta = entry
            result.append(
                {
                    "id": meta_key,
                    "name": img.name,
                    "format": img.format,
                    "fs_type": str(meta.get("fs_type", img.convert_to)),
                    "added": human_readable_time(str(meta.get("pulled_at", "")))
                    if meta.get("pulled_at")
                    else "-",
                }
            )
        for meta_id, meta in all_meta.items():
            if str(meta.get("os_slug", meta_id)) in os_slugs:
                continue
            result.append(
                {
                    "id": meta_id,
                    "name": str(meta.get("os_name", meta_id)),
                    "format": str(meta.get("fs_type", "unknown")),
                    "fs_type": str(meta.get("fs_type", "unknown")),
                    "added": human_readable_time(str(meta.get("pulled_at", "")))
                    if meta.get("pulled_at")
                    else "-",
                }
            )
        typer.echo(json.dumps(result, indent=2))
        return

    rows: list[list[str]] = []

    for img in images:
        entry = _find_image_by_os_slug(all_meta, img.id)
        if entry is None:
            continue
        meta_key, meta = entry
        found_path = _resolve_image_file(images_dir, meta_key, meta)
        is_default = bool(meta.get("is_default", 0))
        is_missing = is_file_missing(found_path)
        added = (
            human_readable_time(str(meta.get("pulled_at", ""))) if meta.get("pulled_at") else "-"
        )
        fs_type = str(
            meta.get("fs_type", found_path.suffix.lstrip(".") if found_path else "unknown")
        )
        display_id = get_combined_marker(is_default, is_missing) + shorten_hash(meta_key, 12)
        _raw_size = meta.get("compressed_size")
        size = get_file_size(
            found_path, int(_raw_size) if isinstance(_raw_size, (int, float)) else 0
        )
        rows.append(
            [
                display_id,
                img.name,
                fs_type,
                format_bytes_human_readable(size) if size > 0 else "-",
                added,
            ]
        )

    for meta_id, meta in all_meta.items():
        if str(meta.get("os_slug", meta_id)) in os_slugs:
            continue
        found_path = _resolve_image_file(images_dir, meta_id, meta)
        is_default = bool(meta.get("is_default", 0))
        is_missing = is_file_missing(found_path)
        os_name = str(meta.get("os_name", meta_id))
        added = (
            human_readable_time(str(meta.get("pulled_at", ""))) if meta.get("pulled_at") else "-"
        )
        fs_type = str(
            meta.get("fs_type", found_path.suffix.lstrip(".") if found_path else "unknown")
        )
        display_id = get_combined_marker(is_default, is_missing) + shorten_hash(meta_id, 12)
        _raw_size = meta.get("compressed_size")
        size = get_file_size(
            found_path, int(_raw_size) if isinstance(_raw_size, (int, float)) else 0
        )
        rows.append(
            [
                display_id,
                os_name,
                fs_type,
                format_bytes_human_readable(size) if size > 0 else "-",
                added,
            ]
        )

    print_table(
        columns=["ID", "OS Name", "FS Type", "Size", "Added"],
        rows=rows,
    )


@image_app.command(name="ls")
def image_ls(
    json_output: bool = typer.Option(False, "--json", help="Output as JSON"),
    images_dir: Optional[Path] = typer.Option(None, "--images-dir", help="Images directory"),
    remote: bool = typer.Option(False, "--remote", "-r", help="Show available remote images"),
    name_filter: Optional[str] = typer.Option(None, "--name", help="Filter by image name"),
) -> None:
    """List cached images (or available remote images with --remote)."""
    images_dir = images_dir if images_dir is not None else get_images_dir()
    images_dir.mkdir(parents=True, exist_ok=True)

    images = load_images_config(get_assets_dir() / "images.yaml")
    if name_filter:
        images = [
            img
            for img in images
            if name_filter.lower() in img.name.lower() or name_filter.lower() in img.id.lower()
        ]

    if remote:
        _output_remote_images(images, images_dir, json_output)
    else:
        _output_local_images(images, images_dir, json_output)


@image_app.command(name="fetch")
def image_fetch(
    image_selector: str = typer.Argument(
        ...,
        help="Image ID or image type from 'mvm image ls --remote' (e.g. ubuntu-24.04 or ubuntu)",
    ),
    image_type: Optional[str] = typer.Option(
        None,
        "--type",
        help="Image type from images.yaml (e.g. ubuntu, debian, firecracker)",
    ),
    version: Optional[str] = typer.Option(
        None,
        "--version",
        help="Image spec version from images.yaml (required if multiple images share the same type)",
    ),
    arch: Optional[str] = typer.Option(
        None,
        "--arch",
        help="Image architecture (e.g. x86_64, arm64)",
    ),
    out: Optional[Path] = typer.Option(None, "--out", help="Output directory"),
    force: bool = typer.Option(False, "--force", "-f", help="Re-download even if exists"),
    set_default: bool = typer.Option(
        False, "--set-default", help="Set as default image after download"
    ),
    no_prompt: bool = typer.Option(
        False, "--no-prompt", help="Exit with error on detection failure"
    ),
    skip_optimization: bool = typer.Option(
        False, "--skip-optimization", help="Skip shrink and compression, keep plain ext4"
    ),
) -> None:
    """Download an image by its ID. Run 'mvm image ls -r' to list available image IDs."""
    out = out if out is not None else get_images_dir()
    out.mkdir(parents=True, exist_ok=True)
    config_path = get_assets_dir() / "images.yaml"
    images = load_images_config(config_path)

    if image_type is not None and image_selector != image_type:
        if any(img.id == image_selector for img in images):
            print_error("--type cannot be used when selector is an image ID")
            raise typer.Exit(code=1)
        print_error("image selector and --type must match when both are provided")
        raise typer.Exit(code=1)

    effective_selector = image_type or image_selector

    spec = _resolve_image_spec(images, effective_selector, version)

    spec.arch = arch if arch is not None else DEFAULT_IMAGE_ARCH

    effective_force = force
    if not effective_force:
        compressed_extensions = list(COMPRESSION_EXTENSION_MAP.values())
        existing_compressed = [
            out / f"{spec.id}{ext}"
            for ext in compressed_extensions
            if (out / f"{spec.id}{ext}").exists()
        ]
        if not existing_compressed:
            all_meta = list_image_entries(get_cache_dir(), out, include_missing=False)
            db_entry = _find_image_by_os_slug(all_meta, spec.id)
            if db_entry:
                db_key, db_meta = db_entry
                db_path = _resolve_image_file(out, db_key, db_meta)
                if db_path and db_path.exists():
                    existing_compressed = [db_path]
        if existing_compressed:
            print_warning(f"Image '{spec.id}' already exists locally:")
            for path in existing_compressed:
                print_info(f"  {path}")
            meta = _load_image_meta(spec.id)
            if meta.get("pulled_at"):
                print_info(f"    Pulled: {meta['pulled_at'][:19]}")
            if not typer.confirm("Re-download anyway?", default=False):
                print_info("Skipping download. Use --force to overwrite.")
                if set_default:
                    try:
                        set_default_image_by_os_slug(get_cache_dir(), spec.id)
                    except KeyError:
                        pass
                    print_success(f"Default image set to: {spec.id}")
                raise typer.Exit(code=0)
            effective_force = True

    result = _handle_partition_detection_retry(
        fetch_image,
        spec,
        out,
        effective_force,
        no_prompt=no_prompt,
        skip_optimization=skip_optimization,
    )

    if result:
        from datetime import datetime, timezone

        result_path = result.path
        result_fs_type = result.fs_type
        result_fs_uuid = result.fs_uuid

        timestamp = datetime.now(tz=timezone.utc).isoformat()
        full_id = generate_full_hash_image(result_path, spec.id, timestamp)
        short_id = shorten_hash(full_id, 12)

        _save_image_meta(
            full_id,
            result_path,
            {
                "os_name": spec.name,
                "os_slug": spec.id,
                "full_hash": full_id,
                "path": result_path.name,
            },
            fs_type=result_fs_type,
            fs_uuid=result_fs_uuid,
            compressed_size=result.compressed_size,
            original_size=result.original_size,
            compression_ratio=result.compression_ratio,
            arch=spec.arch,
        )
        print_success(f"Image ready: {result_path}")
        print_info(f"  ID: {short_id}")
        if set_default:
            set_default_image_by_os_slug(get_cache_dir(), spec.id)
            print_success(f"Default image set to: {spec.id}")
        raise typer.Exit(code=0)
    else:
        print_error(f"Failed to download image '{spec.id}'")
        raise typer.Exit(code=1)


@image_app.command(name="set-default")
def image_set_default(
    prefix: str = typer.Argument(..., help="Image ID prefix to set as default"),
    images_dir: Optional[Path] = typer.Option(None, "--images-dir", help="Images directory"),
) -> None:
    """Set the default image for VM creation."""
    images_dir = images_dir if images_dir is not None else get_images_dir()
    images_dir.mkdir(parents=True, exist_ok=True)
    cache_dir = get_cache_dir()

    match = resolve_single_by_id_prefix(prefix, find_images_by_id_prefix, cache_dir, "image")
    if match is None:
        raise typer.Exit(code=1)

    full_key, meta = match
    filename = str(meta.get("path", ""))

    if filename and (images_dir / filename).exists():
        pass
    else:
        found = any(
            (images_dir / f"{full_key}{ext}").exists() for ext in SUPPORTED_IMAGE_EXTENSIONS
        )
        if not found:
            print_error(f"Image file not found for ID '{prefix}'")
            raise typer.Exit(code=1)

    set_default_image_entry(cache_dir, full_key)
    print_success(f"Default image set to: {prefix}")


@image_app.command(name="rm")
def image_rm(
    prefixes: Optional[List[str]] = typer.Argument(None, help="Image ID prefixes to remove"),
    images_dir: Optional[Path] = typer.Option(None, "--images-dir", help="Images directory"),
    force: bool = typer.Option(False, "--force", "-f", help="Remove even if referenced by VMs"),
) -> None:
    """Remove cached images by ID prefix.

    Examples:
        mvm image rm abc123
        mvm image rm abc123 def456
    """
    images_dir = images_dir if images_dir is not None else get_images_dir()
    effective_ids: list[str] = list(prefixes) if prefixes else []
    if not effective_ids:
        print_error("Provide at least one image ID prefix")
        raise typer.Exit(code=1)

    cache_dir = get_cache_dir()
    exit_code = 0

    for prefix in effective_ids:
        match = resolve_single_by_id_prefix(prefix, find_images_by_id_prefix, cache_dir, "image")
        if match is None:
            if not find_images_by_id_prefix(cache_dir, prefix):
                print_error(f"No image found with ID prefix '{prefix}'")
            else:
                print_error(
                    f"Ambiguous ID prefix '{prefix}' matches {len(find_images_by_id_prefix(cache_dir, prefix))} images — use more characters"
                )
            exit_code = 1
            continue

        full_key, meta = match
        filename = str(meta.get("path", ""))
        files_to_remove: list[Path] = []

        if filename:
            candidate = images_dir / filename
            if candidate.exists():
                files_to_remove.append(candidate)

        if not files_to_remove:
            files_to_remove = [
                images_dir / f"{full_key}{ext}"
                for ext in SUPPORTED_IMAGE_EXTENSIONS
                if (images_dir / f"{full_key}{ext}").exists()
            ]

        if not files_to_remove:
            print_error(
                f"Image file not found for ID '{prefix}' (metadata exists but file missing)"
            )
            remove_image_entry(cache_dir, full_key)
            exit_code = 1
            continue

        # Check if image is referenced by any VMs
        for path in files_to_remove:
            referencing_vms = _get_vms_using_image(path)
            if referencing_vms and not force:
                print_warning(
                    f"Image '{prefix}' is referenced by active VMs: {', '.join(referencing_vms)}"
                )
                print_info("Use --force to remove anyway (this may break those VMs)")
                exit_code = 1
                break
        else:
            # No referencing VMs found (or force is True) - proceed with removal
            for path in files_to_remove:
                if path.is_dir():
                    shutil.rmtree(path)
                else:
                    path.unlink()
                print_success(f"Removed: {path}")

            remove_image_entry(cache_dir, full_key)

    raise typer.Exit(code=exit_code)


@image_app.command(name="inspect")
def image_inspect(
    prefix: str = typer.Argument(..., help="Image ID prefix to inspect"),
    json_output: bool = typer.Option(False, "--json", help="Output as JSON"),
    tree: bool = typer.Option(False, "--tree", help="Output in tree format"),
    images_dir: Optional[Path] = typer.Option(None, "--images-dir", help="Images directory"),
) -> None:
    """Show detailed information about an image.

    Examples:
        mvm image inspect abc123
        mvm image inspect abc123 --json
        mvm image inspect abc123 --tree
    """
    from datetime import datetime

    images_dir = images_dir if images_dir is not None else get_images_dir()
    cache_dir = get_cache_dir()

    match = resolve_single_by_id_prefix(prefix, find_images_by_id_prefix, cache_dir, "image")
    if match is None:
        if not find_images_by_id_prefix(cache_dir, prefix):
            print_error(f"No image found with ID prefix '{prefix}'")
        else:
            print_error(
                f"Ambiguous ID prefix '{prefix}' matches {len(find_images_by_id_prefix(cache_dir, prefix))} images — use more characters"
            )
        raise typer.Exit(code=1)

    full_id, meta = match

    filename = str(meta.get("path", ""))
    found_path = None
    if filename:
        candidate = images_dir / filename
        if candidate.exists():
            found_path = candidate
    if not found_path:
        for ext in SUPPORTED_IMAGE_EXTENSIONS:
            candidate = images_dir / f"{full_id}{ext}"
            if candidate.exists():
                found_path = candidate
                break

    is_missing = is_file_missing(found_path)

    pulled_at = meta.get("pulled_at")
    if pulled_at:
        try:
            dt = datetime.fromisoformat(str(pulled_at).replace("Z", "+00:00"))
            pulled_str = dt.strftime("%Y/%m/%d %H:%M:%S")
        except (ValueError, AttributeError):
            pulled_str = str(pulled_at)
    else:
        pulled_str = "-"

    original_size = meta.get("original_size")
    compressed_size = meta.get("compressed_size")
    compression_ratio = meta.get("compression_ratio")
    compressed_format = meta.get("compressed_format", "-")

    original_size_str = format_bytes_human_readable(int(original_size)) if original_size else "-"
    compressed_size_str = (
        format_bytes_human_readable(int(compressed_size)) if compressed_size else "-"
    )
    ratio_str = f"{float(compression_ratio):.2f}x" if compression_ratio else "-"

    file_size_str = "-"
    if found_path and found_path.exists():
        try:
            file_size = found_path.stat().st_size
            file_size_str = format_bytes_human_readable(file_size)
        except OSError:
            pass

    info = {
        "id": full_id,
        "name": str(meta.get("os_name", "-")),
        "os_slug": str(meta.get("os_slug", "-")),
        "filename": filename or "-",
        "fs_type": str(meta.get("fs_type", "-")),
        "fs_uuid": str(meta.get("fs_uuid", "-")),
        "pulled_at": pulled_str,
        "original_size": original_size_str,
        "compressed_size": compressed_size_str,
        "compression_ratio": ratio_str,
        "compressed_format": str(compressed_format),
        "file_size": file_size_str,
        "missing": is_missing,
    }

    if json_output:
        typer.echo(json.dumps(info, indent=2))
        return

    if tree:
        _print_image_details_tree(info, found_path)
    else:
        _print_image_details(info, found_path)


# Mapping from CLI detector names to internal detector names
CLI_TO_INTERNAL_DETECTOR = {
    "type": "type_code",
    "label": "label",
    "size": "size",
    "filesystem": "filesystem",
}


@image_app.command(name="import")
def image_import(
    name: str = typer.Argument(..., help="Display name for the imported image"),
    source_path: Path = typer.Argument(..., help="Path to local image file"),
    format: str = typer.Option(
        None,
        "--format",
        help="Image format: qcow2, raw, tar-rootfs, or auto",
    ),
    convert_to: str = typer.Option(None, "--convert-to", help="Target filesystem format"),
    size_mib: int = typer.Option(None, "--size-mib", help="Size in MiB for tar-rootfs import"),
    force: bool = typer.Option(False, "--force", "-f", help="Overwrite existing"),
    set_default: bool = typer.Option(False, "--set-default", help="Set as default after import"),
    images_dir: Optional[Path] = typer.Option(None, "--images-dir", help="Output directory"),
    disable_detector: Optional[str] = typer.Option(
        None,
        "--disable-detector",
        help="Comma-separated detectors to disable: type,label,size,filesystem,all",
    ),
    no_prompt: bool = typer.Option(
        False, "--no-prompt", help="Exit with error on detection failure"
    ),
) -> None:
    """Import a local image file (qcow2, raw, tar-rootfs). The first argument is a display name."""
    from datetime import datetime, timezone

    images_dir = images_dir if images_dir is not None else get_images_dir()

    if not source_path.exists():
        print_error(f"Source file not found: {source_path}")
        raise typer.Exit(code=1)

    # Parse disabled detectors
    disabled_detectors: list[str] = []
    if disable_detector:
        names = [n.strip() for n in disable_detector.split(",")]
        for detector_name in names:
            if detector_name == "all":
                disabled_detectors = list(CLI_TO_INTERNAL_DETECTOR.values())
                break
            elif detector_name in CLI_TO_INTERNAL_DETECTOR:
                disabled_detectors.append(CLI_TO_INTERNAL_DETECTOR[detector_name])
            else:
                print_error(
                    f"Unknown detector: {detector_name}. Valid: type,label,size,filesystem,all"
                )
                raise typer.Exit(code=1)

    timestamp = datetime.now(tz=timezone.utc).isoformat()
    full_id = generate_full_hash_image(source_path, name, timestamp)
    image_id = full_id

    resolved_format: str | None = format
    # Runtime resolution: if no format specified, use default and auto-detect from filename
    if resolved_format is None:
        resolved_format = DEFAULT_IMAGE_IMPORT_FORMAT
    if resolved_format == DEFAULT_IMAGE_IMPORT_FORMAT:
        fname = source_path.name.lower()
        resolved_format = next(
            (fmt for ext, fmt in IMAGE_IMPORT_FORMAT_MAP.items() if fname.endswith(ext)),
            None,
        )
    if resolved_format is None:
        print_error(
            f"Cannot auto-detect format from '{source_path.name}'. "
            "Use --format qcow2|raw|tar-rootfs."
        )
        raise typer.Exit(code=1)

    spec = ImageImportSpec(
        id=image_id,
        name=name,
        source_path=source_path,
        format=str(resolved_format),
        convert_to=convert_to,
        minimum_rootfs_size=size_mib,
        disabled_detectors=disabled_detectors,
    )

    try:
        result = _handle_partition_detection_retry(
            import_image,
            spec,
            images_dir,
            no_prompt=no_prompt,
            disabled_detectors=disabled_detectors,
            force=force,
        )
    except ImageError as exc:
        print_error(str(exc))
        raise typer.Exit(code=1)

    result_path = result.path
    result_fs_type = result.fs_type
    result_fs_uuid = result.fs_uuid

    _save_image_meta(
        image_id,
        result_path,
        {
            "os_name": name,
            "full_hash": full_id,
            "path": result_path.name,
        },
        fs_type=result_fs_type,
        fs_uuid=result_fs_uuid,
        compressed_size=result.compressed_size,
        original_size=result.original_size,
        compression_ratio=result.compression_ratio,
    )
    short_id = shorten_hash(full_id, 12)
    print_success(f"Image imported: {result_path}")
    print_info(f"  Name: {name}")
    print_info(f"  ID:   {short_id}")

    if set_default:
        set_default_image_entry(get_cache_dir(), image_id)
        print_success(f"Default image set to: {image_id}")

    raise typer.Exit(code=0)


def _format_bin_row(bv: BinaryVersion, is_missing: bool = False) -> list[str]:
    version = get_combined_marker(bv.is_active, is_missing) + bv.version
    return [version, str(bv.firecracker_path)]


@bin_app.command(name="ls")
def bin_ls(
    remote: bool = typer.Option(False, "--remote", "-r", help="Also show remote versions"),
    limit: int = typer.Option(None, "--limit", help="Max remote versions to show"),
    json_output: bool = typer.Option(False, "--json", help="Output as JSON"),
) -> None:
    """List local (and optionally remote) Firecracker versions."""
    from mvmctl.api.metadata import list_binary_entries

    local = list_local_versions()
    local_versions = {bv.version for bv in local}

    # Also check metadata for binaries with missing files
    binary_meta = list_binary_entries(get_cache_dir())
    missing_binaries: list[tuple[str, Path]] = []

    for name, entry in binary_meta.items():
        binary_path = entry.get("binary_path", "")
        if binary_path:
            path = Path(binary_path)
            if not path.exists():
                # Extract version from path or entry
                version = entry.get("full_version", "").lstrip("v")
                if version and version not in local_versions:
                    missing_binaries.append((version, path))

    if json_output:
        import json

        data = [
            {
                "active": bv.is_active,
                "version": bv.version,
                "path": str(bv.firecracker_path) if bv.firecracker_path else "",
                "missing": False,
            }
            for bv in local
        ]
        # Add missing binaries
        for version, path in missing_binaries:
            data.append(
                {
                    "active": False,
                    "version": version,
                    "path": str(path),
                    "missing": True,
                }
            )
        print(json.dumps(data, indent=2))
        return

    if local or missing_binaries:
        rows = [_format_bin_row(bv, is_missing=False) for bv in local]
        # Add missing binaries with X mark
        for version, path in missing_binaries:
            rows.append([get_combined_marker(False, True) + version, str(path)])
        print_table(columns=["Version", "Path"], rows=rows)
    else:
        # Check if there are any binaries in metadata with existing files
        # that might not be in the standard bin directory
        meta_rows: list[list[str]] = []
        found_in_meta = False
        for name, entry in binary_meta.items():
            binary_path = entry.get("binary_path", "")
            if binary_path:
                path = Path(binary_path)
                if path.exists():
                    found_in_meta = True
                    version = entry.get("full_version", "").lstrip("v")
                    is_default = entry.get("is_default") == 1
                    version_str = get_combined_marker(is_default, False) + version
                    meta_rows.append([version_str, str(path)])
        if found_in_meta:
            print_table(columns=["Version", "Path"], rows=meta_rows)
        else:
            print_warning("No local binaries found")

    if remote:
        try:
            remote_versions = list_remote_versions(limit=limit)
        except BinaryError as exc:
            print_error(str(exc))
            raise typer.Exit(code=1)

        rows = []
        for ver in remote_versions:
            cached = "✓" if ver in local_versions else " "
            rows.append([cached, ver])

        print_table(columns=["Downloaded", "Version"], rows=rows)


@bin_app.command(name="fetch")
def bin_fetch(
    version: str = typer.Argument(..., help="Version to download (e.g. 1.15.0)"),
) -> None:
    """Download a specific Firecracker version."""
    try:
        bv = fetch_binary(version)
    except BinaryError as exc:
        print_error(str(exc))
        raise typer.Exit(code=1)

    print_success(f"Downloaded v{bv.version}: {bv.firecracker_path}")
    if bv.is_active:
        print_success(f"Default binary set to v{bv.version}")


@bin_app.command(name="set-default")
def bin_set_default(
    version: str = typer.Argument(..., help="Version to set as active default"),
) -> None:
    """Set the active Firecracker binary version."""
    try:
        set_active_version(version)
    except AssetNotFoundError as exc:
        print_error(str(exc))
        raise typer.Exit(code=1)
    print_success(f"Active version set to {version}")


@bin_app.command(name="rm")
def bin_rm(
    versions: Optional[List[str]] = typer.Argument(None, help="Version(s) to remove"),
) -> None:
    """Remove one or more cached Firecracker versions."""
    effective_versions: list[str] = list(versions) if versions else []
    if not effective_versions:
        print_error("Provide at least one version to remove")
        raise typer.Exit(code=1)

    exit_code = 0
    for version in effective_versions:
        try:
            remove_version(version)
            print_success(f"Removed v{version}")
        except AssetNotFoundError as exc:
            print_error(str(exc))
            exit_code = 1

    raise typer.Exit(code=exit_code)
