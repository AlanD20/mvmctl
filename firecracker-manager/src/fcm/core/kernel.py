"""Kernel download and build utilities."""

import hashlib
import os
import subprocess
import tarfile
from pathlib import Path
from urllib.request import urlopen, Request
from urllib.error import URLError

from fcm.utils.console import print_error, print_success, print_info


def download_kernel_source(
    url: str,
    dest: Path,
    expected_sha256: str | None = None,
) -> bool:
    """Download kernel source tarball.

    Args:
        url: URL to download from
        dest: Destination path
        expected_sha256: Optional SHA-256 checksum

    Returns:
        True if successful, False otherwise
    """
    dest.parent.mkdir(parents=True, exist_ok=True)

    try:
        print_info(f"Downloading kernel from {url}")
        req = Request(url, headers={"User-Agent": "fcm/0.1.0"})

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
                        print(f"\r  Progress: {percent:.1f}%", end="", flush=True)

        print()  # Newline

        if expected_sha256 and sha256_hash:
            actual = sha256_hash.hexdigest()
            if actual.lower() != expected_sha256.lower():
                print_error(f"Checksum mismatch! Expected {expected_sha256}, got {actual}")
                dest.unlink()
                return False
            print_success("Checksum verified")

        return True

    except URLError as e:
        print_error(f"Download failed: {e}")
        return False
    except IOError as e:
        print_error(f"I/O error: {e}")
        return False


def extract_kernel_tarball(
    tarball: Path,
    extract_dir: Path,
) -> Path | None:
    """Extract kernel tarball.

    Args:
        tarball: Path to tarball
        extract_dir: Directory to extract to

    Returns:
        Path to extracted kernel directory or None
    """
    try:
        print_info(f"Extracting {tarball.name}...")

        with tarfile.open(tarball, "r:xz") as tar:
            tar.extractall(path=extract_dir, filter="data")

        # Find extracted directory (should be linux-X.Y.Z)
        for item in extract_dir.iterdir():
            if item.is_dir() and item.name.startswith("linux-"):
                print_success(f"Extracted to {item.name}")
                return item

        print_error("Could not find extracted kernel directory")
        return None

    except tarfile.TarError as e:
        print_error(f"Extraction failed: {e}")
        return None


def download_firecracker_config(
    kernel_dir: Path,
) -> bool:
    """Download Firecracker microvm kernel config.

    Args:
        kernel_dir: Kernel source directory

    Returns:
        True if successful, False otherwise
    """
    config_url = (
        "https://raw.githubusercontent.com/firecracker-microvm/firecracker/main/"
        "resources/guest_configs/microvm-kernel-ci-x86_64-6.1.config"
    )

    try:
        print_info("Downloading Firecracker kernel config...")
        req = Request(config_url, headers={"User-Agent": "fcm/0.1.0"})

        with urlopen(req, timeout=60) as response:
            config_content = response.read().decode("utf-8")
            config_path = kernel_dir / ".config"

            with open(config_path, "w") as f:
                f.write(config_content)

            print_success("Config downloaded")
            return True

    except URLError as e:
        print_error(f"Failed to download config: {e}")
        return False


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


def configure_kernel(
    kernel_dir: Path,
) -> bool:
    """Configure kernel with Firecracker settings.

    Args:
        kernel_dir: Kernel source directory

    Returns:
        True if successful, False otherwise
    """

    # Download Firecracker config
    if not download_firecracker_config(kernel_dir):
        print_info("Using defconfig instead...")
        returncode, _, _ = run_make(kernel_dir, "defconfig")
        if returncode != 0:
            print_error("defconfig failed")
            return False

    # Sync config to current kernel version
    print_info("Synchronizing config...")
    returncode, _, _ = run_make(kernel_dir, "olddefconfig")
    if returncode != 0:
        print_error("olddefconfig failed")
        return False

    # Enable filesystems
    print_info("Enabling filesystems...")
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
        subprocess.run(
            [str(config_script), flag, option],
            cwd=kernel_dir,
            capture_output=True,
        )

    # Enable VirtIO (built-in, not module)
    print_info("Enabling VirtIO drivers...")
    virtio_options = [
        "CONFIG_VIRTIO",
        "CONFIG_VIRTIO_MENU",
        "CONFIG_VIRTIO_PCI",
        "CONFIG_VIRTIO_BLK",
        "CONFIG_VIRTIO_NET",
        "CONFIG_VIRTIO_CONSOLE",
    ]

    for option in virtio_options:
        subprocess.run(
            [str(config_script), "--enable", option],
            cwd=kernel_dir,
            capture_output=True,
        )

    # Enable serial console
    print_info("Enabling serial console...")
    subprocess.run(
        [str(config_script), "--enable", "CONFIG_SERIAL_8250"],
        cwd=kernel_dir,
        capture_output=True,
    )
    subprocess.run(
        [str(config_script), "--enable", "CONFIG_SERIAL_8250_CONSOLE"],
        cwd=kernel_dir,
        capture_output=True,
    )
    subprocess.run(
        [str(config_script), "--set-val", "CONFIG_SERIAL_8250_NR_UARTS", "4"],
        cwd=kernel_dir,
        capture_output=True,
    )

    # Enable network
    print_info("Enabling network support...")
    network_options = ["CONFIG_NET", "CONFIG_INET", "CONFIG_IPV6"]
    for option in network_options:
        subprocess.run(
            [str(config_script), "--enable", option],
            cwd=kernel_dir,
            capture_output=True,
        )

    # Enable KVM guest optimizations
    print_info("Enabling KVM guest optimizations...")
    subprocess.run(
        [str(config_script), "--enable", "CONFIG_KVM_GUEST"],
        cwd=kernel_dir,
        capture_output=True,
    )
    subprocess.run(
        [str(config_script), "--enable", "CONFIG_PARAVIRT"],
        cwd=kernel_dir,
        capture_output=True,
    )

    # Enable LandLock
    print_info("Enabling LandLock...")
    subprocess.run(
        [str(config_script), "--enable", "CONFIG_SECURITY_LANDLOCK"],
        cwd=kernel_dir,
        capture_output=True,
    )
    subprocess.run(
        [str(config_script), "--enable", "CONFIG_BPF_SYSCALL"],
        cwd=kernel_dir,
        capture_output=True,
    )
    subprocess.run(
        [str(config_script), "--enable", "CONFIG_CGROUPS"],
        cwd=kernel_dir,
        capture_output=True,
    )
    subprocess.run(
        [str(config_script), "--enable", "CONFIG_MEMCG"],
        cwd=kernel_dir,
        capture_output=True,
    )

    # Resolve dependencies again
    print_info("Resolving dependencies...")
    returncode, _, _ = run_make(kernel_dir, "olddefconfig")
    if returncode != 0:
        print_error("olddefconfig failed after enabling options")
        return False

    # Verify critical settings
    print_info("Verifying configuration...")
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
            print_success(f"  {setting}")
        else:
            print_error(f"  MISSING: {setting}")
            all_present = False

    return all_present


def build_kernel(
    kernel_dir: Path,
    output_path: Path,
    jobs: int = 1,
) -> bool:
    """Build the kernel.

    Args:
        kernel_dir: Kernel source directory
        output_path: Where to copy vmlinux
        jobs: Number of parallel jobs

    Returns:
        True if successful, False otherwise
    """
    print_info(f"Building vmlinux with {jobs} parallel jobs...")
    print_info("This may take 10-30 minutes...")

    returncode, stdout, stderr = run_make(kernel_dir, "vmlinux", jobs, capture_output=True)

    if returncode != 0:
        print_error("Build failed!")
        # Show last error lines
        lines = stderr.split("\n")
        error_lines = [
            line for line in lines if "error:" in line.lower() or "undefined" in line.lower()
        ]
        if error_lines:
            print_info("Errors found:")
            for line in error_lines[-10:]:
                print(f"  {line}")
        return False

    # Copy vmlinux to output
    vmlinux_path = kernel_dir / "vmlinux"
    if not vmlinux_path.exists():
        print_error("Build succeeded but vmlinux not found")
        return False

    output_path.parent.mkdir(parents=True, exist_ok=True)

    import shutil

    shutil.copy2(vmlinux_path, output_path)
    output_path.chmod(0o755)

    size = output_path.stat().st_size
    size_mb = size / (1024 * 1024)
    print_success(f"Kernel built: {output_path.name} ({size_mb:.1f} MiB)")

    return True


def build_kernel_pipeline(
    version: str,
    source_url: str,
    output_path: Path,
    build_dir: Path,
    sha256: str | None = None,
    jobs: int | None = None,
) -> bool:
    """Full kernel build pipeline.

    Args:
        version: Kernel version string
        source_url: URL to kernel tarball
        output_path: Where to copy vmlinux
        build_dir: Build directory
        sha256: Optional SHA-256 checksum
        jobs: Number of parallel jobs (defaults to CPU count)

    Returns:
        True if successful, False otherwise
    """
    if jobs is None:
        jobs = os.cpu_count() or 1

    if output_path.exists():
        print_success(f"Using cached kernel: {output_path}")
        return True

    tarball = build_dir / f"linux-{version}.tar.xz"
    kernel_src_dir = build_dir / f"linux-{version}"

    # Download
    if not tarball.exists():
        if not download_kernel_source(source_url, tarball, sha256):
            return False
    else:
        print_success(f"Using cached tarball: {tarball}")

    # Extract
    if not kernel_src_dir.exists():
        extracted = extract_kernel_tarball(tarball, build_dir)
        if not extracted:
            return False
    else:
        print_success(f"Using existing source: {kernel_src_dir}")

    # Configure
    if not configure_kernel(kernel_src_dir):
        return False

    # Build
    if not build_kernel(kernel_src_dir, output_path, jobs):
        return False

    return True
