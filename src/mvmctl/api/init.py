"""Configuration wizard API --- host init escalation and default kernel build.

Provides the privilege-boundary functions for the configure wizard.
All subprocess calls and privilege escalation happen here, not in the CLI.
"""

from __future__ import annotations

import os
import shutil
import subprocess
import sys
from pathlib import Path

from mvmctl.api.assets import build_kernel_pipeline
from mvmctl.constants import DEFAULT_KERNEL_VERSION, KERNEL_TARBALL_URL_TEMPLATE
from mvmctl.core.kernel import load_kernel_spec
from mvmctl.exceptions import HostError, KernelError
from mvmctl.utils.fs import get_cache_dir, get_kernels_dir

__all__ = [
    "run_host_init_escalated",
    "build_default_kernel",
    "init_database",
]


def init_database() -> None:
    """Initialize the local state database.

    Creates the MVMDatabase instance and runs migrations.

    Raises:
        Exception: Any error from the database migration.
    """
    from mvmctl.core.mvm_db import MVMDatabase

    db = MVMDatabase()
    db.migrate()


def run_host_init_escalated() -> int:
    """Run host initialisation with sudo privilege escalation.

    This function calls 'sudo mvm host init' as a subprocess, which is
    required for one-time host setup (creating mvm group, sudoers drop-in,
    network bridges). After this setup, sudo is no longer needed.

    The subprocess suppresses the "running as root" warning by setting
    MVM_ESCALATED=1 in the environment.

    Returns:
        The return code from the sudo subprocess (0 on success).

    Raises:
        HostError: If the subprocess fails to launch or other system errors.
    """
    mvm_bin = shutil.which("mvm") or sys.argv[0]
    env = os.environ.copy()
    # Signal to the subprocess that this escalation was user-prompted so
    # the "running as root" warning is suppressed.
    env["MVM_ESCALATED"] = "1"

    try:
        result = subprocess.run(
            ["sudo", "-E", mvm_bin, "host", "init"],
            env=env,
            capture_output=False,
        )
        return result.returncode
    except Exception as exc:
        raise HostError(f"Failed to run host init: {exc}") from exc


def build_default_kernel() -> Path:
    """Build the default minimal kernel for Firecracker.

    Builds the kernel specified by DEFAULT_KERNEL_VERSION using the
    kernel-official spec if available. The resulting vmlinux binary
    is placed in the kernels directory.

    Returns:
        Path to the built kernel binary.

    Raises:
        KernelError: If the kernel build fails.
    """
    version = DEFAULT_KERNEL_VERSION
    out = get_kernels_dir() / "vmlinux"
    out.parent.mkdir(parents=True, exist_ok=True)

    kernel_spec = None
    try:
        spec = load_kernel_spec("kernel-official")
        # Official kernels need to be built from source
        if spec.kernel_type == "official":
            kernel_spec = spec
    except KernelError:
        pass

    source_url = KERNEL_TARBALL_URL_TEMPLATE.format(version=version)

    build_kernel_pipeline(
        version=version,
        source_url=source_url,
        output_path=out,
        build_dir=get_cache_dir() / "kernel-build",
        jobs=None,
        kernel_spec=kernel_spec,
    )

    return out
