"""Volume domain system tests — setup."""

from __future__ import annotations

import os
from pathlib import Path

import pytest


@pytest.fixture(scope="session", autouse=True)
def prepare_system_env(mvm_binary, check_system_prerequisites) -> None:
    """Verify prerequisites for volume tests."""
    if os.environ.get("PYTEST_XDIST_WORKER"):
        return
    # Skip-reason: mvmctl not initialized — no database found.
    # This is a prerequisite for all volume tests. The user must run
    # 'mvm host init' (or 'mvm init --non-interactive --skip-host')
    # before volume system tests can execute.
    if not (Path.home() / ".cache" / "mvmctl" / "mvmdb.db").exists():
        pytest.skip("mvmctl not initialized (run 'mvm host init')")
