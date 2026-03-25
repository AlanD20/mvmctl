"""Kernel download and build utilities."""

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
from urllib.error import URLError
from urllib.request import Request, urlopen

from fcm.constants import (
    DEFAULT_FC_KERNEL_ARCH,
    FALLBACK_KERNEL_BUILD_JOBS,
    FIRECRACKER_CI_KERNEL_LIST_URL,
    FIRECRACKER_CI_KERNEL_S3_BASE,
    FIRECRACKER_KERNEL_CONFIG_URL,
    HTTP_USER_AGENT,
    KERNEL_DISABLED_CONFIGS,
    KERNEL_ENABLED_CONFIGS,
    KERNEL_REQUIRED_SETTINGS,
    KERNEL_SET_VAL_CONFIGS,
    KERNEL_SHA256_URL_TEMPLATE,
)
from fcm.core.metadata import (
    list_kernel_entries,
    migrate_legacy_metadata,
    update_kernel_entry,
)
from fcm.exceptions import ChecksumMismatchError, FCMError, KernelError
from fcm.utils.fs import get_cache_dir, get_images_dir
from fcm.utils.http import download_file

logger = logging.getLogger(__name__)

_BUILD_LOG_PATTERNS = re.compile(
    r"(?i)(warning|error|cannot find|undefined reference|fatal|note:)",
)


@dataclass
class ParsedKernelFilename:
    """Parsed components from a kernel filename."""

    base_name: str
    version: str
    arch: str


def generate_kernel_id(kernel_name: str, last_modified: str) -> str:
    """Generate a short ID from kernel name and last modified date.

    Args:
        kernel_name: Kernel filename
        last_modified: Last modified timestamp

    Returns:
        First 6 characters of SHA256 hash
    """
    data = f"{kernel_name}:{last_modified}"
    hash_full = hashlib.sha256(data.encode()).hexdigest()
    return hash_full[:6]


def human_readable_time(iso_timestamp: str) -> str:
    """Convert ISO timestamp to human-readable relative time.

    Args:
        iso_timestamp: ISO format timestamp (e.g., 2026-03-24T17:37:45.896256+00:00)

    Returns:
        Human-readable string like "2 minutes ago", "1 hour ago", "3 days ago"
    """
    if not iso_timestamp or iso_timestamp == "-":
        return "-"

    try:
        # Parse the ISO timestamp
        dt = datetime.fromisoformat(iso_timestamp)
        now = datetime.now(tz=timezone.utc)
        diff = now - dt

        total_seconds = int(diff.total_seconds())

        if total_seconds < 60:
            return "just now"
        elif total_seconds < 3600:
            minutes = total_seconds // 60
            return f"{minutes} minute{'s' if minutes != 1 else ''} ago"
        elif total_seconds < 86400:
            hours = total_seconds // 3600
            return f"{hours} hour{'s' if hours != 1 else ''} ago"
        elif total_seconds < 604800:
            days = total_seconds // 86400
            return f"{days} day{'s' if days != 1 else ''} ago"
        elif total_seconds < 2592000:
            weeks = total_seconds // 604800
            return f"{weeks} week{'s' if weeks != 1 else ''} ago"
        elif total_seconds < 31536000:
            months = total_seconds // 2592000
            return f"{months} month{'s' if months != 1 else ''} ago"
        else:
            years = total_seconds // 31536000
            return f"{years} year{'s' if years != 1 else ''} ago"
    except (ValueError, TypeError):
        return "-"


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
) -> None:
    """Download kernel source tarball.

    Args:
        url: URL to download from
        dest: Destination path
        expected_sha256: Optional SHA-256 checksum
        allow_missing_checksum: If True, allow download without checksum

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
            timeout=600,
            allow_missing_checksum=allow_missing_checksum,
        )
    except ChecksumMismatchError:
        raise
    except FCMError as e:
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
) -> None:
    """Download Firecracker microvm kernel config.

    Args:
        kernel_dir: Kernel source directory
        version: Kernel version string (e.g. ``"6.1.102"``); the major.minor
            component is used to select the matching config file.

    Raises:
        KernelError: If download fails
    """
    major_minor = ".".join(version.split(".")[:2])
    config_url = FIRECRACKER_KERNEL_CONFIG_URL.format(major_minor=major_minor)

    try:
        logger.info("Downloading Firecracker kernel config...")
        req = Request(config_url, headers={"User-Agent": HTTP_USER_AGENT})

        with urlopen(req, timeout=60) as response:
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
    jobs: int = FALLBACK_KERNEL_BUILD_JOBS,
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


def configure_kernel(
    kernel_dir: Path,
    version: str,
    user_config_path: Path | None = None,
) -> None:
    """Configure kernel with Firecracker settings.

    Args:
        kernel_dir: Kernel source directory
        version: Kernel version string (e.g. ``"6.1.102"``) used to select the
            matching Firecracker config file.

    Raises:
        KernelError: If configuration fails
    """

    # Download Firecracker config
    try:
        download_firecracker_config(kernel_dir, version)
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

    logger.info("Applying default kernel options from constants...")
    for option in KERNEL_ENABLED_CONFIGS:
        _run_config_script(config_script, ["--enable", option], kernel_dir)

    for option in KERNEL_DISABLED_CONFIGS:
        _run_config_script(config_script, ["--disable", option], kernel_dir)

    for option, value in KERNEL_SET_VAL_CONFIGS:
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
    all_present = True
    missing_settings: list[str] = []

    for setting in KERNEL_REQUIRED_SETTINGS:
        if setting in config_content:
            logger.info("  %s", setting)
        else:
            logger.error("  MISSING: %s", setting)
            missing_settings.append(setting)
            all_present = False

    if not all_present:
        import typer

        from fcm.utils.console import print_info, print_warning

        print_warning(f"Required kernel settings missing: {', '.join(missing_settings)}")
        if not typer.confirm(
            "Proceed with build anyway? (missing settings may affect VM stability)", default=False
        ):
            raise KernelError("Required kernel settings are missing from configuration")
        print_info("Proceeding with build despite missing settings...")

        print_info("Proceeding with build despite missing settings...")


def build_kernel(
    kernel_dir: Path,
    output_path: Path,
    jobs: int = FALLBACK_KERNEL_BUILD_JOBS,
    build_log_path: Path | None = None,
) -> None:
    """Build the kernel.

    Args:
        kernel_dir: Kernel source directory
        output_path: Where to copy vmlinux
        jobs: Number of parallel jobs
        build_log_path: Optional path to write build log (for caching)

    Raises:
        KernelError: If build fails
    """
    logger.info("Building vmlinux with %d parallel jobs...", jobs)
    logger.info("This may take 10-30 minutes...")

    from fcm.utils.console import console

    console.print("[yellow]Building kernel... (this may take 10-30 minutes)[/yellow]")

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

        with open(build_log_path, encoding="utf-8") as f:
            for line in f:
                line = line.rstrip("\n")
                logger.debug("%s", line)
                if _BUILD_LOG_PATTERNS.search(line):
                    console.print(f"[dim]{line}[/dim]")

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
    output_path.chmod(0o755)

    size = output_path.stat().st_size
    size_mb = size / (1024 * 1024)
    logger.info("Kernel built: %s (%.1f MiB)", output_path.name, size_mb)


def fetch_kernel_sha256(version: str) -> str | None:
    sha256_url = KERNEL_SHA256_URL_TEMPLATE.format(version=version)
    try:
        req = Request(sha256_url, headers={"User-Agent": HTTP_USER_AGENT})
        with urlopen(req, timeout=30) as resp:
            content = resp.read().decode().strip()
        parts = content.split()
        return str(parts[0]).lower() if parts else None
    except (URLError, OSError):
        logger.debug("Could not fetch SHA-256 for kernel %s", version)
        return None


def _compute_config_hash(
    version: str,
    user_config_path: Path | None = None,
) -> str:
    """Compute a hash of kernel configuration parameters for caching.

    Args:
        version: Kernel version
        user_config_path: Optional path to user config overlay

    Returns:
        Short hash string for cache key
    """
    hasher = hashlib.sha256()
    hasher.update(version.encode())
    hasher.update(str(KERNEL_ENABLED_CONFIGS).encode())
    hasher.update(str(KERNEL_DISABLED_CONFIGS).encode())
    hasher.update(str(KERNEL_SET_VAL_CONFIGS).encode())
    hasher.update(str(KERNEL_REQUIRED_SETTINGS).encode())
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
) -> Path:
    if jobs is None:
        jobs = os.cpu_count() or 1

    if build_dir is None:
        import tempfile

        from fcm.constants import PROJECT_NAME

        build_id = str(uuid.uuid4())[:8]
        build_dir = Path(tempfile.gettempdir()) / PROJECT_NAME / f"build-{build_id}"

    build_dir.mkdir(parents=True, exist_ok=True)

    # Compute config hash for caching
    config_hash = _compute_config_hash(version, user_config_path)
    cache_key = f"{version}-{config_hash}"
    cache_marker = build_dir.parent / f"kernel-cache-{cache_key}.marker"
    cached_kernel_path = build_dir.parent / f"kernel-cache-{cache_key}.vmlinux"

    if cache_marker.exists() and cached_kernel_path.exists():
        output_path.parent.mkdir(parents=True, exist_ok=True)
        shutil.copy2(cached_kernel_path, output_path)
        output_path.chmod(0o755)
        logger.info("Using cached kernel build (config hash match): %s", output_path)
        return build_dir

    if output_path.exists() and cache_marker.exists():
        logger.info("Using cached kernel (config hash match): %s", output_path)
        return build_dir

    if output_path.exists():
        logger.info("Kernel exists but config changed, rebuilding: %s", output_path)
        output_path.unlink(missing_ok=True)

    if sha256 is None:
        sha256 = fetch_kernel_sha256(version)

    if sha256 is None:
        raise KernelError(f"Checksum required for kernel source download: {source_url}")

    tarball = build_dir / f"linux-{version}.tar.xz"
    kernel_src_dir = build_dir / f"linux-{version}"

    if not tarball.exists():
        download_kernel_source(source_url, tarball, sha256)
    else:
        logger.info("Using cached tarball: %s", tarball)

    if not kernel_src_dir.exists():
        extract_kernel_tarball(tarball, build_dir)
    else:
        logger.info("Using existing source: %s", kernel_src_dir)

    configure_kernel(kernel_src_dir, version, user_config_path=user_config_path)

    build_kernel(kernel_src_dir, output_path, jobs)

    shutil.copy2(output_path, cached_kernel_path)
    cache_marker.write_text(cache_key)

    save_kernel_metadata(
        output_path.parent,
        output_path.name,
        version=version,
        kernel_type="official",
        arch=arch,
    )

    if not keep_build_dir:
        shutil.rmtree(build_dir, ignore_errors=True)
        logger.info("Build directory cleaned up")
    else:
        logger.info("Build directory kept at: %s", build_dir)

    return build_dir


def save_kernel_metadata(
    kernels_dir: Path,
    kernel_name: str,
    version: str | None = None,
    kernel_type: str | None = None,
    arch: str | None = None,
) -> None:
    """Save kernel metadata to unified metadata.json.

    Parses the kernel filename to extract base_name, version, and arch if not
    provided. Uses file modification time for last_modified.

    Args:
        kernels_dir: Directory containing kernel files
        kernel_name: Kernel filename
        version: Kernel version (parsed from filename if None)
        kernel_type: Kernel type (e.g., "firecracker", "official")
        arch: Architecture (parsed from filename if None)
    """
    kernel_path = kernels_dir / kernel_name

    parsed = parse_kernel_filename(kernel_name)

    if version is None:
        version = parsed.version
    if arch is None:
        arch = parsed.arch
    if kernel_type is None:
        kernel_type = "unknown"

    last_modified = "-"
    if kernel_path.exists():
        mtime = kernel_path.stat().st_mtime
        last_modified = datetime.fromtimestamp(mtime, tz=timezone.utc).isoformat()

    cache_dir = get_cache_dir()
    update_kernel_entry(
        cache_dir,
        kernel_name,
        name=kernel_name,
        base_name=parsed.base_name,
        version=version,
        arch=arch,
        type=kernel_type,
        last_modified=last_modified,
    )


def list_kernels(kernels_dir: Path) -> list[dict[str, str]]:
    kernels_dir.mkdir(parents=True, exist_ok=True)

    cache_dir = get_cache_dir()
    images_dir = get_images_dir()

    migrate_legacy_metadata(cache_dir, kernels_dir, images_dir)

    default_name = _load_default_kernel(kernels_dir)
    entries = list_kernel_entries(cache_dir, kernels_dir)

    results: list[dict[str, str]] = []

    for path in sorted(kernels_dir.iterdir()):
        if not path.is_file() or path.suffix == ".json":
            continue
        if not (path.name.startswith("vmlinux") or path.name.startswith("kernel")):
            continue

        size_mb = path.stat().st_size / (1024 * 1024)

        meta = entries.get(path.name, {})

        last_modified = meta.get("last_modified")
        if not last_modified:
            last_modified = meta.get("built_at", "-")

        kernel_id = generate_kernel_id(path.name, last_modified)

        if meta.get("base_name"):
            base_name = meta["base_name"]
            version = meta.get("version", "-")
            arch = meta.get("arch", "-")
            kernel_type = meta.get("type", "unknown")
        else:
            parsed = parse_kernel_filename(path.name)
            base_name = parsed.base_name
            version = parsed.version
            arch = parsed.arch
            kernel_type = "unknown"

        results.append(
            {
                "id": kernel_id,
                "name": base_name,
                "full_name": path.name,
                "version": version,
                "type": kernel_type,
                "arch": arch,
                "last_modified": last_modified,
                "size": f"{size_mb:.1f} MiB",
                "is_default": str(path.name == default_name).lower(),
            }
        )

    return results


def _load_default_kernel(kernels_dir: Path) -> str | None:
    from fcm.core.config_state import get_defaults_config

    return get_defaults_config().get("kernel")


def set_default_kernel(kernels_dir: Path, kernel_name: str) -> None:
    from fcm.core.config_state import set_defaults_value

    kernel_path = kernels_dir / kernel_name
    if not kernel_path.exists():
        raise KernelError(f"Kernel not found: {kernel_path}")
    set_defaults_value("kernel", kernel_name)
    logger.info("Default kernel set to: %s", kernel_name)


def get_default_kernel_path(kernels_dir: Path) -> Path | None:
    name = _load_default_kernel(kernels_dir)
    if name is None:
        return None
    path = kernels_dir / name
    return path if path.exists() else None


def download_firecracker_kernel(
    ci_version: str,
    arch: str = DEFAULT_FC_KERNEL_ARCH,
    kernels_dir: Path | None = None,
    output_name: str | None = None,
) -> Path:
    if kernels_dir is None:
        from fcm.utils.fs import get_kernels_dir

        kernels_dir = get_kernels_dir()
    kernels_dir.mkdir(parents=True, exist_ok=True)

    list_url = FIRECRACKER_CI_KERNEL_LIST_URL.format(ci_version=ci_version, arch=arch)
    try:
        req = Request(list_url, headers={"User-Agent": HTTP_USER_AGENT})
        with urlopen(req, timeout=30) as resp:
            xml_content = resp.read().decode("utf-8")
    except (URLError, OSError) as exc:
        raise KernelError(f"Failed to list CI kernels: {exc}") from exc

    pattern = (
        rf"<Key>(firecracker-ci/{re.escape(ci_version)}/{re.escape(arch)}/vmlinux-[\d.]+)</Key>"
    )
    keys = re.findall(pattern, xml_content)
    if not keys:
        raise KernelError(f"No vmlinux found for Firecracker CI version {ci_version} / arch {arch}")

    keys.sort()
    chosen_key = keys[-1]
    kernel_version = chosen_key.split("/vmlinux-")[-1]

    if output_name is None:
        output_name = f"vmlinux-fc-{ci_version}-{arch}"

    output_path = kernels_dir / output_name

    if output_path.exists():
        logger.info("Firecracker CI kernel already cached: %s", output_path)
        return output_path

    download_url = f"{FIRECRACKER_CI_KERNEL_S3_BASE}/{chosen_key}"
    sha256_url = f"{download_url}.sha256"
    expected_sha256: str | None = None
    try:
        req_sha = Request(sha256_url, headers={"User-Agent": HTTP_USER_AGENT})
        with urlopen(req_sha, timeout=15) as resp_sha:
            content = resp_sha.read().decode().strip()
        parts = content.split()
        expected_sha256 = str(parts[0]).lower() if parts else None
        logger.info("Fetched CI kernel checksum: %s", expected_sha256)
    except (URLError, OSError):
        logger.debug("No sha256 sidecar for CI kernel %s — proceeding without checksum", chosen_key)

    if expected_sha256 is None:
        raise KernelError(f"Checksum required for Firecracker CI kernel download: {download_url}")

    logger.info("Downloading Firecracker CI kernel from %s", download_url)
    try:
        download_file(
            download_url,
            output_path,
            expected_sha256=expected_sha256,
            timeout=300,
        )
    except FCMError as exc:
        raise KernelError(f"Failed to download Firecracker CI kernel: {exc}") from exc

    output_path.chmod(0o755)

    save_kernel_metadata(
        kernels_dir,
        output_name,
        version=kernel_version,
        kernel_type="firecracker",
        arch=arch,
    )
    logger.info("Firecracker CI kernel saved: %s", output_path)
    return output_path
