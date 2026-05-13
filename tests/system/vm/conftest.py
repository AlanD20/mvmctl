"""VM domain system tests — setup."""

from __future__ import annotations

import os
import uuid
from pathlib import Path
from typing import Any, Generator

import pytest

from tests.system.conftest import (
    _cleanup_vm_resources,
    _create_minimal_vm_core,
    _run_mvm,
)


@pytest.fixture(scope="session", autouse=True)
def prepare_system_env(mvm_binary, check_system_prerequisites) -> None:
    """Verify prerequisites for vm tests."""
    if os.environ.get("PYTEST_XDIST_WORKER"):
        return
    if not (Path.home() / ".cache" / "mvmctl" / "mvmdb.db").exists():
        pytest.skip("mvmctl not initialized (run 'mvm host init')")


@pytest.fixture(scope="module")
def lifecycle_vm(mvm_binary) -> Generator[dict[str, Any], None, None]:
    """Module-scoped VM for state transition tests (pause/resume, stop/start, reboot)."""
    vm_name = f"sys-lifecycle-{uuid.uuid4().hex[:8]}"
    net_name = f"sys-lifecycle-net-{uuid.uuid4().hex[:6]}"
    key_name = f"sys-lifecycle-key-{uuid.uuid4().hex[:6]}"

    _run_mvm(mvm_binary, "key", "create", key_name, "--algorithm", "ed25519")
    vm_info = _create_minimal_vm_core(
        mvm_binary, vm_name, net_name, ssh_key_name=key_name
    )
    try:
        yield vm_info
    finally:
        _cleanup_vm_resources(mvm_binary, vm_name, net_name, key_name)
