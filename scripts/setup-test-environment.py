#!/usr/bin/env python3
"""
mvmctl Test Environment Setup Script

Prepares a fresh Linux system for running mvmctl system integration tests.
Target: Intel 10th gen, 8GB RAM, 512GB SSD

Usage:
    sudo python3 setup-test-environment.py [options]

Options:
    --cleanup           Clean up existing mvmctl installation and test artifacts
    --skip-assets       Skip pre-downloading test images (faster setup)
    --run-tests         Run system tests immediately after setup
    --binary            Test compiled binary instead of source
    --repo-path PATH    Use existing mvmctl clone at PATH instead of cloning
"""

from __future__ import annotations

import argparse
import logging
import os
import shutil
import subprocess
import sys
import textwrap
from pathlib import Path
from typing import Optional

from common import (
    BOLD,
    PROJECT_ROOT,
    RESET,
    SCRIPT_DIR,
    print_banner,
    print_fail,
    print_info,
    print_success,
    print_warn,
)

logger = logging.getLogger(__name__)

LOG_FILE = SCRIPT_DIR / "setup-test-environment.log"

UBUNTU_DEBIAN_PACKAGES = [
    # Core networking and system
    "iproute2",
    "iptables",
    "procps",
    "kmod",
    "sudo",
    # Image and cloud-init tools
    "genisoimage",
    "qemu-utils",
    "cloud-image-utils",
    "e2fsprogs",
    "squashfs-tools",
    # File utilities
    "util-linux",
    "tar",
    "coreutils",
    # SSH
    "openssh-client",
    # Python
    "python3",
    "python3-pip",
    "git",
    # libguestfs (for direct cloud-init injection)
    "libguestfs0",
    "libguestfs-tools",
    "supermin",
    "python3-libguestfs",
]

ARCH_PACKAGES = [
    # Core networking and system
    "iproute2",
    "iptables",
    "procps-ng",
    "kmod",
    "sudo",
    # Image and cloud-init tools
    "libisoburn",
    "qemu-img",
    "cloud-utils",
    "e2fsprogs",
    "squashfs-tools",
    # File utilities
    "util-linux",
    "tar",
    "coreutils",
    # SSH
    "openssh",
    # Python
    "python",
    "git",
    # libguestfs (for direct cloud-init injection - includes Python bindings on Arch)
    "libguestfs",
    "supermin",
]

TEST_IMAGES = [
    "alpine-3.21",
    "ubuntu-24.04-minimal",
    "ubuntu-24.04",
    "archlinux",
    "debian-bookworm",
]

MIN_RAM_GB = 4
RECOMMENDED_RAM_GB = 8
MIN_DISK_GB = 50
RECOMMENDED_CPU_CORES = 4
VM_MEM_MIB = 128


def run_cmd(
    cmd: list[str],
    *,
    check: bool = True,
    capture: bool = False,
    sudo: bool = False,
    description: str = "",
    workdir: Optional[str] = None,
) -> subprocess.CompletedProcess:
    full_cmd = ["sudo"] + cmd if sudo else cmd
    desc = description or " ".join(cmd)
    logger.debug("Running: %s", " ".join(full_cmd))
    try:
        kwargs: dict = {"check": check}
        if capture:
            kwargs["capture_output"] = True
            kwargs["text"] = True
        if workdir:
            kwargs["cwd"] = workdir
        result = subprocess.run(full_cmd, **kwargs)
        return result
    except subprocess.CalledProcessError as e:
        if check:
            msg = f"Command failed: {desc}"
            if e.stderr:
                msg += f"\n  stderr: {e.stderr.strip()}"
            logger.error(msg)
            print_fail(msg)
            sys.exit(1)
        return e  # type: ignore[return-value]
    except FileNotFoundError:
        if check:
            msg = f"Command not found: {full_cmd[0]}"
            logger.error(msg)
            print_fail(msg)
            sys.exit(1)
        raise


def detect_os() -> str:
    os_release = Path("/etc/os-release")
    if os_release.exists():
        content = os_release.read_text()
        if "ubuntu" in content.lower() or "debian" in content.lower():
            return "ubuntu-debian"
        if "arch" in content.lower():
            return "arch"
    if shutil.which("apt-get"):
        return "ubuntu-debian"
    if shutil.which("pacman"):
        return "arch"
    return "unknown"


def get_ram_print_info() -> tuple[float, float]:
    meminfo = Path("/proc/meminfo").read_text()
    total_kb = 0
    available_kb = 0
    for line in meminfo.splitlines():
        if line.startswith("MemTotal:"):
            total_kb = int(line.split()[1])
        elif line.startswith("MemAvailable:"):
            available_kb = int(line.split()[1])
    return available_kb / 1_048_576, total_kb / 1_048_576


def get_disk_print_info(path: str = "/") -> tuple[float, float]:
    stat = os.statvfs(path)
    total_gb = (stat.f_blocks * stat.f_frsize) / 1_073_741_824
    available_gb = (stat.f_bavail * stat.f_frsize) / 1_073_741_824
    return available_gb, total_gb


def get_cpu_print_info() -> tuple[int, str]:
    cores = os.cpu_count() or 0
    model = "Unknown CPU"
    try:
        cpuinfo = Path("/proc/cpuinfo").read_text()
        for line in cpuinfo.splitlines():
            if line.startswith("model name"):
                model = line.split(":", 1)[1].strip()
                break
    except OSError:
        pass
    return cores, model


def check_kvm_support() -> tuple[bool, str]:
    try:
        result = subprocess.run(
            ["egrep", "-c", "(vmx|svm)", "/proc/cpuinfo"],
            capture_output=True,
            text=True,
            check=False,
        )
        count = int(result.stdout.strip()) if result.returncode == 0 else 0
        if count > 0:
            vendor = "Intel VT-x" if "vmx" in result.stdout else "AMD-V"
            return True, vendor
        return False, "No virtualization extensions detected"
    except (FileNotFoundError, ValueError):
        return False, "Could not check CPU virtualization support"


def check_nested_virt_intel() -> bool:
    try:
        param = Path("/sys/module/kvm_intel/parameters/nested")
        if param.exists():
            return param.read_text().strip().lower() in ("y", "1")
    except OSError:
        pass
    return False


def check_nested_virt_amd() -> bool:
    try:
        param = Path("/sys/module/kvm_amd/parameters/nested")
        if param.exists():
            return param.read_text().strip().lower() in ("y", "1")
    except OSError:
        pass
    return False


def is_uv_installed() -> bool:
    return shutil.which("uv") is not None


def install_uv() -> None:
    print_info("Installing uv...")
    run_cmd(
        ["curl", "-LsSf", "https://astral.sh/uv/install.sh", "|", "sh"],
        description="Install uv",
        check=False,
    )
    if not is_uv_installed():
        print_info("Falling back to pip install for uv...")
        run_cmd(
            ["pip3", "install", "uv"],
            description="Install uv via pip",
        )
    if is_uv_installed():
        print_success("uv installed successfully")
    else:
        print_warn("uv installation may have failed — please install manually")


def setup_packages(os_family: str) -> None:
    print_banner("Installing System Packages")

    if os_family == "ubuntu-debian":
        print_info("Updating package lists...")
        run_cmd(["apt-get", "update"], sudo=True, description="apt-get update")
        print_info("Installing Ubuntu/Debian packages...")
        run_cmd(
            ["apt-get", "install", "-y"] + UBUNTU_DEBIAN_PACKAGES,
            sudo=True,
            description="Install Ubuntu/Debian packages",
        )
    elif os_family == "arch":
        print_info("Installing Arch Linux packages...")
        run_cmd(
            ["pacman", "-S", "--needed", "--noconfirm"] + ARCH_PACKAGES,
            sudo=True,
            description="Install Arch packages",
        )
    else:
        print_fail(f"Unsupported OS family: {os_family}")
        sys.exit(1)

    print_success("System packages installed")


def setup_kvm(os_family: str) -> None:
    print_banner("Configuring KVM & Nested Virtualization")

    has_kvm, vendor = check_kvm_support()
    if has_kvm:
        print_success(f"CPU supports {vendor}")
    else:
        print_warn("CPU does not appear to support hardware virtualization")
        print_info("  VMs will not work without KVM support")
        print_info("  Ensure virtualization is enabled in BIOS/UEFI")

    print_info("Loading KVM kernel modules...")
    run_cmd(["modprobe", "kvm"], sudo=True, description="Load kvm module")
    print_success("kvm module loaded")

    is_intel = Path("/sys/module/kvm_intel").exists()
    is_amd = Path("/sys/module/kvm_amd").exists()

    if is_intel:
        print_info("Detected Intel CPU — configuring kvm_intel...")
        nested_conf = Path("/etc/modprobe.d/kvm-intel.conf")
        nested_content = "options kvm_intel nested=1\n"
        if (
            not nested_conf.exists()
            or nested_conf.read_text() != nested_content
        ):
            run_cmd(
                [
                    "sh",
                    "-c",
                    f"echo 'options kvm_intel nested=1' > {nested_conf}",
                ],
                sudo=True,
                description="Enable nested virtualization for Intel",
            )
            print_info("Reloading kvm_intel module...")
            run_cmd(["modprobe", "-r", "kvm_intel"], sudo=True, check=False)
            run_cmd(
                ["modprobe", "kvm_intel"],
                sudo=True,
                description="Reload kvm_intel",
            )

        if check_nested_virt_intel():
            print_success("Nested virtualization enabled for Intel")
        else:
            print_warn(
                "Could not verify nested virtualization — check BIOS settings"
            )
    elif is_amd:
        print_info("Detected AMD CPU — configuring kvm_amd...")
        nested_conf = Path("/etc/modprobe.d/kvm-amd.conf")
        nested_content = "options kvm_amd nested=1\n"
        if (
            not nested_conf.exists()
            or nested_conf.read_text() != nested_content
        ):
            run_cmd(
                [
                    "sh",
                    "-c",
                    f"echo 'options kvm_amd nested=1' > {nested_conf}",
                ],
                sudo=True,
                description="Enable nested virtualization for AMD",
            )
            run_cmd(["modprobe", "-r", "kvm_amd"], sudo=True, check=False)
            run_cmd(
                ["modprobe", "kvm_amd"], sudo=True, description="Reload kvm_amd"
            )

        if check_nested_virt_amd():
            print_success("Nested virtualization enabled for AMD")
        else:
            print_warn(
                "Could not verify nested virtualization — check BIOS settings"
            )
    else:
        print_warn("No KVM vendor module detected")

    current_user = os.environ.get("SUDO_USER") or os.environ.get("USER")
    if current_user:
        print_info(f"Adding user '{current_user}' to kvm group...")
        run_cmd(
            ["usermod", "-aG", "kvm", current_user],
            sudo=True,
            description="Add user to kvm group",
        )
        print_success(f"User '{current_user}' added to kvm group")

    kvm_path = Path("/dev/kvm")
    if kvm_path.exists():
        print_success("/dev/kvm exists")
        if os.access(kvm_path, os.R_OK | os.W_OK):
            print_success("/dev/kvm is accessible")
        else:
            print_warn(
                "/dev/kvm exists but may not be accessible — try logging out and back in"
            )
    else:
        print_fail("/dev/kvm not found — KVM is not available")


def setup_mvmctl(
    os_family: str,
    repo_path: Optional[str] = None,
) -> Path:
    print_banner("Setting Up mvmctl")

    target_dir: Path

    if repo_path:
        target_dir = Path(repo_path).resolve()
        if not target_dir.exists():
            print_fail(f"Repository path does not exist: {target_dir}")
            sys.exit(1)
        print_success(f"Using existing clone at {target_dir}")
    else:
        target_dir = PROJECT_ROOT
        if (target_dir / "pyproject.toml").exists():
            print_success(f"Using existing clone at {target_dir}")
        else:
            print_info("Cloning mvmctl repository...")
            run_cmd(
                [
                    "git",
                    "clone",
                    "https://github.com/AlanD20/mvmctl.git",
                    str(target_dir),
                ],
                description="Clone mvmctl repo",
            )
            print_success("Repository cloned")

    if not is_uv_installed():
        install_uv()

    print_info("Installing Python dependencies with uv...")
    run_cmd(
        ["uv", "sync", "--group", "dev"],
        workdir=str(target_dir),
        description="Install mvmctl dependencies",
    )
    print_success("Dependencies installed")

    print_info("Running mvm host init...")
    run_cmd(
        ["uv", "run", "mvm", "host", "init"],
        sudo=True,
        workdir=str(target_dir),
        description="mvm host init",
    )
    print_success("Host initialization complete")

    return target_dir


def download_assets(target_dir: Path) -> None:
    print_banner("Pre-downloading Test Assets")

    for image in TEST_IMAGES:
        print_info(f"Downloading image: {image}...")
        result = run_cmd(
            ["uv", "run", "mvm", "image", "pull", image],
            workdir=str(target_dir),
            description=f"Fetch image {image}",
            check=False,
        )
        if result.returncode == 0:
            print_success(f"Image downloaded: {image}")
        else:
            print_warn(
                f"Failed to download image: {image} (will be fetched on-demand during tests)"
            )

    print_info("Downloading Firecracker kernel...")
    result = run_cmd(
        [
            "uv",
            "run",
            "mvm",
            "kernel",
            "pull",
            "--type",
            "firecracker",
            "--default",
        ],
        workdir=str(target_dir),
        description="Fetch kernel",
        check=False,
    )
    if result.returncode == 0:
        print_success("Kernel downloaded")
    else:
        print_warn("Failed to download kernel — will be fetched on-demand")

    print_info("Downloading Firecracker binary...")
    result = run_cmd(
        ["uv", "run", "mvm", "bin", "pull", "1.15.1", "--default"],
        workdir=str(target_dir),
        description="Fetch Firecracker binary",
        check=False,
    )
    if result.returncode == 0:
        print_success("Firecracker binary downloaded")
    else:
        print_warn(
            "Failed to download Firecracker binary — will be fetched on-demand"
        )


def validate_resources() -> dict:
    print_banner("Validating System Resources")

    avail_ram, total_ram = get_ram_print_info()
    avail_disk, total_disk = get_disk_print_info()
    cpu_cores, cpu_model = get_cpu_print_info()

    info = {
        "ram_available_gb": avail_ram,
        "ram_total_gb": total_ram,
        "disk_available_gb": avail_disk,
        "disk_total_gb": total_disk,
        "cpu_cores": cpu_cores,
        "cpu_model": cpu_model,
    }

    if avail_ram < MIN_RAM_GB:
        print_fail(
            f"RAM: {avail_ram:.1f} GiB available (minimum {MIN_RAM_GB} GiB required)"
        )
    elif avail_ram < RECOMMENDED_RAM_GB:
        print_warn(
            f"RAM: {avail_ram:.1f} GiB available (recommended {RECOMMENDED_RAM_GB} GiB)"
        )
    else:
        print_success(
            f"RAM: {avail_ram:.1f} GiB available (recommended {RECOMMENDED_RAM_GB} GiB)"
        )

    if avail_disk < MIN_DISK_GB:
        print_fail(
            f"Disk: {avail_disk:.0f} GiB available (minimum {MIN_DISK_GB} GiB required)"
        )
    else:
        print_success(
            f"Disk: {avail_disk:.0f} GiB available (recommended {MIN_DISK_GB} GiB)"
        )

    if cpu_cores < RECOMMENDED_CPU_CORES:
        print_warn(
            f"CPU: {cpu_cores} cores (recommended {RECOMMENDED_CPU_CORES}+)"
        )
    else:
        print_success(f"CPU: {cpu_cores} cores ({cpu_model})")

    max_vms_ram = int((avail_ram * 1024) / VM_MEM_MIB)
    max_vms_disk = int((avail_disk * 1024) / 2048)
    max_vms = min(max_vms_ram, max_vms_disk)

    print_info("")
    print_info(f"  {BOLD}VM Capacity (with {VM_MEM_MIB} MiB per VM):{RESET}")
    print_info(
        f"    Max concurrent VMs: ~{max_vms} (limited by {'RAM' if max_vms == max_vms_ram else 'disk'})"
    )
    print_info("    Recommended test batch: 10-20 VMs")
    print_info("")
    print_info(f"  {BOLD}Test Execution Estimate:{RESET}")
    print_info("    System tests can be run with: pytest tests/system/ -v")
    print_info("    Run subsets: pytest tests/system/test_network.py -v")
    print_info("")

    return info


def run_tests(target_dir: Path, binary_mode: bool = False) -> None:
    print_banner("Running System Integration Tests")

    if binary_mode:
        print_info("Running tests in binary mode...")
        print_warn("Binary mode requires a compiled binary in dist/mvm")
        print_info("  Ensure you have built the binary first:")
        print_info("    uv sync --group build")
        print_info(
            "    uv run python -m nuitka --onefile --output-dir=dist --output-filename=mvm ..."
        )
        print_info("")

        binary_path = target_dir / "dist" / "mvm"
        if not binary_path.exists():
            print_fail(f"Binary not found at {binary_path}")
            print_info("  Build the binary first, then re-run with --binary")
            return

        print_info("Running tests against compiled binary...")
        env = os.environ.copy()
        env["MVM_BINARY"] = str(binary_path)
        result = subprocess.run(
            ["uv", "run", "pytest", "tests/", "-v", "--tb=short"],
            cwd=target_dir,
            env=env,
            check=False,
        )
    else:
        print_info("Running tests in source mode...")
        result = subprocess.run(
            ["uv", "run", "pytest", "tests/", "-v", "--tb=short"],
            cwd=target_dir,
            check=False,
        )

    if result.returncode == 0:
        print_success("All tests passed!")
    else:
        print_warn(f"Tests completed with exit code {result.returncode}")
        print_info("  Review the output above for failures")


def cleanup(uninstall_packages: bool = False) -> None:
    print_banner("Cleaning Up mvmctl Installation")

    cache_dir = Path.home() / ".cache" / "mvmctl"
    config_dir = Path.home() / ".config" / "mvmctl"

    if cache_dir.exists():
        print_info(f"Removing cache directory: {cache_dir}")
        shutil.rmtree(cache_dir)
        print_success("Cache removed")
    else:
        print_info("Cache directory does not exist — skipping")

    if config_dir.exists():
        print_info(f"Removing config directory: {config_dir}")
        shutil.rmtree(config_dir)
        print_success("Config removed")
    else:
        print_info("Config directory does not exist — skipping")

    tmp_mvm = Path("/tmp/mvm")
    if tmp_mvm.exists():
        print_info(f"Removing temporary mvm directory: {tmp_mvm}")
        run_cmd(
            ["rm", "-rf", str(tmp_mvm)],
            sudo=True,
            description="Remove /tmp/mvm",
        )
        print_success("Temporary files removed")

    vms_dir = cache_dir / "vms"
    if vms_dir.exists():
        print_info("Removing VM state files...")
        shutil.rmtree(vms_dir)
        print_success("VM state removed")

    if uninstall_packages:
        print_info("Uninstalling system packages...")
        os_family = detect_os()
        if os_family == "ubuntu-debian":
            run_cmd(
                ["apt-get", "remove", "-y"] + UBUNTU_DEBIAN_PACKAGES,
                sudo=True,
                description="Remove Ubuntu/Debian packages",
            )
        elif os_family == "arch":
            run_cmd(
                ["pacman", "-Rns", "--noconfirm"] + ARCH_PACKAGES,
                sudo=True,
                description="Remove Arch packages",
            )
        print_success("System packages removed")

    print_success("Cleanup complete")


def setup_logging() -> None:
    LOG_FILE.parent.mkdir(parents=True, exist_ok=True)
    logging.basicConfig(
        level=logging.DEBUG,
        format="%(asctime)s [%(levelname)s] %(message)s",
        handlers=[
            logging.FileHandler(LOG_FILE),
        ],
    )


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Prepare a Linux system for running mvmctl system integration tests.",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog=textwrap.dedent(
            """\
            Examples:
              sudo python3 setup-test-environment.py
              sudo python3 setup-test-environment.py --skip-assets
              sudo python3 setup-test-environment.py --run-tests
              sudo python3 setup-test-environment.py --cleanup
              sudo python3 setup-test-environment.py --repo-path /path/to/mvmctl
            """
        ),
    )
    parser.add_argument(
        "--cleanup",
        action="store_true",
        help="Clean up existing mvmctl installation and test artifacts",
    )
    parser.add_argument(
        "--skip-assets",
        action="store_true",
        help="Skip pre-downloading test images (faster setup)",
    )
    parser.add_argument(
        "--run-tests",
        action="store_true",
        help="Run system tests immediately after setup",
    )
    parser.add_argument(
        "--binary",
        action="store_true",
        help="Test compiled binary instead of source",
    )
    parser.add_argument(
        "--repo-path",
        type=str,
        default=None,
        help="Use existing mvmctl clone at PATH instead of cloning",
    )
    parser.add_argument(
        "--uninstall-packages",
        action="store_true",
        help="During cleanup, also uninstall system packages",
    )
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    setup_logging()
    logger.print_info("Starting mvmctl test environment setup")

    if args.cleanup:
        cleanup(uninstall_packages=args.uninstall_packages)
        return 0

    if os.geteuid() != 0:
        print_fail("This script must be run as root (use sudo)")
        return 1

    os_family = detect_os()
    if os_family == "unknown":
        print_fail("Could not detect Linux distribution")
        print_info("  Supported: Ubuntu, Debian, Arch Linux")
        return 1

    print_banner("mvmctl Test Environment Setup")
    print_info(f"  Detected OS: {os_family}")
    print_info(f"  Log file: {LOG_FILE}")
    print_info("")

    setup_packages(os_family)

    setup_kvm(os_family)

    target_dir = setup_mvmctl(os_family, repo_path=args.repo_path)

    if not args.skip_assets:
        download_assets(target_dir)
    else:
        print_banner("Skipping Asset Download (--skip-assets)")
        print_info("  Assets will be downloaded on-demand during tests")

    validate_resources()

    if args.run_tests:
        run_tests(target_dir, binary_mode=args.binary)

    print_banner("Setup Complete")
    print_success("Your system is ready for mvmctl system integration tests!")
    print_info("")
    print_info(f"  Project directory: {target_dir}")
    print_info(f"  Run tests: cd {target_dir} && uv run pytest tests/ -v")
    print_info(
        "  Run specific test file: uv run pytest tests/system/test_network.py -v"
    )
    print_info(f"  Clean up: sudo python3 {__file__} --cleanup")
    print_info("")
    print_info(f"  Log file: {LOG_FILE}")
    print_info("")

    logger.print_info("Setup completed successfully")
    return 0


if __name__ == "__main__":
    sys.exit(main())
