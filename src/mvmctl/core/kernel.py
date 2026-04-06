"""Kernel download and build utilities."""

import functools
import hashlib
import logging
import os
import re
import shutil
import subprocess
import tarfile
import uuid
from dataclasses import dataclass
from datetime import datetime, timezone
from pathlib import Path
from typing import Any
from urllib.error import URLError
from urllib.request import Request, urlopen

import yaml

from mvmctl.constants import (
    CONST_FILE_PERMS_EXECUTABLE,
    CONST_HTTP_TIMEOUT_SECONDS,
    CONST_MEBIBYTE_BYTES,
    DEFAULT_KERNEL_ARCH,
    DEFAULT_KERNEL_BUILD_JOBS,
    HTTP_TIMEOUT_KERNEL_CONFIG_S,
    HTTP_TIMEOUT_KERNEL_DOWNLOAD_S,
    HTTP_TIMEOUT_SHA256_FETCH_S,
    HTTP_TIMEOUT_SHA256_SIDECAR_S,
    HTTP_USER_AGENT,
    KERNEL_TYPE_FIRECRACKER,
    KERNEL_TYPE_OFFICIAL,
    KERNEL_TYPE_UNKNOWN,
)
from mvmctl.core.metadata import (
    list_kernel_entries,
    set_default_kernel_by_filename,
    update_kernel_entry,
)
from mvmctl.exceptions import ChecksumMismatchError, KernelError, MVMError
from mvmctl.models.kernel import KernelSpec
from mvmctl.utils.fs import get_cache_dir
from mvmctl.utils.full_hash import generate_full_hash_kernel
from mvmctl.utils.progress import download_with_progress
from mvmctl.utils.template import render_optional_template, render_template
from mvmctl.utils.yaml import (
    optional_int,
    optional_str,
    parse_set_val_list,
    require_str,
    require_str_list,
)


# Compatibility wrapper: download_file now uses download_with_progress internally
# This allows tests that patch download_file to still work
def download_file(
    url: str,
    dest: Path,
    expected_sha256: str | None = None,
    show_progress: bool = True,
    timeout: int = CONST_HTTP_TIMEOUT_SECONDS,
    allow_missing_checksum: bool = False,
    resume: bool = False,
    silent_missing_checksum: bool = False,
    title: str = "Downloading",
) -> bool:
    """Download file wrapper that delegates to download_with_progress.

    This maintains compatibility with the old download_file signature
    while using the new progress-based implementation.
    """
    return download_with_progress(
        url=url,
        dest=dest,
        title=title,
        expected_sha256=expected_sha256,
        timeout=timeout,
        allow_missing_checksum=allow_missing_checksum,
        silent_missing_checksum=silent_missing_checksum,
    )


logger = logging.getLogger(__name__)


@dataclass
class KernelConfigResult:
    """Result from kernel configuration step.

    Contains status and any warnings/info that should be displayed by CLI layer.
    """

    success: bool
    missing_settings: list[str]
    warnings: list[str]
    info_messages: list[str]


@dataclass
class KernelBuildResult:
    """Result from kernel build step.

    Contains status and any warnings that should be displayed by CLI layer.
    """

    success: bool
    output_path: Path | None
    warnings: list[str]
    info_messages: list[str]


@dataclass
class KernelPipelineResult:
    """Result from the complete kernel build pipeline.

    Contains build directory path and any warnings/info from all stages
    that should be displayed by CLI layer.
    """

    build_dir: Path
    config_result: KernelConfigResult | None
    build_result: KernelBuildResult | None


_KERNELS_YAML_PATH = Path(__file__).parent.parent / "assets" / "kernels.yaml"
_ASSETS_DIR = _KERNELS_YAML_PATH.parent


@functools.lru_cache(maxsize=1)
def list_kernel_specs() -> dict[str, KernelSpec]:
    try:
        with _KERNELS_YAML_PATH.open("r", encoding="utf-8") as fh:
            data: Any = yaml.safe_load(fh) or {}
    except (OSError, yaml.YAMLError) as exc:
        raise KernelError(f"Failed to load kernels.yaml: {exc}") from exc

    if not isinstance(data, dict):
        raise KernelError("Invalid kernels.yaml: expected mapping at root")

    specs: dict[str, KernelSpec] = {}
    for spec_name, raw_any in data.items():
        if not isinstance(spec_name, str) or not isinstance(raw_any, dict):
            raise KernelError("Invalid kernels.yaml entry format")
        raw: dict[str, Any] = raw_any
        try:
            specs[spec_name] = KernelSpec(
                name=spec_name,
                kernel_type=require_str(raw, "type"),
                version=require_str(raw, "version"),
                source=require_str(raw, "source"),
                output_name=require_str(raw, "output_name"),
                build_dir=require_str(raw, "build_dir"),
                list_url_template=optional_str(raw, "list_url_template"),
                config_url_template=optional_str(raw, "config_url_template"),
                sha256=optional_str(raw, "sha256"),
                sha256_url=optional_str(raw, "sha256_url"),
                parallel_jobs=optional_int(raw, "parallel_jobs"),
                config_fragments=require_str_list(raw, "config_fragments"),
                enabled_configs=require_str_list(raw, "enabled_configs"),
                disabled_configs=require_str_list(raw, "disabled_configs"),
                required_settings=require_str_list(raw, "required_settings"),
                set_val_configs=parse_set_val_list(raw, "set_val_configs"),
            )
        except ValueError as exc:
            raise KernelError(f"Invalid kernels.yaml entry '{spec_name}': {exc}") from exc
    return specs


def select_kernel_specs(
    kernel_type: str | None = None,
    version: str | None = None,
) -> list[KernelSpec]:
    specs = list(list_kernel_specs().values())
    if kernel_type is not None:
        specs = [spec for spec in specs if spec.kernel_type == kernel_type]
    if version is not None:
        specs = [spec for spec in specs if spec.version == version]
    return specs


def resolve_kernel_spec(kernel_type: str, version: str | None = None) -> KernelSpec:
    specs = select_kernel_specs(kernel_type=kernel_type)
    if not specs:
        raise KernelError(f"No kernel specs found for type '{kernel_type}'")

    if version is not None:
        version_matches = [spec for spec in specs if spec.version == version]
        if len(version_matches) == 1:
            return version_matches[0]
        if len(version_matches) > 1:
            names = ", ".join(spec.name for spec in version_matches)
            raise KernelError(
                f"Multiple '{kernel_type}' kernel specs with version '{version}': {names}"
            )
        versions = ", ".join(sorted({spec.version for spec in specs}))
        raise KernelError(
            f"No '{kernel_type}' kernel spec with version '{version}'. Available: {versions}"
        )

    if len(specs) == 1:
        return specs[0]

    versions = ", ".join(sorted({spec.version for spec in specs}))
    raise KernelError(
        f"Multiple '{kernel_type}' kernel specs found. Provide --version. Available: {versions}"
    )


@functools.lru_cache(maxsize=None)
def load_kernel_spec(kernel_name: str) -> KernelSpec:
    """Load a kernel specification from the bundled kernels.yaml.

    All fields are read strictly from the YAML; missing required fields raise
    :class:`KernelError` rather than silently substituting hardcoded values.

    Args:
        kernel_name: Top-level key in kernels.yaml (e.g. ``"kernel-official"``).

    Returns:
        Populated :class:`KernelSpec` for the requested entry.

    Raises:
        KernelError: If the file cannot be read, the key is absent, or a
            required field is missing or wrong-typed.
    """
    specs = list_kernel_specs()
    if kernel_name not in specs:
        raise KernelError(f"Kernel spec '{kernel_name}' not found in kernels.yaml")
    return specs[kernel_name]


_BUILD_LOG_PATTERNS = re.compile(
    r"(?i)(warning|error|cannot find|undefined reference|fatal|note:)",
)


@dataclass
class ParsedKernelFilename:
    """Parsed components from a kernel filename."""

    base_name: str
    version: str
    arch: str


# Re-export for backward compatibility


def parse_kernel_filename(filename: str) -> ParsedKernelFilename:
    """Parse a kernel filename to extract base name, version, and arch.

    Supports formats like:
    - vmlinux-fc-v1.15-x86_64 -> base_name="vmlinux-fc", version="v1.15", arch="x86_64"
    - vmlinux-fc-1.15-arm64 -> base_name="vmlinux-fc", version="1.15", arch="arm64"
    - vmlinux-6.1.102 -> base_name="vmlinux", version="6.1.102", arch="-"
    - vmlinux -> base_name="vmlinux", version="-", arch="-"

    Args:
        filename: Kernel filename (without path)

    Returns:
        ParsedKernelFilename with base_name, version, and arch
    """
    name = filename
    arches = ["x86_64", "amd64", "arm64", "aarch64"]

    arch = "-"
    for a in arches:
        if name.endswith(f"-{a}"):
            arch = a
            name = name[: -(len(a) + 1)]
            break

    version = "-"
    base_name = name

    version_pattern = r"-v?(\d+(?:\.\d+)*)(?:-[a-z]+)?$"
    match = re.search(version_pattern, name)
    if match:
        full_match = match.group(0)
        version_num = match.group(1)
        if full_match.startswith("-v"):
            version = f"v{version_num}"
        else:
            version = version_num
        base_name = name[: match.start()]

    return ParsedKernelFilename(base_name=base_name, version=version, arch=arch)


def download_kernel_source(
    url: str,
    dest: Path,
    expected_sha256: str | None = None,
    allow_missing_checksum: bool = False,
    silent_missing_checksum: bool = False,
) -> None:
    """Download kernel source tarball.

    Args:
        url: URL to download from
        dest: Destination path
        expected_sha256: Optional SHA-256 checksum
        allow_missing_checksum: If True, allow download without checksum
        silent_missing_checksum: If True, skip warnings/prompt when no checksum

    Raises:
        KernelError: If download fails
        ChecksumMismatchError: If checksum verification fails
    """
    logger.info("Downloading kernel from %s", url)
    try:
        download_file(
            url,
            dest,
            expected_sha256,
            timeout=HTTP_TIMEOUT_KERNEL_DOWNLOAD_S,
            allow_missing_checksum=allow_missing_checksum,
            silent_missing_checksum=silent_missing_checksum,
        )
    except ChecksumMismatchError:
        raise
    except MVMError as e:
        raise KernelError(f"Download failed: {e}") from e


def extract_kernel_tarball(
    tarball: Path,
    extract_dir: Path,
) -> Path:
    """Extract kernel tarball.

    Args:
        tarball: Path to tarball
        extract_dir: Directory to extract to

    Returns:
        Path to extracted kernel directory

    Raises:
        KernelError: If extraction fails or kernel directory not found
    """
    try:
        logger.info("Extracting %s...", tarball.name)

        with tarfile.open(tarball, "r:xz") as tar:
            tar.extractall(path=extract_dir, filter="data")

        # Find extracted directory (should be linux-X.Y.Z)
        for item in extract_dir.iterdir():
            if item.is_dir() and item.name.startswith("linux-"):
                logger.info("Extracted to %s", item.name)
                return item

        raise KernelError("Could not find extracted kernel directory")

    except tarfile.TarError as e:
        raise KernelError(f"Extraction failed: {e}") from e


def download_firecracker_config(
    kernel_dir: Path,
    version: str,
    arch: str = DEFAULT_KERNEL_ARCH,
    kernel_spec: KernelSpec | None = None,
) -> None:
    """Download Firecracker microvm kernel config.

    Args:
        kernel_dir: Kernel source directory
        version: Kernel version string (e.g. ``"6.1.102"``); the major.minor
            component is used to select the matching config file.

    Raises:
        KernelError: If download fails
    """
    if kernel_spec is None:
        kernel_spec = resolve_kernel_spec(kernel_type=KERNEL_TYPE_FIRECRACKER)

    config_url_template = kernel_spec.config_url_template
    if not config_url_template:
        raise KernelError(f"Missing 'config_url_template' in kernels.yaml for {kernel_spec.name}")

    major_minor = ".".join(version.split(".")[:2])
    template_vars = {
        "major_minor": major_minor,
        "version": major_minor,
        "arch": arch,
    }
    config_url = config_url_template.format(**template_vars)

    try:
        logger.info("Downloading Firecracker kernel config...")
        req = Request(config_url, headers={"User-Agent": HTTP_USER_AGENT})

        with urlopen(req, timeout=HTTP_TIMEOUT_KERNEL_CONFIG_S) as response:
            config_content = response.read().decode("utf-8")
            config_path = kernel_dir / ".config"

            with open(config_path, "w") as f:
                f.write(config_content)

            logger.info("Config downloaded")

    except URLError as e:
        raise KernelError(f"Failed to download config: {e}") from e


def run_make(
    kernel_dir: Path,
    target: str,
    jobs: int = DEFAULT_KERNEL_BUILD_JOBS,
    capture_output: bool = False,
) -> tuple[int, str, str]:
    """Run make command in kernel directory.

    Args:
        kernel_dir: Kernel source directory
        target: Make target
        jobs: Number of parallel jobs
        capture_output: Whether to capture output

    Returns:
        Tuple of (returncode, stdout, stderr)
    """
    cmd = ["make", target, f"-j{jobs}"]

    if capture_output:
        result = subprocess.run(
            cmd,
            cwd=kernel_dir,
            capture_output=True,
            text=True,
        )
        return result.returncode, result.stdout, result.stderr
    else:
        returncode = subprocess.run(cmd, cwd=kernel_dir).returncode
        return returncode, "", ""


def _run_config_script(config_script: Path, args: list[str], kernel_dir: Path) -> None:
    """Run scripts/config with the given args, logging a warning on failure.

    Args:
        config_script: Path to the kernel scripts/config helper
        args: Arguments to pass (e.g. ["--enable", "CONFIG_FOO"])
        kernel_dir: Kernel source directory (used as cwd)
    """
    result = subprocess.run(
        [str(config_script)] + args,
        cwd=kernel_dir,
        capture_output=True,
        text=True,
    )
    if result.returncode != 0:
        logger.warning(
            "scripts/config %s failed (rc=%d): %s",
            " ".join(args),
            result.returncode,
            result.stderr.strip(),
        )


def _fetch_fragment_content(url: str) -> str:
    req = Request(url, headers={"User-Agent": HTTP_USER_AGENT})
    with urlopen(req, timeout=HTTP_TIMEOUT_SHA256_FETCH_S) as resp:
        raw: bytes = resp.read()
    return raw.decode("utf-8")


def _extract_config_key(config_line: str) -> str | None:
    line = config_line.strip()
    if not line:
        return None

    if line.startswith("# ") and line.endswith(" is not set"):
        key = line[2:-11]
        return key if key.startswith("CONFIG_") else None

    if line.startswith("CONFIG_") and "=" in line:
        return line.split("=", 1)[0]

    return None


def _apply_config_fragments(
    fragments: list[str],
    template_vars: dict[str, str],
    kernel_dir: Path,
) -> None:
    config_path = kernel_dir / ".config"

    for idx, fragment in enumerate(fragments):
        rendered = render_template(fragment, template_vars)
        if rendered.startswith("http://") or rendered.startswith("https://"):
            try:
                content = _fetch_fragment_content(rendered)
            except (URLError, OSError) as exc:
                raise KernelError(f"Failed to fetch config fragment {rendered}: {exc}") from exc
            logger.info("Applying remote config fragment: %s", rendered)
        else:
            rel = rendered[len("assets/") :] if rendered.startswith("assets/") else rendered
            path = _ASSETS_DIR / rel
            if not path.exists():
                raise KernelError(f"Config fragment not found: {path} (from '{fragment}')")
            content = path.read_text(encoding="utf-8")
            logger.info("Applying local config fragment: %s", path)

        if idx == 0 and not config_path.exists():
            base_content = content if content.endswith("\n") else f"{content}\n"
            config_path.write_text(base_content, encoding="utf-8")
            continue

        if not config_path.exists():
            config_path.write_text("", encoding="utf-8")

        existing_lines = config_path.read_text(encoding="utf-8").splitlines()
        key_to_index: dict[str, int] = {}

        for line_index, line in enumerate(existing_lines):
            key = _extract_config_key(line)
            if key:
                key_to_index[key] = line_index

        for fragment_line in content.splitlines():
            normalized = fragment_line.strip()
            key = _extract_config_key(normalized)
            if key is None:
                continue

            if key in key_to_index:
                existing_lines[key_to_index[key]] = normalized
            else:
                key_to_index[key] = len(existing_lines)
                existing_lines.append(normalized)

        merged_content = "\n".join(existing_lines)
        if merged_content:
            merged_content += "\n"
        config_path.write_text(merged_content, encoding="utf-8")


def configure_kernel(
    kernel_dir: Path,
    version: str,
    user_config_path: Path | None = None,
    kernel_spec: KernelSpec | None = None,
    skip_confirm: bool = False,
    arch: str | None = None,
) -> KernelConfigResult:
    """Configure kernel with Firecracker settings.

    Args:
        kernel_dir: Kernel source directory
        version: Kernel version string (e.g. ``"6.1.102"``) used to select the
            matching Firecracker config file.
        user_config_path: Optional path to a user-supplied ``.config`` overlay.
        kernel_spec: Kernel specification with config lists. Defaults to the
            ``kernel-official`` entry from ``kernels.yaml``.
        skip_confirm: If True, skip interactive confirmation for missing settings
            and raise KernelError instead. Used by non-interactive callers.

    Returns:
        KernelConfigResult with status, warnings, and info messages for CLI display.

    Raises:
        KernelError: If configuration fails or required settings are missing
            and skip_confirm is True.
    """
    if kernel_spec is None:
        kernel_spec = resolve_kernel_spec(kernel_type=KERNEL_TYPE_OFFICIAL)

    warnings: list[str] = []
    info_messages: list[str] = []

    effective_arch = arch or DEFAULT_KERNEL_ARCH
    major_minor = ".".join(version.split(".")[:2])
    template_vars = {
        "major_minor": major_minor,
        "version": major_minor,
        "kernel_version": version,
        "ci_version": version,
        "arch": effective_arch,
    }

    try:
        download_firecracker_config(
            kernel_dir,
            version,
            arch=effective_arch,
            kernel_spec=kernel_spec,
        )
        if kernel_spec.config_fragments:
            _apply_config_fragments(kernel_spec.config_fragments, template_vars, kernel_dir)
    except KernelError:
        logger.info("Using defconfig instead...")
        returncode, _, _ = run_make(kernel_dir, "defconfig")
        if returncode != 0:
            raise KernelError("defconfig failed")

    # Sync config to current kernel version
    logger.info("Synchronizing config...")
    returncode, _, _ = run_make(kernel_dir, "olddefconfig")
    if returncode != 0:
        raise KernelError("olddefconfig failed")

    config_script = kernel_dir / "scripts" / "config"

    logger.info("Applying kernel options from kernels.yaml...")
    for option in kernel_spec.enabled_configs:
        _run_config_script(config_script, ["--enable", option], kernel_dir)

    for option in kernel_spec.disabled_configs:
        _run_config_script(config_script, ["--disable", option], kernel_dir)

    for option, value in kernel_spec.set_val_configs:
        _run_config_script(config_script, ["--set-val", option, value], kernel_dir)

    if user_config_path is not None:
        logger.info("Applying user kernel config overlay from %s...", user_config_path)
        import shutil

        shutil.copy2(user_config_path, kernel_dir / ".config")

    logger.info("Resolving dependencies...")
    returncode, _, _ = run_make(kernel_dir, "olddefconfig")
    if returncode != 0:
        raise KernelError("olddefconfig failed after enabling options")

    logger.info("Verifying configuration...")
    config_path = kernel_dir / ".config"
    config_content = config_path.read_text()
    config_lines = set(config_content.splitlines())
    all_present = True
    missing_settings: list[str] = []

    for setting in kernel_spec.required_settings:
        if "=" in setting:
            present = setting in config_lines
        else:
            present = (
                f"{setting}=y" in config_lines
                or f"{setting}=m" in config_lines
                or f"# {setting} is not set" in config_lines
            )

        if present:
            logger.info("  %s", setting)
        else:
            logger.error("  MISSING: %s", setting)
            missing_settings.append(setting)
            all_present = False

    if not all_present:
        warnings.append(f"Required kernel settings missing: {', '.join(missing_settings)}")
        if skip_confirm:
            raise KernelError("Required kernel settings are missing from configuration")
        # Return result indicating missing settings - caller (CLI) should handle confirmation
        return KernelConfigResult(
            success=False,
            missing_settings=missing_settings,
            warnings=warnings,
            info_messages=info_messages,
        )

    return KernelConfigResult(
        success=True,
        missing_settings=[],
        warnings=warnings,
        info_messages=info_messages,
    )


def build_kernel(
    kernel_dir: Path,
    output_path: Path,
    jobs: int = DEFAULT_KERNEL_BUILD_JOBS,
    build_log_path: Path | None = None,
) -> KernelBuildResult:
    """Build the kernel.

    Args:
        kernel_dir: Kernel source directory
        output_path: Where to copy vmlinux
        jobs: Number of parallel jobs
        build_log_path: Optional path to write build log (for caching)

    Returns:
        KernelBuildResult with status, output path, warnings, and info messages.

    Raises:
        KernelError: If build fails
    """
    logger.info("Building vmlinux with %d parallel jobs...", jobs)
    logger.info("This may take 10-30 minutes...")

    warnings: list[str] = []
    info_messages: list[str] = []

    warnings.append("Building kernel... (this may take 10-30 minutes)")

    cmd = ["make", "vmlinux", f"-j{jobs}"]
    temp_log_path: Path | None = None

    if build_log_path is None:
        import tempfile

        with tempfile.NamedTemporaryFile(suffix=".log", delete=False) as tmp_log:
            build_log_path = Path(tmp_log.name)
        temp_log_path = build_log_path

    try:
        with open(build_log_path, "w", encoding="utf-8") as log_file:
            proc = subprocess.Popen(
                cmd,
                cwd=kernel_dir,
                stdout=log_file,
                stderr=subprocess.STDOUT,
            )
            returncode = proc.wait()

        build_output_lines: list[str] = []
        with open(build_log_path, encoding="utf-8") as f:
            for line in f:
                line = line.rstrip("\n")
                logger.debug("%s", line)
                if _BUILD_LOG_PATTERNS.search(line):
                    build_output_lines.append(line)

        if returncode != 0:
            raise KernelError(f"Kernel build failed: Command failed (exit {returncode}): make")

    except OSError as e:
        raise KernelError("Kernel build failed: unable to execute make") from e
    finally:
        if temp_log_path and temp_log_path.exists():
            try:
                temp_log_path.unlink()
            except OSError:
                pass

    # Copy vmlinux to output
    vmlinux_path = kernel_dir / "vmlinux"
    if not vmlinux_path.exists():
        raise KernelError("Build succeeded but vmlinux not found")

    output_path.parent.mkdir(parents=True, exist_ok=True)

    shutil.copy2(vmlinux_path, output_path)
    output_path.chmod(CONST_FILE_PERMS_EXECUTABLE)

    size = output_path.stat().st_size
    size_mb = size / CONST_MEBIBYTE_BYTES
    logger.info("Kernel built: %s (%.1f MiB)", output_path.name, size_mb)

    return KernelBuildResult(
        success=True,
        output_path=output_path,
        warnings=warnings,
        info_messages=info_messages,
    )


def fetch_kernel_sha256_from_url(sha256_url: str) -> str | None:
    try:
        req = Request(sha256_url, headers={"User-Agent": HTTP_USER_AGENT})
        with urlopen(req, timeout=HTTP_TIMEOUT_SHA256_FETCH_S) as resp:
            content = resp.read().decode().strip()
        parts = content.split()
        return str(parts[0]).lower() if parts else None
    except (URLError, OSError):
        return None


def check_build_dependencies() -> list[str]:
    """Check for required kernel build dependencies.

    Returns:
        Empty list if all dependencies are present.

    Raises:
        KernelError: If any dependencies are missing, with install instructions.
    """
    required_commands = ["git", "curl", "make", "gcc", "flex", "bison", "bc", "pahole", "ld"]
    missing_deps: list[str] = []

    # Check for required commands
    for cmd in required_commands:
        if shutil.which(cmd) is None:
            missing_deps.append(cmd)

    # Check for required libraries via pkg-config
    library_checks = [
        ("libelf", "libelf"),
        ("openssl", "libssl-dev"),
    ]

    for pkg_name, display_name in library_checks:
        try:
            result = subprocess.run(
                ["pkg-config", "--exists", pkg_name],
                capture_output=True,
                text=True,
            )
            if result.returncode != 0:
                missing_deps.append(display_name)
        except FileNotFoundError:
            missing_deps.append(display_name)

    if missing_deps:
        missing_str = ", ".join(sorted(missing_deps))
        msg = (
            f"Missing kernel build dependencies: {missing_str}\n"
            "\n"
            "Install on Ubuntu/Debian:\n"
            "  sudo apt update\n"
            "  sudo apt install -y build-essential libncurses-dev bison flex\n"
            "  sudo apt install -y libssl-dev libelf-dev bc curl git dwarves\n"
            "\n"
            "Install on Arch Linux:\n"
            "  sudo pacman -S base-devel ncurses bison flex\n"
            "  sudo pacman -S openssl bc curl git pahole\n"
        )
        raise KernelError(msg)

    return []


def _compute_config_hash(
    version: str,
    user_config_path: Path | None = None,
    kernel_spec: KernelSpec | None = None,
) -> str:
    """Compute a hash of kernel configuration parameters for caching.

    Args:
        version: Kernel version
        user_config_path: Optional path to user config overlay
        kernel_spec: Kernel specification with config lists. Defaults to the
            ``kernel-official`` entry from ``kernels.yaml``.

    Returns:
        Short hash string for cache key
    """
    if kernel_spec is None:
        kernel_spec = resolve_kernel_spec(kernel_type=KERNEL_TYPE_OFFICIAL)

    hasher = hashlib.sha256()
    hasher.update(version.encode())
    hasher.update(str(kernel_spec.config_fragments).encode())
    hasher.update(str(kernel_spec.enabled_configs).encode())
    hasher.update(str(kernel_spec.disabled_configs).encode())
    hasher.update(str(kernel_spec.set_val_configs).encode())
    hasher.update(str(kernel_spec.required_settings).encode())
    if user_config_path and user_config_path.exists():
        hasher.update(user_config_path.read_bytes())
    return hasher.hexdigest()[:16]


def build_kernel_pipeline(
    version: str,
    source_url: str,
    output_path: Path,
    build_dir: Path | None = None,
    sha256: str | None = None,
    jobs: int | None = None,
    keep_build_dir: bool = False,
    user_config_path: Path | None = None,
    arch: str | None = None,
    kernel_spec: KernelSpec | None = None,
    use_cache: bool = True,
) -> KernelPipelineResult:
    check_build_dependencies()

    if kernel_spec is None:
        kernel_spec = resolve_kernel_spec(kernel_type=KERNEL_TYPE_OFFICIAL)

    if jobs is None:
        jobs = os.cpu_count() or 1

    if build_dir is None:
        import tempfile

        from mvmctl.constants import PROJECT_NAME

        build_id = str(uuid.uuid4())[:8]
        build_dir = Path(tempfile.gettempdir()) / PROJECT_NAME / f"build-{build_id}"

    build_dir.mkdir(parents=True, exist_ok=True)

    # Compute config hash for caching
    config_hash = _compute_config_hash(version, user_config_path, kernel_spec)
    cache_key = f"{version}-{config_hash}"
    cache_marker = build_dir.parent / f"kernel-cache-{cache_key}.marker"
    cached_kernel_path = build_dir.parent / f"kernel-cache-{cache_key}.vmlinux"

    if use_cache and cache_marker.exists() and cached_kernel_path.exists():
        output_path.parent.mkdir(parents=True, exist_ok=True)
        shutil.copy2(cached_kernel_path, output_path)
        output_path.chmod(CONST_FILE_PERMS_EXECUTABLE)
        logger.info("Using cached kernel build (config hash match): %s", output_path)
        save_kernel_metadata(
            output_path.parent,
            output_path.name,
            version=version,
            kernel_type=KERNEL_TYPE_OFFICIAL,
            arch=arch,
        )
        return KernelPipelineResult(
            build_dir=build_dir,
            config_result=None,
            build_result=None,
        )

    if use_cache and output_path.exists() and cache_marker.exists():
        logger.info("Using cached kernel (config hash match): %s", output_path)
        save_kernel_metadata(
            output_path.parent,
            output_path.name,
            version=version,
            kernel_type=KERNEL_TYPE_OFFICIAL,
            arch=arch,
        )
        return KernelPipelineResult(
            build_dir=build_dir,
            config_result=None,
            build_result=None,
        )

    if output_path.exists():
        logger.info("Kernel exists but config changed, rebuilding: %s", output_path)
        output_path.unlink(missing_ok=True)

    template_vars = {
        "version": version,
        "kernel_version": version,
        "ci_version": version,
        "arch": str(arch or DEFAULT_KERNEL_ARCH),
    }
    resolved_source_url = (
        render_template(source_url, template_vars) if "{" in source_url else source_url
    )

    intentional_no_checksum = kernel_spec.sha256 is None and kernel_spec.sha256_url is None

    # needs to properly check if sha256 url exist and use it
    if sha256 is None and not intentional_no_checksum:
        resolved_sha256_url = render_optional_template(kernel_spec.sha256_url, template_vars)
        if resolved_sha256_url is not None:
            sha256 = fetch_kernel_sha256_from_url(resolved_sha256_url)

    if sha256 is None and not intentional_no_checksum:
        raise KernelError(f"Checksum required for kernel source download: {resolved_source_url}")

    tarball = build_dir / f"linux-{version}.tar.xz"
    kernel_src_dir = build_dir / f"linux-{version}"

    try:
        if not tarball.exists():
            download_file(
                resolved_source_url,
                tarball,
                title="Downloading kernel source",
                expected_sha256=sha256,
                timeout=HTTP_TIMEOUT_KERNEL_DOWNLOAD_S,
            )
        else:
            logger.info("Using cached tarball: %s", tarball)

        if not kernel_src_dir.exists():
            extract_kernel_tarball(tarball, build_dir)
        else:
            logger.info("Using existing source: %s", kernel_src_dir)

        config_result = configure_kernel(
            kernel_src_dir,
            version,
            user_config_path=user_config_path,
            kernel_spec=kernel_spec,
            arch=arch,
        )

        build_result = build_kernel(kernel_src_dir, output_path, jobs)

        if use_cache:
            shutil.copy2(output_path, cached_kernel_path)
            cache_marker.write_text(cache_key)

        save_kernel_metadata(
            output_path.parent,
            output_path.name,
            version=version,
            kernel_type=KERNEL_TYPE_OFFICIAL,
            arch=arch,
        )
    except Exception:
        raise
    else:
        if not keep_build_dir:
            try:
                shutil.rmtree(build_dir)
                logger.info("Build directory cleaned up: %s", build_dir)
            except OSError as exc:
                logger.warning("Failed to clean up build directory %s: %s", build_dir, exc)
        else:
            logger.info("Build directory kept at: %s", build_dir)

    return KernelPipelineResult(
        build_dir=build_dir,
        config_result=config_result,
        build_result=build_result,
    )


def save_kernel_metadata(
    kernels_dir: Path,
    kernel_name: str,
    version: str | None = None,
    kernel_type: str | None = None,
    arch: str | None = None,
) -> str:
    kernel_path = kernels_dir / kernel_name

    parsed = parse_kernel_filename(kernel_name)

    if version is None:
        version = parsed.version
    if arch is None:
        arch = parsed.arch
    if kernel_type is None:
        kernel_type = KERNEL_TYPE_UNKNOWN

    last_modified = "-"
    if kernel_path.exists():
        mtime = kernel_path.stat().st_mtime
        last_modified = datetime.fromtimestamp(mtime, tz=timezone.utc).isoformat()

    full_id = generate_full_hash_kernel(
        kernel_path,
        version,
        arch,
    )

    cache_dir = get_cache_dir()
    update_kernel_entry(
        cache_dir,
        full_id,
        path=kernel_name,
        full_hash=full_id,
        name=kernel_name,
        base_name=parsed.base_name,
        version=version,
        arch=arch,
        type=kernel_type,
        last_modified=last_modified,
    )
    return full_id


def list_kernels(kernels_dir: Path) -> list[dict[str, str]]:
    kernels_dir.mkdir(parents=True, exist_ok=True)

    cache_dir = get_cache_dir()

    entries = list_kernel_entries(cache_dir, kernels_dir, include_missing=True)

    results: list[dict[str, str]] = []

    for entry_id, meta in sorted(entries.items()):
        path = str(meta.get("path", entry_id))
        kernel_file_path = kernels_dir / path
        # Include entries even if file is missing - CLI will show X mark
        file_exists = kernel_file_path.is_file()

        size_mb = kernel_file_path.stat().st_size / CONST_MEBIBYTE_BYTES if file_exists else 0

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
        is_default_flag = "true" if str(is_default_val) in ("1", "true") else "false"

        results.append(
            {
                "id": entry_id,
                "name": base_name,
                "path": path,
                "full_name": path,
                "version": version,
                "type": kernel_type,
                "arch": arch,
                "last_modified": str(last_modified) if last_modified else "-",
                "size": f"{size_mb:.1f} MiB",
                "is_default": is_default_flag,
            }
        )

    return results


def _load_default_kernel(kernels_dir: Path) -> str | None:
    from mvmctl.api.metadata import get_default_kernel_entry

    default_entry = get_default_kernel_entry(get_cache_dir())
    if default_entry is None:
        return None
    _kernel_id, entry = default_entry
    path = entry.get("path")
    if isinstance(path, str) and path:
        return path
    return None


def set_default_kernel(kernels_dir: Path, kernel_name: str) -> None:
    kernel_path = kernels_dir / kernel_name
    if not kernel_path.exists():
        raise KernelError(f"Kernel not found: {kernel_path}")
    set_default_kernel_by_filename(get_cache_dir(), kernel_name)
    logger.info("Default kernel set to: %s", kernel_name)


def get_default_kernel_path(kernels_dir: Path) -> Path | None:
    name = _load_default_kernel(kernels_dir)
    if name is None:
        return None
    path = kernels_dir / name
    return path if path.exists() else None


def download_firecracker_kernel(
    ci_version: str,
    arch: str = DEFAULT_KERNEL_ARCH,
    kernels_dir: Path | None = None,
    output_name: str | None = None,
    output_path: Path | None = None,
    kernel_spec: KernelSpec | None = None,
) -> Path:
    if kernels_dir is None:
        from mvmctl.utils.fs import get_kernels_dir

        kernels_dir = get_kernels_dir()
    kernels_dir.mkdir(parents=True, exist_ok=True)

    if kernel_spec is None:
        kernel_spec = resolve_kernel_spec(kernel_type=KERNEL_TYPE_FIRECRACKER)
    list_url_template = kernel_spec.list_url_template
    if not list_url_template:
        raise KernelError(f"Missing 'list_url_template' in kernels.yaml for {kernel_spec.name}")

    template_version = kernel_spec.version
    template_vars = {
        "ci_version": ci_version,
        "arch": arch,
        "version": template_version,
    }
    list_url = render_template(list_url_template, template_vars)
    try:
        req = Request(list_url, headers={"User-Agent": HTTP_USER_AGENT})
        with urlopen(req, timeout=HTTP_TIMEOUT_SHA256_FETCH_S) as resp:
            xml_content = resp.read().decode("utf-8")
    except (URLError, OSError) as exc:
        raise KernelError(f"Failed to list CI kernels: {exc}") from exc

    pattern = (
        rf"<Key>(firecracker-ci/{re.escape(ci_version)}/{re.escape(arch)}/vmlinux-[\d.]+)</Key>"
    )
    keys = re.findall(pattern, xml_content)
    if not keys:
        raise KernelError(f"No vmlinux found for Firecracker CI version {ci_version} / arch {arch}")

    keys.sort(key=lambda k: tuple(int(x) for x in k.split("/vmlinux-")[-1].split(".")))
    chosen_key = keys[-1]
    kernel_version = chosen_key.split("/vmlinux-")[-1]

    resolved_output_name = output_name or kernel_spec.output_name
    resolved_output_path = (
        output_path
        if output_path is not None
        else kernels_dir / f"{resolved_output_name}-{kernel_version}-{arch}"
    )

    if resolved_output_path.exists():
        logger.info("Firecracker CI kernel already cached: %s", resolved_output_path)
        save_kernel_metadata(
            kernels_dir,
            resolved_output_path.name,
            version=kernel_version,
            kernel_type=KERNEL_TYPE_FIRECRACKER,
            arch=arch,
        )
        return resolved_output_path

    intentional_no_checksum = kernel_spec.sha256 is None and kernel_spec.sha256_url is None

    template_vars["kernel_version"] = kernel_version
    download_url = f"{kernel_spec.source.rstrip('/')}/{chosen_key}"
    sha256_url = render_optional_template(kernel_spec.sha256_url, template_vars)
    if sha256_url is None and not intentional_no_checksum:
        sha256_url = f"{download_url}.sha256"
    expected_sha256: str | None = None
    if sha256_url is not None:
        try:
            req_sha = Request(sha256_url, headers={"User-Agent": HTTP_USER_AGENT})
            with urlopen(req_sha, timeout=HTTP_TIMEOUT_SHA256_SIDECAR_S) as resp_sha:
                content = resp_sha.read().decode().strip()
            parts = content.split()
            expected_sha256 = str(parts[0]).lower() if parts else None
            logger.info("Fetched CI kernel checksum: %s", expected_sha256)
        except (URLError, OSError):
            logger.debug(
                "No sha256 sidecar for CI kernel %s — proceeding without checksum", chosen_key
            )

    if expected_sha256 is None and not intentional_no_checksum:
        raise KernelError(f"Checksum required for Firecracker CI kernel download: {download_url}")

    logger.info("Downloading Firecracker CI kernel from %s", download_url)
    try:
        download_file(
            download_url,
            resolved_output_path,
            title=f"Downloading kernel {ci_version}",
            expected_sha256=expected_sha256,
            timeout=HTTP_TIMEOUT_SHA256_FETCH_S,
            allow_missing_checksum=True,
            silent_missing_checksum=True,
        )
    except MVMError as exc:
        raise KernelError(f"Failed to download Firecracker CI kernel: {exc}") from exc

    resolved_output_path.chmod(CONST_FILE_PERMS_EXECUTABLE)

    save_kernel_metadata(
        kernels_dir,
        resolved_output_path.name,
        version=kernel_version,
        kernel_type=KERNEL_TYPE_FIRECRACKER,
        arch=arch,
    )
    logger.info("Firecracker CI kernel saved: %s", resolved_output_path)
    return resolved_output_path


def resolve_kernel_path(kernel: str) -> Path:
    from mvmctl.utils.fs import get_cache_dir, get_kernels_dir

    kernels_dir = get_kernels_dir()
    candidate = kernels_dir / kernel
    if candidate.exists():
        return candidate

    direct = Path(kernel)
    if direct.is_absolute() and direct.exists():
        return direct

    from mvmctl.core.metadata import list_kernel_entries

    matches = [
        (k, m)
        for k, m in list_kernel_entries(get_cache_dir(), kernels_dir).items()
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

    raise MVMError(f"Kernel not found: {kernel!r}")


def resolve_kernel_id_path(kernel: str) -> Path:
    from mvmctl.core.metadata import list_kernel_entries
    from mvmctl.utils.fs import get_cache_dir, get_kernels_dir
    from mvmctl.utils.id_lookup import resolve_single_by_id_prefix

    kernels_dir = get_kernels_dir()

    def _find(cache_dir: Path, prefix: str) -> list[tuple[str, dict[str, object]]]:
        return [
            (k, m)
            for k, m in list_kernel_entries(cache_dir, kernels_dir).items()
            if k.startswith(prefix)
        ]

    match = resolve_single_by_id_prefix(kernel, _find, get_cache_dir())
    if match is None:
        raise MVMError(f"Kernel ID not found or ambiguous: {kernel!r}")

    full_key, meta = match
    path = str(meta.get("path", ""))
    if path:
        candidate = kernels_dir / path
        if candidate.exists():
            return candidate
    candidate = kernels_dir / full_key
    if candidate.exists():
        return candidate

    raise MVMError(f"Kernel not found: {kernel!r}")
