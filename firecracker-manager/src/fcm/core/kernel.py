"""Kernel download and build utilities."""

import hashlib
import logging
import os
import subprocess
import tarfile
from pathlib import Path
from urllib.request import urlopen, Request
from urllib.error import URLError

from fcm.constants import HTTP_USER_AGENT
from fcm.exceptions import KernelError, ChecksumMismatchError

logger = logging.getLogger(__name__)


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
    dest.parent.mkdir(parents=True, exist_ok=True)

    try:
        logger.info("Downloading kernel from %s", url)
        req = Request(url, headers={"User-Agent": HTTP_USER_AGENT})

        sha256_hash = hashlib.sha256() if expected_sha256 else None

        with urlopen(req, timeout=600) as response:
            total_size = response.headers.get("Content-Length")
            total_size = int(total_size) if total_size else None
            downloaded = 0

            with open(dest, "wb") as f:
                while True:
                    chunk = response.read(8192)
                    if not chunk:
                        break
                    f.write(chunk)
                    downloaded += len(chunk)

                    if sha256_hash:
                        sha256_hash.update(chunk)

                    if total_size:
                        percent = (downloaded / total_size) * 100
                        logger.debug("Download progress: %.1f%%", percent)

        if expected_sha256 and sha256_hash:
            actual = sha256_hash.hexdigest()
            if actual.lower() != expected_sha256.lower():
                dest.unlink()
                raise ChecksumMismatchError(
                    f"Checksum mismatch! Expected {expected_sha256}, got {actual}"
                )
            logger.info("Checksum verified")

    except (URLError, IOError) as e:
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
    config_url = (
        "https://raw.githubusercontent.com/firecracker-microvm/firecracker/main/"
        f"resources/guest_configs/microvm-kernel-ci-x86_64-{major_minor}.config"
    )

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

    # Enable filesystems
    logger.info("Enabling filesystems...")
    config_script = kernel_dir / "scripts" / "config"

    options = [
        ("--enable", "CONFIG_BTRFS_FS"),
        ("--enable", "CONFIG_BTRFS_FS_POSIX_ACL"),
        ("--enable", "CONFIG_EXT4_FS"),
        ("--enable", "CONFIG_EXT4_FS_POSIX_ACL"),
        ("--enable", "CONFIG_XFS_FS"),
        ("--enable", "CONFIG_SQUASHFS"),
    ]

    for flag, option in options:
        _run_config_script(config_script, [flag, option], kernel_dir)

    # Enable VirtIO (built-in, not module)
    logger.info("Enabling VirtIO drivers...")
    virtio_options = [
        "CONFIG_VIRTIO",
        "CONFIG_VIRTIO_MENU",
        "CONFIG_VIRTIO_PCI",
        "CONFIG_VIRTIO_BLK",
        "CONFIG_VIRTIO_NET",
        "CONFIG_VIRTIO_CONSOLE",
    ]

    for option in virtio_options:
        _run_config_script(config_script, ["--enable", option], kernel_dir)

    # Enable serial console
    logger.info("Enabling serial console...")
    _run_config_script(config_script, ["--enable", "CONFIG_SERIAL_8250"], kernel_dir)
    _run_config_script(config_script, ["--enable", "CONFIG_SERIAL_8250_CONSOLE"], kernel_dir)
    _run_config_script(config_script, ["--set-val", "CONFIG_SERIAL_8250_NR_UARTS", "4"], kernel_dir)

    # Enable network
    logger.info("Enabling network support...")
    network_options = ["CONFIG_NET", "CONFIG_INET", "CONFIG_IPV6"]
    for option in network_options:
        _run_config_script(config_script, ["--enable", option], kernel_dir)

    # Enable KVM guest optimizations
    logger.info("Enabling KVM guest optimizations...")
    _run_config_script(config_script, ["--enable", "CONFIG_KVM_GUEST"], kernel_dir)
    _run_config_script(config_script, ["--enable", "CONFIG_PARAVIRT"], kernel_dir)

    # Enable LandLock
    logger.info("Enabling LandLock...")
    _run_config_script(config_script, ["--enable", "CONFIG_SECURITY_LANDLOCK"], kernel_dir)
    _run_config_script(config_script, ["--enable", "CONFIG_BPF_SYSCALL"], kernel_dir)
    _run_config_script(config_script, ["--enable", "CONFIG_CGROUPS"], kernel_dir)
    _run_config_script(config_script, ["--enable", "CONFIG_MEMCG"], kernel_dir)

    # Resolve dependencies again
    logger.info("Resolving dependencies...")
    returncode, _, _ = run_make(kernel_dir, "olddefconfig")
    if returncode != 0:
        raise KernelError("olddefconfig failed after enabling options")

    # Verify critical settings
    logger.info("Verifying configuration...")
    config_path = kernel_dir / ".config"
    required_settings = [
        "CONFIG_BTRFS_FS=y",
        "CONFIG_VIRTIO_BLK=y",
        "CONFIG_VIRTIO_NET=y",
        "CONFIG_SERIAL_8250_CONSOLE=y",
        "CONFIG_KVM_GUEST=y",
    ]

    config_content = config_path.read_text()
    all_present = True

    for setting in required_settings:
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

    returncode, stdout, stderr = run_make(kernel_dir, "vmlinux", jobs, capture_output=True)

    if returncode != 0:
        # Show last error lines
        lines = stderr.split("\n")
        error_lines = [
            line for line in lines if "error:" in line.lower() or "undefined" in line.lower()
        ]
        if error_lines:
            logger.error("Build errors:")
            for line in error_lines[-10:]:
                logger.error("  %s", line)
        raise KernelError("Kernel build failed")

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


def build_kernel_pipeline(
    version: str,
    source_url: str,
    output_path: Path,
    build_dir: Path,
    sha256: str | None = None,
    jobs: int | None = None,
) -> None:
    """Full kernel build pipeline.

    Args:
        version: Kernel version string
        source_url: URL to kernel tarball
        output_path: Where to copy vmlinux
        build_dir: Build directory
        sha256: Optional SHA-256 checksum
        jobs: Number of parallel jobs (defaults to CPU count)

    Raises:
        KernelError: If any pipeline step fails
        ChecksumMismatchError: If checksum verification fails
    """
    if jobs is None:
        jobs = os.cpu_count() or 1

    if output_path.exists():
        logger.info("Using cached kernel: %s", output_path)
        return

    tarball = build_dir / f"linux-{version}.tar.xz"
    kernel_src_dir = build_dir / f"linux-{version}"

    # Download
    if not tarball.exists():
        download_kernel_source(source_url, tarball, sha256)
    else:
        logger.info("Using cached tarball: %s", tarball)

    # Extract
    if not kernel_src_dir.exists():
        extract_kernel_tarball(tarball, build_dir)
    else:
        logger.info("Using existing source: %s", kernel_src_dir)

    # Configure
    configure_kernel(kernel_src_dir, version)

    # Build
    build_kernel(kernel_src_dir, output_path, jobs)
