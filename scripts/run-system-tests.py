#!/usr/bin/env python3
"""Run mvmctl system tests with per-domain VM isolation.

Usage:
  # Smoke-test the provisioning pipeline before running tests
  MVM_ASSET_MIRROR=~/.cache/mvm-asset-mirror \\
    python3 scripts/run-system-tests.py --prepare

  # Run all domains (T1 + T2 + T3)
  MVM_ASSET_MIRROR=~/.cache/mvm-asset-mirror \\
    python3 scripts/run-system-tests.py

  # Run specific domains
  python3 scripts/run-system-tests.py cli network vm_nested_virt

  # Run only specific tiers
  python3 scripts/run-system-tests.py --tier1-only
  python3 scripts/run-system-tests.py --tier2-only
  python3 scripts/run-system-tests.py --tier3-only

  # Limit parallel workers (default: 4)
  python3 scripts/run-system-tests.py --workers 2

  # Re-seed the shared asset volume even if it exists
  python3 scripts/run-system-tests.py --rebuild
"""

from __future__ import annotations

import argparse
import getpass
import json
import os
import shlex
import subprocess
import sys
import time
import uuid
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path
from typing import Any

# ============================================================================
# Configuration
# ============================================================================

ASSET_MIRROR_HOST = os.path.expanduser("~/.cache/mvm-asset-mirror")
if os.environ.get("MVM_ASSET_MIRROR"):
    ASSET_MIRROR_HOST = os.environ["MVM_ASSET_MIRROR"]
MVM_BINARY = os.environ.get("MVM_BINARY", os.path.expanduser("~/.local/bin/mvm"))
SHARED_VOLUME_NAME = "asset-mirror"
SHARED_VOLUME_SIZE = "6G"
TEST_NETWORK_NAME = "sys-test-net"

# Custom base image built during --prepare
BASE_IMAGE_NAME = "mvm-test-runner"

# Base VM used to build the custom image
BASE_VM_NAME = "base-img-builder"
BASE_VM_DISK = "12G"

# SSH key for provisioning runner user in builder & test VMs
BUILDER_KEY_NAME = "builder-key"

# Tier 1: shared volume, host-level CLI operations (no nested virt needed)
TIER1_DOMAINS: dict[str, list[str]] = {
    "cli": ["tests/system/cli/test_cli.py"],
    "config": ["tests/system/config/test_config.py"],
    "init": ["tests/system/init/test_init.py"],
    "cache": ["tests/system/cache/test_cache.py"],
    "keys": ["tests/system/keys/test_keys.py"],
    "invariants": ["tests/system/invariants/test_invariants.py"],
    "bin": ["tests/system/bin/test_bin.py"],
    "images": ["tests/system/images/test_images.py"],
    "kernel": [
        "tests/system/kernel/test_kernel_import.py",
    ],
    "network": [
        "tests/system/network/test_network.py",
    ],
    "host": ["tests/system/host/test_host.py"],
    "run": ["tests/system/run/test_run.py"],
}

# Tier 2: shared volume + nested virt (VM creation/interaction)
TIER2_DOMAINS: dict[str, list[str]] = {
    "volume": [
        "tests/system/volume/test_volume.py",
    ],
    "vm_lifecycle": ["tests/system/vm/test_vm_lifecycle.py"],
    "ssh": ["tests/system/ssh/test_ssh.py"],
    "console": ["tests/system/console/test_console.py"],
    "logs": ["tests/system/logs/test_logs.py"],
    "full_journeys": ["tests/system/full_journeys/test_full_journeys.py"],
    "env": ["tests/system/env/test_env.py"],
    "nftables": ["tests/system/network/test_nftables.py"],
}

# Tier 3: directly on host (no runner VM)
TIER3_DOMAINS: dict[str, list[str]] = {
    "vm_nested_virt": ["tests/system/vm/test_vm_fresh_env.py"],
    "vm_nested_isolated": ["tests/system/vm/test_vm_nested_isolated.py"],
    "vm_fresh_env": ["tests/system/vm/test_vm_fresh_env.py"],
    "vm_snapshot_load": ["tests/system/vm/test_vm_snapshot_load.py"],
    "kernel_build": ["tests/system/kernel/test_kernel.py"],
    "volume_hotplug": [
        "tests/system/volume/test_volume_hotplug.py",
    ],
    "cp": ["tests/system/cp/test_cp.py"],
}

# Tier classification for display
TIER_LABELS: dict[str, int] = {}
for d in TIER1_DOMAINS:
    TIER_LABELS[d] = 1
for d in TIER2_DOMAINS:
    TIER_LABELS[d] = 2
for d in TIER3_DOMAINS:
    TIER_LABELS[d] = 3

# ============================================================================
# Helpers
# ============================================================================


def mvm(
    *args: str, check: bool = True, timeout: int = 300, capture: bool = True
) -> subprocess.CompletedProcess:
    """Run an mvm command on the host."""
    if MVM_BINARY == "mvm":
        cmd = ["mvm", *args]
    else:
        cmd = shlex.split(MVM_BINARY) + list(args)
    result = subprocess.run(
        cmd,
        capture_output=capture,
        text=True,
        timeout=timeout,
        env={**os.environ, "NO_COLOR": "1"},
    )
    if not capture:
        # Stream output live so the user can see mirror vs HTTP download decision
        if result.stdout:
            print(result.stdout, end="", flush=True)
        if result.stderr:
            print(result.stderr, end="", flush=True, file=sys.stderr)
    if check and result.returncode != 0:
        raise RuntimeError(
            f"mvm command failed: {' '.join(args)}\n"
            f"  rc: {result.returncode}\n"
            f"  stderr: {result.stderr.strip()}"
        )
    return result


def log(msg: str) -> None:
    """Print a timestamped log message."""
    ts = time.strftime("%H:%M:%S")
    print(f"[{ts}] {msg}", flush=True)


def _run_pytest(
    test_files: list[str],
    *,
    xdist: bool = False,
    timeout: int = 600,
    extra_env: dict[str, str] | None = None,
) -> subprocess.CompletedProcess:
    """Run pytest against the given test files on the host (Tier 3)."""
    cmd = ["python3", "-m", "pytest", "--tb=short", "-q"]
    if xdist:
        cmd.append("-n")
        cmd.append("auto")
    cmd.extend(test_files)
    env = {**os.environ, "NO_COLOR": "1"}
    if extra_env:
        env.update(extra_env)
    result = subprocess.run(
        cmd,
        capture_output=True,
        text=True,
        timeout=timeout,
        env=env,
    )
    return result


# ============================================================================
# Shared Volume Management
# ============================================================================


def _get_volume_path(name: str) -> str | None:
    """Get the on-disk path of a volume, or None if it doesn't exist."""
    result = mvm("volume", "inspect", name, "--json", check=False, timeout=30)
    if result.returncode != 0:
        return None
    try:
        info = json.loads(result.stdout)
        return info.get("volume", {}).get("path")
    except (json.JSONDecodeError, KeyError):
        return None


def _create_and_seed_volume() -> None:
    """Create the shared asset volume and seed it with asset mirror contents.

    This requires sudo for loop mount. Prompts the user for a password.
    """
    log(f"Creating shared volume '{SHARED_VOLUME_NAME}' ({SHARED_VOLUME_SIZE})...")
    mvm(
        "volume",
        "create",
        SHARED_VOLUME_NAME,
        SHARED_VOLUME_SIZE,
        "--shareable",
        "--read-only",
        "--format",
        "raw",
        timeout=30,
    )

    vol_path = _get_volume_path(SHARED_VOLUME_NAME)
    if not vol_path:
        log("ERROR: Failed to find volume path after creation")
        sys.exit(1)

    # Check if asset mirror has content
    mirror_path = Path(ASSET_MIRROR_HOST).expanduser()
    if not mirror_path.exists() or not any(mirror_path.iterdir()):
        log(f"WARNING: Asset mirror at {mirror_path} is empty or missing.")
        log(
            "Populating volume with empty filesystem. Tests that need assets will fail."
        )

    # Need sudo for loop mount
    log("Populating volume with asset mirror contents (requires sudo)...")
    password = getpass.getpass("sudo password: ")

    def sudo_run(cmd_args: list[str], input_data: str | None = None) -> None:
        full_cmd = ["sudo", "-S"] + cmd_args
        subprocess.run(
            full_cmd,
            input=input_data or "",
            text=True,
            capture_output=True,
            check=True,
            timeout=120,
        )

    mount_point = "/mnt/.mvm-asset-populate"
    try:
        sudo_run(["mkfs.ext4", "-F", vol_path], password)
        Path(mount_point).mkdir(parents=True, exist_ok=True)
        sudo_run(["mount", "-o", "loop", vol_path, mount_point], password)
        sudo_run(
            ["cp", "-r"]
            + [str(p) for p in mirror_path.glob("*")]
            + [f"{mount_point}/"],
            password,
        )
        sudo_run(["chmod", "-R", "a+rX", mount_point], password)
        log(f"Seeded volume with contents from {mirror_path}")
    except subprocess.CalledProcessError as e:
        log(f"ERROR: Failed to populate volume: {e.stderr if e.stderr else e}")
        # Clean up mount if it succeeded
        subprocess.run(
            ["sudo", "-S", "umount", mount_point],
            input=password,
            text=True,
            capture_output=True,
            check=False,
            timeout=30,
        )
        sys.exit(1)
    finally:
        sudo_run(["umount", mount_point], password)
        try:
            Path(mount_point).rmdir()
        except OSError:
            pass


def ensure_shared_volume(*, rebuild: bool = False) -> None:
    """Ensure the shared asset volume exists and is populated.

    If the volume doesn't exist (or --rebuild is passed), remove it,
    recreate it, and populate it with the host asset mirror contents.
    """
    mirror_path = Path(ASSET_MIRROR_HOST).expanduser()
    if not mirror_path.exists():
        log(f"WARNING: Asset mirror at {mirror_path} does not exist.")
        log(
            "Create it first by pulling images/kernels/binaries with MVM_ASSET_MIRROR set."
        )

    existing_path = _get_volume_path(SHARED_VOLUME_NAME)
    if existing_path and not rebuild:
        log(f"Shared volume '{SHARED_VOLUME_NAME}' already exists at {existing_path}")
        return

    if existing_path and rebuild:
        log(f"Rebuilding: removing existing volume '{SHARED_VOLUME_NAME}'...")
        mvm("volume", "rm", SHARED_VOLUME_NAME, "--force", check=False, timeout=30)

    _create_and_seed_volume()


def ensure_test_network() -> None:
    """Create the shared test network if it doesn't exist."""
    result = mvm(
        "network", "inspect", TEST_NETWORK_NAME, "--json", check=False, timeout=15
    )
    if result.returncode == 0:
        log(f"Test network '{TEST_NETWORK_NAME}' already exists")
        return
    log(f"Creating test network '{TEST_NETWORK_NAME}'...")
    mvm(
        "network",
        "create",
        TEST_NETWORK_NAME,
        "--subnet",
        "10.88.0.0/24",
        "--non-interactive",
        timeout=30,
    )


# ============================================================================
# VM Provisioning
# ============================================================================


def _get_mvm_version() -> str:
    """Get the mvm binary version string (e.g. '0.1.0' or '0.1.0-7-gdeadbeef').

    Strips the ``-dirty`` suffix if present — the base image was built from a
    clean tree and uses the version without ``-dirty``.
    """
    result = mvm("--version", timeout=10)
    # "mvm 0.1.0" → "0.1.0"
    parts = result.stdout.strip().split()
    version = parts[-1] if len(parts) >= 2 else "latest"
    return version.removesuffix("-dirty")


def _unique_name(prefix: str = "sys-runner") -> str:
    return f"{prefix}-{uuid.uuid4().hex[:8]}"


def _ensure_builder_key() -> None:
    """Create the builder SSH key if it doesn't exist yet."""
    check = mvm("key", "inspect", BUILDER_KEY_NAME, check=False, timeout=10)
    if check.returncode != 0:
        mvm(
            "key",
            "create",
            BUILDER_KEY_NAME,
            "--algorithm",
            "ed25519",
            "--force",
            timeout=30,
        )


def provision_t1(vm_name: str, mvm_version: str) -> None:
    """Provision a Tier 1 VM from custom base image + shared volume."""
    _ensure_builder_key()
    _ensure_official_kernel_on_host(vm_name)
    log(
        f"  Creating T1 VM '{vm_name}' (from {BASE_IMAGE_NAME}:{mvm_version} + volume)..."
    )
    mvm(
        "vm",
        "create",
        vm_name,
        "--image",
        f"{BASE_IMAGE_NAME}:{mvm_version}",
        "--user",
        "runner",
        "--vcpu",
        "2",
        "--mem",
        "1024",
        "--disk-size",
        "9G",
        "--kernel",
        "official:7.0.11",
        "--ssh-key",
        BUILDER_KEY_NAME,
        "--nested-virt",
        "--network",
        TEST_NETWORK_NAME,
        "--volume",
        SHARED_VOLUME_NAME,
        timeout=180,
    )
    # CRITICAL: mvm init MUST run as the unprivileged user (runner), NOT via sudo.
    # Running as root creates the cache dir with root ownership — test VMs inherit
    # this state and break with 'permission denied' on /home/runner/.cache/mvmctl.
    mvm(
        "vm",
        "exec",
        vm_name,
        "--user",
        "runner",
        "--timeout",
        "300",
        "--",
        "sudo mkdir -p /mnt && sudo mount /dev/vdb /mnt && "
        "MVM_ASSET_MIRROR=/mnt mvm init --non-interactive",
        timeout=360,
    )


def _ensure_official_kernel_on_host(vm_name: str) -> None:
    """Ensure official:7.0.11 kernel is available on the HOST.

    Tier 3 kernel_build tests may remove it (test_kernel_remove, etc.).
    Uses a lockfile to prevent concurrent pulls from parallel workers.
    """
    lockfile = "/tmp/mvm-kernel-pull.lock"
    check = mvm(
        "kernel", "inspect", "official:7.0.11", "--json", check=False, timeout=15
    )
    if check.returncode == 0:
        return
    log(f"  official:7.0.11 not found — pulling (this may take a few minutes)...")
    # Acquire lock to prevent concurrent pulls
    import fcntl

    with open(lockfile, "w") as lf:
        fcntl.flock(lf, fcntl.LOCK_EX)
        try:
            # Re-check after acquiring lock (another worker may have pulled)
            check2 = mvm(
                "kernel",
                "inspect",
                "official:7.0.11",
                "--json",
                check=False,
                timeout=15,
            )
            if check2.returncode == 0:
                return
            mvm(
                "kernel",
                "pull",
                "official:7.0.11",
                "--features",
                "nftables,tuntap,kvm,btrfs",
                timeout=900,
            )
        finally:
            fcntl.flock(lf, fcntl.LOCK_UN)


def provision_t2(vm_name: str, mvm_version: str) -> None:
    """Provision a Tier 2 VM from custom base image + shared volume (binary + tests + deps pre-installed)."""
    _ensure_builder_key()
    _ensure_official_kernel_on_host(vm_name)
    log(
        f"  Creating T2 VM '{vm_name}' (from {BASE_IMAGE_NAME}:{mvm_version} + shared volume)..."
    )
    mvm(
        "vm",
        "create",
        vm_name,
        "--image",
        f"{BASE_IMAGE_NAME}:{mvm_version}",
        "--kernel",
        "official:7.0.11",
        "--vcpu",
        "4",
        "--mem",
        "4096",
        "--disk-size",
        "9G",
        "--network",
        TEST_NETWORK_NAME,
        "--user",
        "runner",
        "--ssh-key",
        BUILDER_KEY_NAME,
        "--nested-virt",
        "--volume",
        SHARED_VOLUME_NAME,
        timeout=300,
    )

    # Inside VM: mount shared volume, init, register assets
    # CRITICAL: mvm init MUST run as the unprivileged user (runner), NOT via sudo.
    # Running as root creates the cache dir with root ownership — test VMs inherit
    # this state and break with 'permission denied' on /home/runner/.cache/mvmctl.
    log(f"  Initializing mvm inside '{vm_name}'...")
    mvm(
        "vm",
        "exec",
        vm_name,
        "--user",
        "runner",
        "--timeout",
        "60",
        "--",
        "sudo mkdir -p /mnt && sudo mount /dev/vdb /mnt && "
        "MVM_ASSET_MIRROR=/mnt mvm init --non-interactive",
        timeout=180,
    )

    log(f"  Registering assets in '{vm_name}' (cache hits)...")
    mvm(
        "vm",
        "exec",
        vm_name,
        "--user",
        "runner",
        "--timeout",
        "120",
        "--",
        "MVM_ASSET_MIRROR=/mnt mvm kernel pull --type firecracker "
        "--version v1.15 --default",
        timeout=150,
    )
    mvm(
        "vm",
        "exec",
        vm_name,
        "--user",
        "runner",
        "--timeout",
        "120",
        "--",
        "MVM_ASSET_MIRROR=/mnt mvm image pull alpine:3.23",
        timeout=150,
    )
    mvm(
        "vm",
        "exec",
        vm_name,
        "--user",
        "runner",
        "--timeout",
        "300",
        "--",
        "MVM_ASSET_MIRROR=/mnt mvm image pull ubuntu:noble",
        timeout=360,
    )
    mvm(
        "vm",
        "exec",
        vm_name,
        "--user",
        "runner",
        "--timeout",
        "120",
        "--",
        "MVM_ASSET_MIRROR=/mnt mvm bin pull firecracker "
        "--version 1.16.0 --default --force",
        timeout=150,
    )


def destroy_vm(vm_name: str) -> None:
    """Destroy a runner VM."""
    mvm("vm", "rm", vm_name, "--force", timeout=60, check=False)


def _build_base_image(mvm_version: str, *, rebuild: bool = False) -> str:
    """Build a custom base image with all test dependencies pre-installed.

    Creates a VM from ubuntu-minimal:noble, copies in the mvm binary and
    system tests, installs OS packages, stops the VM, imports the rootfs
    as a custom image, then destroys the builder VM.
    Returns the image version tag used.
    """
    img_tag = mvm_version
    log(f"Building custom base image '{BASE_IMAGE_NAME}:{img_tag}'...")

    # When rebuilding, remove the existing image first so it's rebuilt fresh.
    if rebuild:
        mvm(
            "image",
            "rm",
            f"{BASE_IMAGE_NAME}:{img_tag}",
            "--force",
            check=False,
            timeout=15,
        )

    # Check if already built (skip check when rebuilding)
    if not rebuild:
        check = mvm(
            "image",
            "inspect",
            f"{BASE_IMAGE_NAME}:{img_tag}",
            "--json",
            check=False,
            timeout=15,
        )
        if check.returncode == 0:
            log(f"  Base image '{BASE_IMAGE_NAME}:{img_tag}' already exists, skipping")
            return img_tag

    # Clean up any leftover builder VM from a previous aborted run
    destroy_vm(BASE_VM_NAME)

    log(f"  Creating builder VM '{BASE_VM_NAME}'...")
    mvm(
        "vm",
        "create",
        BASE_VM_NAME,
        "--image",
        "ubuntu-minimal:noble",
        "--user",
        "runner",
        "--vcpu",
        "4",
        "--mem",
        "3G",
        "--disk-size",
        BASE_VM_DISK,
        "--nested-virt",
        "--network",
        TEST_NETWORK_NAME,
        "--volume",
        SHARED_VOLUME_NAME,
        timeout=180,
    )
    try:
        log(f"  Copying mvm binary and system tests into '{BASE_VM_NAME}'...")
        mvm("cp", MVM_BINARY, f"{BASE_VM_NAME}:/usr/local/bin/mvm", timeout=60)
        # Verify the binary transferred correctly
        verify = mvm(
            "vm",
            "exec",
            BASE_VM_NAME,
            "--user",
            "runner",
            "--timeout",
            "15",
            "--",
            "stat -c%s /usr/local/bin/mvm",
            timeout=20,
        )
        expected = Path(shlex.split(MVM_BINARY)[0]).expanduser().stat().st_size
        actual = int(verify.stdout.strip())
        if actual != expected:
            raise RuntimeError(
                f"mvm binary size mismatch in builder VM: expected {expected}, got {actual}"
            )
        mvm("cp", "tests/system", f"{BASE_VM_NAME}:/tests/", timeout=60)

        log(f"  Installing test dependencies in '{BASE_VM_NAME}'...")
        mvm(
            "vm",
            "exec",
            BASE_VM_NAME,
            "--user",
            "runner",
            "--timeout",
            "240",
            "--",
            "sudo apt-get update -qq && "
            "sudo apt-get install -y -qq "
            "python3-pytest qemu-utils nftables iptables zstd htop "
            "build-essential bc bison flex libncurses-dev "
            "libssl-dev libelf-dev git curl dwarves "
            "cloud-image-utils && "
            "sudo apt-get clean",
            timeout=300,
        )
        log("  Installing Python test packages...")
        mvm(
            "vm",
            "exec",
            BASE_VM_NAME,
            "--user",
            "runner",
            "--timeout",
            "60",
            "--",
            "pip3 install pytest-timeout --break-system-packages --quiet 2>&1 | tail -3",
            timeout=90,
        )
        log("  Adding runner to required groups (mvm, kvm)...")
        mvm(
            "vm",
            "exec",
            BASE_VM_NAME,
            "--user",
            "runner",
            "--timeout",
            "15",
            "--",
            "sudo groupadd -f mvm && "
            "sudo usermod -aG mvm runner && "
            "sudo usermod -aG kvm runner",
            timeout=30,
        )
        log("  Changing ownership of /tests to runner user...")
        mvm(
            "vm",
            "exec",
            BASE_VM_NAME,
            "--user",
            "runner",
            "--timeout",
            "15",
            "--",
            "sudo chown -R runner:runner /tests",
            timeout=30,
        )
        log(f"  Initializing mvm inside '{BASE_VM_NAME}'...")
        # CRITICAL: mvm init MUST run as the unprivileged user (runner), NOT via sudo.
        # Running as root creates the cache dir at /root/.cache/mvmctl or with root
        # ownership. The base image caches this state — test VMs run as 'runner' and
        # need /home/runner/.cache/mvmctl owned by runner. Permission denied = broken.
        # Asset pulls are pre-baked into the base image so each test VM doesn't
        # re-pull them (saves ~3 min per T1 VM).
        mvm(
            "vm",
            "exec",
            BASE_VM_NAME,
            "--user",
            "runner",
            "--timeout",
            "600",
            "--",
            "sudo mkdir -p /mnt && "
            "sudo mount /dev/vdb /mnt && "
            "MVM_ASSET_MIRROR=/mnt mvm init --non-interactive && "
            "MVM_ASSET_MIRROR=/mnt mvm binary pull firecracker --default --force --version 1.16.0 && "
            "MVM_ASSET_MIRROR=/mnt mvm kernel pull --type firecracker --version v1.15 --default && "
            "MVM_ASSET_MIRROR=/mnt mvm image pull alpine:3.23 && "
            "MVM_ASSET_MIRROR=/mnt mvm image pull ubuntu-minimal:24.04 && "
            "MVM_ASSET_MIRROR=/mnt mvm image pull ubuntu --version 24.04 && "
            "echo 'Verifying cached image integrity...' && "
            "for f in /home/runner/.cache/mvmctl/images/*.zst; do "
            'zstd -t "$f" || exit 1; '
            "done && "
            "echo 'All images verified OK'",
            timeout=360,
            capture=False,
        )
        log(f"  Stopping '{BASE_VM_NAME}'...")
        mvm("vm", "stop", BASE_VM_NAME, timeout=60)

        log(f"  Importing image as '{BASE_IMAGE_NAME}:{img_tag}'...")
        mvm(
            "image",
            "import",
            f"{BASE_IMAGE_NAME}:{img_tag}",
            BASE_VM_NAME,
            "--default",
            "--skip-optimization",
            timeout=300,
        )
        log(f"  Base image '{BASE_IMAGE_NAME}:{img_tag}' built successfully")
    finally:
        destroy_vm(BASE_VM_NAME)

    return img_tag


# ============================================================================
# Test Execution per Domain
# ============================================================================


def run_tier1_domain(
    domain: str, test_files: list[str], mvm_version: str, push: bool = False
) -> dict[str, Any]:
    """Full lifecycle for one Tier 1 domain. Max 5 minutes per domain."""
    vm_name = _unique_name(f"t1-{domain}")
    result: dict[str, Any] = {
        "domain": domain,
        "tier": 1,
        "passed": False,
        "output": "",
    }

    try:
        provision_t1(vm_name, mvm_version)
        if push:
            log(f"  Pushing test files into '{vm_name}'...")
            mvm("cp", "-f", "tests/system", f"{vm_name}:/tests/", timeout=60)
            # Clear Python bytecode cache so updated .py files are used
            mvm(
                "vm",
                "exec",
                vm_name,
                "--user",
                "runner",
                "--timeout",
                "10",
                "--",
                "find /tests -name '*.pyc' -delete 2>/dev/null; true",
                check=False,
                timeout=30,
            )
        log(f"  Running {domain} tests...")
        pytest_result = mvm(
            "vm",
            "exec",
            vm_name,
            "--user",
            "runner",
            "--timeout",
            "600",  # 10 minutes per test run (invariants may take >8 min)
            "--",
            f"cd / && MVM_ASSET_MIRROR=/mnt MVM_TEST_VM={vm_name} "
            f"python3 -m pytest {' '.join(f'/{f}' for f in test_files)} --tb=short -q",
            check=False,
            timeout=660,
        )
        result["passed"] = pytest_result.returncode == 0
        result["output"] = pytest_result.stdout + pytest_result.stderr
    except Exception as e:
        result["output"] = str(e)
    finally:
        destroy_vm(vm_name)

    return result


def run_tier2_domain(
    domain: str, test_files: list[str], mvm_version: str, push: bool = False
) -> dict[str, Any]:
    """Full lifecycle for one Tier 2 domain."""
    vm_name = _unique_name(f"t2-{domain}")
    result: dict[str, Any] = {
        "domain": domain,
        "tier": 2,
        "passed": False,
        "output": "",
    }

    try:
        provision_t2(vm_name, mvm_version)
        if push:
            log(f"  Pushing test files into '{vm_name}'...")
            mvm("cp", "-f", "tests/system", f"{vm_name}:/tests/", timeout=60)
            # Clear Python bytecode cache so updated .py files are used
            mvm(
                "vm",
                "exec",
                vm_name,
                "--user",
                "runner",
                "--timeout",
                "10",
                "--",
                "find /tests -name '*.pyc' -delete 2>/dev/null; true",
                check=False,
                timeout=30,
            )
        log(f"  Running {domain} tests...")
        pytest_result = mvm(
            "vm",
            "exec",
            vm_name,
            "--user",
            "runner",
            "--timeout",
            "900",
            "--",
            f"cd / && MVM_ASSET_MIRROR=/mnt MVM_TEST_VM={vm_name} "
            f"python3 -m pytest {' '.join(f'/{f}' for f in test_files)} --tb=short -q",
            check=False,
            timeout=960,
        )
        result["passed"] = pytest_result.returncode == 0
        result["output"] = pytest_result.stdout + pytest_result.stderr
    except Exception as e:
        result["output"] = str(e)
    finally:
        destroy_vm(vm_name)

    return result


def run_tier3_domain(domain: str, test_files: list[str]) -> dict[str, Any]:
    """Run one Tier 3 domain directly on the host."""
    result: dict[str, Any] = {
        "domain": domain,
        "tier": 3,
        "passed": False,
        "output": "",
    }
    log(f"  Running {domain} tests on host...")
    try:
        pytest_result = _run_pytest(test_files)
        result["passed"] = pytest_result.returncode == 0
        result["output"] = pytest_result.stdout + pytest_result.stderr
    except Exception as e:
        result["output"] = str(e)
    return result


# ============================================================================
# Main Orchestrator
# ============================================================================


def run_domains(
    domains: dict[str, list[str]],
    tier: int,
    runner_fn,
    workers: int,
    mvm_version: str,
    push: bool = False,
) -> list[dict[str, Any]]:
    """Run a set of domains in parallel using the given runner function."""
    if not domains:
        return []

    log(f"Tier {tier}: running {len(domains)} domain(s) with {workers} worker(s)...")
    results: list[dict[str, Any]] = []
    # Overall timeout per domain: 15 minutes.
    domain_timeout = 900

    pool = ThreadPoolExecutor(max_workers=workers)
    try:
        future_map = {
            pool.submit(runner_fn, domain, files, mvm_version, push): domain
            for domain, files in domains.items()
        }
        import concurrent.futures

        remaining = set(future_map.keys())
        while remaining:
            done, remaining = concurrent.futures.wait(
                remaining,
                timeout=domain_timeout,
                return_when=concurrent.futures.FIRST_COMPLETED,
            )
            if not done:
                # Timeout reached — no futures completed within domain_timeout
                log(
                    f"  [TIMEOUT] {len(remaining)} domain(s) timed out after {domain_timeout}s — moving on"
                )
                for future in remaining:
                    domain = future_map[future]
                    results.append(
                        {
                            "domain": domain,
                            "tier": tier,
                            "passed": False,
                            "output": f"Domain timed out after {domain_timeout}s",
                        }
                    )
                    log(f"  [TIMEOUT] {domain}")
                break
            for future in done:
                domain = future_map[future]
                try:
                    domain_result = future.result()
                    results.append(domain_result)
                    status = "PASS" if domain_result["passed"] else "FAIL"
                    log(f"  [{status}] {domain} (tier {domain_result['tier']})")
                    if not domain_result["passed"]:
                        _print_failure(domain, domain_result)
                except Exception as e:
                    results.append(
                        {
                            "domain": domain,
                            "tier": tier,
                            "passed": False,
                            "output": str(e),
                        }
                    )
                    log(f"  [ERROR] {domain}: {e}")
    finally:
        # Don't wait for timed-out threads — they'll be daemon-killed on exit
        pool.shutdown(wait=False, cancel_futures=True)

    return results


def _print_failure(domain: str, result: dict[str, Any]) -> None:
    """Print the full failure output."""
    output = result.get("output", "")
    if not output:
        return
    print(f"    --- {domain} failure output ---")
    print(output)
    print(f"    --- end {domain} failure ---")


def print_summary(all_results: list[dict[str, Any]]) -> None:
    """Print a summary of all test results."""
    total = len(all_results)
    passed = sum(1 for r in all_results if r["passed"])
    failed = total - passed

    print()
    print("=" * 60)
    print(f"  RESULTS: {passed} passed, {failed} failed, {total} total")
    print("=" * 60)
    for r in sorted(all_results, key=lambda x: (x["tier"], x["domain"])):
        status = "PASS" if r["passed"] else "FAIL"
        print(f"  [{status}] Tier {r['tier']} {r['domain']}")
    print("=" * 60)

    if failed > 0:
        sys.exit(1)


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Run mvmctl system tests with per-domain VM isolation.",
    )
    parser.add_argument(
        "domains",
        nargs="*",
        help="Specific domains to test (default: all).",
    )
    parser.add_argument(
        "--tier1-only",
        action="store_true",
        help="Run only Tier 1 domains.",
    )
    parser.add_argument(
        "--tier2-only",
        action="store_true",
        help="Run only Tier 2 domains.",
    )
    parser.add_argument(
        "--tier3-only",
        action="store_true",
        help="Run only Tier 3 domains.",
    )
    parser.add_argument(
        "--workers",
        type=int,
        default=4,
        help="Maximum parallel VMs (default: 4).",
    )
    parser.add_argument(
        "--rebuild",
        action="store_true",
        help="Remove and recreate shared volume AND base image from scratch.",
    )
    parser.add_argument(
        "--skip-volume-check",
        action="store_true",
        help="Skip shared volume check (assume it exists).",
    )
    parser.add_argument(
        "--prepare",
        action="store_true",
        help="Build custom base image (mvm binary + tests + deps), smoke-test "
        "T1 and T2 provisioning. Run before running test suites.",
    )
    parser.add_argument(
        "--push",
        action="store_true",
        help="Push test files into each VM before running (overrides baked-in tests). "
        "Use when modifying tests without rebuilding the base image.",
    )
    return parser.parse_args()


def run_prepare(*, rebuild: bool = False) -> None:
    """Smoke-test the provisioning pipeline.

    Builds a custom base image from ubuntu-minimal:noble with all test
    dependencies pre-installed, then validates T1 and T2 provisioning.
    """
    log("=== Prepare: provisioning pipeline ===")

    # --- Step 1: Get mvm version ---
    log("[1/8] Detecting mvm version...")
    mvm_version = _get_mvm_version()
    log(f"      mvm version: {mvm_version}")

    # --- Step 1: Ensure shared volume and network ---
    log("[1/8] Checking shared volume and network...")
    ensure_shared_volume(rebuild=rebuild)
    ensure_test_network()

    # --- Step 2: Ensure kernel with nftables support ---
    log("[2/8] Ensuring kernel with nftables support...")
    mvm(
        "kernel",
        "pull",
        "official:7.0.11",
        "--default",
        "--features",
        "nftables,tuntap,kvm,btrfs",
        timeout=900,
    )

    # --- Step 3: Build custom base image ---
    log("[3/8] Building custom base image...")
    _build_base_image(mvm_version, rebuild=rebuild)

    # --- Step 4: Create T1 VM from custom image + volume ---
    t1 = _unique_name("prep-t1")
    log(f"[4/8] Creating T1 VM '{t1}' from {BASE_IMAGE_NAME}:{mvm_version} + volume...")
    mvm(
        "vm",
        "create",
        t1,
        "--image",
        f"{BASE_IMAGE_NAME}:{mvm_version}",
        "--user",
        "runner",
        "--vcpu",
        "2",
        "--mem",
        "1024",
        "--disk-size",
        "9G",
        "--nested-virt",
        "--network",
        TEST_NETWORK_NAME,
        "--volume",
        SHARED_VOLUME_NAME,
        timeout=180,
    )
    try:
        log(f"      Running mvm init inside '{t1}'...")
        mvm(
            "vm",
            "exec",
            t1,
            "--user",
            "runner",
            "--timeout",
            "60",
            "--",
            "sudo mkdir -p /mnt && sudo mount /dev/vdb /mnt && "
            "MVM_ASSET_MIRROR=/mnt mvm init --non-interactive",
            timeout=90,
        )
    finally:
        destroy_vm(t1)

    # --- Step 5: Create T2 VM from custom image + shared volume ---
    t2 = _unique_name("prep-t2")
    log(
        f"[5/8] Creating T2 VM '{t2}' from {BASE_IMAGE_NAME}:{mvm_version} + shared volume..."
    )
    mvm(
        "vm",
        "create",
        t2,
        "--image",
        f"{BASE_IMAGE_NAME}:{mvm_version}",
        "--kernel",
        "official:7.0.11",
        "--user",
        "runner",
        "--vcpu",
        "4",
        "--mem",
        "4096",
        "--disk-size",
        "9G",
        "--network",
        TEST_NETWORK_NAME,
        "--nested-virt",
        "--volume",
        SHARED_VOLUME_NAME,
        timeout=300,
    )
    try:
        log(f"[6/8] Setting up '{t2}' (mount + init)...")
        mvm(
            "vm",
            "exec",
            t2,
            "--user",
            "runner",
            "--timeout",
            "120",
            "--",
            "sudo mkdir -p /mnt && sudo mount /dev/vdb /mnt && "
            "MVM_ASSET_MIRROR=/mnt mvm init --non-interactive",
            timeout=180,
        )

        log(f"[7/8] Validating cache hit (pulling 1 asset)...")
        result = mvm(
            "vm",
            "exec",
            t2,
            "--user",
            "runner",
            "--timeout",
            "120",
            "--",
            "MVM_ASSET_MIRROR=/mnt mvm image pull alpine:3.23 2>&1 | head -5",
            timeout=150,
            check=False,
        )
        if "Downloading image" in result.stdout or "Downloading image" in result.stderr:
            log(f"  Cache hit confirmed: asset pulled from local mirror")
        else:
            log(f"  Pull output: {result.stdout[:200] if result.stdout else '(empty)'}")

        vol_check = mvm("volume", "inspect", SHARED_VOLUME_NAME, "--json", timeout=15)
        import json as _json

        vol_info = _json.loads(vol_check.stdout)
        vol_status = vol_info.get("volume", {}).get("status", "unknown")

        log("=== Prepare: ALL STEPS PASSED ===")
        log(f"  Base image: {BASE_IMAGE_NAME}:{mvm_version}")
        log(f"  T1: '{t1}' — created, binary copied, init completed")
        log(f"  T2: '{t2}' — created, volume attached, cache hit verified")
        log(f"  Volume status: {vol_status} (correctly 'available')")
        log("  Environment is ready for running tests.")

    finally:
        destroy_vm(t2)


def main() -> None:
    args = parse_args()

    # Handle --rebuild: build binary before anything else
    if args.rebuild:
        log("Rebuilding mvm binary...")
        script_dir = Path(__file__).resolve().parent
        repo_root = script_dir.parent
        build_script = repo_root / "scripts" / "build.sh"
        result = subprocess.run(
            [
                str(build_script),
                "release",
                "--output",
                str(Path(MVM_BINARY).expanduser()),
            ],
            cwd=str(repo_root),
        )
        if result.returncode != 0:
            log("ERROR: Build failed")
            sys.exit(1)
        log("Build complete")

    # Validate MVM_BINARY exists
    binary = shlex.split(MVM_BINARY)[0]
    if not shutil.which(binary) and not Path(binary).is_file():
        log(f"ERROR: mvm binary not found: {MVM_BINARY}")
        log("Set MVM_BINARY or ensure 'mvm' is in PATH.")
        sys.exit(1)

    # Handle --prepare (build base image + smoke test, then exit)
    if args.prepare:
        run_prepare(rebuild=args.rebuild)
        return

    # Detect mvm version for base image lookup
    mvm_version = _get_mvm_version()

    # Ensure shared volume exists (unless skipped)
    if not args.skip_volume_check:
        ensure_shared_volume(rebuild=args.rebuild)
        ensure_test_network()

    # Select domains
    tier1 = dict(TIER1_DOMAINS)
    tier2 = dict(TIER2_DOMAINS)
    tier3 = dict(TIER3_DOMAINS)

    if args.domains:
        # Filter to requested domains
        all_domains = {}
        for d in args.domains:
            if d in tier1:
                all_domains[d] = tier1[d]
            elif d in tier2:
                all_domains[d] = tier2[d]
            elif d in tier3:
                all_domains[d] = tier3[d]
            else:
                log(f"WARNING: Unknown domain '{d}'. Skipping.")
        tier1 = {}
        tier2 = {}
        tier3 = {}
        for d, files in all_domains.items():
            t = TIER_LABELS.get(d, 0)
            if t == 1:
                tier1[d] = files
            elif t == 2:
                tier2[d] = files
            elif t == 3:
                tier3[d] = files

    if args.tier2_only:
        tier1 = {}
        tier3 = {}
    if args.tier1_only:
        tier2 = {}
        tier3 = {}
    if args.tier3_only:
        tier1 = {}
        tier2 = {}

    if not tier1 and not tier2 and not tier3:
        log("No domains selected. Use --help for options.")
        sys.exit(0)

    all_results: list[dict[str, Any]] = []

    # Run T1 in parallel
    if tier1:
        results = run_domains(
            tier1, 1, run_tier1_domain, args.workers, mvm_version, push=args.push
        )
        all_results.extend(results)

    # Run T2 in parallel
    if tier2:
        results = run_domains(
            tier2, 2, run_tier2_domain, args.workers, mvm_version, push=args.push
        )
        all_results.extend(results)

    # Run T3 (sequential — destructive tests need ordering)
    if tier3:
        log(f"Tier 3: running {len(tier3)} domain(s) on host...")
        for domain, files in sorted(tier3.items()):
            result = run_tier3_domain(domain, files)
            all_results.append(result)
            status = "PASS" if result["passed"] else "FAIL"
            log(f"  [{status}] {domain} (tier 3)")
            if not result["passed"]:
                _print_failure(domain, result)

    print_summary(all_results)


if __name__ == "__main__":
    # Import here to avoid circular import issues with argparse processing
    import shutil

    main()
