# API Layer Refactor — Image/Kernel/Binary CLI → API Migration

**Date:** 2026-04-08
**Status:** Kernel redesign in progress (free-refactor agent `bg_5030e305`)
**Goal:** Eliminate all resolution logic from CLI layer; enforce API as sole orchestration layer; replace field-sprawl save functions with `register_*` record-based API.

---

## Background: What Is Wrong

### Violation 1 — Resolution logic in CLI

The CLI layer (`cli/bin.py`) currently contains business logic that must live in the API layer:

| Function in `cli/bin.py` | What it does | Belongs in |
|---|---|---|
| `_resolve_image_spec(images, selector, version)` | YAML spec lookup + validation | `api/image.py` |
| `_validate_image_type_selector(type, selector, images)` | Input cross-validation | `api/image.py` |
| `_find_existing_image_files(spec, images_dir)` | DB + filesystem existence check | `api/image.py` |
| `_fetch_image_with_partition_retry(...)` | Partition retry orchestration | `api/image.py` |
| `_persist_image_result(result, spec, set_default)` | Hash generation + metadata save + set-default | `api/image.py` |
| `_find_image_by_os_slug(all_meta, os_slug)` | DB metadata lookup | `api/metadata.py` |
| `_find_local_image_path(images_dir, image_id)` | Filesystem path resolution | `api/image.py` |
| `_resolve_image_file(images_dir, image_id, meta)` | Path resolution | `api/image.py` |
| `_load_image_meta(image_id)` | Metadata read | `api/metadata.py` (already `get_image_entry`) |
| `_save_image_meta(...)` — 9 individual args | Metadata write | `api/image.py` → `register_fetched_image()` |

Same pattern for kernel helpers in `cli/bin.py`.

### Violation 2 — Save-metadata functions take individual fields instead of records

```python
# CURRENT (wrong)
_save_image_meta(
    image_id, result.path,
    {"os_name": ..., "os_slug": ..., "full_hash": ..., "path": ...},
    fs_type=result.fs_type, fs_uuid=result.fs_uuid,
    compressed_size=result.compressed_size,
    original_size=result.original_size,
    compression_ratio=result.compression_ratio, arch=spec.arch,
)

# CORRECT
register_fetched_image(result, spec)  # result + spec are the records
```

The same applies to `save_kernel_metadata` (5 individual args) and binary persistence in `fetch_binary` / `set_active_version`.

---

## Core Design: The `register_*` Pattern

Every asset fetch/import flow follows this pattern:

```
CLI (thin) → API orchestrator → Core (isolated) → API register_* → DB
```

### Rule: CLI does ONLY

1. Parse user input (typer arguments)
2. Call ONE API function
3. Format output (`print_success`, `print_error`, etc.)

### Rule: API does EVERYTHING else

1. Resolve YAML specs (single resolution — not 3×)
2. Check DB for existing assets
3. Call core for the actual work
4. Assemble the record from result + spec
5. Persist to DB via `update_*_entry()`
6. Handle retry/partition logic internally
7. Return structured result to CLI

### Rule: `register_fetched_*` signature principle

```python
# WRONG — field sprawl
def save_metadata(id, path, meta_dict, fs_type=None, fs_uuid=None, compressed_size=None, ...):

# CORRECT — takes result + spec records
def register_fetched_image(result: ImageImportResult, spec: ImageSpec) -> str:
    """Persist image record. Returns full image ID."""
    # 1. Build record from result + spec
    # 2. upsert via update_image_entry()
    # 3. Return full_id
```

All fields come from the two input objects. No individual scalar args beyond those two objects.

---

## Layer Rules (from AGENTS.md)

| Layer | May do | Forbidden |
|---|---|---|
| **CLI** | Parse args, call API, format output | DB queries, metadata assembly, business logic |
| **API** | Resolve DB defaults, orchestrate core, assemble records, persist | — |
| **Core** | Execute isolated operations (download, build, convert) | DB queries, default resolution, orchestration |
| **Models** | Pure data containers | Business logic, DB access |

**CLI never imports from `mvmctl.core.metadata` or `mvmctl.db`. CLI passes raw user input to API and formats API output.**

---

## Commit Order

Each commit is **independent** and **reviewable** in isolation. CI must pass after each.

---

### Commit 1: `feat(api): create api/image.py with image fetch orchestration`

**Status:** Waiting on kernel redesign (pattern prototype)
**Files:** `src/mvmctl/api/image.py` (new), `src/mvmctl/api/assets.py` (remove image funcs), `src/mvmctl/cli/bin.py` (remove helpers)

**What it does:**

Creates `api/image.py` with all image API functions, following the pattern established by `api/kernel.py`.

#### Public API surface (`api/image.py`)

```python
# Resolution
def resolve_image_spec(
    images: list[Any], selector: str, version: str | None
) -> ImageSpec:
    """Resolve ImageSpec from YAML config by selector and optional version."""

def validate_image_type_selector(
    image_type: str | None, image_selector: str, images: list[Any]
) -> None:
    """Raise UsageError if --type and selector conflict."""

# Persistence
def register_fetched_image(result: ImageImportResult, spec: ImageSpec) -> str:
    """Persist image to DB after successful fetch/import. Returns full image ID.
    
    Takes ImageImportResult (from core/image.py) and ImageSpec (from models/image.py).
    Assembles ImageItem, generates full hash, upserts via update_image_entry().
    """
    from datetime import datetime, timezone
    from mvmctl.models.image import ImageItem

    timestamp = datetime.now(tz=timezone.utc).isoformat()
    full_id = generate_full_hash_image(result.path, spec.id, timestamp)

    record = ImageItem(
        id=full_id,
        os_slug=spec.id,
        path=result.path.name,
        os_name=spec.name,
        fs_type=result.fs_type,
        fs_uuid=result.fs_uuid,
        compressed_size=result.compressed_size,
        original_size=result.original_size,
        compression_ratio=result.compression_ratio,
        compressed_format="zst",
        arch=spec.arch,
        pulled_at=timestamp,
    )
    update_image_entry(get_cache_dir(), full_id, **record.to_dict())
    return full_id

# Orchestrators
def find_existing_image_files(spec: ImageSpec, images_dir: Path) -> list[Path]:
    """Check filesystem + DB for existing files for this image spec.
    
    Returns list of existing Paths. Called by CLI before fetch to decide
    whether to prompt user or skip.
    """

def fetch_image_and_register(
    spec: ImageSpec,
    output_dir: Path,
    force: bool = False,
    partition: int | None = None,
    skip_optimization: bool = False,
    no_prompt: bool = False,
) -> ImageImportResult:
    """Fetch image from remote URL, handle partition detection/retry, persist to DB.
    
    Flow:
    1. find_existing_image_files() → skip if exists and not force
    2. fetch_image(spec, output_dir, force, skip_optimization)
       - catches RootPartitionDetectionError, TieDetectedError
       - if no_prompt=False and error: prompt CLI (partition selection stays in CLI)
       - retry with partition
    3. register_fetched_image(result, spec)
    4. return ImageImportResult (caller formats output)
    
    NOTE: partition selection prompt stays in CLI (user interaction).
    """

def import_image_and_register(
    spec: ImageImportInput,
    output_dir: Path,
    force: bool = False,
    partition: int | None = None,
) -> ImageImportResult:
    """Import local image file, convert, persist to DB.
    
    Same pattern as fetch_image_and_register but for local source files.
    Separate function because import path (local file → conversion) differs from
    fetch path (remote URL → download → conversion).
    """
```

#### What to remove from `cli/bin.py`

Delete all of these (they move to `api/image.py`):
- `_resolve_image_spec`
- `_validate_image_type_selector`
- `_find_existing_image_files`
- `_check_and_confirm_existing` — **split**: existence check in API (`find_existing_image_files`), user prompt stays in CLI
- `_fetch_image_with_partition_retry`
- `_persist_image_result`
- `_find_image_by_os_slug` → `api/metadata.py`
- `_find_local_image_path`
- `_resolve_image_file`
- `_load_image_meta` → replaced by `api/metadata.get_image_entry()`
- `_save_image_meta` → replaced by `api/image.register_fetched_image()`

#### What `image_fetch` CLI command becomes

```python
@image_app.command(name="fetch")
def image_fetch(
    image_selector: str = typer.Argument(...),
    image_type: Optional[str] = typer.Option(None, "--type", ...),
    version: Optional[str] = typer.Option(None, "--version", ...),
    arch: Optional[str] = typer.Option(None, "--arch", ...),
    out: Optional[Path] = typer.Option(None, "--out", ...),
    force: bool = typer.Option(False, "--force", "-f", ...),
    set_default: bool = typer.Option(False, "--set-default", ...),
    no_prompt: bool = typer.Option(False, "--no-prompt", ...),
    skip_optimization: bool = typer.Option(False, "--skip-optimization", ...),
) -> None:
    # SETUP
    images_dir = out or get_images_dir()
    images_dir.mkdir(parents=True, exist_ok=True)
    images = load_images_config(get_assets_dir() / "images.yaml")

    # VALIDATE (API call — raises UsageError on conflict)
    validate_image_type_selector(image_type, image_selector, images)

    # RESOLVE (API call — one resolution)
    spec = resolve_image_spec(images, image_type or image_selector, version)
    spec.arch = arch or DEFAULT_IMAGE_ARCH

    # GUARD (CLI — user confirmation; API for existence check)
    if not force:
        existing = find_existing_image_files(spec, images_dir)
        if existing:
            print_warning(f"Image '{spec.id}' already exists locally:")
            for path in existing:
                print_info(f"  {path}")
            meta = get_image_entry(get_cache_dir(), spec.id)
            if meta.get("pulled_at"):
                print_info(f"    Pulled: {meta['pulled_at'][:19]}")
            if not typer.confirm("Re-download anyway?", default=False):
                if set_default:
                    set_default_image_by_os_slug(get_cache_dir(), spec.id)
                raise typer.Exit(code=0)

    # EXECUTE (API — handles partition retry internally when no_prompt=False)
    try:
        result = fetch_image_and_register(
            spec, images_dir, force=force,
            skip_optimization=skip_optimization, no_prompt=no_prompt,
        )
    except (RootPartitionDetectionError, TieDetectedError) as exc:
        if no_prompt:
            print_error(str(exc))
            raise typer.Exit(code=1)
        tied = exc.tied_partitions if isinstance(exc, TieDetectedError) else None
        selected = _prompt_for_partition_selection(exc.partitions, tied_partitions=tied)
        result = fetch_image_and_register(
            spec, images_dir, force=True,
            partition=selected, skip_optimization=skip_optimization,
        )

    # OUTPUT (CLI — no business logic)
    short_id = shorten_hash(result.path.name, 12)  # Extract from path
    print_success(f"Image ready: {result.path}")
    print_info(f"  ID: {short_id}")
    if set_default:
        set_default_image_by_os_slug(get_cache_dir(), spec.id)
        print_success(f"Default image set to: {spec.id}")
```

**Key insight:** The partition retry loop stays in CLI because it requires `_prompt_for_partition_selection()` which is a user-interaction function. The API function `fetch_image_and_register` handles the retry when `partition=` is passed, but CLI prompts for partition selection on detection failure.

#### What `image_import` CLI command becomes

```python
@image_app.command(name="import")
def image_import(
    name: str = typer.Argument(...),
    source: Path = typer.Argument(...),
    format: ImageFormat = typer.Option("qcow2", "--format", ...),
    convert_to: str = typer.Option("ext4", "--convert-to", ...),
    arch: Optional[str] = typer.Option(None, "--arch", ...),
    out: Optional[Path] = typer.Option(None, "--out", ...),
    force: bool = typer.Option(False, "--force", "-f", ...),
    partition: Optional[int] = typer.Option(None, "--partition", ...),
    set_default: bool = typer.Option(False, "--set-default", ...),
) -> None:
    # SETUP
    images_dir = out or get_images_dir()
    images_dir.mkdir(parents=True, exist_ok=True)
    spec = ImageImportInput(
        id=generate_image_id(source, format),  # or from user
        name=name,
        source_path=source,
        format=format,
        convert_to=convert_to,
        arch=arch or DEFAULT_IMAGE_ARCH,
    )

    # EXECUTE
    result = import_image_and_register(spec, images_dir, force=force, partition=partition)

    # OUTPUT
    short_id = shorten_hash(result.path.name, 12)
    print_success(f"Image imported: {result.path}")
    print_info(f"  Name: {name}")
    print_info(f"  ID:   {short_id}")
    if set_default:
        set_default_image_by_os_slug(get_cache_dir(), spec.id)
        print_success(f"Default image set to: {spec.id}")
```

#### Remove from `api/assets.py`

- `load_images_config` (move to `api/image.py`)
- All image-related functions that are replaced by `api/image.py`

**Success criteria:**
- `image_fetch` and `image_import` work identically after refactor
- No `list_image_entries`, `get_cache_dir`, `update_image_entry` called from `cli/bin.py`
- `register_fetched_image(result, spec)` is the only metadata persistence call for images
- All existing tests pass

---

### Commit 2: `feat(api): create api/kernel.py — kernel API functions`

**Status:** Handled by free-refactor agent `bg_5030e305` — IN PROGRESS
**Files:** `src/mvmctl/api/kernel.py` (new), `src/mvmctl/api/assets.py` (remove kernel funcs), `src/mvmctl/cli/bin.py` (remove helpers)

#### Design decisions from free-refactor agent

The agent is resolving these in real-time:
- [ ] `KernelPipelineResult` → simplify to flat `KernelBuildResult(path, version, arch, type, warnings, infos)`
- [ ] `download_firecracker_kernel` → return `KernelFetchResult(path, version, arch, type, warnings, infos)` instead of bare `Path`
- [ ] Single `fetch_kernel(spec, output_dir, ...)` orchestrator for both firecracker and official paths
- [ ] `register_fetched_kernel(result, spec)` replacing `save_kernel_metadata(kernels_dir, kernel_name, version, type, arch)`
- [ ] `resolve_kernel_spec` — single resolution in API, not 3× in firecracker path
- [ ] Remove `_fetch_firecracker_kernel` and `_build_official_kernel` from CLI

#### What `kernel_fetch` CLI command becomes (target)

```python
@kernel_app.command(name="fetch")
def kernel_fetch(
    kernel_type: Optional[str] = typer.Option(None, "--type", ...),
    firecracker: bool = typer.Option(False, "--firecracker", ...),
    official: bool = typer.Option(False, "--official", ...),
    version: Optional[str] = typer.Option(None, "--version", ...),
    arch: Optional[str] = typer.Option(None, "--arch", ...),
    out: Optional[Path] = typer.Option(None, "--out", ...),
    name: Optional[str] = typer.Option(None, "--name", ...),
    jobs: Optional[int] = typer.Option(None, "--jobs", "-j", ...),
    keep_build_dir: bool = typer.Option(False, "--keep-build-dir", ...),
    clean_build: bool = typer.Option(False, "--clean-build", ...),
    kernel_config: Optional[Path] = typer.Option(None, "--kernel-config", ...),
    set_default: bool = typer.Option(False, "--set-default", ...),
) -> None:
    # SETUP
    kernels_dir = get_kernels_dir()
    kernels_dir.mkdir(parents=True, exist_ok=True)

    # VALIDATE (CLI — mutual exclusivity of --firecracker/--official/--type)
    if firecracker and official:
        print_error("--firecracker cannot be combined with --official")
        raise typer.Exit(code=1)
    if firecracker and kernel_type and kernel_type != KERNEL_TYPE_FIRECRACKER:
        print_error("--firecracker cannot be combined with a different --type value")
        raise typer.Exit(code=1)
    if official and kernel_type and kernel_type != KERNEL_TYPE_OFFICIAL:
        print_error("--official cannot be combined with a different --type value")
        raise typer.Exit(code=1)

    resolved_type = (
        KERNEL_TYPE_FIRECRACKER if firecracker else
        KERNEL_TYPE_OFFICIAL if official else
        kernel_type
    )
    if resolved_type is None:
        print_error("Provide --type <kernel-type> or use --firecracker/--official")
        raise typer.Exit(code=1)

    # RESOLVE + EXECUTE (ONE API call — spec resolution happens inside)
    try:
        result = fetch_kernel(
            spec=None,  # API resolves from type + version
            kernel_type=resolved_type,
            version=version,
            output_dir=kernels_dir,
            out=out,
            name=name,
            arch=arch or DEFAULT_IMAGE_ARCH,
            jobs=jobs,
            keep_build_dir=keep_build_dir,
            clean_build=clean_build,
            kernel_config=kernel_config,
            set_default=set_default,
        )
    except KernelError as exc:
        print_error(str(exc))
        raise typer.Exit(code=1)

    # OUTPUT (CLI — no business logic)
    print_success(f"Kernel ready: {result.path}")
    _print_pipeline_results(result)  # warnings and info_messages
    if set_default:
        print_success(f"Default kernel set to: {result.name}")
```

#### Remove from `cli/bin.py`

- `_fetch_firecracker_kernel`
- `_build_official_kernel`
- `save_kernel_metadata` → replaced by `register_fetched_kernel(result, spec)`

#### Remove from `api/assets.py`

All kernel functions → `api/kernel.py`:
- `resolve_kernel_spec`
- `download_firecracker_kernel`
- `build_kernel_pipeline`
- `save_kernel_metadata`
- `set_default_kernel`
- `get_default_kernel_path`
- `list_kernels`
- `resolve_kernel_path`
- `resolve_kernel_id_path`

**Success criteria:**
- `kernel_fetch --type firecracker` and `kernel_fetch --type official` produce identical output
- `kernel ls`, `kernel set-default`, `kernel rm` all work
- `register_fetched_kernel(result, spec)` is the only kernel metadata persistence call
- All existing tests pass

---

### Commit 3: `refactor(api): replace binary update calls with register_binary()`

**Status:** Ready to implement (small)
**Files:** `src/mvmctl/api/assets.py`

#### Current state

`fetch_binary()` and `set_active_version()` both call `update_binary_entry()` with individual fields inline:

```python
# inline in fetch_binary
update_binary_entry(cache_dir, full_version, **{
    "name": "firecracker",
    "version": version,
    "full_version": release["name"],
    "path": str(jailer_root / f"firecracker-{full_version}")),
    "ci_version": ci_version,
    "is_default": 1 if is_default else 0,
})

# inline in set_active_version
update_binary_entry(cache_dir, version, **{
    "path": str(jailer_root / f"firecracker-{version}"),
    "is_default": 1,
})
```

#### New function

```python
# api/assets.py (or new api/bin.py — TBD, see Q1)
def register_binary(
    result: BinaryVersion,  # from core/binary_manager.py
    is_default: bool = False,
) -> None:
    """Persist binary version record to DB.
    
    Args:
        result: BinaryVersion from core/binary_manager.fetch_binary()
        is_default: Whether to set this as the default binary
    """
    from mvmctl.core.metadata import update_binary_entry
    cache_dir = get_cache_dir()
    
    fields = {
        "name": "firecracker",
        "version": result.version,
        "full_version": result.full_version,
        "path": str(result.path),
        "ci_version": getattr(result, "ci_version", None),
        "is_default": 1 if is_default else 0,
    }
    update_binary_entry(cache_dir, result.version, **fields)
```

#### Replace inline `update_binary_entry` calls

In `fetch_binary()`: replace inline dict assembly with `register_binary(result, is_default)`
In `set_active_version()`: replace inline dict assembly with `register_binary(result, is_default=True)`

**No new file needed** — this goes in `api/assets.py` alongside the remaining binary functions.

**Success criteria:**
- Binary fetch and set-default produce identical output
- No field-sprawl in binary persistence calls
- All existing tests pass

---

### Commit 4: `refactor(cli): remove all resolution logic from cli/bin.py`

**Status:** Last — after commits 1-3 complete
**Files:** `src/mvmctl/cli/bin.py`

This is the cleanup pass after all helpers have migrated to API layer.

#### What must be removed from `cli/bin.py`

**Image helpers (delete entirely):**
```
_find_image_by_os_slug
_find_local_image_path
_resolve_image_file
_load_image_meta
_save_image_meta
_validate_image_type_selector
_find_existing_image_files
_check_and_confirm_existing
_persist_image_result
_fetch_image_with_partition_retry
_resolve_image_spec
_load_images_config  (moved to api/image.py)
```

**Kernel helpers (delete entirely):**
```
_fetch_firecracker_kernel
_build_official_kernel
save_kernel_metadata  (replaced by register_fetched_kernel)
```

**Imports to remove:**
```python
from mvmctl.core.metadata import (
    find_images_by_id_prefix,     # only used by image helpers — DELETE
    find_kernels_by_id_prefix,    # only used by kernel helpers — DELETE
    list_image_entries,           # only used by image helpers — DELETE
    get_image_entry,             # only used by image helpers — DELETE
    update_image_entry,          # only used by image helpers — DELETE
    remove_kernel_entry,         # only used by kernel rm — KEEP (api/metadata.py has it)
)
```

**Imports to add:**
```python
from mvmctl.api.image import (
    resolve_image_spec,
    validate_image_type_selector,
    find_existing_image_files,
    fetch_image_and_register,
    import_image_and_register,
)
from mvmctl.api.kernel import (
    fetch_kernel,
    register_fetched_kernel,
    list_kernels,
    set_default_kernel,
    remove_kernel,
    resolve_kernel_path,
)
from mvmctl.api.metadata import (
    get_image_entry,             # still needed for inspect/ls
    find_images_by_id_prefix,    # still needed for rm/set-default
    find_kernels_by_id_prefix,   # still needed for rm/set-default
    remove_kernel_entry,         # still needed for rm
    set_default_image_by_os_slug,# still needed for set-default
)
```

**Imports to keep (already correct):**
```python
from mvmctl.utils.console import ...   # output formatting
from mvmctl.utils.fs import (
    get_images_dir,
    get_kernels_dir,
    get_cache_dir,
    is_file_missing,
    get_file_size,
    format_bytes_human_readable,
)
from mvmctl.utils.id_lookup import resolve_single_by_id_prefix
```

#### After cleanup, `cli/bin.py` should contain ONLY:

- Typer app definitions (`image_app`, `kernel_app`, `bin_app`)
- Typer command functions (the `*_fetch`, `*_ls`, `*_rm`, `*_set-default` functions)
- Output formatting helpers (`_print_image_details`, `_print_pipeline_results`, etc.)
- User-interaction helpers (`_prompt_for_partition_selection`)
- VM reference helpers (`_get_vms_using_kernel`, `_get_vms_using_image`)

**NO:**
- Metadata assembly
- YAML spec resolution
- DB queries
- Hash generation
- `get_cache_dir()` in command bodies (only in setup blocks)

#### Verify with layer compliance test

After this commit, run:
```bash
grep -n "update_image_entry\|update_kernel_entry\|update_binary_entry\|get_cache_dir\|list_image_entries\|resolve_kernel_spec\|resolve_image_spec" src/mvmctl/cli/bin.py
```
Should return **zero results**.

---

## Data Classes Reference

### Existing records (don't need to change)

```python
# models/image.py
@dataclass
class ImageItem:
    id: str
    os_slug: str
    path: str
    os_name: str | None
    fs_type: str | None
    fs_uuid: str | None
    compressed_size: int | None
    original_size: int | None
    compression_ratio: float | None
    compressed_format: str | None
    pulled_at: str | None
    arch: str | None = None
    is_default: bool = False
    created_at: str | None = None
    updated_at: str | None = None

# models/kernel.py
@dataclass
class KernelItem:
    id: str
    name: str
    version: str
    arch: str
    path: str
    base_name: str | None = None
    type: str | None = None
    is_default: bool = False
    created_at: str | None = None
    updated_at: str | None = None

# core/image.py
@dataclass
class ImageImportResult:
    path: Path
    fs_type: str | None
    fs_uuid: str | None
    compressed_size: int | None = None
    original_size: int | None = None
    shrunk_size: int | None = None
    compression_ratio: float | None = None

# models/image.py
@dataclass
class ImageSpec:
    id: str
    image_type: str
    version: str
    name: str
    source: str
    format: str
    convert_to: str
    minimum_rootfs_size: int
    arch: str = field(default_factory=platform.machine)
    ...

@dataclass
class ImageImportInput:
    id: str
    name: str
    source_path: Path
    format: str
    convert_to: str = "ext4"
    minimum_rootfs_size: int = field(default=2048)
    disabled_detectors: list[str] = field(default_factory=list)
```

### Kernel result dataclass (TBD by free-refactor agent)

The free-refactor agent is determining the final shape of `KernelFetchResult` / simplified `KernelBuildResult`. Target: flat structure with `path, version, arch, kernel_type, warnings, info_messages`.

---

## Files Summary

| File | Change |
|---|---|
| `src/mvmctl/api/image.py` | **NEW** — all image API functions |
| `src/mvmctl/api/kernel.py` | **NEW** — all kernel API functions (free-refactor agent) |
| `src/mvmctl/api/assets.py` | Remove all image and kernel functions; keep binary + shared |
| `src/mvmctl/api/metadata.py` | No changes (already correct) |
| `src/mvmctl/cli/bin.py` | Remove all resolution helpers; slim to input → API → output |
| `src/mvmctl/core/image.py` | No changes (result dataclass already exists) |
| `src/mvmctl/core/kernel.py` | Simplify result dataclass (free-refactor agent) |
| `src/mvmctl/models/image.py` | No changes |
| `src/mvmctl/models/kernel.py` | No changes |

---

## Behavioral Preservation Checklist

After **each** commit, verify:

- [ ] `uv run pytest tests/unit/cli/test_bin.py -x -q` — image and kernel commands work
- [ ] `uv run pytest tests/unit/api/ -x -q` — API tests pass
- [ ] `uv run ruff check src/mvmctl/api/ src/mvmctl/cli/bin.py`
- [ ] `uv run mypy src/mvmctl/api/ src/mvmctl/cli/bin.py`
- [ ] `grep -c "update_image_entry\|get_cache_dir\|resolve_kernel_spec" src/mvmctl/cli/bin.py` → should be 0 after commit 4

Specific scenarios to test manually after full refactor:

```bash
# Image
mvm image ls
mvm image fetch ubuntu-24.04 --dry-run  # if possible
mvm image ls --json

# Kernel  
mvm kernel ls
mvm kernel ls --json
mvm kernel set-default <prefix>

# Binary
mvm bin ls
```

---

## CI Gates (after each commit)

```bash
uv run ruff check src/
uv run ruff format --check src/
uv run mypy src/
uv run pytest tests/ -q --cov=src/mvmctl --cov-fail-under=80
```

All must pass. No test deletion to make things pass.

---

## Status Tracking

| Commit | Description | Status |
|---|---|---|
| 1 | `api/image.py` + image fetch orchestration | Waiting on kernel |
| 2 | `api/kernel.py` + kernel redesign | 🔄 In progress (free-refactor agent) |
| 3 | `register_binary()` for binary persistence | Ready |
| 4 | Slim `cli/bin.py` — remove all helpers | Waiting on 1, 2, 3 |

---

## Key Decisions Log

| Date | Decision | Rationale |
|---|---|---|
| 2026-04-08 | Don't create `api/bin.py` | Binary flow is simple enough; `register_binary()` goes in `api/assets.py` |
| 2026-04-08 | `import_image_and_register()` separate from `fetch_image_and_register()` | Different input types (`ImageImportInput` vs `ImageSpec`) — local vs remote path |
| 2026-04-08 | Partition prompt stays in CLI | User interaction (`typer.confirm`, `input()`) is CLI's job; API handles retry when given partition number |
| 2026-04-08 | Kernel full redesign via `free-refactor` | User confirmed over-engineering requires exhaustive redesign — kernel is pattern prototype for image |
