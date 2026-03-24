"""Kernel download and build utilities."""

import json
import logging
import os
import re
import subprocess
import tarfile
import uuid
from datetime import datetime, timezone
from pathlib import Path
from urllib.error import URLError
from urllib.request import Request, urlopen

from fcm.constants import (
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
from fcm.exceptions import ChecksumMismatchError, FCMError, KernelError, ProcessError
from fcm.utils.http import download_file
from fcm.utils.process import stream_cmd

logger = logging.getLogger(__name__)

_BUILD_LOG_PATTERNS = re.compile(
    r"(?i)(warning|error|cannot find|undefined reference|fatal|note:)",
)


def download_kernel_source(
    url: str,
    dest: Path,
    expected_sha256: str | None = None,
) -> None:
    """Download kernel source tarball.

    Args:
        url: URL to download from
        dest: Destination path
        expected_sha256: Optional SHA-256 checksum

    Raises:
        KernelError: If download fails
        ChecksumMismatchError: If checksum verification fails
    """
    logger.info("Downloading kernel from %s", url)
    try:
        download_file(url, dest, expected_sha256, timeout=600)
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
    jobs: int = 1,
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

    for setting in KERNEL_REQUIRED_SETTINGS:
        if setting in config_content:
            logger.info("  %s", setting)
        else:
            logger.error("  MISSING: %s", setting)
            all_present = False

    if not all_present:
        raise KernelError("Required kernel settings are missing from configuration")


def build_kernel(
    kernel_dir: Path,
    output_path: Path,
    jobs: int = 1,
) -> None:
    """Build the kernel.

    Args:
        kernel_dir: Kernel source directory
        output_path: Where to copy vmlinux
        jobs: Number of parallel jobs

    Raises:
        KernelError: If build fails
    """
    logger.info("Building vmlinux with %d parallel jobs...", jobs)
    logger.info("This may take 10-30 minutes...")

    from fcm.utils.console import console

    console.print("[yellow]Building kernel... (this may take 10-30 minutes)[/yellow]")

    cmd = ["make", "vmlinux", f"-j{jobs}"]
    try:
        for line in stream_cmd(cmd, cwd=str(kernel_dir)):
            logger.debug("%s", line)
            if _BUILD_LOG_PATTERNS.search(line):
                console.print(f"[dim]{line}[/dim]")
    except ProcessError as e:
        raise KernelError(f"Kernel build failed: {e}") from e

    # Copy vmlinux to output
    vmlinux_path = kernel_dir / "vmlinux"
    if not vmlinux_path.exists():
        raise KernelError("Build succeeded but vmlinux not found")

    output_path.parent.mkdir(parents=True, exist_ok=True)

    import shutil

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


def build_kernel_pipeline(
    version: str,
    source_url: str,
    output_path: Path,
    build_dir: Path | None = None,
    sha256: str | None = None,
    jobs: int | None = None,
    keep_build_dir: bool = False,
    user_config_path: Path | None = None,
) -> Path:
    if jobs is None:
        jobs = os.cpu_count() or 1

    if build_dir is None:
        from fcm.constants import PROJECT_NAME

        build_id = str(uuid.uuid4())[:8]
        build_dir = Path(f"/tmp/{PROJECT_NAME}") / f"build-{build_id}"

    build_dir.mkdir(parents=True, exist_ok=True)

    if output_path.exists():
        logger.info("Using cached kernel: %s", output_path)
        return build_dir

    if sha256 is None:
        sha256 = fetch_kernel_sha256(version)

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

    save_kernel_metadata(
        output_path.parent,
        output_path.name,
        version=version,
        kernel_type="official",
    )

    if not keep_build_dir:
        import shutil

        shutil.rmtree(build_dir, ignore_errors=True)
        logger.info("Build directory cleaned up")
    else:
        logger.info("Build directory kept at: %s", build_dir)

    return build_dir


def save_kernel_metadata(
    kernels_dir: Path,
    kernel_name: str,
    version: str,
    kernel_type: str,
    arch: str = "x86_64",
) -> None:
    meta = {
        "name": kernel_name,
        "version": version,
        "type": kernel_type,
        "arch": arch,
        "built_at": datetime.now(tz=timezone.utc).isoformat(),
    }
    meta_path = kernels_dir / f"{kernel_name}.json"
    meta_path.write_text(json.dumps(meta, indent=2))


def list_kernels(kernels_dir: Path) -> list[dict[str, str]]:
    kernels_dir.mkdir(parents=True, exist_ok=True)
    default_name = _load_default_kernel(kernels_dir)
    results: list[dict[str, str]] = []
    for path in sorted(kernels_dir.iterdir()):
        if not path.is_file() or path.suffix == ".json":
            continue
        if not (path.name.startswith("vmlinux") or path.name.startswith("kernel")):
            continue
        size_mb = path.stat().st_size / (1024 * 1024)
        meta_path = path.with_suffix(".json")
        if meta_path.exists():
            try:
                meta: dict[str, str] = json.loads(meta_path.read_text())
            except (json.JSONDecodeError, OSError):
                meta = {}
        else:
            meta = {}
        results.append(
            {
                "name": path.name,
                "version": meta.get("version", "-"),
                "type": meta.get("type", "unknown"),
                "arch": meta.get("arch", "-"),
                "built_at": meta.get("built_at", "-"),
                "size": f"{size_mb:.1f} MiB",
                "is_default": str(path.name == default_name).lower(),
            }
        )
    return results


def _default_kernel_path(kernels_dir: Path) -> Path:
    return kernels_dir / "default.json"


def _load_default_kernel(kernels_dir: Path) -> str | None:
    path = _default_kernel_path(kernels_dir)
    if not path.exists():
        return None
    try:
        data: dict[str, str] = json.loads(path.read_text())
        return data.get("name")
    except (json.JSONDecodeError, OSError):
        return None


def set_default_kernel(kernels_dir: Path, kernel_name: str) -> None:
    kernel_path = kernels_dir / kernel_name
    if not kernel_path.exists():
        raise KernelError(f"Kernel not found: {kernel_path}")
    _default_kernel_path(kernels_dir).write_text(json.dumps({"name": kernel_name}, indent=2))
    logger.info("Default kernel set to: %s", kernel_name)


def get_default_kernel_path(kernels_dir: Path) -> Path | None:
    name = _load_default_kernel(kernels_dir)
    if name is None:
        vmlinux = kernels_dir / "vmlinux"
        if vmlinux.exists():
            return vmlinux
        return None
    path = kernels_dir / name
    return path if path.exists() else None


def download_firecracker_kernel(
    ci_version: str,
    arch: str = "x86_64",
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
    logger.info("Downloading Firecracker CI kernel from %s", download_url)
    try:
        download_file(download_url, output_path, expected_sha256=None, timeout=300)
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
