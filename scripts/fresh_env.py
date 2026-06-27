#!/usr/bin/env python3
"""
fresh_env.py — Create a pristine mvmctl environment from scratch.

Wipes all state, sets up host infrastructure, pulls fresh assets,
creates a test VM (fenv) with an attached volume, and provisions it
via SSH.

Steps executed (in exact order):

   1. cache clean --force
   2. init --non-interactive
   3. key create test --default --algorithm ed25519 --force
   4. image pull ubuntu:noble --default
   5. kernel pull official:6.19.9 --default --features kvm,nftables,tuntap
   6. bin pull firecracker --git-ref main --default
    7. vm create fenv --vcpu 6 --mem 4g --nested-virt -s 8g
   8. vol create vol1 8g
   9. vm stop fenv
   10. volume attach fenv vol1
  11. vm start fenv
  12. ssh fenv --cmd 'apt update && apt install -y qemu-utils net-tools && mkfs.ext4 /dev/vdb && mount /dev/vdb /mnt'
  13. cp dist/mvm fenv:/root
  14. ssh fenv --cmd 'cp /root/mvm /usr/bin/mvm && mkdir -p /mnt/tmp'
  15. cp <default-kernel> fenv:/mnt/
  16. cp <default-image> fenv:/mnt/
  17. cp firecracker-v1.15.1-x86_64.tgz fenv:/mnt/
  18. ssh ... (init + config + image/kernel import + bin pull + vm create)

MVM_ASSET_MIRROR is set to ``~/.cache/mvm-asset-mirror`` for all steps so
assets are cached locally after download.

Usage:

    # Default (uv run mvm — no build required)
    python scripts/fresh_env.py

    # Use a specific mvm binary (pre-built)
    python scripts/fresh_env.py --bin ~/.local/bin/mvm

    # Use a different mvm command prefix
    python scripts/fresh_env.py --bin "uv run --frozen mvm"
"""

from __future__ import annotations

import argparse
import json
import os
import subprocess
import sys
import time
from pathlib import Path
from typing import Any

from common import (
    DEFAULT_MIRROR,
    add_bin_arg,
    print_banner,
    print_fail,
    print_info,
    print_step,
    print_success,
    print_warn,
    resolve_mvm_cmd,
    run_mvm,
)

# ---------------------------------------------------------------------------
# User-facing names (tweak these to match your naming preference)
# ---------------------------------------------------------------------------

KEY_NAME = "fenv-test-1"
VM_NAME = "fenv-vm-1"
VOLUME_NAME = "fenv-vol-1"
NESTED_VM_NAME = "nested-vm-1"
IMAGE_SELECTOR = "ubuntu:noble"
KERNEL_SELECTOR = "official:7.0.11"

# ---------------------------------------------------------------------------
# Resolver helpers
# ---------------------------------------------------------------------------


def _resolve_default_kernel_path(mvm_cmd: str) -> str:
    """Resolve the default kernel file path from mvm kernel ls --json.

    Returns the full path string. Exits the script with error if resolution fails.
    """
    cmd = mvm_cmd.split() + ["kernel", "ls", "--json"]
    env = os.environ.copy()
    env["MVM_ASSET_MIRROR"] = str(DEFAULT_MIRROR)
    try:
        result = subprocess.run(
            cmd, capture_output=True, text=True, timeout=30, env=env
        )
    except subprocess.TimeoutExpired:
        print_fail("Timed out resolving default kernel path")
        sys.exit(1)

    if result.returncode != 0:
        print_fail(f"Failed to list kernels (exit {result.returncode})")
        sys.exit(1)

    try:
        kernels = json.loads(result.stdout)
    except json.JSONDecodeError:
        print_fail("Failed to parse kernel list JSON output")
        sys.exit(1)

    default_kernel = next((k for k in kernels if k.get("is_default")), None)
    if default_kernel is None:
        print_fail("No default kernel found")
        sys.exit(1)

    path = default_kernel.get("path")
    if not path:
        print_fail("Default kernel has no path field")
        sys.exit(1)

    return str(path)


def _resolve_default_image_path(mvm_cmd: str) -> str:
    """Resolve the default image's decompressed ext4 path from warm cache.

    Runs ``mvm image ls --json`` to find the default image, then resolves
    its decompressed file at ``~/.cache/mvmctl/warm/{id}.{fs_type}`` (the
    warm image pool) rather than the compressed ``.zst`` in the image cache,
    so the file can be directly used by ``mvm image import``.
    """
    cmd = mvm_cmd.split() + ["image", "ls", "--json"]
    env = os.environ.copy()
    env["MVM_ASSET_MIRROR"] = str(DEFAULT_MIRROR)
    try:
        result = subprocess.run(
            cmd, capture_output=True, text=True, timeout=30, env=env
        )
    except subprocess.TimeoutExpired:
        print_fail("Timed out resolving default image path")
        sys.exit(1)

    if result.returncode != 0:
        print_fail(f"Failed to list images (exit {result.returncode})")
        sys.exit(1)

    try:
        images = json.loads(result.stdout)
    except json.JSONDecodeError:
        print_fail("Failed to parse image list JSON output")
        sys.exit(1)

    default_image = next((i for i in images if i.get("is_default")), None)
    if default_image is None:
        print_fail("No default image found")
        sys.exit(1)

    image_id = default_image.get("id")
    fs_type = default_image.get("fs_type")
    if not image_id or not fs_type:
        print_fail("Default image missing id or fs_type")
        sys.exit(1)

    from mvmctl.utils.common import CacheUtils

    warm_path = CacheUtils.get_warm_image_dir() / f"{image_id}.{fs_type}"
    if not warm_path.exists():
        print_fail(
            f"Decompressed image not found at warm cache: {warm_path}\n"
            f"Run the VM at least once to populate the warm cache, "
            f"or copy the compressed .zst and use --format raw"
        )
        sys.exit(1)

    return str(warm_path)


# ---------------------------------------------------------------------------
# Step builders (declared at module level for visibility)
# ---------------------------------------------------------------------------


def _build_chain_cmd(kernel_filename: str, image_filename: str) -> str:
    """Build the SSH chain command for nested VM setup inside the guest."""
    return (
        "export MVM_ASSET_MIRROR=/mnt MVM_TEMP_DIR=/mnt/tmp "
        "&& set -x "
        '&& echo "=== [1/8] mvm init --skip-network ===" '
        "&& mvm init --non-interactive --skip-network "
        '&& echo "=== [2/8] mvm config set subnet ===" '
        "&& mvm config set defaults.network subnet 10.10.0.0/24 "
        '&& echo "=== [3/8] mvm init (full) ===" '
        "&& mvm init --non-interactive "
        '&& echo "=== [4/8] mvm key create ===" '
        f"&& mvm key create {KEY_NAME} --default --algorithm ed25519 --force "
        '&& echo "=== [5/8] mvm image import ===" '
        f"&& mvm image import {IMAGE_SELECTOR} /mnt/{image_filename} --default --force "
        '&& echo "=== [6/8] mvm kernel import ===" '
        f"&& mvm kernel import {KERNEL_SELECTOR} /mnt/{kernel_filename} --version 7.0.11 --default "
        '&& echo "=== [7/8] mvm bin pull ===" '
        "&& MVM_ASSET_MIRROR=/mnt mvm bin pull firecracker --version 1.15.1 --default --force"
        '&& echo "=== [8/8] mvm vm create ===" '
        f"&& mvm vm create {NESTED_VM_NAME} --vcpu 2 --mem 512m --nested-virt -s 4g"
    )


def _build_base_steps() -> dict[str, dict[str, Any]]:
    """Build step definitions for steps 1-14 (no asset paths required).

    These steps set up the host infrastructure, pull assets, create the VM,
    install guest dependencies, and copy the mvm binary. They run *before*
    kernel/image path resolution because the database must be initialised
    first (step 2: ``mvm init``).
    """
    return {
        "wipe_caches": {
            "desc": "Wipe all caches",
            "args": ["cache", "clean", "--force"],
            "ignore_errors": True,
        },
        "init_host": {
            "desc": "Initialise host (bridges, iptables)",
            "args": ["init", "--non-interactive"],
            "sudo": True,
        },
        "create_key": {
            "desc": f"Create default ED25519 SSH key '{KEY_NAME}'",
            "args": [
                "key",
                "create",
                KEY_NAME,
                "--default",
                "--algorithm",
                "ed25519",
                "--force",
            ],
        },
        "pull_image": {
            "desc": f"Pull default image ({IMAGE_SELECTOR})",
            "args": ["image", "pull", IMAGE_SELECTOR, "--default"],
            "timeout": 1800,
        },
        "pull_kernel": {
            "desc": f"Pull kernel {KERNEL_SELECTOR} with features",
            "args": [
                "kernel",
                "pull",
                KERNEL_SELECTOR,
                "--default",
                "--features",
                "kvm,nftables,tuntap",
            ],
            "timeout": 7200,
        },
        "build_bin": {
            "desc": "Build Firecracker from git ref main",
            "args": [
                "bin",
                "pull",
                "firecracker",
                "--git-ref",
                "main",
                "--default",
            ],
            "timeout": 7200,
        },
        "create_vm": {
            "desc": f"Create VM '{VM_NAME}' (6 vcpu, 4G, nested-virt, 8G root)",
            "args": [
                "vm",
                "create",
                VM_NAME,
                "--vcpu",
                "6",
                "--mem",
                "4g",
                "--nested-virt",
                "-s",
                "20g",
            ],
            "timeout": 600,
        },
        "create_volume": {
            "desc": f"Create volume '{VOLUME_NAME}' (8g)",
            "args": ["vol", "create", VOLUME_NAME, "8g"],
            "timeout": 60,
        },
        "stop_vm": {
            "desc": f"Stop VM '{VM_NAME}'",
            "args": ["vm", "stop", VM_NAME],
            "timeout": 60,
        },
        "attach_volume": {
            "desc": f"Attach volume '{VOLUME_NAME}' to '{VM_NAME}'",
            "args": ["volume", "attach", VM_NAME, VOLUME_NAME],
            "timeout": 30,
        },
        "start_vm": {
            "desc": f"Start VM '{VM_NAME}'",
            "args": ["vm", "start", VM_NAME],
            "timeout": 120,
        },
        "ssh_setup": {
            "desc": f"SSH into '{VM_NAME}', install packages & mount volume",
            "args": [
                "ssh",
                VM_NAME,
                "--cmd",
                "apt update && apt install -y "
                "qemu-utils net-tools "
                "&& mkfs.ext4 /dev/vdb "
                "&& mount /dev/vdb /mnt",
            ],
            "timeout": 300,
            "max_retry_sec": 5,
            "retry_interval_sec": 1,
        },
        "copy_bin": {
            "desc": "Copy mvm binary into guest",
            "args": ["cp", "./mvm", f"{VM_NAME}:/root/"],
            "timeout": 60,
        },
        "install_bin": {
            "desc": "Install mvm binary & create temp dirs inside guest",
            "args": [
                "ssh",
                VM_NAME,
                "--cmd",
                "cp /root/mvm /usr/bin/mvm && mkdir -p /mnt/tmp",
            ],
            "timeout": 60,
        },
    }


def _build_copy_steps(
    kernel_path: str,
    image_path: str,
    chain_cmd: str,
) -> dict[str, dict[str, Any]]:
    """Build step definitions for steps 15-18 (requires resolved asset paths).

    These steps copy the kernel, rootfs image, and Firecracker tarball into
    the guest, then run the nested VM setup command. They execute *after*
    kernel/image path resolution, which requires a populated database.
    """
    return {
        "copy_kernel": {
            "desc": "Copy default kernel binary into guest",
            "args": ["cp", kernel_path, f"{VM_NAME}:/mnt/"],
            "timeout": 120,
        },
        "copy_image": {
            "desc": "Copy default image into guest",
            "args": ["cp", image_path, f"{VM_NAME}:/mnt/"],
            "timeout": 120,
        },
        "copy_bin_tgz": {
            "desc": "Copy Firecracker tarball into guest",
            "args": [
                "cp",
                str(DEFAULT_MIRROR / "firecracker-v1.15.1-x86_64.tgz"),
                f"{VM_NAME}:/mnt/",
            ],
            "timeout": 120,
        },
        "nested_setup": {
            "desc": "Set up nested VM environment inside guest (8 sub-steps with tracing)",
            "args": [
                "ssh",
                VM_NAME,
                "--cmd",
                chain_cmd,
            ],
            "timeout": 900,
            "ignore_errors": True,
        },
    }


# ---------------------------------------------------------------------------
# CLI
# ---------------------------------------------------------------------------


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description=__doc__,
        formatter_class=argparse.RawDescriptionHelpFormatter,
    )
    add_bin_arg(parser)
    return parser.parse_args()


# ---------------------------------------------------------------------------
# Entry point
# ---------------------------------------------------------------------------


def main() -> int:
    args = parse_args()
    mvm_cmd = resolve_mvm_cmd(args.bin)

    print_banner("Fresh mvmctl Environment Setup")
    print_info(f"  mvm command: {mvm_cmd}")
    print_info(f"  asset mirror: {DEFAULT_MIRROR}")
    DEFAULT_MIRROR.mkdir(parents=True, exist_ok=True)

    # ------------------------------------------------------------------
    # Phase 1: Base steps (1-14) — no asset resolution needed
    # ------------------------------------------------------------------
    base_steps = _build_base_steps()
    print_info(f"  base steps: {len(base_steps)}")
    print()

    total_start = time.monotonic()
    base_failed_step: int | None = None

    for i, (key, step) in enumerate(base_steps.items(), start=1):
        print_step(i, step["desc"])

        max_retries = step.get("max_retry_sec", 0)
        retry_interval = step.get("retry_interval_sec", 0)
        step_ok = False

        for attempt in range(max_retries + 1):
            if attempt > 0:
                print_info(
                    f"Retrying in {retry_interval}s "
                    f"(attempt {attempt + 1}/{max_retries + 1})..."
                )
                time.sleep(retry_interval)

            step_ok = run_mvm(
                mvm_cmd,
                step["args"],
                sudo=step.get("sudo", False),
                description=step["desc"],
                timeout=step.get("timeout", 7200),
            )
            if step_ok:
                break

        if not step_ok:
            if step.get("ignore_errors"):
                print_warn(
                    f"Step {i} failed but marked as ignorable — continuing"
                )
            else:
                base_failed_step = i
                break

    if base_failed_step is not None:
        total_elapsed = int(time.monotonic() - total_start)
        print()
        print_fail(
            f"Failed at base step {base_failed_step} after {total_elapsed}s"
        )
        print_info(
            "Fix the issue and re-run the script. It is safe to re-run --"
        )
        print_info(
            "all destructive commands use --force and will overwrite "
            "existing state."
        )
        return 1

    # ------------------------------------------------------------------
    # Phase 2: Resolve asset paths (DB now exists after step 2 = mvm init)
    # ------------------------------------------------------------------
    kernel_path = _resolve_default_kernel_path(mvm_cmd)
    image_path = _resolve_default_image_path(mvm_cmd)
    chain_cmd = _build_chain_cmd(Path(kernel_path).name, Path(image_path).name)

    # ------------------------------------------------------------------
    # Phase 3: Copy steps (15-18) — dependent on resolved asset paths
    # ------------------------------------------------------------------
    copy_steps = _build_copy_steps(kernel_path, image_path, chain_cmd)
    print()
    print_info(f"  copy steps: {len(copy_steps)}")
    print()

    copy_failed_step: int | None = None

    for i, (key, step) in enumerate(copy_steps.items(), start=15):
        print_step(i, step["desc"])

        max_retries = step.get("max_retry_sec", 0)
        retry_interval = step.get("retry_interval_sec", 0)
        step_ok = False

        for attempt in range(max_retries + 1):
            if attempt > 0:
                print_info(
                    f"Retrying in {retry_interval}s "
                    f"(attempt {attempt + 1}/{max_retries + 1})..."
                )
                time.sleep(retry_interval)

            step_ok = run_mvm(
                mvm_cmd,
                step["args"],
                sudo=step.get("sudo", False),
                description=step["desc"],
                timeout=step.get("timeout", 7200),
            )
            if step_ok:
                break

        if not step_ok:
            if step.get("ignore_errors"):
                print_warn(
                    f"Step {i} failed but marked as ignorable — continuing"
                )
            else:
                copy_failed_step = i
                break

    total_elapsed = int(time.monotonic() - total_start)
    print()

    if copy_failed_step is not None:
        print_fail(
            f"Failed at copy step {copy_failed_step} after {total_elapsed}s"
        )
        print_info(
            "Fix the issue and re-run the script. It is safe to re-run --"
        )
        print_info(
            "all destructive commands use --force and will overwrite "
            "existing state."
        )
        return 1

    print_banner("Environment Ready")
    print_success(f"All 18 steps completed in {total_elapsed}s")
    print()
    print_info(
        f"VM '{VM_NAME}' is running with volume '{VOLUME_NAME}' attached and mounted at /mnt."
    )
    print_info(f"Connect:  mvm ssh {VM_NAME}")
    print_info(f"Or use:   mvm console {VM_NAME}")
    print_info("Binary:   mvm copied to guest at /root/mvm")
    print_info("Guest env: export MVM_ASSET_MIRROR=/mnt MVM_TEMP_DIR=/mnt/tmp")
    print()
    return 0


if __name__ == "__main__":
    sys.exit(main())
